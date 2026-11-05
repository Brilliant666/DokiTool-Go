package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/x"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

type stubXExtractor struct {
	enabled bool
	extract func(context.Context, string) (*ytengine.Info, error)
}

func (s *stubXExtractor) Enabled() bool { return s != nil && s.enabled }

func (s *stubXExtractor) Extract(ctx context.Context, rawURL string) (*ytengine.Info, error) {
	if s.extract == nil {
		return nil, &ytengine.Error{Code: ytengine.CodeServiceNotConfigured, Message: "disabled"}
	}
	return s.extract(ctx, rawURL)
}

func TestXHTTPParseResolveAndDegradation(t *testing.T) {
	const now int64 = 1793448000
	douyinBytes, err := os.ReadFile("../../../docs/schema/unified-v1/examples/01-douyin-quality-and-cdn.json")
	if err != nil {
		t.Fatal(err)
	}
	douyinFixture, err := unified.Decode(douyinBytes)
	if err != nil {
		t.Fatal(err)
	}

	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Register(catalog.XPlatform()); err != nil {
		t.Fatal(err)
	}

	makeVideo := func(num, mediaID string) ytengine.Info {
		w720, h720 := int64(1280), int64(720)
		w1080, h1080 := int64(1920), int64(1080)
		tbr720, tbr1080 := 2176.0, 4200.0
		dur := 12.5
		return ytengine.Info{
			ID:        mediaID,
			DisplayID: "1577719286659006464",
			Title:     "Ultima - Video " + num,
			Duration:  &dur,
			Thumbnail: "https://pbs.twimg.com/ext_tw_video_thumb/" + mediaID + "/pu/img/thumb.jpg?name=orig",
			Formats: []ytengine.Format{
				{
					FormatID:     "hls-4200-1080p",
					URL:          "https://video.twimg.com/ext_tw_video/" + mediaID + "/pu/pl/1920x1080/playlist.m3u8",
					Ext:          "mp4",
					Protocol:     "m3u8_native",
					Width:        &w1080,
					Height:       &h1080,
					TBR:          &tbr1080,
					DynamicRange: "SDR",
					VCodec:       "avc1.640032",
					ACodec:       "mp4a.40.2",
				},
				{
					FormatID:     "http-2176",
					URL:          "https://video.twimg.com/ext_tw_video/" + mediaID + "/pu/vid/1280x720/vid.mp4?tag=12",
					Ext:          "mp4",
					Protocol:     "https",
					Width:        &w720,
					Height:       &h720,
					TBR:          &tbr720,
					DynamicRange: "SDR",
					HTTPHeaders:  map[string]string{"Authorization": "Bearer never-expose"},
				},
			},
		}
	}

	xExtractor := &stubXExtractor{
		enabled: true,
		extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("missing request deadline on X adapter")
			}
			ts := 1664992565.0
			likes := int64(2096)
			return &ytengine.Info{
				ID:          "1577719286659006464",
				Type:        "playlist",
				Title:       "Ultima - Test",
				Description: "Test https://t.co/Y3KEZD7Dad",
				Uploader:    "Ultima",
				UploaderID:  "UltimaShadowX",
				Timestamp:   &ts,
				LikeCount:   &likes,
				Entries: []ytengine.Info{
					makeVideo("1", "1577719236209922048"),
					makeVideo("2", "1577719239326273536"),
				},
			}, nil
		},
	}

	adapters := map[string]Adapter{
		"douyin": AdapterFunc(func(context.Context, string, int64) (unified.Object, error) {
			return unified.Clone(douyinFixture), nil
		}),
		"x": x.New(xExtractor),
	}
	h, err := New(c, adapters, func() time.Time { return time.Unix(now, 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 1. /api/platforms lists 10 platforms including x when registered.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/platforms?schema_version=1.0", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/api/platforms status=%d body=%s", w.Code, w.Body.String())
	}
	platformsEnv, err := unified.Decode(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	platforms := unified.Slice(unified.Map(platformsEnv["data"])["platforms"])
	if len(platforms) != 10 {
		t.Fatalf("expected 10 platforms with X registered, got %d", len(platforms))
	}

	// 2. /api/parse with X share text returns validated public unified-v1 envelope.
	shareInput := "看看这个推文 https://x.com/UltimaShadowX/status/1577719286659006464?s=20！"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/parse?schema_version=1.0&url="+url.QueryEscape(shareInput), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/api/parse X status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "never-expose") {
		t.Fatalf("private HTTP headers leaked into HTTP response: %s", w.Body.String())
	}
	parseOut, err := unified.Decode(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := unified.ValidateEnvelopeAt(parseOut, now); err != nil {
		t.Fatalf("public X response failed schema validation: %v", err)
	}
	work := unified.Map(parseOut["data"])
	items := unified.Slice(work["items"])
	if len(items) != 2 {
		t.Fatalf("expected 2 items in X work, got %d", len(items))
	}
	secondItemID := unified.Str(unified.Map(items[1])["id"])

	// 3. /api/resolve selects the requested item while preserving the whole work.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(
		http.MethodGet,
		"/api/resolve?schema_version=1.0&url="+url.QueryEscape(shareInput)+"&item_id="+url.QueryEscape(secondItemID),
		nil,
	))
	if w.Code != http.StatusOK {
		t.Fatalf("/api/resolve X status=%d body=%s", w.Code, w.Body.String())
	}
	resolveOut, err := unified.Decode(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if unified.Str(unified.Map(resolveOut["data"])["default_item_id"]) != secondItemID {
		t.Fatalf("expected resolve to select %q, got %v", secondItemID, unified.Map(resolveOut["data"])["default_item_id"])
	}

	// 4. Degrade X engine (e.g. missing or mismatched yt-dlp binary):
	// X returns 503 SERVICE_NOT_CONFIGURED cleanly while existing platforms (douyin) remain 200 OK.
	xExtractor.enabled = false
	xExtractor.extract = nil

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/parse?schema_version=1.0&url="+url.QueryEscape(shareInput), nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when X ytengine is disabled, got %d: %s", w.Code, w.Body.String())
	}
	errOut, err := unified.Decode(w.Body.Bytes())
	if err != nil || unified.Str(errOut["code"]) != "SERVICE_NOT_CONFIGURED" {
		t.Fatalf("expected SERVICE_NOT_CONFIGURED envelope, got %v (err=%v)", errOut, err)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/123", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("existing douyin platform broke when X was degraded: status=%d body=%s", w.Code, w.Body.String())
	}
}
