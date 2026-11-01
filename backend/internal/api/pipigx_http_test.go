package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/pipigx"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// Substitute only upstream IO. The single POST retains a real deadline. Every request
// still uses the real catalog, Pipigx parser, adapter and public HTTP handler.
type pipigxHTTPFixtureTape struct {
	t                   *testing.T
	responses, requests unified.Array
	next                int
}

func (f *pipigxHTTPFixtureTape) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return fetch.Response{}, err
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 20*time.Second {
		f.t.Fatal("Pipigx HTTP request lost its shared 20-second deadline")
	}
	if f.next >= len(f.responses) || f.next >= len(f.requests) {
		f.t.Fatalf("unexpected Pipigx request: %s %s", request.Method, request.URL)
	}
	wantResponse := unified.Map(f.responses[f.next])
	wantRequest := unified.Clone(unified.Map(f.requests[f.next]))
	wantHeaders := unified.Map(wantRequest["headers"])
	if wantHeaders == nil {
		f.t.Fatal("frozen PHP request headers are missing")
	}
	// Account only for libcurl's implicit Accept header and suppressed Go UA.
	wantHeaders["accept"] = "*/*"
	headers := unified.Object{}
	for key, values := range request.Header {
        if strings.EqualFold(key,"User-Agent")&&len(values)==1&&values[0]=="" {continue}
		headers[strings.ToLower(key)] = strings.Join(values, ", ")
	}
	pipigxHTTPSameJSON(f.t, unified.Object{
		"method": request.Method, "url": request.URL, "headers": headers,
		"body": string(request.Body), "follow_redirects": request.Policy.FollowRedirects,
		"timeout_seconds": int64(request.Policy.Timeout / time.Second),
	}, wantRequest)
	if request.Policy.Timeout < time.Second || request.Policy.Timeout > 20*time.Second || request.Policy.Timeout%time.Second != 0 {
		f.t.Fatal("Pipigx request timeout is not a bounded whole-second PHP budget")
	}
	if request.Method != wantResponse["method"] || request.URL != wantResponse["url"] || request.Policy.FollowRedirects != wantResponse["follow_redirects"] {
		f.t.Fatal("PHP request observation and upstream response tape disagree")
	}
	// Request authorization remains independent of the public media URLs.
	// The single POST must stay pinned to its exact target.
	wantPurpose, wantLimit := "pipigx-post", int64(8<<20)
	policy := request.Policy
	if policy.Purpose != wantPurpose || policy.MaxBytes != wantLimit || policy.FollowRedirects || policy.AllowedURL == nil || !policy.AllowedURL(request.URL) {
		f.t.Fatalf("missing or violated %s request policy", wantPurpose)
	}
	for _, rejected := range []string{"https://example.invalid/video/7501991960077500979/", "https://127.0.0.1/", "https://www.pipigx.com:8080/", "https://user@www.pipigx.com/"} {
		if policy.AllowedURL(rejected) {
			f.t.Fatalf("%s authorizes an unrelated or invalid target: %s", wantPurpose, rejected)
		}
	}
	if wantPurpose != "pipigx-share" && policy.AllowedURL(request.URL+"?unrelated=1") {
		f.t.Fatalf("%s is not pinned to its exact target", wantPurpose)
	}
	f.next++
	if message := unified.Str(wantResponse["error"]); message != "" {
        if unified.Equal(wantResponse["errno"],28) {return fetch.Response{},context.DeadlineExceeded}
		return fetch.Response{}, errors.New(message)
	}
	status, ok := unified.Numeric(wantResponse["status"])
	if !ok {
		f.t.Fatal("upstream response status is missing")
	}
	response := fetch.Response{StatusCode: int(status), Header: http.Header{}, Body: []byte(unified.Str(wantResponse["body"])), URL: request.URL}
	for key, raw := range unified.Map(wantResponse["headers"]) {
		if values := unified.Slice(raw); values != nil {
			for _, value := range values {
				response.Header.Add(key, unified.Str(value))
			}
		} else {
			response.Header.Set(key, unified.Str(raw))
		}
	}
	return response, nil
}

func pipigxHTTPFixtures(t *testing.T) []unified.Object {
	t.Helper()
	directory := filepath.Join("..", "platform", "pipigx", "testdata")
	read := func(name string) unified.Object {
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		value, err := unified.Decode(body)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	document, inputs := read("php-baseline.json"), read("inputs.json")
	if !unified.Equal(document["format_version"], 1) || document["baseline_commit"] != "25beaee107f8c1a208ced9deb3c637068445e316" || document["baseline_tree"] != "e4b9d40f5e6c12a86fff629c105553886b434dcd" {
		t.Fatal("unexpected fixed PHP Pipigx oracle provenance")
	}
	cases, inputCases := unified.Slice(document["cases"]), unified.Slice(inputs["cases"])
	if len(cases) != 42 || len(inputCases) != len(cases) {
		t.Fatalf("incomplete Pipigx HTTP fixtures: baseline=%d inputs=%d", len(cases), len(inputCases))
	}
	seen := map[string]bool{}
	fixtures := make([]unified.Object, 0, len(cases))
	for index, value := range cases {
		fixture, source := unified.Map(value), unified.Map(inputCases[index])
		id := unified.Str(fixture["id"])
		if id == "" || seen[id] || id != source["id"] {
			t.Fatal("missing, duplicate or reordered Pipigx fixture identity")
		}
		seen[id] = true
		pipigxHTTPSameJSON(t, fixture["input"], source["input"])
		fixtures = append(fixtures, fixture)
	}
	return fixtures
}

func pipigxHTTPSameJSON(t *testing.T, got, want any) {
	t.Helper()
	if !unified.Equal(got, want) {
		actual, _ := unified.Encode(got)
		expected, _ := unified.Encode(want)
		t.Fatalf("Pipigx HTTP JSON differs:\ngot:  %s\nwant: %s", actual, expected)
	}
}

func pipigxHTTPRequestPath(fixture unified.Object, route, view, itemID string) string {
	query := url.Values{"schema_version": {"1.0"}, "url": {unified.Str(unified.Map(fixture["input"])["url"])}}
	if view == "debug" {
		query.Set("debug", "1")
	}
	if route == "resolve" {
		query.Set("item_id", itemID)
	}
	return "/api/" + route + "?" + query.Encode()
}

func pipigxFixtureHTTP(t *testing.T, fixture unified.Object, path string) unified.Object {
	t.Helper()
	input, expected := unified.Map(fixture["input"]), unified.Map(fixture["expected"])
	tape := &pipigxHTTPFixtureTape{t: t, responses: unified.Slice(input["http_responses"]), requests: unified.Slice(expected["http_requests"])}
	clockCalls:=0
	now, ok := unified.Numeric(input["now"])
	if !ok || now != 1793361600 {
		t.Fatal("unexpected fixed public clock")
	}
	registry, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Pipigx direct sources do not need a production media-signing secret.
	handler, err := New(registry, map[string]Adapter{"pipigx": pipigx.New(tape)}, func() time.Time { return time.Unix(int64(now), 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if tape.next != len(tape.responses) || tape.next != len(tape.requests) {
		t.Fatalf("unused Pipigx upstream fixture: consumed %d, responses %d, PHP requests %d", tape.next, len(tape.responses), len(tape.requests))
	}
	pipigxHTTPSameJSON(t, clockCalls, expected["clock_calls"])
	for header, want := range map[string]string{"Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Access-Control-Allow-Origin": ""} {
		if got := w.Header().Get(header); got != want {
			t.Fatalf("%s: got %q, want %q", header, got, want)
		}
	}
	payload, err := unified.Decode(w.Body.Bytes())
	if err != nil {
		t.Fatalf("Pipigx HTTP response is not exactly one JSON object: %v", err)
	}
	return unified.Object{"status": w.Code, "payload": payload}
}

func pipigxHTTPExpected(fixture unified.Object,view string) unified.Object {
 return unified.Clone(unified.Map(unified.Map(fixture["expected"])[view]))
}

func pipigxHTTPExpectedStatus(t *testing.T, payload unified.Object) int {
	t.Helper()
	// Fixed PHP snapEnvelopeStatus mapping, independent of the Go handler's
	// Status function and of the individual upstream HTTP response status.
	statuses := map[string]int{
		"OK": 200, "INVALID_INPUT": 400, "UNSUPPORTED_PLATFORM": 400,
		"NO_MEDIA": 404, "CONTENT_UNAVAILABLE": 404, "UPSTREAM_BLOCKED": 403,
		"RATE_LIMITED": 429, "UPSTREAM_TIMEOUT": 504, "SERVICE_NOT_CONFIGURED": 503,
		"INTERNAL_ERROR": 500, "UPSTREAM_INVALID_RESPONSE": 502, "UPSTREAM_UNAVAILABLE": 502,
	}
	status, ok := statuses[unified.Str(payload["code"])]
	if !ok {
		t.Fatal("unknown frozen PHP envelope status")
	}
	return status
}

func TestPipigxHTTPParsePHPFixtures(t *testing.T) {
	for _, fixture := range pipigxHTTPFixtures(t) {
		for _, view := range []string{"normal", "debug"} {
			t.Run(unified.Str(fixture["id"])+"/"+view, func(t *testing.T) {
				want := pipigxHTTPExpected(fixture, view)
				got := pipigxFixtureHTTP(t, fixture, pipigxHTTPRequestPath(fixture, "parse", view, ""))
				pipigxHTTPSameJSON(t, got, unified.Object{"status": pipigxHTTPExpectedStatus(t, want), "payload": want})
			})
		}
	}
}

func TestPipigxHTTPResolvePHPFixtures(t *testing.T) {
	for _, fixture := range pipigxHTTPFixtures(t) {
		for _, view := range []string{"normal", "debug"} {
			frozen := pipigxHTTPExpected(fixture, view)
			if frozen["code"] != "OK" {
				continue
			}
			items := unified.Slice(unified.Map(frozen["data"])["items"])
			if len(items) == 0 {
				t.Fatal("successful frozen Pipigx response has no media item")
			}
			for _, value := range items {
				item := unified.Map(value)
				itemID := unified.Str(item["id"])
				if itemID == "" || (item["state"] != nil && item["state"] != "ready") {
					t.Fatal("frozen Pipigx item is not resolvable")
				}
				t.Run(unified.Str(fixture["id"])+"/"+view+"/"+itemID, func(t *testing.T) {
					got := pipigxFixtureHTTP(t, fixture, pipigxHTTPRequestPath(fixture, "resolve", view, itemID))
					want := unified.Clone(frozen)
					// PHP resolve changes only this pointer; complete item and
					// variant inventories, metadata and stable IDs remain exact.
					unified.Map(want["data"])["default_item_id"] = itemID
					pipigxHTTPSameJSON(t, got, unified.Object{"status": http.StatusOK, "payload": want})
				})
			}
		}
	}
}

func TestPipigxHTTPResolveMissingItem(t *testing.T) {
	const missing = "__missing_pipigx_item__"
	for _, fixture := range pipigxHTTPFixtures(t) {
		for _, view := range []string{"normal", "debug"} {
			frozen := pipigxHTTPExpected(fixture, view)
			if frozen["code"] != "OK" {
				continue
			}
			for _, value := range unified.Slice(unified.Map(frozen["data"])["items"]) {
				if unified.Map(value)["id"] == missing {
					t.Fatal("missing-item sentinel occurs in the frozen response")
				}
			}
			t.Run(unified.Str(fixture["id"])+"/"+view, func(t *testing.T) {
				got := pipigxFixtureHTTP(t, fixture, pipigxHTTPRequestPath(fixture, "resolve", view, missing))
				want := unified.Object{"schema_version": "1.0", "code": "CONTENT_UNAVAILABLE", "error": unified.Object{"code": "CONTENT_UNAVAILABLE", "message": "请求的媒体条目暂时不可用"}}
				pipigxHTTPSameJSON(t, got, unified.Object{"status": http.StatusNotFound, "payload": want})
			})
		}
	}
}
