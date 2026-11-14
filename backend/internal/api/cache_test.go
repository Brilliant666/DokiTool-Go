package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/instagram"
	weiboplatform "github.com/Brilliant666/DokiTool-Go/backend/internal/platform/weibo"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/x"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/ytgeneric"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

func loadCacheTestCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []unified.Object{
		catalog.XPlatform(),
		catalog.YouTubePlatform(),
		catalog.InstagramPlatform(),
	} {
		if err := c.Register(extra); err != nil {
			t.Fatal(err)
		}
	}
	for _, siteID := range []string{"facebook", "tumblr", "dailymotion"} {
		spec, ok := ytgeneric.LookupSite(siteID)
		if !ok {
			t.Fatalf("missing builtin site %q", siteID)
		}
		if err := c.Register(spec.CatalogEntry()); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func loadDouyinSampleEnvelope(t *testing.T) unified.Object {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/schema/unified-v1/examples/01-douyin-quality-and-cdn.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := unified.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func makeSyntheticEnvelope(platform, sourceURL string, now int64, expiresAt string, itemIDs ...string) unified.Object {
	if len(itemIDs) == 0 {
		itemIDs = []string{"item-1"}
	}
	items := make(unified.Array, 0, len(itemIDs))
	for i, id := range itemIDs {
		src := unified.Object{
			"id":  "s-" + id,
			"url": "https://cdn.example.com/" + platform + "/" + id + ".mp4",
		}
		def := unified.Object{
			"variant_id": "v-" + id,
			"source_id":  "s-" + id,
			"url":        "https://cdn.example.com/" + platform + "/" + id + ".mp4",
			"access":     "direct",
		}
		if expiresAt != "" {
			src["expires_at"] = expiresAt
			def["expires_at"] = expiresAt
		}
		items = append(items, unified.Object{
			"id":          id,
			"index":       i + 1,
			"type":        "video",
			"source_url":  sourceURL,
			"resolve_url": unified.ResolveURL(sourceURL, id),
			"default":     def,
			"variants": unified.Array{
				unified.Object{
					"id": "v-" + id,
					"metadata": unified.Object{
						"container": "mp4",
						"extension": "mp4",
						"mime_type": "video/mp4",
						"width":     1920,
						"height":    1080,
						"has_audio": true,
						"evidence":  "upstream",
					},
					"delivery": unified.Object{
						"kind":    "file",
						"sources": unified.Array{src},
					},
				},
			},
		})
	}
	data := unified.Object{
		"platform":        platform,
		"source_url":      sourceURL,
		"refresh_url":     "/api/parse?schema_version=1.0&url=" + unified.RFC3986Query(sourceURL),
		"generated_at":    time.Unix(now, 0).UTC().Format(time.RFC3339),
		"default_item_id": itemIDs[0],
		"items":           items,
	}
	return unified.Object{
		"schema_version": "1.0",
		"code":           "OK",
		"data":           data,
	}
}

func TestCacheConfigValidation(t *testing.T) {
	c := loadCacheTestCatalog(t)
	def := DefaultCacheConfig()
	if err := def.Validate(); err != nil {
		t.Fatalf("DefaultCacheConfig failed validation: %v", err)
	}
	if def.MaxEntries != 4096 || def.MaxBytes != 64<<20 || def.Disabled {
		t.Fatalf("unexpected DefaultCacheConfig: %+v", def)
	}

	if err := (CacheConfig{Disabled: true}).Validate(); err != nil {
		t.Fatalf("Disabled CacheConfig failed validation: %v", err)
	}

	invalidConfigs := []CacheConfig{
		{MaxEntries: 0, MaxBytes: 64 << 20},
		{MaxEntries: -1, MaxBytes: 64 << 20},
		{MaxEntries: maxCacheMaxEntries + 1, MaxBytes: 64 << 20},
		{MaxEntries: 128, MaxBytes: 0},
		{MaxEntries: 128, MaxBytes: minCacheMaxBytes - 1},
		{MaxEntries: 128, MaxBytes: maxCacheMaxBytes + 1},
		{Disabled: true, MaxEntries: -1, MaxBytes: 0},
		{Disabled: true, MaxEntries: maxCacheMaxEntries + 1, MaxBytes: 0},
		{Disabled: true, MaxEntries: 0, MaxBytes: -1},
		{Disabled: true, MaxEntries: 0, MaxBytes: maxCacheMaxBytes + 1},
	}
	for i, bad := range invalidConfigs {
		if err := bad.Validate(); err == nil {
			t.Fatalf("case %d (%+v): expected Validate error, got nil", i, bad)
		}
		if h, err := NewWithCacheConfig(c, nil, time.Now, nil, bad); err == nil || h != nil {
			t.Fatalf("case %d (%+v): expected NewWithCacheConfig failure, got h=%v err=%v", i, bad, h, err)
		}
	}
}

func TestParseCache50ConcurrentRequestsCoalesceToSingleUpstreamParse(t *testing.T) {
	const baseNow int64 = 1793448000
	c := loadCacheTestCatalog(t)

	var (
		upstreamCalls atomic.Int32
		startedOnce   sync.Once
		started       = make(chan struct{})
		release       = make(chan struct{})
	)
	adapter := AdapterFunc(func(ctx context.Context, source string, now int64) (unified.Object, error) {
		upstreamCalls.Add(1)
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return makeSyntheticEnvelope("x", source, now, "", "tweet-vid-1", "tweet-vid-2"), nil
	})

	h, err := New(c, map[string]Adapter{"x": adapter}, func() time.Time { return time.Unix(baseNow, 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}

	variants := []string{
		"https://x.com/user/status/1800000000000000001",
		"HTTPS://X.COM:443/user/status/1800000000000000001#top",
		"https://twitter.com/user/status/1800000000000000001",
		"https://mobile.x.com/user/status/1800000000000000001",
		"分享推文 https://www.x.com/user/status/1800000000000000001！",
	}

	const concurrency = 50
	recorders := make([]*httptest.ResponseRecorder, concurrency)
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		recorders[i] = httptest.NewRecorder()
		go func(idx int) {
			defer wg.Done()
			q := url.Values{
				"schema_version": {"1.0"},
				"url":            {variants[idx%len(variants)]},
			}
			if idx%2 == 1 {
				q.Set("platform", "x")
			}
			req := httptest.NewRequest(http.MethodGet, "/api/parse?"+q.Encode(), nil)
			h.ServeHTTP(recorders[idx], req)
		}(i)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream parse to start")
	}
	// Allow remaining concurrent goroutines to arrive and join the singleflight group.
	time.Sleep(25 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := upstreamCalls.Load(); got != 1 {
		t.Fatalf("expected 50 concurrent identical requests to trigger 1 upstream parse, got %d", got)
	}
	firstBody := recorders[0].Body.Bytes()
	for i, rec := range recorders {
		if rec.Code != http.StatusOK {
			t.Fatalf("caller %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
		if !bytes.Equal(rec.Body.Bytes(), firstBody) {
			t.Fatalf("caller %d received non-identical body", i)
		}
	}

	// Also verify 50 concurrent /api/resolve requests on an uncached URL + item_id trigger 1 upstream parse.
	var (
		resolveCalls   atomic.Int32
		resolveStarted sync.Once
		resStartCh     = make(chan struct{})
		resReleaseCh   = make(chan struct{})
	)
	hResolve, err := New(c, map[string]Adapter{
		"x": AdapterFunc(func(ctx context.Context, source string, now int64) (unified.Object, error) {
			resolveCalls.Add(1)
			resolveStarted.Do(func() { close(resStartCh) })
			select {
			case <-resReleaseCh:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return makeSyntheticEnvelope("x", source, now, "", "tweet-vid-1", "tweet-vid-2"), nil
		}),
	}, func() time.Time { return time.Unix(baseNow, 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	resRecs := make([]*httptest.ResponseRecorder, concurrency)
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		resRecs[i] = httptest.NewRecorder()
		go func(idx int) {
			defer wg.Done()
			q := url.Values{
				"schema_version": {"1.0"},
				"url":            {variants[idx%len(variants)]},
				"item_id":        {"tweet-vid-2"},
			}
			hResolve.ServeHTTP(resRecs[idx], httptest.NewRequest(http.MethodGet, "/api/resolve?"+q.Encode(), nil))
		}(i)
	}
	<-resStartCh
	time.Sleep(25 * time.Millisecond)
	close(resReleaseCh)
	wg.Wait()
	if got := resolveCalls.Load(); got != 1 {
		t.Fatalf("expected 50 concurrent /api/resolve requests to trigger 1 upstream parse, got %d", got)
	}
	for i, rec := range resRecs {
		if rec.Code != http.StatusOK {
			t.Fatalf("resolve caller %d status=%d", i, rec.Code)
		}
	}
}

func TestNormalizeCacheURLAliasesAndDistinctQueries(t *testing.T) {
	equivalentGroups := []struct {
		platform string
		inputs   []string
		want     string
	}{
		{
			platform: "x",
			inputs: []string{
				"https://x.com/user/status/123",
				"http://x.com:80/user/status/123",
				"HTTPS://WWW.X.COM:443/user/status/123#reply",
				"https://twitter.com/user/status/123",
				"https://www.twitter.com/user/status/123",
				"https://mobile.twitter.com/user/status/123",
				"https://m.twitter.com/user/status/123",
				"https://mobile.x.com/user/status/123",
				"https://m.x.com/user/status/123",
			},
			want: "https://x.com/user/status/123",
		},
		{
			platform: "instagram",
			inputs: []string{
				"https://instagram.com/reel/CTApqC8BGWd/",
				"https://m.instagram.com/reel/CTApqC8BGWd/",
				"http://www.instagram.com:80/reel/CTApqC8BGWd/",
				"https://www.instagram.com:443/reel/CTApqC8BGWd/#ig",
			},
			want: "https://www.instagram.com/reel/CTApqC8BGWd/",
		},
		{
			platform: "facebook",
			inputs: []string{
				"https://facebook.com/reel/1517545242764683",
				"https://m.facebook.com/reel/1517545242764683",
				"https://www.facebook.com/reel/1517545242764683#t=1",
			},
			want: "https://www.facebook.com/reel/1517545242764683",
		},
		{
			platform: "tumblr",
			inputs: []string{
				"https://tumblr.com/blog/123",
				"https://www.tumblr.com/blog/123",
			},
			want: "https://www.tumblr.com/blog/123",
		},
		{
			platform: "dailymotion",
			inputs: []string{
				"https://dai.ly/x8t9abc",
				"https://dai.ly/x8t9abc/",
				"https://dai.ly/x8t9abc?",
				"https://dailymotion.com/video/x8t9abc",
				"https://www.dailymotion.com/video/x8t9abc#start",
			},
			want: "https://www.dailymotion.com/video/x8t9abc",
		},
		{
			platform: "dailymotion",
			inputs: []string{
				"https://dai.ly/x8t9abc?playlist=1",
				"https://dailymotion.com/video/x8t9abc?playlist=1#frag",
				"https://www.dailymotion.com/video/x8t9abc?playlist=1",
			},
			want: "https://www.dailymotion.com/video/x8t9abc?playlist=1",
		},
	}
	for _, grp := range equivalentGroups {
		for _, in := range grp.inputs {
			if got := normalizeCacheURL(grp.platform, in); got != grp.want {
				t.Fatalf("normalizeCacheURL(%q, %q) = %q, want %q", grp.platform, in, got, grp.want)
			}
		}
	}

	// Invalid dai.ly paths MUST NOT rewrite onto www.dailymotion.com/video/...
	for _, badShort := range []string{
		"https://dai.ly///x8t9abc///",
		"https://dai.ly/foo-bar",
		"https://dai.ly/",
		"https://dai.ly/x8t9abc/extra",
		"https://dai.ly/" + strings.Repeat("a", 65),
	} {
		got := normalizeCacheURL("dailymotion", badShort)
		if strings.Contains(got, "dailymotion.com/video/") {
			t.Fatalf("invalid dai.ly URL %q rewrote to %q", badShort, got)
		}
	}

	// >2048-byte inputs on strict platforms MUST NEVER collide with <=2048-byte keys
	// (including when a valid <=2048-byte dai.ly short link expands past 2048 bytes).
	validShortExpanded := "https://dai.ly/x8t9abc?" + strings.Repeat("a", 2020)                 // len == 2043 <= 2048
	invalidExpanded := "https://www.dailymotion.com/video/x8t9abc?" + strings.Repeat("a", 2020) // len == 2062 > 2048
	if normalizeCacheURL("dailymotion", validShortExpanded) == normalizeCacheURL("dailymotion", invalidExpanded) {
		t.Fatal("overlong dailymotion URL collided with expanded valid dai.ly short URL")
	}

	// Distinct media selectors / query tokens MUST remain distinct.
	if normalizeCacheURL("x", "https://x.com/u/status/1/photo/1") == normalizeCacheURL("x", "https://x.com/u/status/1/photo/2") {
		t.Fatal("X /photo/1 and /photo/2 collided")
	}
	if normalizeCacheURL("xiaohongshu", "https://www.xiaohongshu.com/explore/1?xsec_token=a") ==
		normalizeCacheURL("xiaohongshu", "https://www.xiaohongshu.com/explore/1?xsec_token=b") {
		t.Fatal("XHS xsec_token collided")
	}
}

func TestWarmCacheDoesNotBypassPlatformURLValidation(t *testing.T) {
	const nowUnix int64 = 1793448000
	c := loadCacheTestCatalog(t)

	w720, h720 := int64(1280), int64(720)
	dur := 10.0
	tbr := 1800.0

	var dmExtracts atomic.Int32
	mkExtractor := func(mediaURL, thumbURL string, counter *atomic.Int32) ytengine.Extractor {
		return &stubXExtractor{
			enabled: true,
			extract: func(_ context.Context, rawURL string) (*ytengine.Info, error) {
				if counter != nil {
					counter.Add(1)
				}
				return &ytengine.Info{
					ID:         "vid123456",
					DisplayID:  "vid123456",
					Title:      "Sample",
					Duration:   &dur,
					Thumbnail:  thumbURL,
					WebpageURL: rawURL,
					Formats: []ytengine.Format{
						{
							FormatID: "mp4-720p",
							URL:      mediaURL,
							Ext:      "mp4",
							Protocol: "https",
							Width:    &w720,
							Height:   &h720,
							TBR:      &tbr,
							VCodec:   "avc1.64001f",
							ACodec:   "mp4a.40.2",
						},
					},
				}, nil
			},
		}
	}
	dmExtractor := &stubXExtractor{
		enabled: true,
		extract: func(_ context.Context, rawURL string) (*ytengine.Info, error) {
			dmExtracts.Add(1)
			return &ytengine.Info{
				ID:         "x8t9abc",
				DisplayID:  "x8t9abc",
				Title:      "Dailymotion Sample",
				Duration:   &dur,
				Thumbnail:  "https://s2.dmcdn.net/v/x8t9abc/x1080",
				WebpageURL: rawURL,
				Formats: []ytengine.Format{
					{
						FormatID: "hls-1080",
						URL:      "https://vod2.cf.dmcdn.net/sec2(tok)/cloud/fmp4/1/h264_aac_fhd/1/manifest.m3u8",
						Ext:      "mp4",
						Protocol: "m3u8_native",
						Width:    &w720,
						Height:   &h720,
						TBR:      &tbr,
						VCodec:   "avc1.64001f",
						ACodec:   "mp4a.40.2",
					},
				},
			}, nil
		},
	}

	fbSpec, _ := ytgeneric.LookupSite("facebook")
	tumblrSpec, _ := ytgeneric.LookupSite("tumblr")
	dmSpec, _ := ytgeneric.LookupSite("dailymotion")

	h, err := New(c, map[string]Adapter{
		"x":           x.New(mkExtractor("https://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.mp4", "https://pbs.twimg.com/media/a.jpg", nil)),
		"instagram":   instagram.New(mkExtractor("https://scontent.cdninstagram.com/v/t50.2886-16/100_n.mp4", "https://scontent.cdninstagram.com/v/t51.2885-15/100_n.jpg", nil)),
		"facebook":    ytgeneric.NewWithResolver(fbSpec, mkExtractor("https://video.xx.fbcdn.net/v/t42.1790-2/100_n.mp4", "https://scontent.xx.fbcdn.net/v/t15.5256-10/100_n.jpg", nil), nil, staticPublicResolver{}),
		"tumblr":      ytgeneric.NewWithResolver(tumblrSpec, mkExtractor("https://va.media.tumblr.com/tumblr_abc_720.mp4", "https://64.media.tumblr.com/thumb.jpg", nil), nil, staticPublicResolver{}),
		"dailymotion": ytgeneric.NewWithResolver(dmSpec, dmExtractor, nil, staticPublicResolver{}),
	}, func() time.Time { return time.Unix(nowUnix, 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}

	callParse := func(rawURL string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		q := url.Values{"schema_version": {"1.0"}, "url": {rawURL}}
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/parse?"+q.Encode(), nil))
		return w
	}

	// 1. Warm Dailymotion entries and verify dai.ly validation parity + query rewrite.
	for _, warmURL := range []string{
		"https://www.dailymotion.com/video/x8t9abc",
		"https://www.dailymotion.com/video/foo-bar",
		"https://dai.ly/x8t9abc?playlist=1",
	} {
		if w := callParse(warmURL); w.Code != http.StatusOK {
			t.Fatalf("warm %q status=%d body=%s", warmURL, w.Code, w.Body.String())
		}
	}
	beforeShared := dmExtracts.Load()
	if w := callParse("https://www.dailymotion.com/video/x8t9abc?playlist=1"); w.Code != http.StatusOK || dmExtracts.Load() != beforeShared {
		t.Fatalf("expected dai.ly?playlist=1 to share cache with www.dailymotion.com/video/x8t9abc?playlist=1: status=%d extracts=%d->%d",
			w.Code, beforeShared, dmExtracts.Load())
	}
	for _, invalidShort := range []string{
		"https://dai.ly///x8t9abc///",
		"https://dai.ly/foo-bar",
		"https://dai.ly/",
		"https://dai.ly/x8t9abc/extra",
	} {
		w := callParse(invalidShort)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("warm cache bypassed dai.ly validation for %q: got status=%d body=%s, want 400",
				invalidShort, w.Code, w.Body.String())
		}
	}

	// 2. Warm valid <=2048-byte URLs on all 5 strict platforms, then verify >2048-byte
	// inputs STILL return 400 INVALID_INPUT (both shrinkage and dai.ly expansion cases).
	qPad := strings.Repeat("a", 2020)
	longShortValid := "https://dai.ly/x8t9abc?" + strings.Repeat("b", 2020) // 2043 bytes <= 2048
	for _, warmURL := range []string{
		"https://x.com/user/status/1577719286659006464",
		"https://x.com/user/status/1577719286659006464?" + strings.Repeat("q", 1995),
		"https://www.instagram.com/reel/CTApqC8BGWd/",
		"https://www.facebook.com/reel/1517545242764683",
		"https://www.tumblr.com/blog/123456",
		longShortValid,
	} {
		if len(warmURL) > maxYTDLPInputBytes {
			t.Fatalf("test bug: warmURL len=%d > %d", len(warmURL), maxYTDLPInputBytes)
		}
		if w := callParse(warmURL); w.Code != http.StatusOK {
			t.Fatalf("warm %q status=%d body=%s", warmURL, w.Code, w.Body.String())
		}
	}

	overlongCases := []string{
		"https://x.com/user/status/1577719286659006464#" + qPad,
		"https://mobile.twitter.com:443/user/status/1577719286659006464?" + strings.Repeat("q", 1995),
		"https://m.x.com:443/user/status/1577719286659006464#" + qPad,
		"https://m.instagram.com:443/reel/CTApqC8BGWd/#" + qPad,
		"https://m.facebook.com:443/reel/1517545242764683#" + qPad,
		"https://tumblr.com:443/blog/123456#" + qPad,
		"https://www.dailymotion.com/video/x8t9abc#" + qPad,
		"https://www.dailymotion.com/video/x8t9abc?" + strings.Repeat("b", 2020), // 2062 bytes > 2048, matches expanded longShortValid!
	}
	for _, overlong := range overlongCases {
		if len(overlong) <= maxYTDLPInputBytes {
			t.Fatalf("test bug: overlong len=%d <= %d: %q", len(overlong), maxYTDLPInputBytes, overlong)
		}
		w := callParse(overlong)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("warm cache bypassed >2048-byte rejection for len=%d (%s...): status=%d body=%s",
				len(overlong), overlong[:48], w.Code, w.Body.String())
		}
	}
}

func TestParseCacheTTLRulesAndClockExpiry(t *testing.T) {
	const startUnix int64 = 1793448000
	c := loadCacheTestCatalog(t)

	t.Run("missing-expires_at-uses-60s-and-reparses-after-expiry", func(t *testing.T) {
		var nowSec atomic.Int64
		nowSec.Store(startUnix)
		var calls atomic.Int32
		h, err := New(c, map[string]Adapter{
			"douyin": AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
				calls.Add(1)
				return makeSyntheticEnvelope("douyin", src, now, ""), nil
			}),
		}, func() time.Time { return time.Unix(nowSec.Load(), 0) }, nil)
		if err != nil {
			t.Fatal(err)
		}

		path := "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/1001"
		for _, delta := range []int64{0, 30, 59} {
			nowSec.Store(startUnix + delta)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("delta=%ds status=%d", delta, w.Code)
			}
		}
		if calls.Load() != 1 {
			t.Fatalf("expected 1 upstream call before 60s TTL, got %d", calls.Load())
		}

		nowSec.Store(startUnix + 60)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || calls.Load() != 2 {
			t.Fatalf("expected re-parse at +60s: status=%d calls=%d", w.Code, calls.Load())
		}
	})

	t.Run("long-expires_at-capped-at-180s", func(t *testing.T) {
		var nowSec atomic.Int64
		nowSec.Store(startUnix)
		var calls atomic.Int32
		h, err := New(c, map[string]Adapter{
			"douyin": AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
				calls.Add(1)
				exp := time.Unix(now+3600, 0).UTC().Format(time.RFC3339)
				return makeSyntheticEnvelope("douyin", src, now, exp), nil
			}),
		}, func() time.Time { return time.Unix(nowSec.Load(), 0) }, nil)
		if err != nil {
			t.Fatal(err)
		}

		path := "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/1002"
		for _, delta := range []int64{0, 120, 179} {
			nowSec.Store(startUnix + delta)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("delta=%ds status=%d", delta, w.Code)
			}
		}
		if calls.Load() != 1 {
			t.Fatalf("expected cached hit through +179s, got calls=%d", calls.Load())
		}

		nowSec.Store(startUnix + 180)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || calls.Load() != 2 {
			t.Fatalf("expected re-parse at +180s cap: status=%d calls=%d", w.Code, calls.Load())
		}
	})

	t.Run("ttl-below-10s-not-cached-and-10s-boundary-cached", func(t *testing.T) {
		var nowSec atomic.Int64
		nowSec.Store(startUnix)
		var shortCalls, exactCalls atomic.Int32
		h, err := New(c, map[string]Adapter{
			"douyin": AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
				if strings.HasSuffix(src, "/short") {
					shortCalls.Add(1)
					// earliest - now - 60s = 69s - 60s = 9s (< 10s -> do NOT cache).
					exp := time.Unix(now+69, 0).UTC().Format(time.RFC3339)
					return makeSyntheticEnvelope("douyin", src, now, exp), nil
				}
				exactCalls.Add(1)
				// earliest - now - 60s = 70s - 60s = 10s (== 10s -> cache for 10s).
				exp := time.Unix(now+70, 0).UTC().Format(time.RFC3339)
				return makeSyntheticEnvelope("douyin", src, now, exp), nil
			}),
		}, func() time.Time { return time.Unix(nowSec.Load(), 0) }, nil)
		if err != nil {
			t.Fatal(err)
		}

		shortPath := "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/short"
		for i := 0; i < 2; i++ {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, shortPath, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("short status=%d", w.Code)
			}
		}
		if shortCalls.Load() != 2 {
			t.Fatalf("expected TTL=9s (<10s) not to be cached, got calls=%d", shortCalls.Load())
		}

		exactPath := "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/exact"
		for _, delta := range []int64{0, 9} {
			nowSec.Store(startUnix + delta)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, exactPath, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("exact status=%d", w.Code)
			}
		}
		if exactCalls.Load() != 1 {
			t.Fatalf("expected TTL=10s boundary to be cached at +9s, got calls=%d", exactCalls.Load())
		}
		nowSec.Store(startUnix + 10)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, exactPath, nil))
		if w.Code != http.StatusOK || exactCalls.Load() != 2 {
			t.Fatalf("expected TTL=10s boundary to expire at +10s, got status=%d calls=%d", w.Code, exactCalls.Load())
		}
	})

	t.Run("earliest-expiry-across-nested-mux-and-attachment-sources", func(t *testing.T) {
		var nowSec atomic.Int64
		nowSec.Store(startUnix)
		var calls atomic.Int32
		h, err := New(c, map[string]Adapter{
			"douyin": AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
				calls.Add(1)
				longExp := time.Unix(now+3600, 0).UTC().Format(time.RFC3339)
				// Attachment expires at now + 85s => TTL = 85s - 60s = 25s.
				shortAttExp := time.Unix(now+85, 0).UTC().Format(time.RFC3339)
				env := makeSyntheticEnvelope("douyin", src, now, longExp)
				unified.Map(env["data"])["attachments"] = unified.Array{
					unified.Object{
						"id":   "bgm",
						"role": "background_audio",
						"resource": unified.Object{
							"id":   "audio",
							"role": "audio",
							"delivery": unified.Object{
								"kind": "file",
								"sources": unified.Array{
									unified.Object{
										"id":         "s-bgm",
										"url":        "https://cdn.example.com/douyin/bgm.mp3",
										"expires_at": shortAttExp,
									},
								},
							},
						},
					},
				}
				return env, nil
			}),
		}, func() time.Time { return time.Unix(nowSec.Load(), 0) }, nil)
		if err != nil {
			t.Fatal(err)
		}

		path := "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/nested-exp"
		for _, delta := range []int64{0, 24} {
			nowSec.Store(startUnix + delta)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("delta=%ds status=%d body=%s", delta, w.Code, w.Body.String())
			}
		}
		if calls.Load() != 1 {
			t.Fatalf("expected hit at +24s, got calls=%d", calls.Load())
		}
		nowSec.Store(startUnix + 25)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || calls.Load() != 2 {
			t.Fatalf("expected nested attachment expiry to trigger re-parse at +25s, got calls=%d", calls.Load())
		}
	})

	t.Run("slow-parse-evaluates-ttl-against-finish-time", func(t *testing.T) {
		var nowSec atomic.Int64
		nowSec.Store(startUnix)
		var calls atomic.Int32
		h, err := New(c, map[string]Adapter{
			"douyin": AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
				calls.Add(1)
				// expires_at is startNow + 72s, but upstream took 5s so finishNow sees 67s left (67-60 = 7s < 10s).
				exp := time.Unix(now+72, 0).UTC().Format(time.RFC3339)
				nowSec.Add(5)
				return makeSyntheticEnvelope("douyin", src, now, exp), nil
			}),
		}, func() time.Time { return time.Unix(nowSec.Load(), 0) }, nil)
		if err != nil {
			t.Fatal(err)
		}
		path := "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/slow-exp"
		for i := 0; i < 2; i++ {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d", w.Code)
			}
		}
		if calls.Load() != 2 {
			t.Fatalf("expected slow parse leaving <70s at finish not to be cached, got calls=%d", calls.Load())
		}
	})
}

func TestParseCacheErrorCodePolicy(t *testing.T) {
	const startUnix int64 = 1793448000
	c := loadCacheTestCatalog(t)

	t.Run("CONTENT_UNAVAILABLE-cached-for-10s", func(t *testing.T) {
		var nowSec atomic.Int64
		nowSec.Store(startUnix)
		var parseCalls, resolveCalls atomic.Int32
		h, err := New(c, map[string]Adapter{
			"douyin": AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
				if strings.HasSuffix(src, "/deleted") {
					parseCalls.Add(1)
					return unified.ContractError("CONTENT_UNAVAILABLE", "作品已删除或不可见", false), nil
				}
				resolveCalls.Add(1)
				return makeSyntheticEnvelope("douyin", src, now, "", "item-1"), nil
			}),
		}, func() time.Time { return time.Unix(nowSec.Load(), 0) }, nil)
		if err != nil {
			t.Fatal(err)
		}

		parsePath := "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/deleted"
		resolveMissingPath := "/api/resolve?schema_version=1.0&url=https://www.douyin.com/video/alive&item_id=missing-part"
		for _, delta := range []int64{0, 9} {
			nowSec.Store(startUnix + delta)
			for _, p := range []string{parsePath, resolveMissingPath} {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
				if w.Code != http.StatusNotFound {
					t.Fatalf("path=%s delta=%ds status=%d, want 404", p, delta, w.Code)
				}
			}
		}
		if parseCalls.Load() != 1 || resolveCalls.Load() != 1 {
			t.Fatalf("expected CONTENT_UNAVAILABLE cached through +9s: parseCalls=%d resolveCalls=%d", parseCalls.Load(), resolveCalls.Load())
		}

		nowSec.Store(startUnix + 10)
		for _, p := range []string{parsePath, resolveMissingPath} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
			if w.Code != http.StatusNotFound {
				t.Fatalf("path=%s +10s status=%d, want 404", p, w.Code)
			}
		}
		if parseCalls.Load() != 2 || resolveCalls.Load() != 2 {
			t.Fatalf("expected CONTENT_UNAVAILABLE re-parsed at +10s: parseCalls=%d resolveCalls=%d", parseCalls.Load(), resolveCalls.Load())
		}
	})

	t.Run("all-other-errors-never-cached", func(t *testing.T) {
		nonCachedCodes := []struct {
			code       string
			retryable  bool
			wantStatus int
			goErr      error
		}{
			{code: "INVALID_INPUT", retryable: false, wantStatus: 400},
			{code: "UNSUPPORTED_PLATFORM", retryable: false, wantStatus: 400},
			{code: "NO_MEDIA", retryable: false, wantStatus: 404},
			{code: "UPSTREAM_BLOCKED", retryable: false, wantStatus: 403},
			{code: "RATE_LIMITED", retryable: true, wantStatus: 429},
			{code: "UPSTREAM_TIMEOUT", retryable: true, wantStatus: 504},
			{code: "UPSTREAM_UNAVAILABLE", retryable: true, wantStatus: 502},
			{code: "UPSTREAM_INVALID_RESPONSE", retryable: false, wantStatus: 502},
			{code: "SERVICE_NOT_CONFIGURED", retryable: false, wantStatus: 503},
			{code: "ADAPTER_ERROR", wantStatus: 502, goErr: errors.New("synthetic upstream failure")},
		}

		for _, tc := range nonCachedCodes {
			t.Run(tc.code, func(t *testing.T) {
				var calls atomic.Int32
				h, err := New(c, map[string]Adapter{
					"douyin": AdapterFunc(func(_ context.Context, _ string, _ int64) (unified.Object, error) {
						calls.Add(1)
						if tc.goErr != nil {
							return nil, tc.goErr
						}
						return unified.ContractError(tc.code, "失败："+tc.code, tc.retryable), nil
					}),
				}, func() time.Time { return time.Unix(startUnix, 0) }, nil)
				if err != nil {
					t.Fatal(err)
				}

				path := "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/err-" + tc.code
				for i := 0; i < 2; i++ {
					w := httptest.NewRecorder()
					h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
					if w.Code != tc.wantStatus {
						t.Fatalf("attempt %d status=%d, want %d", i+1, w.Code, tc.wantStatus)
					}
				}
				if calls.Load() != 2 {
					t.Fatalf("error %s was unexpectedly cached: calls=%d, want 2", tc.code, calls.Load())
				}
			})
		}
	})
}

func TestParseCacheResolveItemsAndViewsNeverCrossContaminate(t *testing.T) {
	const nowUnix int64 = 1793448000
	c := loadCacheTestCatalog(t)
	fixture := loadDouyinSampleEnvelope(t)

	var seenSources sync.Map
	var calls atomic.Int32
	h, err := New(c, map[string]Adapter{
		"douyin": AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
			calls.Add(1)
			seenSources.Store(src, true)
			return makeSyntheticEnvelope("douyin", src, now, "", "part-1", "part-2", "part-3"), nil
		}),
		"bilibili": AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
			calls.Add(1)
			partID := "BV1xx411c7mD-p1"
			if strings.Contains(src, "p=2") && !strings.HasSuffix(src, "p=1") {
				partID = "BV1xx411c7mD-p2"
			}
			return makeSyntheticEnvelope("bilibili", src, now, "", partID), nil
		}),
		"xiaohongshu": AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
			calls.Add(1)
			out := unified.Clone(fixture)
			data := unified.Map(out["data"])
			data["platform"] = "xiaohongshu"
			data["source_url"] = src
			data["refresh_url"] = "/api/parse?schema_version=1.0&url=" + unified.RFC3986Query(src)
			return out, nil
		}),
	}, func() time.Time { return time.Unix(nowUnix, 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Concurrent /api/resolve requests across part-1, part-2, part-3, and absent part-999 on the same URL.
	const perTarget = 10
	targets := []string{"part-1", "part-2", "part-3", "part-999"}
	var wg sync.WaitGroup
	errs := make(chan error, len(targets)*perTarget)
	for _, itemID := range targets {
		for i := 0; i < perTarget; i++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				q := url.Values{
					"schema_version": {"1.0"},
					"url":            {"https://www.douyin.com/video/multi-999"},
					"item_id":        {id},
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/resolve?"+q.Encode(), nil))
				if id == "part-999" {
					if w.Code != http.StatusNotFound {
						errs <- fmt.Errorf("missing item status=%d, want 404", w.Code)
					}
					return
				}
				if w.Code != http.StatusOK {
					errs <- fmt.Errorf("item %s status=%d", id, w.Code)
					return
				}
				env, decErr := unified.Decode(w.Body.Bytes())
				if decErr != nil {
					errs <- decErr
					return
				}
				gotDefault := unified.Str(unified.Map(env["data"])["default_item_id"])
				if gotDefault != id {
					errs <- fmt.Errorf("cross-contamination: requested %q, got default_item_id=%q", id, gotDefault)
				}
			}(itemID)
		}
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}

	// 2. Platform mismatch on an already-cached URL must still return 400 INVALID_INPUT.
	wMismatch := httptest.NewRecorder()
	h.ServeHTTP(wMismatch, httptest.NewRequest(http.MethodGet, "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/multi-999&platform=bilibili", nil))
	if wMismatch.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on platform mismatch against warm URL, got %d", wMismatch.Code)
	}

	// 3. Bilibili ?p=1 vs ?p=2 and duplicate query ?p=1&p=2 vs ?p=2&p=1 never collide.
	biliCases := []struct {
		rawURL string
		wantID string
	}{
		{"https://www.bilibili.com/video/BV1xx411c7mD/?p=1", "BV1xx411c7mD-p1"},
		{"https://www.bilibili.com/video/BV1xx411c7mD/?p=2", "BV1xx411c7mD-p2"},
		{"https://www.bilibili.com/video/BV1xx411c7mD/?p=1&p=2", "BV1xx411c7mD-p2"},
		{"https://www.bilibili.com/video/BV1xx411c7mD/?p=2&p=1", "BV1xx411c7mD-p1"},
	}
	for _, bc := range biliCases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/parse?schema_version=1.0&url="+url.QueryEscape(bc.rawURL), nil))
		if w.Code != http.StatusOK {
			t.Fatalf("bilibili %s status=%d body=%s", bc.rawURL, w.Code, w.Body.String())
		}
		env, err := unified.Decode(w.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if got := unified.Str(unified.Map(env["data"])["default_item_id"]); got != bc.wantID {
			t.Fatalf("bilibili %s got default_item_id=%q, want %q", bc.rawURL, got, bc.wantID)
		}
	}

	// 4. debug=0 vs debug=1 views never pollute each other's refresh_url or variants.
	xhsURL := "https://www.xiaohongshu.com/explore/66a1b2c3?xsec_token=tokA"
	wNormal := httptest.NewRecorder()
	h.ServeHTTP(wNormal, httptest.NewRequest(http.MethodGet, "/api/parse?schema_version=1.0&url="+url.QueryEscape(xhsURL), nil))
	wDebug := httptest.NewRecorder()
	h.ServeHTTP(wDebug, httptest.NewRequest(http.MethodGet, "/api/parse?schema_version=1.0&debug=1&url="+url.QueryEscape(xhsURL), nil))
	if wNormal.Code != http.StatusOK || wDebug.Code != http.StatusOK {
		t.Fatalf("xhs normal=%d debug=%d", wNormal.Code, wDebug.Code)
	}
	envNormal, _ := unified.Decode(wNormal.Body.Bytes())
	envDebug, _ := unified.Decode(wDebug.Body.Bytes())
	if strings.Contains(unified.Str(unified.Map(envNormal["data"])["refresh_url"]), "debug=1") {
		t.Fatal("normal view was polluted with debug=1 refresh_url")
	}
	if !strings.Contains(unified.Str(unified.Map(envDebug["data"])["refresh_url"]), "debug=1") {
		t.Fatal("debug=1 view lost debug=1 refresh_url")
	}
}

func TestParseCacheCanceledWaiterDoesNotAffectOtherCallers(t *testing.T) {
	const nowUnix int64 = 1793448000
	c := loadCacheTestCatalog(t)

	t.Run("leader-and-middle-waiter-cancellation-isolated", func(t *testing.T) {
		var (
			upstreamCalls atomic.Int32
			startedOnce   sync.Once
			started       = make(chan struct{})
			release       = make(chan struct{})
			sawCanceled   atomic.Bool
		)
		h, err := New(c, map[string]Adapter{
			"douyin": AdapterFunc(func(ctx context.Context, src string, now int64) (unified.Object, error) {
				upstreamCalls.Add(1)
				startedOnce.Do(func() { close(started) })
				<-release
				if ctx.Err() != nil {
					sawCanceled.Store(true)
					return nil, ctx.Err()
				}
				return makeSyntheticEnvelope("douyin", src, now, ""), nil
			}),
		}, func() time.Time { return time.Unix(nowUnix, 0) }, nil)
		if err != nil {
			t.Fatal(err)
		}

		target := "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/cancel-isolation"

		// Caller 0 is the leader that initiates the upstream flight, then cancels!
		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		leaderDone := make(chan struct{})
		go func() {
			defer close(leaderDone)
			req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(leaderCtx)
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()

		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("leader did not start upstream parse")
		}

		// Launch 9 additional waiters; waiter #4 will also cancel mid-flight.
		const waiters = 9
		midCtx, cancelMid := context.WithCancel(context.Background())
		defer cancelMid()
		recs := make([]*httptest.ResponseRecorder, waiters)
		var wg sync.WaitGroup
		wg.Add(waiters)
		for i := 0; i < waiters; i++ {
			recs[i] = httptest.NewRecorder()
			go func(idx int) {
				defer wg.Done()
				reqCtx := context.Background()
				if idx == 4 {
					reqCtx = midCtx
				}
				req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(reqCtx)
				h.ServeHTTP(recs[idx], req)
			}(i)
		}
		time.Sleep(20 * time.Millisecond)

		// Cancel BOTH the initiating leader and middle waiter #4 while upstream is still blocked!
		cancelLeader()
		cancelMid()
		select {
		case <-leaderDone:
		case <-time.After(time.Second):
			t.Fatal("canceled leader request did not exit promptly")
		}

		// Release upstream parse: all 8 remaining waiters must receive 200 OK.
		close(release)
		wg.Wait()

		if sawCanceled.Load() {
			t.Fatal("upstream parse context was canceled despite active waiters")
		}
		if upstreamCalls.Load() != 1 {
			t.Fatalf("expected 1 upstream call, got %d", upstreamCalls.Load())
		}
		for i, rec := range recs {
			if i == 4 {
				continue
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("surviving waiter %d failed with status=%d body=%s", i, rec.Code, rec.Body.String())
			}
		}

		// Result must also be cached for subsequent callers.
		wAfter := httptest.NewRecorder()
		h.ServeHTTP(wAfter, httptest.NewRequest(http.MethodGet, target, nil))
		if wAfter.Code != http.StatusOK || upstreamCalls.Load() != 1 {
			t.Fatalf("expected post-flight cache hit, got status=%d calls=%d", wAfter.Code, upstreamCalls.Load())
		}
	})

	t.Run("all-waiters-canceled-cancels-upstream-and-does-not-poison-cache", func(t *testing.T) {
		var (
			calls       atomic.Int32
			firstStart  = make(chan struct{})
			firstCancel = make(chan struct{})
		)
		h, err := New(c, map[string]Adapter{
			"douyin": AdapterFunc(func(ctx context.Context, src string, now int64) (unified.Object, error) {
				n := calls.Add(1)
				if n == 1 {
					close(firstStart)
					<-ctx.Done()
					close(firstCancel)
					return nil, ctx.Err()
				}
				return makeSyntheticEnvelope("douyin", src, now, ""), nil
			}),
		}, func() time.Time { return time.Unix(nowUnix, 0) }, nil)
		if err != nil {
			t.Fatal(err)
		}

		target := "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/all-canceled"
		ctx1, cancel1 := context.WithCancel(context.Background())
		done1 := make(chan struct{})
		go func() {
			defer close(done1)
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx1))
		}()
		<-firstStart
		cancel1()
		<-done1
		select {
		case <-firstCancel:
		case <-time.After(2 * time.Second):
			t.Fatal("upstream context was not canceled when the sole waiter disconnected")
		}

		// A subsequent caller starts a clean flight and receives 200 OK.
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, target, nil))
		if w2.Code != http.StatusOK || calls.Load() != 2 {
			t.Fatalf("subsequent request after abandoned flight failed: status=%d calls=%d", w2.Code, calls.Load())
		}
	})
}

func TestParseCachePreservesOriginalTimestampsByteForByte(t *testing.T) {
	var nowSec atomic.Int64
	nowSec.Store(1793448000) // 2026-10-31T12:00:00Z
	wantGeneratedAt := time.Unix(1793448000, 0).UTC().Format(time.RFC3339)
	c := loadCacheTestCatalog(t)
	var calls atomic.Int32

	h, err := New(c, map[string]Adapter{
		"douyin": AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
			calls.Add(1)
			exp := time.Unix(now+3600, 0).UTC().Format(time.RFC3339)
			return makeSyntheticEnvelope("douyin", src, now, exp), nil
		}),
	}, func() time.Time { return time.Unix(nowSec.Load(), 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}

	path := "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/preserve-ts"
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, httptest.NewRequest(http.MethodGet, path, nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("first call status=%d", w1.Code)
	}

	// Advance clock by +120s (still within 180s TTL).
	nowSec.Store(1793448120)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, path, nil))
	if w2.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("second call status=%d calls=%d", w2.Code, calls.Load())
	}
	if !bytes.Equal(w1.Body.Bytes(), w2.Body.Bytes()) {
		t.Fatalf("cached body mutated across time advance:\nfirst:  %s\nsecond: %s", w1.Body.String(), w2.Body.String())
	}
	env2, err := unified.Decode(w2.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got := unified.Str(unified.Map(env2["data"])["generated_at"]); got != wantGeneratedAt {
		t.Fatalf("generated_at forged on cache hit: got %q, want %q", got, wantGeneratedAt)
	}
}

func TestParseCacheLRUEvictionByEntryCountAndByteBudget(t *testing.T) {
	const nowUnix int64 = 1793448000
	c := loadCacheTestCatalog(t)

	t.Run("max-entries-lru-eviction", func(t *testing.T) {
		counts := map[string]int{}
		var mu sync.Mutex
		h, err := NewWithCacheConfig(c, map[string]Adapter{
			"douyin": AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
				mu.Lock()
				counts[src]++
				mu.Unlock()
				return makeSyntheticEnvelope("douyin", src, now, ""), nil
			}),
		}, func() time.Time { return time.Unix(nowUnix, 0) }, nil, CacheConfig{
			MaxEntries: 3,
			MaxBytes:   1 << 20,
		})
		if err != nil {
			t.Fatal(err)
		}

		fetchID := func(id string) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/"+id, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("id=%s status=%d", id, w.Code)
			}
		}

		fetchID("a") // [a]
		fetchID("b") // [b, a]
		fetchID("c") // [c, b, a]
		fetchID("a") // promote a -> [a, c, b] (b is LRU)
		fetchID("d") // evict b -> [d, a, c]

		fetchID("a") // hit
		fetchID("c") // hit
		fetchID("d") // hit
		if counts["https://www.douyin.com/video/a"] != 1 ||
			counts["https://www.douyin.com/video/c"] != 1 ||
			counts["https://www.douyin.com/video/d"] != 1 {
			t.Fatalf("expected MRU entries a, c, d to remain cached: counts=%v", counts)
		}

		fetchID("b") // was LRU-evicted -> must re-parse (count becomes 2)
		if counts["https://www.douyin.com/video/b"] != 2 {
			t.Fatalf("expected LRU entry b to be evicted and re-parsed: counts=%v", counts)
		}
		entries, _ := h.cache.stats()
		if entries != 3 {
			t.Fatalf("expected cache entry count capped at 3, got %d", entries)
		}
	})

	t.Run("max-bytes-lru-eviction-and-oversized-skip", func(t *testing.T) {
		// Measure exact entry size for a standard 1-item synthetic response first.
		probeEnv := makeSyntheticEnvelope("douyin", "https://www.douyin.com/video/v1", nowUnix, "")
		probeEnv = unified.PublicView(probeEnv, false, nowUnix)
		probeRes, _ := encodeResponse(200, probeEnv)
		oneEntryBytes := cacheEntrySize(cacheKey{
			route:    "/api/parse",
			platform: "douyin",
			url:      "https://www.douyin.com/video/v1",
		}, probeRes)

		// Budget fits 2 standard entries, not 3.
		budget := oneEntryBytes*2 + 64
		if budget < minCacheMaxBytes {
			budget = minCacheMaxBytes
		}

		counts := map[string]int{}
		var mu sync.Mutex
		h, err := NewWithCacheConfig(c, map[string]Adapter{
			"douyin": AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
				mu.Lock()
				counts[src]++
				mu.Unlock()
				env := makeSyntheticEnvelope("douyin", src, now, "")
				if strings.HasSuffix(src, "/huge") {
					unified.Map(env["data"])["description"] = strings.Repeat("X", int(budget)+512)
				}
				return env, nil
			}),
		}, func() time.Time { return time.Unix(nowUnix, 0) }, nil, CacheConfig{
			MaxEntries: 100,
			MaxBytes:   budget,
		})
		if err != nil {
			t.Fatal(err)
		}

		fetchID := func(id string) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/parse?schema_version=1.0&url=https://www.douyin.com/video/"+id, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("id=%s status=%d body=%s", id, w.Code, w.Body.String())
			}
		}

		fetchID("v1") // [v1]
		fetchID("v2") // [v2, v1]
		fetchID("v1") // promote v1 -> [v1, v2] (v2 is LRU)
		fetchID("v3") // exceeds MaxBytes -> evicts v2 -> [v3, v1]

		_, usedBytes := h.cache.stats()
		if usedBytes > budget {
			t.Fatalf("cache totalBytes=%d exceeded MaxBytes=%d", usedBytes, budget)
		}

		// Oversized single entry (> MaxBytes) still returns 200 OK without evicting v1/v3 or entering cache.
		fetchID("huge")
		fetchID("huge")
		if counts["https://www.douyin.com/video/huge"] != 2 {
			t.Fatalf("expected oversized entry not to be cached, got count=%d", counts["https://www.douyin.com/video/huge"])
		}

		fetchID("v1") // still cached!
		fetchID("v3") // still cached!
		fetchID("v2") // was evicted by byte cap -> re-parses
		if counts["https://www.douyin.com/video/v1"] != 1 ||
			counts["https://www.douyin.com/video/v3"] != 1 ||
			counts["https://www.douyin.com/video/v2"] != 2 {
			t.Fatalf("unexpected byte-cap LRU counts: %v", counts)
		}
	})
}

type countingYouTubeHelper struct {
	ctxCalls      atomic.Int32
	finalizeCalls atomic.Int32
}

func (y *countingYouTubeHelper) Context(context.Context) (unified.Object, int) {
	n := y.ctxCalls.Add(1)
	return unified.Object{
		"schema_version": "1.0",
		"code":           "OK",
		"data":           unified.Object{"seq": int64(n)},
	}, http.StatusOK
}

func (y *countingYouTubeHelper) Finalize(_ context.Context, _ []byte, _ bool) (unified.Object, int) {
	n := y.finalizeCalls.Add(1)
	return unified.Object{
		"schema_version": "1.0",
		"code":           "OK",
		"data":           unified.Object{"seq": int64(n)},
	}, http.StatusOK
}

func TestParseCacheExcludesRequesterBoundContent(t *testing.T) {
	c := loadCacheTestCatalog(t)
	var nowSec atomic.Int64
	nowSec.Store(1793448000)

	var weiboCalls, youtubeCalls atomic.Int32
	rawKey := make([]byte, 32)
	if _, err := rand.Read(rawKey); err != nil {
		t.Fatal(err)
	}
	signingKey := hex.EncodeToString(rawKey)

	weiboAdapter := AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
		weiboCalls.Add(1)
		env := makeSyntheticEnvelope("weibo", src, now, "")
		item := unified.Map(unified.Slice(unified.Map(env["data"])["items"])[0])
		const upstream = "https://f.video.weibocdn.com/000abc.mp4?Expires=1793451600"
		unified.Map(item["default"])["url"] = upstream
		variant := unified.Map(unified.Slice(item["variants"])[0])
		source := unified.Map(unified.Slice(unified.Map(variant["delivery"])["sources"])[0])
		source["url"] = upstream
		return env, nil
	})
	youtubeAdapter := AdapterFunc(func(_ context.Context, src string, now int64) (unified.Object, error) {
		youtubeCalls.Add(1)
		return makeSyntheticEnvelope("youtube", src, now, ""), nil
	})

	h, err := New(c, map[string]Adapter{
		"weibo":   weiboAdapter,
		"youtube": youtubeAdapter,
	}, func() time.Time { return time.Unix(nowSec.Load(), 0) }, weiboplatform.PreparePublic(signingKey))
	if err != nil {
		t.Fatal(err)
	}
	ytHelper := &countingYouTubeHelper{}
	h.SetYouTubeHelper(ytHelper)

	// 1. Weibo signed relay tickets must NEVER be cached across requests.
	weiboPath := "/api/parse?schema_version=1.0&url=" + url.QueryEscape("https://weibo.com/1234567890/AbCdEfGhI")
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, httptest.NewRequest(http.MethodGet, weiboPath, nil))
	nowSec.Add(5)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, weiboPath, nil))
	if w1.Code != http.StatusOK || w2.Code != http.StatusOK || weiboCalls.Load() != 2 {
		t.Fatalf("weibo should bypass cache: code1=%d code2=%d calls=%d", w1.Code, w2.Code, weiboCalls.Load())
	}
	if bytes.Equal(w1.Body.Bytes(), w2.Body.Bytes()) {
		t.Fatal("weibo responses at distinct timestamps unexpectedly reused identical signed ticket payload")
	}

	// 2. YouTube /api/parse and /api/youtube/{context,finalize} must NEVER enter parseCache.
	ytPath := "/api/parse?schema_version=1.0&url=" + url.QueryEscape("https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, ytPath, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("youtube parse status=%d body=%s", w.Code, w.Body.String())
		}
		wCtx := httptest.NewRecorder()
		h.ServeHTTP(wCtx, httptest.NewRequest(http.MethodGet, "/api/youtube/context?schema_version=1.0", nil))
		if wCtx.Code != http.StatusOK {
			t.Fatalf("youtube context status=%d", wCtx.Code)
		}
		wFin := httptest.NewRecorder()
		h.ServeHTTP(wFin, httptest.NewRequest(http.MethodPost, "/api/youtube/finalize?schema_version=1.0", strings.NewReader(`{}`)))
		if wFin.Code != http.StatusOK {
			t.Fatalf("youtube finalize status=%d", wFin.Code)
		}
	}
	if youtubeCalls.Load() != 2 || ytHelper.ctxCalls.Load() != 2 || ytHelper.finalizeCalls.Load() != 2 {
		t.Fatalf("youtube bypassed calls mismatch: parse=%d ctx=%d fin=%d",
			youtubeCalls.Load(), ytHelper.ctxCalls.Load(), ytHelper.finalizeCalls.Load())
	}
	if entries, usedBytes := h.cache.stats(); entries != 0 || usedBytes != 0 {
		t.Fatalf("excluded platforms leaked into parseCache: entries=%d bytes=%d", entries, usedBytes)
	}
}

// TestBenchmarkLocalFakeUpstreamBeforeAfter runs an offline httptest load
// comparison when DOKITOOL_RUN_CACHE_BENCH=1 is set (0 real network calls):
//   - simulates fast pure-Go (0.5s), Shanghai channel (0.8s), slow yt-dlp (3.0s),
//     and multi-P Bilibili /api/resolve (0.5s) workloads across 5/25/60/120 RPS
//     using Production DefaultAdmissionConfig() + Handler.
func TestBenchmarkLocalFakeUpstreamBeforeAfter(t *testing.T) {
	if os.Getenv("DOKITOOL_RUN_CACHE_BENCH") != "1" {
		t.Skip("set DOKITOOL_RUN_CACHE_BENCH=1 to run local fake-upstream before/after benchmark")
	}
	c := loadCacheTestCatalog(t)
	const window = 8 * time.Second

	type benchMetrics struct {
		totalReqs     int
		upstreamCalls int32
		okCount       int
		status429     int
		otherErr      int
		p50MS         float64
		p95MS         float64
		heapInuseKiB  uint64
		peakGoroutine int
		endGoroutine  int
	}

	hotTargets := []string{
		// Fast pure-Go platforms (~0.5s)
		"/api/parse?schema_version=1.0&url=" + url.QueryEscape("https://www.kuaishou.com/short-video/3xhot1"),
		"/api/parse?schema_version=1.0&url=" + url.QueryEscape("https://www.kuaishou.com/short-video/3xhot2"),
		"/api/parse?schema_version=1.0&url=" + url.QueryEscape("https://www.xiaohongshu.com/explore/66hot1?xsec_token=t1"),
		"/api/parse?schema_version=1.0&url=" + url.QueryEscape("https://www.xiaohongshu.com/explore/66hot2?xsec_token=t2"),
		// Multi-P Bilibili /api/resolve (~0.5s)
		"/api/resolve?schema_version=1.0&url=" + url.QueryEscape("https://www.bilibili.com/video/BV1234567890/?p=1") + "&item_id=p-1",
		"/api/resolve?schema_version=1.0&url=" + url.QueryEscape("https://www.bilibili.com/video/BV1234567890/?p=2") + "&item_id=p-2",
		"/api/resolve?schema_version=1.0&url=" + url.QueryEscape("https://www.bilibili.com/video/BV1234567890/?p=3") + "&item_id=p-3",
		// Shanghai Douyin channel (~0.8s)
		"/api/parse?schema_version=1.0&url=" + url.QueryEscape("https://www.douyin.com/video/7300001"),
		"/api/parse?schema_version=1.0&url=" + url.QueryEscape("https://www.douyin.com/video/7300002"),
		"/api/parse?schema_version=1.0&url=" + url.QueryEscape("https://www.douyin.com/video/7300003"),
		// Slow yt-dlp platform (~3.0s)
		"/api/parse?schema_version=1.0&url=" + url.QueryEscape("https://x.com/user/status/1800001"),
		"/api/parse?schema_version=1.0&url=" + url.QueryEscape("https://x.com/user/status/1800002"),
	}

	runRate := func(rps int, disableCache bool, withAdmission bool) benchMetrics {
		var upstreamCalls atomic.Int32
		mkAdapter := func(platform string, delay time.Duration) Adapter {
			return AdapterFunc(func(ctx context.Context, src string, now int64) (unified.Object, error) {
				upstreamCalls.Add(1)
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				exp := time.Unix(now+1800, 0).UTC().Format(time.RFC3339)
				return makeSyntheticEnvelope(platform, src, now, exp, "p-1", "p-2", "p-3", "p-4"), nil
			})
		}

		h, err := NewWithCacheConfig(c, map[string]Adapter{
			"kuaishou":    mkAdapter("kuaishou", 500*time.Millisecond),
			"xiaohongshu": mkAdapter("xiaohongshu", 500*time.Millisecond),
			"bilibili":    mkAdapter("bilibili", 500*time.Millisecond),
			"douyin":      mkAdapter("douyin", 800*time.Millisecond),
			"x":           mkAdapter("x", 3000*time.Millisecond),
		}, time.Now, nil, CacheConfig{
			Disabled:   disableCache,
			MaxEntries: defaultCacheMaxEntries,
			MaxBytes:   defaultCacheMaxBytes,
		})
		if err != nil {
			t.Fatal(err)
		}
		var handler http.Handler = h
		if withAdmission {
			entry, admErr := WithAdmissionConfig(h, nil, "caddy", testEdgeKey, DefaultAdmissionConfig())
			if admErr != nil {
				t.Fatal(admErr)
			}
			handler = entry
		}
		srv := httptest.NewServer(handler)
		defer srv.Close()

		totalReqs := int(window.Seconds()) * rps
		interval := time.Second / time.Duration(rps)

		tr := &http.Transport{
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 256,
		}
		defer tr.CloseIdleConnections()
		client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

		type sample struct {
			code    int
			latency time.Duration
		}
		results := make([]sample, totalReqs)
		var peakG atomic.Int32
		var wg sync.WaitGroup
		startWall := time.Now()

		for i := 0; i < totalReqs; i++ {
			targetAt := startWall.Add(time.Duration(i) * interval)
			if wait := time.Until(targetAt); wait > 0 {
				time.Sleep(wait)
			}
			if g := int32(runtime.NumGoroutine()); g > peakG.Load() {
				peakG.Store(g)
			}
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				req, reqErr := http.NewRequest(http.MethodGet, srv.URL+hotTargets[idx%len(hotTargets)], nil)
				if reqErr != nil {
					return
				}
				// Distinct visitor IPs so per-IP cap (1 concurrent, 0.2 RPS) does not obscure server behavior.
				req.Header.Set(edgeHeader, testEdgeKey)
				req.Header.Set(clientHeader, fmt.Sprintf("198.51.%d.%d", 1+((idx/250)%250), 1+(idx%250)))
				t0 := time.Now()
				resp, doErr := client.Do(req)
				lat := time.Since(t0)
				if doErr != nil {
					results[idx] = sample{code: 0, latency: lat}
					return
				}
				_ = resp.Body.Close()
				results[idx] = sample{code: resp.StatusCode, latency: lat}
			}(i)
		}
		wg.Wait()
		tr.CloseIdleConnections()

		runtime.GC()
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		latencies := make([]float64, 0, totalReqs)
		m := benchMetrics{
			totalReqs:     totalReqs,
			upstreamCalls: upstreamCalls.Load(),
			heapInuseKiB:  mem.HeapAlloc / 1024,
			peakGoroutine: int(peakG.Load()),
			endGoroutine:  runtime.NumGoroutine(),
		}
		for _, r := range results {
			latencies = append(latencies, float64(r.latency.Microseconds())/1000.0)
			switch r.code {
			case http.StatusOK:
				m.okCount++
			case http.StatusTooManyRequests:
				m.status429++
			default:
				m.otherErr++
			}
		}
		sort.Float64s(latencies)
		pct := func(p float64) float64 {
			idx := int(math.Ceil(p*float64(len(latencies)))) - 1
			if idx < 0 {
				idx = 0
			}
			if idx >= len(latencies) {
				idx = len(latencies) - 1
			}
			return latencies[idx]
		}
		m.p50MS = pct(0.50)
		m.p95MS = pct(0.95)
		return m
	}

	for _, withAdmission := range []bool{true, false} {
		mode := "HandlerOnly"
		if withAdmission {
			mode = "WithDefaultAdmission"
		}
		for _, rps := range []int{5, 25, 60, 120} {
			before := runRate(rps, true, withAdmission)
			after := runRate(rps, false, withAdmission)
			t.Logf("[%s] RPS=%d BEFORE: reqs=%d upstream=%d ok=%.1f%% 429=%.1f%% p50=%.2fms p95=%.2fms heap=%dKiB peakG=%d endG=%d",
				mode, rps, before.totalReqs, before.upstreamCalls,
				100*float64(before.okCount)/float64(before.totalReqs),
				100*float64(before.status429)/float64(before.totalReqs),
				before.p50MS, before.p95MS, before.heapInuseKiB, before.peakGoroutine, before.endGoroutine)
			t.Logf("[%s] RPS=%d AFTER:  reqs=%d upstream=%d ok=%.1f%% 429=%.1f%% p50=%.2fms p95=%.2fms heap=%dKiB peakG=%d endG=%d",
				mode, rps, after.totalReqs, after.upstreamCalls,
				100*float64(after.okCount)/float64(after.totalReqs),
				100*float64(after.status429)/float64(after.totalReqs),
				after.p50MS, after.p95MS, after.heapInuseKiB, after.peakGoroutine, after.endGoroutine)
		}
	}
}
