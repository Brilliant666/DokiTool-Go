package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	weibomedia "github.com/Brilliant666/DokiTool-Go/backend/internal/media/weibo"
	weiboplatform "github.com/Brilliant666/DokiTool-Go/backend/internal/platform/weibo"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const weiboFrozenNow int64 = 1730000000
const weiboFrozenInput = "https://weibo.com/1234567890/AbC"
const weiboUpstreamURL = "https://weibo.com/ajax/statuses/show?id=AbC"
const weiboCDNURL = "https://f.video.weibocdn.com/full.mp4?size=1080"
const weiboSyntheticResponse = `{"idstr":"1234567890","text_raw":"合成正文视频","page_info":{"media_info":{"mp4_hd_url":"https://f.video.weibocdn.com/full.mp4?size=1080","video_title":"合成标题","duration":16}},"user":{"screen_name":"合成作者","idstr":"90071992547409930012"},"attitudes_count":0}`

// Only upstream responses are synthetic. The parser, request choreography,
// signing, policy, public gateway and resolve handler all run for real.
type weiboHTTPFixtureExecutor struct {
	t     *testing.T
	body  string
	calls int
}

func (f *weiboHTTPFixtureExecutor) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return fetch.Response{}, err
	}
	if _, ok := ctx.Deadline(); !ok {
		f.t.Fatal("parent request deadline missing")
	}
	if request.Method != http.MethodGet || request.URL != weiboUpstreamURL {
		f.t.Fatalf("unexpected Weibo request: %s %s", request.Method, request.URL)
	}
	if request.Policy.Purpose != "weibo-metadata" || request.Policy.FollowRedirects || request.Policy.MaxBytes != 2<<20 ||
		request.Policy.AllowedURL == nil || !request.Policy.AllowedURL(request.URL) || request.Policy.AllowedURL("https://example.com/other") ||
		request.Policy.Timeout <= 0 || request.Policy.Timeout > 20*time.Second {
		f.t.Fatal("missing bounded official-host request policy")
	}
	if request.Header.Get("Cookie") != "" {
		f.t.Fatal("one parse leaked anonymous cookies from another")
	}
	f.calls++
	return fetch.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       []byte(f.body),
		URL:        request.URL,
	}, nil
}

func newWeiboHTTPFixture(t *testing.T, key, body string) (*Handler, *weiboHTTPFixtureExecutor) {
	t.Helper()
	registry, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	tape := &weiboHTTPFixtureExecutor{t: t, body: body}
	handler, err := New(registry, map[string]Adapter{"weibo": weiboplatform.New(tape)},
		func() time.Time { return time.Unix(weiboFrozenNow, 0) }, weiboplatform.PreparePublic(key))
	if err != nil {
		t.Fatal(err)
	}
	return handler, tape
}

func weiboHTTPCall(t *testing.T, handler *Handler, route, view string) (int, unified.Object) {
	t.Helper()
	query := url.Values{"schema_version": {"1.0"}, "url": {weiboFrozenInput}}
	if route == "resolve" {
		query.Set("item_id", "video")
	}
	if view == "debug" {
		query.Set("debug", "1")
	}
	request := httptest.NewRequest(http.MethodGet, "/api/"+route+"?"+query.Encode(), nil)
	writer := httptest.NewRecorder()
	handler.ServeHTTP(writer, request)
	if writer.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		writer.Header().Get("Cache-Control") != "no-store" || writer.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("public HTTP gateway headers changed")
	}
	payload, err := unified.Decode(writer.Body.Bytes())
	if err != nil {
		t.Fatalf("invalid public JSON: %v", err)
	}
	return writer.Code, payload
}

func TestWeiboHTTPParseAndResolveSignedMedia(t *testing.T) {
	key := strings.Repeat("k", 64)
	for _, route := range []string{"parse", "resolve"} {
		for _, view := range []string{"normal", "debug"} {
			t.Run(route+"/"+view, func(t *testing.T) {
				handler, tape := newWeiboHTTPFixture(t, key, weiboSyntheticResponse)
				status, payload := weiboHTTPCall(t, handler, route, view)
				if status != http.StatusOK || payload["code"] != "OK" || tape.calls != 1 {
					t.Fatalf("HTTP Go parse failed: %d %v; upstream calls %d", status, payload["code"], tape.calls)
				}
				if err := unified.ValidateEnvelopeAt(payload, weiboFrozenNow); err != nil {
					t.Fatalf("public schema/semantics: %v", err)
				}
				data := unified.Map(payload["data"])
				if data["platform"] != "weibo" || data["source_url"] != weiboFrozenInput {
					t.Fatal("work identity changed")
				}
				items := unified.Slice(data["items"])
				if len(items) != 1 || unified.Map(items[0])["id"] != "video" {
					t.Fatal("video item identity changed")
				}
				if route == "resolve" && data["default_item_id"] != "video" {
					t.Fatal("explicit selection did not survive resolve")
				}
				item := unified.Map(items[0])
				selected := unified.Map(item["default"])
				if selected["access"] != "relay" || !strings.HasPrefix(unified.Str(selected["url"]), "/api/media/weibo?ticket=") {
					t.Fatal("default did not select signed relay")
				}
				ticket := strings.TrimPrefix(unified.Str(selected["url"]), "/api/media/weibo?ticket=")
				decoded, ok := weibomedia.Verify(ticket, key, weiboFrozenNow)
				if !ok || decoded["u"] != weiboCDNURL {
					t.Fatal("signed ticket no longer verifies against original provider URL")
				}
				if strings.Contains(string(writerBody(payload)), weiboCDNURL) {
					t.Fatal("public response leaked raw playable media URL")
				}
				for _, value := range unified.Slice(item["variants"]) {
					variant := unified.Map(value)
					for _, sourceValue := range unified.Slice(unified.Map(variant["delivery"])["sources"]) {
						source := unified.Map(sourceValue)
						if source["access"] != "relay" && unified.SourceUsable(source, weiboFrozenNow) {
							t.Fatal("ready source bypassed signing")
						}
					}
				}
			})
		}
	}
}

func writerBody(payload unified.Object) []byte {
	body, _ := unified.Encode(payload)
	return body
}

func TestWeiboHTTPMissingSigningKeyFailsWithoutMediaLeak(t *testing.T) {
	handler, tape := newWeiboHTTPFixture(t, "", weiboSyntheticResponse)
	status, payload := weiboHTTPCall(t, handler, "parse", "normal")
	if status != http.StatusServiceUnavailable || payload["code"] != "SERVICE_NOT_CONFIGURED" || tape.calls != 1 {
		t.Fatalf("missing signer not rejected: %d %v", status, payload["code"])
	}
	if strings.Contains(string(writerBody(payload)), weiboCDNURL) {
		t.Fatal("raw CDN leaked on unavailable signer")
	}
}

func TestWeiboHTTPRejectsInvalidSourceWithoutNetwork(t *testing.T) {
	handler, tape := newWeiboHTTPFixture(t, strings.Repeat("a", 64), weiboSyntheticResponse)
	query := url.Values{"schema_version": {"1.0"}, "url": {"https://weibo.com/tv/show/not-a-fid"}}
	req := httptest.NewRequest(http.MethodGet, "/api/parse?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if tape.calls != 0 || rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid target reached upstream: calls %d, status %d", tape.calls, rec.Code)
	}
}

var _ fetch.Executor = (*weiboHTTPFixtureExecutor)(nil)
