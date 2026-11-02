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
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/douyin"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/testsupport/publicoracle"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// These tests exercise the real HTTP gateway and Douyin parser together. The
// only substitutions are the frozen upstream transport and optional MP4 probe;
// no normalized response, adapter or public policy is stubbed.
type douyinFixtureTape struct {
	t                       *testing.T
	responses, requests     unified.Array
	probeResults, probeURLs unified.Array
	nextHTTP, nextProbe     int
}

func douyinFixtureHeader(fields unified.Object) http.Header {
	result := http.Header{}
	for key, value := range fields {
		if values := unified.Slice(value); values != nil {
			for _, entry := range values {
				result.Add(key, unified.Str(entry))
			}
		} else {
			result.Set(key, unified.Str(value))
		}
	}
	return result
}

func (f *douyinFixtureTape) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return fetch.Response{}, err
	}
	if _, ok := ctx.Deadline(); !ok {
		f.t.Fatal("HTTP handler did not propagate its request deadline")
	}
	if request.Policy.Purpose == "" || request.Policy.AllowedURL == nil || !request.Policy.AllowedURL(request.URL) || request.Policy.Timeout <= 0 || request.Policy.MaxBytes <= 0 {
		f.t.Fatalf("outbound request is missing its platform policy: %s %s", request.Method, request.URL)
	}
	if f.nextHTTP >= len(f.responses) || f.nextHTTP >= len(f.requests) {
		f.t.Fatalf("unexpected outbound HTTP request: %s %s", request.Method, request.URL)
	}
	want := unified.Map(f.responses[f.nextHTTP])
	observed := unified.Clone(unified.Map(f.requests[f.nextHTTP]))
	delete(observed, "transport") // Go replaces both PHP curl and get_headers.
	headers := unified.Object{}
	for key, values := range request.Header {
		headers[strings.ToLower(key)] = strings.Join(values, ", ")
	}
	douyinSameJSON(f.t, unified.Object{"method": request.Method, "url": request.URL, "headers": headers, "follow_redirects": request.Policy.FollowRedirects}, observed)
	if request.Method != want["method"] || request.URL != want["url"] {
		f.t.Fatal("frozen request and response tape disagree")
	}
	f.nextHTTP++
	if message := unified.Str(want["error"]); message != "" {
		return fetch.Response{}, errors.New(message)
	}
	status, _ := unified.Numeric(want["status"])
	response := fetch.Response{StatusCode: int(status), Header: douyinFixtureHeader(unified.Map(want["headers"])), Body: []byte(unified.Str(want["body"])), URL: request.URL}
	if finalURL := unified.Str(want["final_url"]); finalURL != "" {
		response.URL = finalURL
	}
	for _, value := range unified.Slice(want["history"]) {
		response.History = append(response.History, unified.Str(value))
	}
	return response, nil
}

func (f *douyinFixtureTape) Probe(ctx context.Context, source string) (unified.Object, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := ctx.Deadline(); !ok {
		f.t.Fatal("HTTP handler did not propagate its deadline to the media probe")
	}
	if f.nextProbe >= len(f.probeResults) || f.nextProbe >= len(f.probeURLs) {
		f.t.Fatalf("unexpected metadata probe: %s", source)
	}
	want := unified.Map(f.probeResults[f.nextProbe])
	if source != want["url"] || source != f.probeURLs[f.nextProbe] {
		f.t.Fatalf("metadata probe %d changed its source: %s", f.nextProbe, source)
	}
	f.nextProbe++
	if message := unified.Str(want["error"]); message != "" {
		return nil, errors.New(message)
	}
	return unified.Clone(unified.Map(want["result"])), nil
}

func (f *douyinFixtureTape) requireConsumed() {
	f.t.Helper()
	if f.nextHTTP != len(f.responses) || f.nextHTTP != len(f.requests) || f.nextProbe != len(f.probeResults) || f.nextProbe != len(f.probeURLs) {
		f.t.Fatalf("unused upstream fixture: HTTP %d/%d, observed HTTP %d, probes %d/%d, observed probes %d", f.nextHTTP, len(f.responses), len(f.requests), f.nextProbe, len(f.probeResults), len(f.probeURLs))
	}
}

func douyinHTTPFixtures(t *testing.T) map[string]unified.Object {
	t.Helper()
	bytes, err := os.ReadFile(filepath.Join("..", "..", "testdata", "php-douyin", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := unified.Decode(bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !unified.Equal(document["format_version"], 1) {
		t.Fatal("unsupported PHP Douyin fixture format")
	}
	fixtures := map[string]unified.Object{}
	for _, value := range unified.Slice(document["cases"]) {
		fixture := unified.Map(value)
		fixtures[unified.Str(fixture["id"])] = fixture
	}
	return fixtures
}

func douyinFixtureHTTP(t *testing.T, fixture unified.Object, path string) unified.Object {
	t.Helper()
	if fixture == nil {
		t.Fatal("missing PHP Douyin fixture")
	}
	input, expected := unified.Map(fixture["input"]), unified.Map(fixture["expected"])
	tape := &douyinFixtureTape{
		t: t, responses: unified.Slice(input["http_responses"]), requests: unified.Slice(expected["http_requests"]),
		probeResults: unified.Slice(input["probe_results"]), probeURLs: unified.Slice(expected["probe_requests"]),
	}
	now, _ := unified.Numeric(input["now"])
	registry, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(registry, map[string]Adapter{"douyin": douyin.New(tape, tape)}, func() time.Time { return time.Unix(int64(now), 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	tape.requireConsumed()
	for header, want := range map[string]string{"Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Access-Control-Allow-Origin": ""} {
		if got := w.Header().Get(header); got != want {
			t.Fatalf("%s: got %q, want %q", header, got, want)
		}
	}
	payload, err := unified.Decode(w.Body.Bytes())
	if err != nil {
		t.Fatalf("HTTP response is not one JSON object: %v", err)
	}
	return unified.Object{"status": w.Code, "payload": payload}
}

func douyinRequestPath(fixture unified.Object, route, view, itemID string) string {
	input := unified.Map(fixture["input"])
	query := url.Values{"schema_version": {"1.0"}, "url": {unified.Str(input["url"])}}
	if view == "debug" {
		query.Set("debug", "1")
	}
	if route == "resolve" {
		query.Set("item_id", itemID)
	}
	return "/api/" + route + "?" + query.Encode()
}

func douyinSameJSON(t *testing.T, got, want any) {
	t.Helper()
	if !unified.Equal(got, want) {
		actual, _ := unified.Encode(got)
		expected, _ := unified.Encode(want)
		t.Fatalf("JSON differs:\ngot:  %s\nwant: %s", actual, expected)
	}
}

func douyinPublicExpected(t *testing.T, group string, fixture unified.Object, view string) unified.Object {
	t.Helper()
	oracle, err := publicoracle.New()
	if err != nil {
		t.Fatal(err)
	}
	want, err := oracle.Expected(group, unified.Str(fixture["id"]), view, unified.Map(fixture["expected"])[view])
	if err != nil {
		t.Fatal(err)
	}
	return want
}

func TestDouyinHTTPParsePHPFixtures(t *testing.T) {
	fixtures := douyinHTTPFixtures(t)
	for _, test := range []struct {
		id     string
		status int
	}{
		{"video/multi-quality", 200},
		{"pairing/identity/forward", 200}, // Ordinary view filters covered mux; debug keeps both explicit audio pairings.
		{"pairing/mux-only", 200},
		{"live/head-redirect-deduplicated", 200},
		{"live/direct-and-still", 200}, // Keeps the frozen PHP oracle; applies only the declared public URL correction.
		{"original/higher-resolution", 200},
		{"original/throws", 200}, // Optional probe failure still preserves known downloadable media.
		{"entry/short-link-head-fallback", 200},
		{"entry/mismatched-id", 502},
	} {
		for _, view := range []string{"normal", "debug"} {
			t.Run(test.id+"/"+view, func(t *testing.T) {
				fixture := fixtures[test.id]
				got := douyinFixtureHTTP(t, fixture, douyinRequestPath(fixture, "parse", view, ""))
				want := douyinPublicExpected(t, publicoracle.DouyinFixture, fixture, view)
				douyinSameJSON(t, got, unified.Object{"status": test.status, "payload": want})
			})
		}
	}
}

func TestDouyinHTTPResolveKeepsWholeWork(t *testing.T) {
	fixtures := douyinHTTPFixtures(t)
	for _, test := range []struct{ id, itemID string }{
		{"live/direct-and-still", "live-3"},
		{"live/head-redirect-deduplicated", "live-2"},
		{"video/multi-quality", "video"},
		{"pairing/mux-only", "video"},
	} {
		for _, view := range []string{"normal", "debug"} {
			t.Run(test.id+"/"+view, func(t *testing.T) {
				fixture := fixtures[test.id]
				got := douyinFixtureHTTP(t, fixture, douyinRequestPath(fixture, "resolve", view, test.itemID))
				// Resolve's documented change is the top-level item pointer. Every
				// item, per-item default, variant, source, attachment and group must
				// retain the complete PHP public work, including unsupported mux.
				want := douyinPublicExpected(t, publicoracle.DouyinFixture, fixture, view)
				unified.Map(want["data"])["default_item_id"] = test.itemID
				douyinSameJSON(t, got, unified.Object{"status": 200, "payload": want})
			})
		}
	}
}

func TestDouyinHTTPResolveMissingTarget(t *testing.T) {
	fixture := douyinHTTPFixtures(t)["live/direct-and-still"]
	for _, view := range []string{"normal", "debug"} {
		t.Run(view, func(t *testing.T) {
			got := douyinFixtureHTTP(t, fixture, douyinRequestPath(fixture, "resolve", view, "live-999"))
			want := unified.Object{"schema_version": "1.0", "code": "CONTENT_UNAVAILABLE", "error": unified.Object{"code": "CONTENT_UNAVAILABLE", "message": "请求的媒体条目暂时不可用"}}
			douyinSameJSON(t, got, unified.Object{"status": http.StatusNotFound, "payload": want})
		})
	}
}

func TestDouyinHTTPPublicIntegerBoundaries(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", publicoracle.IntegerFixture))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := unified.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range unified.Slice(manifest["cases"]) {
		fixture := unified.Map(value)
		id := unified.Str(fixture["id"])
		if !strings.HasPrefix(id, "source-size/") {
			continue
		}
		for _, view := range []string{"normal", "debug"} {
			for _, route := range []string{"parse", "resolve"} {
				t.Run(id+"/"+view+"/"+route, func(t *testing.T) {
					want := douyinPublicExpected(t, publicoracle.IntegerFixture, fixture, view)
					items := unified.Slice(unified.Map(want["data"])["items"])
					itemID := unified.Str(unified.Map(items[0])["id"])
					if route == "resolve" {
						unified.Map(want["data"])["default_item_id"] = itemID
					}
					got := douyinFixtureHTTP(t, fixture, douyinRequestPath(fixture, route, view, itemID))
					douyinSameJSON(t, got, unified.Object{"status": http.StatusOK, "payload": want})
				})
			}
		}
	}
}

var _ fetch.Executor = (*douyinFixtureTape)(nil)
var _ douyin.Prober = (*douyinFixtureTape)(nil)
