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
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/bilibili"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// Only the upstream transport is substituted. Every response travels through
// the real Bilibili parser, adapter, public policy and HTTP gateway.
type bilibiliHTTPFixtureTape struct {
	t                   *testing.T
	responses, requests unified.Array
	next                int
}

func (f *bilibiliHTTPFixtureTape) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return fetch.Response{}, err
	}
	if _, ok := ctx.Deadline(); !ok {
		f.t.Fatal("Bilibili HTTP request lost its parent deadline")
	}
	if request.Policy.Purpose == "" || request.Policy.AllowedURL == nil || !request.Policy.AllowedURL(request.URL) || request.Policy.Timeout <= 0 || request.Policy.MaxBytes <= 0 {
		f.t.Fatalf("missing or violated Bilibili request policy: %s %s", request.Method, request.URL)
	}
	if f.next >= len(f.responses) || f.next >= len(f.requests) {
		f.t.Fatalf("unexpected Bilibili request: %s %s", request.Method, request.URL)
	}
	want := unified.Map(f.responses[f.next])
	observed := unified.Clone(unified.Map(f.requests[f.next]))
	delete(observed, "transport") // Go replaces PHP's cURL implementation.
	// CURLOPT_ENCODING="" asks cURL to advertise every compiled-in decoder.
	// The bounded Go executor explicitly supports gzip/deflate. Preserve the
	// frozen PHP record and account for this one transport-level distinction;
	// every other header, request URL, method and redirect choice stays exact.
	if headers := unified.Map(observed["headers"]); headers["accept-encoding"] == "" {
		headers["accept-encoding"] = "gzip,deflate"
	}
	headers := unified.Object{}
	for key, values := range request.Header {
		// An explicitly empty User-Agent suppresses net/http's default UA and
		// is absent on the wire, matching PHP's API and short-link requests.
		if strings.EqualFold(key, "User-Agent") && len(values) == 1 && values[0] == "" {
			continue
		}
		headers[strings.ToLower(key)] = strings.Join(values, ", ")
	}
	bilibiliHTTPSameJSON(f.t, unified.Object{"method": request.Method, "url": request.URL, "headers": headers, "follow_redirects": request.Policy.FollowRedirects}, observed)
	if request.Method != want["method"] || request.URL != want["url"] {
		f.t.Fatal("PHP request observation and upstream response tape disagree")
	}
	f.next++
	if message := unified.Str(want["error"]); message != "" {
		return fetch.Response{}, errors.New(message)
	}
	status, _ := unified.Numeric(want["status"])
	response := fetch.Response{StatusCode: int(status), Header: http.Header{}, Body: []byte(unified.Str(want["body"])), URL: request.URL}
	for key, value := range unified.Map(want["headers"]) {
		if values := unified.Slice(value); values != nil {
			for _, entry := range values {
				response.Header.Add(key, unified.Str(entry))
			}
		} else {
			response.Header.Set(key, unified.Str(value))
		}
	}
	if finalURL := unified.Str(want["final_url"]); finalURL != "" {
		if !request.Policy.AllowedURL(finalURL) {
			f.t.Fatalf("frozen redirect target would be rejected by the real executor: %s", finalURL)
		}
		response.URL = finalURL
	}
	for _, target := range unified.Slice(want["history"]) {
		response.History = append(response.History, unified.Str(target))
	}
	return response, nil
}

func bilibiliHTTPFixtures(t *testing.T) map[string]unified.Object {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "platform", "bilibili", "testdata", "php-baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := unified.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if !unified.Equal(document["format_version"], 1) {
		t.Fatal("unsupported Bilibili PHP fixture format")
	}
	fixtures := map[string]unified.Object{}
	for _, value := range unified.Slice(document["cases"]) {
		fixture := unified.Map(value)
		id := unified.Str(fixture["id"])
		if id == "" || fixtures[id] != nil {
			t.Fatal("empty or duplicate Bilibili PHP fixture ID")
		}
		fixtures[id] = fixture
	}
	return fixtures
}

func bilibiliHTTPFixture(t *testing.T, fixtures map[string]unified.Object, id string) unified.Object {
	t.Helper()
	fixture := fixtures["bilibili/"+id]
	if fixture == nil {
		t.Fatalf("missing frozen PHP Bilibili fixture %q", id)
	}
	return fixture
}

func bilibiliHTTPRequestPath(fixture unified.Object, route, view, itemID string) string {
	query := url.Values{"schema_version": {"1.0"}, "url": {unified.Str(unified.Map(fixture["input"])["url"])}}
	if view == "debug" {
		query.Set("debug", "1")
	}
	if route == "resolve" {
		query.Set("item_id", itemID)
	}
	return "/api/" + route + "?" + query.Encode()
}

func bilibiliFixtureHTTP(t *testing.T, fixture unified.Object, path string) unified.Object {
	t.Helper()
	input, expected := unified.Map(fixture["input"]), unified.Map(fixture["expected"])
	tape := &bilibiliHTTPFixtureTape{t: t, responses: unified.Slice(input["http_responses"]), requests: unified.Slice(expected["http_requests"])}
	now, _ := unified.Numeric(input["now"])
	registry, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(registry, map[string]Adapter{"bilibili": bilibili.New(tape)}, func() time.Time { return time.Unix(int64(now), 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if tape.next != len(tape.responses) || tape.next != len(tape.requests) {
		t.Fatalf("unused Bilibili upstream fixture: consumed %d, responses %d, PHP requests %d", tape.next, len(tape.responses), len(tape.requests))
	}
	for header, want := range map[string]string{"Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Access-Control-Allow-Origin": ""} {
		if got := w.Header().Get(header); got != want {
			t.Fatalf("%s: got %q, want %q", header, got, want)
		}
	}
	payload, err := unified.Decode(w.Body.Bytes())
	if err != nil {
		t.Fatalf("Bilibili HTTP response is not exactly one JSON object: %v", err)
	}
	return unified.Object{"status": w.Code, "payload": payload}
}

func bilibiliHTTPSameJSON(t *testing.T, got, want any) {
	t.Helper()
	if !unified.Equal(got, want) {
		actual, _ := unified.Encode(got)
		expected, _ := unified.Encode(want)
		t.Fatalf("Bilibili HTTP JSON differs:\ngot:  %s\nwant: %s", actual, expected)
	}
}

func bilibiliHTTPExpected(fixture unified.Object, view string) unified.Object {
	// These cases have no declared public-contract corrections. Compare the
	// frozen PHP public response directly, without invoking Go normalization.
	return unified.Clone(unified.Map(unified.Map(fixture["expected"])[view]))
}

func TestBilibiliHTTPParsePHPFixtures(t *testing.T) {
	fixtures := bilibiliHTTPFixtures(t)
	for _, test := range []struct {
		id     string
		status int
	}{
		{"file/playback-quality-not-upload-dimensions", http.StatusOK},
		{"parts/p2-only", http.StatusOK},
		{"entry/short-source-p-wins", http.StatusOK},
		{"entry/duplicate-p-last-wins", http.StatusOK},
		{"entry/query-unclosed-bracket", http.StatusOK},
		{"entry/query-nul-key", http.StatusOK},
		{"entry/query-array-then-scalar", http.StatusOK},
		{"entry/short-html-source-p-wins", http.StatusOK},
		{"entry/invalid-p/array", http.StatusBadRequest},
		{"entry/invalid-p/zero", http.StatusBadRequest},
		{"parts/missing-target", http.StatusNotFound},
		{"play/http-412", http.StatusOK},
		{"view/http-412", http.StatusBadGateway}, // PHP's retryable UPSTREAM_UNAVAILABLE, not a direct upstream HTTP status.
		{"view/invalid-json", http.StatusBadGateway},
		{"segments/two-pieces", http.StatusOK},
		{"dash/two-video-two-audio", http.StatusOK},
		{"policy/complete-file-covers-mux", http.StatusOK},
		{"collection/deduplicated-members", http.StatusOK},
	} {
		for _, view := range []string{"normal", "debug"} {
			t.Run(test.id+"/"+view, func(t *testing.T) {
				fixture := bilibiliHTTPFixture(t, fixtures, test.id)
				got := bilibiliFixtureHTTP(t, fixture, bilibiliHTTPRequestPath(fixture, "parse", view, ""))
				bilibiliHTTPSameJSON(t, got, unified.Object{"status": test.status, "payload": bilibiliHTTPExpected(fixture, view)})
			})
		}
	}
}

func TestBilibiliHTTPResolveKeepsSelectedPartAndWholeWork(t *testing.T) {
	fixtures := bilibiliHTTPFixtures(t)
	for _, test := range []struct{ id, itemID string }{
		{"parts/p2-only", "BV1234567890-p2"},
		{"entry/short-source-p-wins", "BV1234567890-p2"},
		{"segments/two-pieces", "BV1234567890-p1"},
		{"dash/two-video-two-audio", "BV1234567890-p1"},
	} {
		for _, view := range []string{"normal", "debug"} {
			t.Run(test.id+"/"+view, func(t *testing.T) {
				fixture := bilibiliHTTPFixture(t, fixtures, test.id)
				got := bilibiliFixtureHTTP(t, fixture, bilibiliHTTPRequestPath(fixture, "resolve", view, test.itemID))
				want := bilibiliHTTPExpected(fixture, view)
				unified.Map(want["data"])["default_item_id"] = test.itemID
				// Parts, groups and complete variants all remain in the response.
				// A resolvable mux/segments description does not execute a merge.
				bilibiliHTTPSameJSON(t, got, unified.Object{"status": http.StatusOK, "payload": want})
			})
		}
	}
}

func TestBilibiliHTTPResolveCannotBorrowAnotherPart(t *testing.T) {
	fixtures := bilibiliHTTPFixtures(t)
	for _, test := range []struct{ id, itemID, state string }{
		{"parts/p2-only", "BV1234567890-p1", "deferred"},
		{"play/http-412", "BV1234567890-p2", "unavailable"},
		{"parts/p2-only", "BV1234567890-p999", "missing"},
		{"collection/deduplicated-members", "BVabcdefghij-p1", "deferred"},
	} {
		for _, view := range []string{"normal", "debug"} {
			t.Run(test.id+"/"+test.itemID+"/"+view, func(t *testing.T) {
				fixture := bilibiliHTTPFixture(t, fixtures, test.id)
				state := "missing"
				for _, value := range unified.Slice(unified.Map(bilibiliHTTPExpected(fixture, view)["data"])["items"]) {
					item := unified.Map(value)
					if item["id"] == test.itemID {
						state = unified.Str(item["state"])
					}
				}
				if state != test.state {
					t.Fatalf("fixture no longer exercises %s target: got %q", test.state, state)
				}
				got := bilibiliFixtureHTTP(t, fixture, bilibiliHTTPRequestPath(fixture, "resolve", view, test.itemID))
				want := unified.Object{"schema_version": "1.0", "code": "CONTENT_UNAVAILABLE", "error": unified.Object{"code": "CONTENT_UNAVAILABLE", "message": "请求的媒体条目暂时不可用"}}
				bilibiliHTTPSameJSON(t, got, unified.Object{"status": http.StatusNotFound, "payload": want})
			})
		}
	}
}

func TestBilibiliHTTPDebugRetainsMuxWithoutChangingDefault(t *testing.T) {
	fixture := bilibiliHTTPFixture(t, bilibiliHTTPFixtures(t), "policy/complete-file-covers-mux")
	views := map[string]unified.Object{}
	for _, view := range []string{"normal", "debug"} {
		got := bilibiliFixtureHTTP(t, fixture, bilibiliHTTPRequestPath(fixture, "parse", view, ""))
		bilibiliHTTPSameJSON(t, got, unified.Object{"status": http.StatusOK, "payload": bilibiliHTTPExpected(fixture, view)})
		items := unified.Slice(unified.Map(unified.Map(got["payload"])["data"])["items"])
		if len(items) != 1 {
			t.Fatal("policy fixture must contain one part")
		}
		views[view] = unified.Map(items[0])
	}
	if len(unified.Slice(views["normal"]["variants"])) != 1 || len(unified.Slice(views["debug"]["variants"])) != 2 {
		t.Fatal("ordinary/debug views no longer exercise removal of a covered mux")
	}
	if views["normal"]["default"] == nil {
		t.Fatal("the complete file must remain directly downloadable")
	}
	bilibiliHTTPSameJSON(t, views["normal"]["default"], views["debug"]["default"])
}

var _ fetch.Executor = (*bilibiliHTTPFixtureTape)(nil)
