package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/instagram"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/x"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/youtube"
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
	var clockNow atomic.Int64
	clockNow.Store(now)
	h, err := New(c, adapters, func() time.Time { return time.Unix(clockNow.Load(), 0) }, nil)
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
	// After cache TTL expires, X returns 503 SERVICE_NOT_CONFIGURED cleanly while existing platforms (douyin) remain 200 OK.
	clockNow.Add(61)
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

type staticPublicResolver struct{}

func (staticPublicResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("104.244.42.1")}, nil
}

func countInvocationMarkers(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read invocation dir %s: %v", dir, err)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "inv.") {
			count++
		}
	}
	return count
}

func TestYTDLPBusyDoesNotExhaustGlobalParsePoolForLegacyPlatforms(t *testing.T) {
	const now int64 = 1793448000
	douyinBytes, err := os.ReadFile("../../../docs/schema/unified-v1/examples/01-douyin-quality-and-cdn.json")
	if err != nil {
		t.Fatal(err)
	}
	douyinFixture, err := unified.Decode(douyinBytes)
	if err != nil {
		t.Fatal(err)
	}

	coordDir := t.TempDir()
	invDir := filepath.Join(coordDir, "invocations")
	if err := os.MkdirAll(invDir, 0o755); err != nil {
		t.Fatal(err)
	}
	releaseFile := filepath.Join(coordDir, "release.flag")
	scriptPath := filepath.Join(coordDir, "yt-dlp")
	scriptBody := fmt.Sprintf("#!/bin/sh\nset -eu\nmktemp \"%s/inv.XXXXXX\" >/dev/null\nwhile [ ! -f %q ]; do\n  sleep 0.01\ndone\nprintf '%%s\\n' '{\"id\":\"1577719286659006464\",\"title\":\"Busy isolation test\",\"formats\":[{\"format_id\":\"http-2176\",\"url\":\"https://video.twimg.com/ext_tw_video/1577719236209922048/pu/vid/1280x720/vid.mp4\",\"ext\":\"mp4\",\"protocol\":\"https\",\"width\":1280,\"height\":720,\"vcodec\":\"avc1.64001f\",\"acodec\":\"mp4a.40.2\"}]}'\n",
		invDir, releaseFile)
	if err := os.WriteFile(scriptPath, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(scriptBody))
	digest := hex.EncodeToString(sum[:])

	baseEng, err := ytengine.New(ytengine.Config{
		BinaryPath:     scriptPath,
		AllowedHashes:  map[string]string{digest: "yt-dlp"},
		MaxConcurrency: 2,
		Timeout:        25 * time.Second,
		Resolver:       staticPublicResolver{},
	})
	if err != nil || !baseEng.Enabled() {
		t.Fatalf("ytengine.New failed: %v", err)
	}

	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []unified.Object{catalog.XPlatform(), catalog.YouTubePlatform(), catalog.InstagramPlatform()} {
		if err := c.Register(p); err != nil {
			t.Fatal(err)
		}
	}

	adapters := map[string]Adapter{
		"douyin": AdapterFunc(func(context.Context, string, int64) (unified.Object, error) {
			return unified.Clone(douyinFixture), nil
		}),
		"x":         x.New(baseEng.WithTargetPolicy(x.AllowedTargetURL, x.AllowedOutboundHost)),
		"youtube":   youtube.New(baseEng.WithTargetPolicy(youtube.AllowedTargetURL, youtube.AllowedOutboundHost)),
		"instagram": instagram.New(baseEng.WithTargetPolicy(instagram.AllowedTargetURL, instagram.AllowedOutboundHost)),
	}
	var clockNow atomic.Int64
	clockNow.Store(now)
	h, err := New(c, adapters, func() time.Time { return time.Unix(clockNow.Load(), 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	admCfg := DefaultAdmissionConfig()
	entry, err := WithAdmissionConfig(h, nil, "caddy", testEdgeKey, admCfg)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Occupy the 2 shared yt-dlp execution slots from 2 distinct client IPs.
	var inFlightWG sync.WaitGroup
	inFlightCodes := make([]int, 2)
	inFlightDone := make(chan int, 2)
	inFlightWG.Add(2)
	t.Cleanup(func() {
		_ = os.WriteFile(releaseFile, []byte("cleanup"), 0o600)
		inFlightWG.Wait()
	})
	go func() {
		defer inFlightWG.Done()
		req := edgeRequest("/api/parse?schema_version=1.0&url="+url.QueryEscape("https://x.com/UltimaShadowX/status/1577719286659006464"), "198.51.100.1")
		inFlightCodes[0] = call(entry, req).Code
		inFlightDone <- inFlightCodes[0]
	}()
	go func() {
		defer inFlightWG.Done()
		req := edgeRequest("/api/parse?schema_version=1.0&url="+url.QueryEscape("https://www.youtube.com/watch?v=dQw4w9WgXcQ"), "198.51.100.2")
		inFlightCodes[1] = call(entry, req).Code
		inFlightDone <- inFlightCodes[1]
	}()

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelWait()
	for {
		if n := countInvocationMarkers(t, invDir); n >= 2 {
			if n != 2 {
				t.Fatalf("expected exactly 2 started yt-dlp subprocesses, got %d", n)
			}
			break
		}
		select {
		case code := <-inFlightDone:
			t.Fatalf("in-flight yt-dlp request exited before release with HTTP %d", code)
		case <-waitCtx.Done():
			t.Fatalf("timed out waiting for 2 yt-dlp subprocesses to start (saw %d)", countInvocationMarkers(t, invDir))
		case <-time.After(5 * time.Millisecond):
		}
	}

	// 2. Send 8 additional yt-dlp requests across distinct client IPs while the 2 slots are full.
	// They must return 429 RATE_LIMITED ("本地解析引擎繁忙，请稍后重试") immediately without occupying the 8-slot parse pool.
	for i := 0; i < 8; i++ {
		clientIP := fmt.Sprintf("198.51.100.%d", 10+i)
		req := edgeRequest("/api/parse?schema_version=1.0&url="+url.QueryEscape("https://www.instagram.com/reel/C123456/"), clientIP)
		w := call(entry, req)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429 when yt-dlp slots full, got %d: %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Retry-After") != "1" {
			t.Fatalf("expected Retry-After: 1 header on local busy response, got %q", w.Header().Get("Retry-After"))
		}
		env, err := unified.Decode(w.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if err := unified.ValidateEnvelopeAt(env, now); err != nil {
			t.Fatalf("busy response failed unified schema validation: %v", err)
		}
		errObj := unified.Map(env["error"])
		if unified.Str(env["code"]) != "RATE_LIMITED" ||
			unified.Str(errObj["message"]) != "本地解析引擎繁忙，请稍后重试" ||
			errObj["retryable"] != true {
			t.Fatalf("unexpected busy error payload: %v", env)
		}
	}

	// 3. While the 2 yt-dlp slots are STILL occupied, legacy platform (douyin) requests from multiple client IPs
	// must enter the global /api/parse pool and succeed with 200 OK.
	for i := 0; i < 6; i++ {
		clientIP := fmt.Sprintf("198.51.100.%d", 30+i)
		req := edgeRequest("/api/parse?schema_version=1.0&url=https://www.douyin.com/video/123", clientIP)
		w := call(entry, req)
		if w.Code != http.StatusOK {
			t.Fatalf("legacy douyin request blocked while yt-dlp was full (client %s): status=%d body=%s", clientIP, w.Code, w.Body.String())
		}
	}

	// Verify only 2 yt-dlp subprocesses were spawned during the entire busy window.
	if got := countInvocationMarkers(t, invDir); got != 2 {
		t.Fatalf("expected only 2 spawned subprocesses, got %d", got)
	}

	// 4. Release the 2 in-flight yt-dlp subprocesses and verify they complete cleanly.
	if err := os.WriteFile(releaseFile, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	inFlightWG.Wait()
	if inFlightCodes[0] != http.StatusOK {
		t.Fatalf("expected first in-flight X request to return 200 OK after release, got %d", inFlightCodes[0])
	}

	// 5. Subsequent X request after slots are freed (and short cache TTL expires) succeeds with 200 OK.
	clockNow.Add(61)
	reqAfter := edgeRequest("/api/parse?schema_version=1.0&url="+url.QueryEscape("https://x.com/UltimaShadowX/status/1577719286659006464"), "198.51.100.50")
	wAfter := call(entry, reqAfter)
	if wAfter.Code != http.StatusOK {
		t.Fatalf("expected X request after slot release to return 200 OK, got %d: %s", wAfter.Code, wAfter.Body.String())
	}
	if got := countInvocationMarkers(t, invDir); got != 3 {
		t.Fatalf("expected 3 total spawned subprocesses after slot release, got %d", got)
	}
}

func TestInstagramHTTPParseAndResolveDefaultConsistency(t *testing.T) {
	const now int64 = 1793448000
	w720, h720 := int64(720), int64(1280)
	w480, h480 := int64(480), int64(854)

	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Register(catalog.InstagramPlatform()); err != nil {
		t.Fatal(err)
	}

	igAdapter := instagram.New(&stubXExtractor{
		enabled: true,
		extract: func(_ context.Context, _ string) (*ytengine.Info, error) {
			return &ytengine.Info{
				ID:       "CTApqC8BGWd",
				Title:    "Video by mrcigar",
				Uploader: "RON GOH",
				Formats: []ytengine.Format{
					{
						FormatID: "dash-high-1",
						URL:      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t2/f2/m86/dash720.mp4",
						Ext:      "mp4",
						Protocol: "https",
						Width:    &w720,
						Height:   &h720,
						VCodec:   "avc1.64001F",
						ACodec:   "none",
					},
					{
						FormatID: "dash-high-2",
						URL:      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t2/f2/m86/dash480.mp4",
						Ext:      "mp4",
						Protocol: "https",
						Width:    &w480,
						Height:   &h480,
						VCodec:   "avc1.64001F",
						ACodec:   "none",
					},
					{
						FormatID: "1",
						URL:      "https://scontent-nrt1-2.cdninstagram.com/o1/v/t16/f2/m69/progressive_default.mp4",
						Ext:      "mp4",
						Protocol: "https",
					},
					{
						FormatID: "dash-audio",
						URL:      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t16/f2/m69/audio.mp4",
						Ext:      "m4a",
						Protocol: "https",
						VCodec:   "none",
						ACodec:   "mp4a.40.5",
					},
				},
			}, nil
		},
	})

	h, err := New(c, map[string]Adapter{"instagram": igAdapter}, func() time.Time { return time.Unix(now, 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}

	reelURL := "https://www.instagram.com/reel/CTApqC8BGWd/"
	var expectedItemID, expectedVariantID, expectedSourceID string

	for _, endpoint := range []string{"/api/parse", "/api/resolve"} {
		for _, debug := range []string{"0", "1"} {
			target := endpoint + "?schema_version=1.0&url=" + url.QueryEscape(reelURL) + "&debug=" + debug
			if endpoint == "/api/resolve" {
				target += "&item_id=" + url.QueryEscape(expectedItemID)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("%s debug=%s status=%d body=%s", endpoint, debug, w.Code, w.Body.String())
			}
			env, err := unified.Decode(w.Body.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if err := unified.ValidateEnvelopeAt(env, now); err != nil {
				t.Fatalf("%s debug=%s failed ValidateEnvelopeAt: %v", endpoint, debug, err)
			}
			item := unified.Map(unified.Slice(unified.Map(env["data"])["items"])[0])
			def := unified.Map(item["default"])
			if expectedItemID == "" {
				expectedItemID = unified.Str(item["id"])
				expectedVariantID = unified.Str(def["variant_id"])
				expectedSourceID = unified.Str(def["source_id"])
			}
			if unified.Str(def["variant_id"]) != expectedVariantID || unified.Str(def["source_id"]) != expectedSourceID {
				t.Fatalf("%s debug=%s default mismatch: got %+v, want variant=%s source=%s",
					endpoint, debug, def, expectedVariantID, expectedSourceID)
			}
			if !strings.Contains(unified.Str(def["url"]), "progressive_default.mp4") {
				t.Fatalf("%s debug=%s expected progressive_default.mp4 default, got %v", endpoint, debug, def["url"])
			}
		}
	}
}
