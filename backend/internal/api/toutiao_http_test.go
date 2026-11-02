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
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/toutiao"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// Substitute only upstream IO and the business budget clock. Every request
// still uses the real catalog, Toutiao parser, adapter and public HTTP handler.
type toutiaoHTTPFixtureTape struct {
	t                   *testing.T
	responses, requests unified.Array
	next                int
}

func (f *toutiaoHTTPFixtureTape) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return fetch.Response{}, err
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 35*time.Second {
		f.t.Fatal("Toutiao HTTP request lost its shared 35-second deadline")
	}
	if f.next >= len(f.responses) || f.next >= len(f.requests) {
		f.t.Fatalf("unexpected Toutiao request: %s %s", request.Method, request.URL)
	}
	wantResponse := unified.Map(f.responses[f.next])
	wantRequest := unified.Clone(unified.Map(f.requests[f.next]))
	wantHeaders := unified.Map(wantRequest["headers"])
	if wantHeaders == nil {
		f.t.Fatal("frozen PHP request headers are missing")
	}
	// PHP's cURL recorder captures explicit options; libcurl supplies Accept.
	// Go declares the same wire header explicitly. This is the sole permitted
	// request-record distinction; the immutable PHP expectation stays intact.
	wantHeaders["accept"] = "*/*"
	headers := unified.Object{}
	for key, values := range request.Header {
		headers[strings.ToLower(key)] = strings.Join(values, ", ")
	}
	toutiaoHTTPSameJSON(f.t, unified.Object{
		"method": request.Method, "url": request.URL, "headers": headers,
		"body": string(request.Body), "follow_redirects": request.Policy.FollowRedirects,
		"timeout_seconds": int64(request.Policy.Timeout / time.Second),
	}, wantRequest)
	if request.Policy.Timeout < time.Second || request.Policy.Timeout > 20*time.Second || request.Policy.Timeout%time.Second != 0 {
		f.t.Fatal("Toutiao request timeout is not a bounded whole-second PHP budget")
	}
	if request.Method != wantResponse["method"] || request.URL != wantResponse["url"] || request.Policy.FollowRedirects != wantResponse["follow_redirects"] {
		f.t.Fatal("PHP request observation and upstream response tape disagree")
	}
	// Request authorization remains independent of the public media URLs.
	// Session and page requests must also stay pinned to their exact target.
	wantPurpose, wantLimit := "toutiao-share", int64(2<<20)
	if request.Method == http.MethodPost {
		wantPurpose, wantLimit = "toutiao-session", 65536
	} else if _, hasCookie := wantHeaders["cookie"]; hasCookie {
		wantPurpose = "toutiao-page"
	}
	policy := request.Policy
	if policy.Purpose != wantPurpose || policy.MaxBytes != wantLimit || policy.FollowRedirects || policy.AllowedURL == nil || !policy.AllowedURL(request.URL) {
		f.t.Fatalf("missing or violated %s request policy", wantPurpose)
	}
	for _, rejected := range []string{"https://example.invalid/video/7501991960077500979/", "https://127.0.0.1/", "https://www.toutiao.com:8080/", "https://user@www.toutiao.com/"} {
		if policy.AllowedURL(rejected) {
			f.t.Fatalf("%s authorizes an unrelated or invalid target: %s", wantPurpose, rejected)
		}
	}
	if wantPurpose != "toutiao-share" && policy.AllowedURL(request.URL+"?unrelated=1") {
		f.t.Fatalf("%s is not pinned to its exact target", wantPurpose)
	}
	f.next++
	if message := unified.Str(wantResponse["error"]); message != "" {
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

func toutiaoHTTPFixtures(t *testing.T) []unified.Object {
	t.Helper()
	directory := filepath.Join("..", "platform", "toutiao", "testdata")
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
		t.Fatal("unexpected fixed PHP Toutiao oracle provenance")
	}
	cases, inputCases := unified.Slice(document["cases"]), unified.Slice(inputs["cases"])
	if len(cases) != 48 || len(inputCases) != len(cases) {
		t.Fatalf("incomplete Toutiao HTTP fixtures: baseline=%d inputs=%d", len(cases), len(inputCases))
	}
	seen := map[string]bool{}
	fixtures := make([]unified.Object, 0, len(cases))
	for index, value := range cases {
		fixture, source := unified.Map(value), unified.Map(inputCases[index])
		id := unified.Str(fixture["id"])
		if id == "" || seen[id] || id != source["id"] {
			t.Fatal("missing, duplicate or reordered Toutiao fixture identity")
		}
		seen[id] = true
		toutiaoHTTPSameJSON(t, fixture["input"], source["input"])
		fixtures = append(fixtures, fixture)
	}
	return fixtures
}

func toutiaoHTTPSameJSON(t *testing.T, got, want any) {
	t.Helper()
	if !unified.Equal(got, want) {
		actual, _ := unified.Encode(got)
		expected, _ := unified.Encode(want)
		t.Fatalf("Toutiao HTTP JSON differs:\ngot:  %s\nwant: %s", actual, expected)
	}
}

func toutiaoHTTPRequestPath(fixture unified.Object, route, view, itemID string) string {
	query := url.Values{"schema_version": {"1.0"}, "url": {unified.Str(unified.Map(fixture["input"])["url"])}}
	if view == "debug" {
		query.Set("debug", "1")
	}
	if route == "resolve" {
		query.Set("item_id", itemID)
	}
	return "/api/" + route + "?" + query.Encode()
}

func toutiaoFixtureHTTP(t *testing.T, fixture unified.Object, path string) unified.Object {
	t.Helper()
	input, expected := unified.Map(fixture["input"]), unified.Map(fixture["expected"])
	tape := &toutiaoHTTPFixtureTape{t: t, responses: unified.Slice(input["http_responses"]), requests: unified.Slice(expected["http_requests"])}
	ticks := unified.Slice(input["clock_ticks"])
	if len(ticks) == 0 {
		t.Fatal("fixed monotonic clock missing")
	}
	clockCalls := 0
	clock := func() time.Time {
		position := clockCalls
		clockCalls++
		if position >= len(ticks) {
			position = len(ticks) - 1
		}
		value, ok := unified.Numeric(ticks[position])
		if !ok {
			t.Fatal("invalid monotonic clock tick")
		}
		seconds := int64(value)
		return time.Unix(seconds, int64((value-float64(seconds))*float64(time.Second)))
	}
	now, ok := unified.Numeric(input["now"])
	if !ok || now != 1793361600 {
		t.Fatal("unexpected fixed public clock")
	}
	registry, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Toutiao direct sources do not need a production media-signing secret.
	handler, err := New(registry, map[string]Adapter{"toutiao": toutiao.NewWithClock(tape, clock)}, func() time.Time { return time.Unix(int64(now), 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if tape.next != len(tape.responses) || tape.next != len(tape.requests) {
		t.Fatalf("unused Toutiao upstream fixture: consumed %d, responses %d, PHP requests %d", tape.next, len(tape.responses), len(tape.requests))
	}
	toutiaoHTTPSameJSON(t, clockCalls, expected["clock_calls"])
	for header, want := range map[string]string{"Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Access-Control-Allow-Origin": ""} {
		if got := w.Header().Get(header); got != want {
			t.Fatalf("%s: got %q, want %q", header, got, want)
		}
	}
	payload, err := unified.Decode(w.Body.Bytes())
	if err != nil {
		t.Fatalf("Toutiao HTTP response is not exactly one JSON object: %v", err)
	}
	return unified.Object{"status": w.Code, "payload": payload}
}

func toutiaoHTTPExpected(fixture unified.Object, view string) unified.Object {
	switch unified.Str(fixture["id"]) {
	case "toutiao/entry/reject-foreign-host", "toutiao/entry/reject-userinfo", "toutiao/entry/reject-nonstandard-port":
		// These three URLs never reach a parser in the original PHP gateway.
		// Its snapHandle preflight error differs from the frozen direct-parser
		// error. Keep the original gateway literal and assert zero requests and
		// zero clock reads; do not rewrite the independent parser expectation.
		return unified.Object{"schema_version": "1.0", "code": "INVALID_INPUT", "error": unified.Object{"code": "INVALID_INPUT", "message": "请输入对应平台的有效分享链接"}}
	default:
		return unified.Clone(unified.Map(unified.Map(fixture["expected"])[view]))
	}
}

func toutiaoHTTPExpectedStatus(t *testing.T, payload unified.Object) int {
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

func TestToutiaoHTTPParsePHPFixtures(t *testing.T) {
	for _, fixture := range toutiaoHTTPFixtures(t) {
		for _, view := range []string{"normal", "debug"} {
			t.Run(unified.Str(fixture["id"])+"/"+view, func(t *testing.T) {
				want := toutiaoHTTPExpected(fixture, view)
				got := toutiaoFixtureHTTP(t, fixture, toutiaoHTTPRequestPath(fixture, "parse", view, ""))
				toutiaoHTTPSameJSON(t, got, unified.Object{"status": toutiaoHTTPExpectedStatus(t, want), "payload": want})
			})
		}
	}
}

func TestToutiaoHTTPResolvePHPFixtures(t *testing.T) {
	for _, fixture := range toutiaoHTTPFixtures(t) {
		for _, view := range []string{"normal", "debug"} {
			frozen := toutiaoHTTPExpected(fixture, view)
			if frozen["code"] != "OK" {
				continue
			}
			items := unified.Slice(unified.Map(frozen["data"])["items"])
			if len(items) == 0 {
				t.Fatal("successful frozen Toutiao response has no media item")
			}
			for _, value := range items {
				item := unified.Map(value)
				itemID := unified.Str(item["id"])
				if itemID == "" || (item["state"] != nil && item["state"] != "ready") {
					t.Fatal("frozen Toutiao item is not resolvable")
				}
				t.Run(unified.Str(fixture["id"])+"/"+view+"/"+itemID, func(t *testing.T) {
					got := toutiaoFixtureHTTP(t, fixture, toutiaoHTTPRequestPath(fixture, "resolve", view, itemID))
					want := unified.Clone(frozen)
					// PHP resolve changes only this pointer; complete item and
					// variant inventories, metadata and stable IDs remain exact.
					unified.Map(want["data"])["default_item_id"] = itemID
					toutiaoHTTPSameJSON(t, got, unified.Object{"status": http.StatusOK, "payload": want})
				})
			}
		}
	}
}

func TestToutiaoHTTPResolveMissingItem(t *testing.T) {
	const missing = "__missing_toutiao_item__"
	for _, fixture := range toutiaoHTTPFixtures(t) {
		for _, view := range []string{"normal", "debug"} {
			frozen := toutiaoHTTPExpected(fixture, view)
			if frozen["code"] != "OK" {
				continue
			}
			for _, value := range unified.Slice(unified.Map(frozen["data"])["items"]) {
				if unified.Map(value)["id"] == missing {
					t.Fatal("missing-item sentinel occurs in the frozen response")
				}
			}
			t.Run(unified.Str(fixture["id"])+"/"+view, func(t *testing.T) {
				got := toutiaoFixtureHTTP(t, fixture, toutiaoHTTPRequestPath(fixture, "resolve", view, missing))
				want := unified.Object{"schema_version": "1.0", "code": "CONTENT_UNAVAILABLE", "error": unified.Object{"code": "CONTENT_UNAVAILABLE", "message": "请求的媒体条目暂时不可用"}}
				toutiaoHTTPSameJSON(t, got, unified.Object{"status": http.StatusNotFound, "payload": want})
			})
		}
	}
}
