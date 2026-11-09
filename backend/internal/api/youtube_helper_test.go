package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type stubYouTubeHelper struct {
	ctxPayload unified.Object
	ctxStatus  int
	finPayload unified.Object
	finStatus  int
	lastBody   []byte
	lastDebug  bool
}

func (s *stubYouTubeHelper) Context(context.Context) (unified.Object, int) {
	return s.ctxPayload, s.ctxStatus
}

func (s *stubYouTubeHelper) Finalize(_ context.Context, body []byte, debug bool) (unified.Object, int) {
	s.lastBody = append([]byte(nil), body...)
	s.lastDebug = debug
	return s.finPayload, s.finStatus
}

func TestYouTubeHelperRoutesDisabledByDefaultAndUnchangedParse(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Register(catalog.YouTubePlatform()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	parseCalled := false
	h, err := New(c, map[string]Adapter{
		"youtube": AdapterFunc(func(_ context.Context, source string, ts int64) (unified.Object, error) {
			parseCalled = true
			return unified.FinalizeData(unified.Object{
				"platform":        "youtube",
				"content_id":      "I9JvDaciaFk",
				"source_url":      source,
				"default_item_id": "video-1",
				"items": unified.Array{
					unified.Object{
						"id":          "video-1",
						"type":        "video",
						"index":       int64(1),
						"default":     nil,
						"resolve_url": unified.ResolveURL(source, "video-1"),
						"variants": unified.Array{
							unified.Object{
								"id":    "v-18",
								"label": "360p",
								"metadata": unified.Object{
									"evidence":    "upstream",
									"has_audio":   true,
									"container":   "mp4",
									"extension":   "mp4",
									"mime_type":   "video/mp4",
									"width":       int64(640),
									"height":      int64(360),
									"video_codec": "avc1.42001E",
									"audio_codec": "mp4a.40.2",
								},
								"delivery": unified.Object{
									"kind":    "file",
									"sources": unified.Sources(unified.Array{"https://rr1---sn-npoe7n7r.googlevideo.com/videoplayback?itag=18"}, ts),
								},
							},
						},
					},
				},
				"generated_at": "2026-11-09T12:00:00Z",
			}, ts), nil
		}),
	}, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 1. With helper disabled, /api/youtube/context and /api/youtube/finalize return 404.
	reqCtx := httptest.NewRequest(http.MethodGet, "/api/youtube/context?schema_version=1.0", nil)
	recCtx := httptest.NewRecorder()
	h.ServeHTTP(recCtx, reqCtx)
	if recCtx.Code != http.StatusNotFound {
		t.Fatalf("GET /api/youtube/context status = %d, want 404", recCtx.Code)
	}

	reqFin := httptest.NewRequest(http.MethodPost, "/api/youtube/finalize?schema_version=1.0", strings.NewReader(`{}`))
	recFin := httptest.NewRecorder()
	h.ServeHTTP(recFin, reqFin)
	if recFin.Code != http.StatusNotFound {
		t.Fatalf("POST /api/youtube/finalize status = %d, want 404", recFin.Code)
	}

	// 2. Existing /api/parse for YouTube remains unchanged.
	reqParse := httptest.NewRequest(http.MethodGet, "/api/parse?schema_version=1.0&url=https%3A%2F%2Fwww.youtube.com%2Fwatch%3Fv%3DI9JvDaciaFk", nil)
	recParse := httptest.NewRecorder()
	h.ServeHTTP(recParse, reqParse)
	if recParse.Code != http.StatusOK || !parseCalled {
		t.Fatalf("GET /api/parse status = %d, parseCalled = %v, body = %s", recParse.Code, parseCalled, recParse.Body.String())
	}
}

func TestYouTubeHelperRoutesEnabledAndParseAdmission(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(c, nil, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	stub := &stubYouTubeHelper{
		ctxPayload: unified.Object{
			"schema_version": "1.0",
			"code":           "OK",
			"data": unified.Object{
				"player_id":           "a5c2aafe",
				"signature_timestamp": int64(20760),
				"client":              unified.Object{"name": "TVHTML5", "version": "7.20260707.07.00"},
				"expires_at":          "2026-11-09T18:00:00Z",
			},
		},
		ctxStatus: http.StatusOK,
		finPayload: unified.Object{
			"schema_version": "1.0",
			"code":           "OK",
			"data":           unified.Object{"platform": "youtube"},
		},
		finStatus: http.StatusOK,
	}
	h.SetYouTubeHelper(stub)

	// Method checks
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/youtube/context?schema_version=1.0", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET" {
		t.Fatalf("POST /api/youtube/context status = %d Allow = %q", rec.Code, rec.Header().Get("Allow"))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/youtube/finalize?schema_version=1.0", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Fatalf("GET /api/youtube/finalize status = %d Allow = %q", rec.Code, rec.Header().Get("Allow"))
	}

	// Body limit 512 KiB check at HTTP boundary
	overLimit := bytes.Repeat([]byte("a"), maxYouTubeFinalizeBodyBytes+1)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/youtube/finalize?schema_version=1.0", bytes.NewReader(overLimit)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized finalize body status = %d, want 400", rec.Code)
	}

	// Parse admission pool enforcement
	cfg := DefaultAdmissionConfig()
	cfg.Parse.ClientBurst = 1
	cfg.Parse.ClientRPS = 0.1
	entry, err := WithAdmissionConfig(h, nil, "direct", "", cfg)
	if err != nil {
		t.Fatal(err)
	}

	req1 := httptest.NewRequest(http.MethodGet, "/api/youtube/context?schema_version=1.0", nil)
	req1.RemoteAddr = "203.0.113.10:12345"
	rec1 := httptest.NewRecorder()
	entry.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/youtube/finalize?schema_version=1.0", strings.NewReader(`{}`))
	req2.RemoteAddr = "203.0.113.10:12345"
	rec2 := httptest.NewRecorder()
	entry.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request sharing Parse admission pool status = %d, want 429", rec2.Code)
	}
}
