package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func testHandler(t *testing.T, adapter Adapter) *Handler {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	adapters := map[string]Adapter{}
	if adapter != nil {
		adapters["douyin"] = adapter
	}
	h, err := New(c, adapters, func() time.Time { return time.Unix(1793448000, 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRouteContract(t *testing.T) {
	h := testHandler(t, nil)
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/api/platforms?schema_version=1.0", 200}, {"GET", "/api/platforms", 409}, {"GET", "/api/platforms?schema_version=2.0", 409}, {"POST", "/api/platforms?schema_version=1.0", 405},
		{"GET", "/api/douyin", 410}, {"GET", "/api/short_videos", 410}, {"GET", "/api/ksjx", 410}, {"GET", "/api/bilibili", 410}, {"GET", "/api/xhsjx", 410}, {"GET", "/api/toutiao", 410},
		{"GET", "/backend/api/douyin/douyin.php", 404}, {"GET", "/api/%70arse?schema_version=1.0", 404}, {"GET", "/api/parse?schema_version=1.0", 400},
		{"GET", "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/123", 503}, {"GET", "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/123&platform=bilibili", 400},
		{"GET", "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/123&url[]=bad", 400}, {"GET", "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/123&platform[]=douyin", 400},
		{"GET", "/api/resolve?schema_version=1.0&url=https://www.douyin.com/video/123", 400}, {"GET", "/api/resolve?schema_version=1.0&url=https://www.douyin.com/video/123&item_id=target", 503},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
			if w.Code != test.status {
				t.Fatalf("status %d want %d: %s", w.Code, test.status, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("JSON headers differ")
			}
			if test.status == 405 && w.Header().Get("Allow") != "GET" {
				t.Fatal("method Allow missing")
			}
			if _, err := unified.Decode(w.Body.Bytes()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParseResolveKeepWholeWorkAndOpaqueIdentity(t *testing.T) {
	bytes, err := os.ReadFile("../../../docs/schema/unified-v1/examples/01-douyin-quality-and-cdn.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := unified.Decode(bytes)
	if err != nil {
		t.Fatal(err)
	}
	data := unified.Map(fixture["data"])
	items := unified.Slice(data["items"])
	second := unified.Clone(unified.Map(items[0]))
	second["id"] = "opaque-part/2"
	data["items"] = append(items, second)
	data["default_item_id"] = "dy-one"
	calls := 0
	h := testHandler(t, AdapterFunc(func(ctx context.Context, source string, now int64) (unified.Object, error) {
		calls++
		if source != "https://www.douyin.com/video/123?a=1%2F2&a=3+4" || now != 1793448000 {
			t.Fatal("source bytes or clock changed")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing request deadline")
		}
		return unified.Clone(fixture), nil
	}))
	base := "schema_version=1.0&url=" + url.QueryEscape("分享 https://www.douyin.com/video/123?a=1%2F2&a=3+4，")
	for _, itemID := range []string{"", "opaque-part/2", "gone"} {
		path := "/api/parse?" + base
		if itemID != "" {
			path = "/api/resolve?" + base + "&item_id=" + url.QueryEscape(itemID)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if itemID == "gone" {
			if w.Code != 404 {
				t.Fatalf("missing identity guessed: %s", w.Body.String())
			}
			continue
		}
		if w.Code != 200 {
			t.Fatalf("valid response rejected: %s", w.Body.String())
		}
		out, _ := unified.Decode(w.Body.Bytes())
		work := unified.Map(out["data"])
		if len(unified.Slice(work["items"])) != 2 {
			t.Fatal("resolve dropped other items")
		}
		if itemID != "" && work["default_item_id"] != itemID {
			t.Fatal("resolve did not select requested item")
		}
	}
	if calls != 3 {
		t.Fatal("unexpected parse count")
	}
}

func TestInvalidAdapterInventoryRejectedBeforeFiltering(t *testing.T) {
	h := testHandler(t, AdapterFunc(func(context.Context, string, int64) (unified.Object, error) {
		return unified.Object{"schema_version": "1.0", "code": "OK", "data": unified.Object{"platform": "douyin", "items": unified.Array{}}}, nil
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/123", nil))
	if w.Code != 502 {
		t.Fatalf("unchecked payload published: %s", w.Body.String())
	}
}

func TestPublicMediaFailureKeepsStatus(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/schema/unified-v1/examples/01-douyin-quality-and-cdn.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := unified.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	h := testHandler(t, AdapterFunc(func(context.Context, string, int64) (unified.Object, error) { return unified.Clone(fixture), nil }))
	h.publicMedia = func(context.Context, string, unified.Object, int64) (unified.Object, error) {
		return unified.ContractError("SERVICE_NOT_CONFIGURED", "Media config missing", false), nil
	}
	for _, path := range []string{"/api/parse?schema_version=1.0&url=https://www.douyin.com/video/123", "/api/resolve?schema_version=1.0&url=https://www.douyin.com/video/123&item_id=dy-one"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 503 {
			t.Errorf("media error status=%d body=%s", w.Code, w.Body.String())
		}
		payload, err := unified.Decode(w.Body.Bytes())
		if err != nil || payload["code"] != "SERVICE_NOT_CONFIGURED" {
			t.Fatal("media failure code lost")
		}
	}
}

func TestWeiboBinaryRelayBypassesUnifiedAPIBoundary(t *testing.T) {
	h := testHandler(t, nil)
	invoked := 0
	h.SetWeiboRelay(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invoked++
		if r.Method != http.MethodHead || r.URL.RawQuery != "ticket=opaque" {
			t.Fatalf("media method/query changed: %s %s", r.Method, r.URL.RawQuery)
		}
		w.Header().Set("Content-Range", "bytes 0-7/100")
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusPartialContent)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/api/media/weibo?ticket=opaque", nil))
	if invoked != 1 || w.Code != http.StatusPartialContent || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("binary relay rewritten: calls=%d code=%d headers=%v body=%q", invoked, w.Code, w.Header(), w.Body.String())
	}
}

func TestInputURLTruncatesAdjacentHanziAndFullwidthPunctuation(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{
			input: "79 小红书发布了一篇笔记 http://xhslink.com/o/abc123XYZ，复制本条信息，打开【小红书】App查看精彩内容！",
			want:  "http://xhslink.com/o/abc123XYZ",
		},
		{
			input: "https://www.bilibili.com/video/BV1xx411c7mD【标题】",
			want:  "https://www.bilibili.com/video/BV1xx411c7mD",
		},
		{
			input: "https://www.bilibili.com/video/BV1xx411c7mD?p=2&vd_source=abc_123【标题】",
			want:  "https://www.bilibili.com/video/BV1xx411c7mD?p=2&vd_source=abc_123",
		},
		{
			input: "https://www.xiaohongshu.com/explore/66a1b2c3d4e5f6a7b8c9d0e1?xsec_token=ABc+123%2B_=-x&xsec_source=pc_feed，复制本条信息",
			want:  "https://www.xiaohongshu.com/explore/66a1b2c3d4e5f6a7b8c9d0e1?xsec_token=ABc+123%2B_=-x&xsec_source=pc_feed",
		},
		{
			input: "看这个视频https://v.douyin.com/iYabc123/复制此链接打开抖音",
			want:  "https://v.douyin.com/iYabc123/",
		},
	}
	for _, tc := range cases {
		if got := InputURL(tc.input); got != tc.want {
			t.Errorf("InputURL(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPlatformErrorCodesDistinguishUnsupportedVsMismatch(t *testing.T) {
	h := testHandler(t, nil)
	cases := []struct {
		name     string
		query    url.Values
		wantCode string
	}{
		{
			name:     "unsupported-url-host",
			query:    url.Values{"schema_version": {"1.0"}, "url": {"https://example.com/video/123"}},
			wantCode: "UNSUPPORTED_PLATFORM",
		},
		{
			name:     "unknown-requested-platform",
			query:    url.Values{"schema_version": {"1.0"}, "url": {"https://www.douyin.com/video/123"}, "platform": {"vimeo"}},
			wantCode: "UNSUPPORTED_PLATFORM",
		},
		{
			name:     "platform-mismatch",
			query:    url.Values{"schema_version": {"1.0"}, "url": {"https://www.douyin.com/video/123"}, "platform": {"bilibili"}},
			wantCode: "INVALID_INPUT",
		},
		{
			name:     "malformed-not-a-url",
			query:    url.Values{"schema_version": {"1.0"}, "url": {"not a URL"}},
			wantCode: "INVALID_INPUT",
		},
		{
			name:     "userinfo-url-rejected-as-invalid-input",
			query:    url.Values{"schema_version": {"1.0"}, "url": {"https://user:pass@www.douyin.com/video/123"}},
			wantCode: "INVALID_INPUT",
		},
		{
			name:     "nonstandard-port-rejected-as-invalid-input",
			query:    url.Values{"schema_version": {"1.0"}, "url": {"https://www.douyin.com:8443/video/123"}},
			wantCode: "INVALID_INPUT",
		},
	}
	var unsupportedMsg, mismatchMsg string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/parse?"+tc.query.Encode(), nil))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400: %s", w.Code, w.Body.String())
			}
			payload, err := unified.Decode(w.Body.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if err := unified.ValidateEnvelopeAt(payload, 1793448000); err != nil {
				t.Fatalf("ValidateEnvelopeAt failed: %v", err)
			}
			if got := unified.Str(payload["code"]); got != tc.wantCode {
				t.Fatalf("code=%q, want %q (body=%s)", got, tc.wantCode, w.Body.String())
			}
			msg := unified.Str(unified.Map(payload["error"])["message"])
			if tc.name == "unsupported-url-host" {
				unsupportedMsg = msg
			}
			if tc.name == "platform-mismatch" {
				mismatchMsg = msg
			}
		})
	}
	if unsupportedMsg == "" || mismatchMsg == "" || unsupportedMsg == mismatchMsg {
		t.Fatalf("expected distinct error messages for unsupported platform (%q) vs mismatch (%q)", unsupportedMsg, mismatchMsg)
	}
}
