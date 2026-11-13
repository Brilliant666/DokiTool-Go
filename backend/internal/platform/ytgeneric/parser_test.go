package ytgeneric

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

type executorFunc func(context.Context, fetch.Request) (fetch.Response, error)

func (f executorFunc) Do(ctx context.Context, r fetch.Request) (fetch.Response, error) {
	return f(ctx, r)
}

type stubResolver struct {
	ips   map[string][]netip.Addr
	calls *int
}

func (s stubResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if s.calls != nil {
		*s.calls++
	}
	if addrs, ok := s.ips[host]; ok {
		return addrs, nil
	}
	return nil, errors.New("not found")
}

type stubExtractor struct {
	enabled bool
	extract func(context.Context, string) (*ytengine.Info, error)
}

func (s *stubExtractor) Enabled() bool { return s != nil && s.enabled }

func (s *stubExtractor) Extract(ctx context.Context, rawURL string) (*ytengine.Info, error) {
	if s.extract == nil {
		return nil, &ytengine.Error{Code: ytengine.CodeServiceNotConfigured, Message: "disabled"}
	}
	return s.extract(ctx, rawURL)
}

func TestUniversalAdapterAllowlistAndUnifiedMapping(t *testing.T) {
	const now int64 = 1762300800
	spec := SiteSpec{
		PlatformID:      "vimeo",
		Name:            "Vimeo",
		SiteURL:         "https://vimeo.com",
		InputHosts:      []string{"vimeo.com", ".vimeo.com"},
		MediaHostSuffix: []string{".vimeocdn.com", "vimeo.com"},
		EnableDASHMux:   true,
		EnableHLS:       true,
	}

	w, h := int64(1920), int64(1080)
	sz := int64(15000000)
	parser := New(spec, &stubExtractor{
		enabled: true,
		extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
			return &ytengine.Info{
				ID:        "987654321",
				Title:     "Vimeo Sample",
				Uploader:  "Creator",
				Thumbnail: "https://i.vimeocdn.com/video/thumb.jpg",
				Formats: []ytengine.Format{
					{
						FormatID: "http-1080p",
						URL:      "https://vod-progressive.akamaized.vimeocdn.com/exp=1/sample1080.mp4",
						Ext:      "mp4",
						Protocol: "https",
						Width:    &w,
						Height:   &h,
						VCodec:   "avc1.640028",
						ACodec:   "mp4a.40.2",
						Filesize: &sz,
					},
				},
			}, nil
		},
	}, nil)

	// Reject SSRF / unlisted domains
	for _, bad := range []string{
		"https://127.0.0.1/video/1",
		"https://evil.com/video/1",
		"https://vimeo.com/",
		"file:///etc/passwd",
	} {
		if parser.AllowedInputURL(bad) {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}

	payload, err := parser.Parse(context.Background(), "https://vimeo.com/987654321", now)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if err := unified.ValidateEnvelopeAt(payload, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt failed: %v", err)
	}
	data := unified.Map(payload["data"])
	if unified.Str(data["platform"]) != "vimeo" || unified.Str(data["content_id"]) != "987654321" {
		t.Fatalf("unexpected platform/content_id: %+v", data)
	}
}

func TestUniversalAdapterBlocksPrivateIPResolution(t *testing.T) {
	const now int64 = 1762300800
	spec := SiteSpec{
		PlatformID:      "vimeo",
		Name:            "Vimeo",
		SiteURL:         "https://vimeo.com",
		InputHosts:      []string{"vimeo.com", ".vimeo.com"},
		MediaHostSuffix: []string{".vimeocdn.com"},
	}

	for _, privateIP := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1"} {
		resolver := stubResolver{
			ips: map[string][]netip.Addr{
				"vimeo.com":        {netip.MustParseAddr(privateIP)},
				"cdn.vimeocdn.com": {netip.MustParseAddr(privateIP)},
			},
		}
		parser := NewWithResolver(spec, &stubExtractor{enabled: true}, nil, resolver)
		payload, err := parser.Parse(context.Background(), "https://vimeo.com/123456", now)
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if unified.Str(payload["code"]) != "UPSTREAM_BLOCKED" {
			t.Fatalf("expected UPSTREAM_BLOCKED when input host resolves to %s, got %v", privateIP, payload["code"])
		}
		if parser.AllowedMediaURL("https://cdn.vimeocdn.com/video.mp4") {
			t.Fatalf("expected AllowedMediaURL to reject media host resolving to %s", privateIP)
		}
	}
}

func TestParseSitesEnvValidation(t *testing.T) {
	specs, err := ParseSitesEnv("")
	if err != nil || len(specs) != 0 {
		t.Fatalf("expected empty env to return no sites, got specs=%v err=%v", specs, err)
	}
	specs, err = ParseSitesEnv("  ")
	if err != nil || len(specs) != 0 {
		t.Fatalf("expected whitespace env to return no sites, got specs=%v err=%v", specs, err)
	}
	specs, err = ParseSitesEnv("facebook, tumblr, dailymotion, facebook")
	if err != nil || len(specs) != 3 {
		t.Fatalf("expected 3 deduplicated sites, got specs=%v err=%v", specs, err)
	}
	gotIDs := []string{specs[0].PlatformID, specs[1].PlatformID, specs[2].PlatformID}
	wantIDs := []string{"facebook", "tumblr", "dailymotion"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("got IDs %v, want %v", gotIDs, wantIDs)
	}

	for _, invalid := range []string{
		"unknown",
		"pinterest",
		"facebook,unknown",
		"facebook,,tumblr",
		"facebook,",
		",facebook",
		"FACEBOOK",
	} {
		if _, err := ParseSitesEnv(invalid); err == nil {
			t.Fatalf("expected ParseSitesEnv(%q) to fail", invalid)
		}
	}
}

func TestBuiltinSitesSpecsAndAllowlists(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"facebook", "tumblr", "dailymotion"} {
		spec, ok := LookupSite(id)
		if !ok {
			t.Fatalf("missing builtin site %q", id)
		}
		if err := c.Register(spec.CatalogEntry()); err != nil {
			t.Fatalf("catalog.Register(%s) failed: %v", id, err)
		}
	}

	fbSpec, _ := LookupSite("facebook")
	if !reflect.DeepEqual(fbSpec.Extractors, []string{"facebook", "facebook:reel"}) {
		t.Fatalf("unexpected facebook extractors: %v", fbSpec.Extractors)
	}
	tbSpec, _ := LookupSite("tumblr")
	if !reflect.DeepEqual(tbSpec.Extractors, []string{"Tumblr"}) {
		t.Fatalf("unexpected tumblr extractors: %v", tbSpec.Extractors)
	}
	dmSpec, _ := LookupSite("dailymotion")
	if !reflect.DeepEqual(dmSpec.Extractors, []string{"Dailymotion"}) {
		t.Fatalf("unexpected dailymotion extractors: %v", dmSpec.Extractors)
	}

	fb := New(fbSpec, nil, nil)
	tb := New(tbSpec, nil, nil)
	dm := New(dmSpec, nil, nil)

	// 1. Input URL allowlist positive + negative checks
	inputCases := []struct {
		parser   *Parser
		platform string
		valid    []string
		invalid  []string
	}{
		{
			parser:   fb,
			platform: "facebook",
			valid: []string{
				"https://www.facebook.com/watch/?v=647537299265662",
				"https://www.facebook.com/WatchESLOne/videos/359649331226507/",
				"https://www.facebook.com/reel/1195289147628387",
				"https://facebook.com/watch/?v=647537299265662",
				"https://m.facebook.com/watch/?v=647537299265662",
			},
			invalid: []string{
				"https://fb.watch/abc123/",
				"https://web.facebook.com/watch/?v=647537299265662",
				"https://sub.fb.watch/abc123/",
				"https://www.fb.watch/abc123/",
				"https://evilfb.watch/abc123/",
				"https://fb.watch.evil.com/abc123/",
				"https://fb.watch:8080/abc123/",
				"https://fb.watch:8443/abc123/",
				"https://user:pass@fb.watch/abc123/",
				"https://fb.watch/",
				"https://evilfacebook.com/watch/?v=1",
				"https://evilwww.facebook.com/watch/?v=1",
				"https://www.facebook.com.evil.com/watch/?v=1",
				"https://www.facebook.com:8443/watch/?v=1",
				"https://user:pass@www.facebook.com/watch/?v=1",
				"https://www.facebook.com/",
				"ftp://www.facebook.com/watch/?v=1",
			},
		},
		{
			parser:   tb,
			platform: "tumblr",
			valid: []string{
				"https://maskofthedragon.tumblr.com/post/626907179849564160/mona-talking-in-english",
				"https://bartlebyshop.tumblr.com/post/180294460076/duality-of-bird",
				"https://www.tumblr.com/maskofthedragon/626907179849564160/mona-talking-in-english",
				"https://tumblr.com/post/626907179849564160",
			},
			invalid: []string{
				"https://eviltumblr.com/post/626907179849564160",
				"https://maskofthedragon.tumblr.com.evil.com/post/626907179849564160",
				"https://maskofthedragon.tumblr.com:8443/post/626907179849564160",
				"https://user:pass@maskofthedragon.tumblr.com/post/626907179849564160",
				"https://www.tumblr.com/",
				"https://tumblr.com/",
				"https://maskofthedragon.tumblr.com/",
				"file:///etc/passwd",
			},
		},
		{
			parser:   dm,
			platform: "dailymotion",
			valid: []string{
				"https://www.dailymotion.com/video/xbvrf7u",
				"https://www.dailymotion.com/video/xbvrdzy",
				"https://dailymotion.com/video/xbvrf7u",
				"https://dai.ly/xbvrf7u",
			},
			invalid: []string{
				"https://www.dai.ly/xbvrf7u",
				"https://sub.dai.ly/xbvrf7u",
				"https://evildai.ly/xbvrf7u",
				"https://dai.ly.evil.com/xbvrf7u",
				"https://dai.ly:8080/xbvrf7u",
				"https://dai.ly:8443/xbvrf7u",
				"https://user:pass@dai.ly/xbvrf7u",
				"https://dai.ly/",
				"https://dai.ly/xbvrf7u/extra",
				"https://graphql.api.dailymotion.com/video/xbvrf7u",
				"https://evildailymotion.com/video/xbvrf7u",
				"https://evilwww.dailymotion.com/video/xbvrf7u",
				"https://www.dailymotion.com.evil.com/video/xbvrf7u",
				"https://www.dailymotion.com:8443/video/xbvrf7u",
				"https://user:pass@www.dailymotion.com/video/xbvrf7u",
				"https://www.dailymotion.com/",
			},
		},
	}

	for _, tc := range inputCases {
		for _, u := range tc.valid {
			if !tc.parser.AllowedInputURL(u) {
				t.Fatalf("%s: expected valid input URL %q to be allowed", tc.platform, u)
			}
			if got := c.PlatformForURL(u); got != tc.platform {
				t.Fatalf("%s: catalog.PlatformForURL(%q) = %q, want %q", tc.platform, u, got, tc.platform)
			}
		}
		for _, u := range tc.invalid {
			if tc.parser.AllowedInputURL(u) {
				t.Fatalf("%s: expected invalid input URL %q to be rejected", tc.platform, u)
			}
		}
	}
	if got := c.PlatformForURL("https://fb.watch/abc123/"); got != "facebook" {
		t.Fatalf("catalog.PlatformForURL(fb.watch) = %q, want facebook", got)
	}

	// 2. Outbound host & media URL allowlist checks (strictly matching P0 proxy logs)
	outboundCases := []struct {
		parser       *Parser
		platform     string
		validHosts   []string
		invalidHosts []string
		validMedia   []string
		invalidMedia []string
	}{
		{
			parser:   fb,
			platform: "facebook",
			validHosts: []string{
				"www.facebook.com",
				"video-nrt6-1.xx.fbcdn.net",
				"scontent-nrt1-2.xx.fbcdn.net",
			},
			invalidHosts: []string{
				"facebook.com",
				"m.facebook.com",
				"fbcdn.net",
				"evilfbcdn.net",
				"www.facebook.com.evil.com",
				"127.0.0.1",
			},
			validMedia: []string{
				"https://video-nrt6-1.xx.fbcdn.net/o1/v/t2/f2/m366/sample.mp4",
				"https://scontent-nrt1-2.xx.fbcdn.net/v/t15.5256-10/thumb.jpg",
			},
			invalidMedia: []string{
				"https://fbcdn.net/sample.mp4",
				"https://www.facebook.com/sample.mp4",
				"https://evilfbcdn.net/sample.mp4",
				"https://video-nrt6-1.xx.fbcdn.net:8443/sample.mp4",
			},
		},
		{
			parser:   tb,
			platform: "tumblr",
			validHosts: []string{
				"maskofthedragon.tumblr.com",
				"bartlebyshop.tumblr.com",
				"www.tumblr.com",
				"va.media.tumblr.com",
				"ve.media.tumblr.com",
				"64.media.tumblr.com",
			},
			invalidHosts: []string{
				"tumblr.com",
				"eviltumblr.com",
				"tumblr.com.evil.com",
				"127.0.0.1",
			},
			validMedia: []string{
				"https://va.media.tumblr.com/tumblr_qfbsolGm3H1r8hd26_720.mp4",
				"https://ve.media.tumblr.com/tumblr_pigdqgqD1d1xdrd7g.mp4",
				"https://64.media.tumblr.com/tumblr_qfbsolGm3H1r8hd26_frame1.jpg",
			},
			invalidMedia: []string{
				"https://media.tumblr.com/sample.mp4",
				"https://maskofthedragon.tumblr.com/sample.mp4",
				"https://tumblr.com/sample.mp4",
				"https://evilmedia.tumblr.com.evil.com/sample.mp4",
			},
		},
		{
			parser:   dm,
			platform: "dailymotion",
			validHosts: []string{
				"www.dailymotion.com",
				"graphql.api.dailymotion.com",
				"cdndirector.dailymotion.com",
				"vod2.cf.dmcdn.net",
				"s2.dmcdn.net",
			},
			invalidHosts: []string{
				"dailymotion.com",
				"api.dailymotion.com",
				"evil.dailymotion.com",
				"dmcdn.net",
				"evildmcdn.net",
				"www.dailymotion.com.evil.com",
				"127.0.0.1",
			},
			validMedia: []string{
				"https://vod2.cf.dmcdn.net/sec2(tok)/cloud/fmp4/1/h264_aac_fhd/1/manifest.m3u8",
				"https://s2.dmcdn.net/v/xbvrdzy/x1080",
			},
			invalidMedia: []string{
				"https://dmcdn.net/manifest.m3u8",
				"https://www.dailymotion.com/manifest.m3u8",
				"https://graphql.api.dailymotion.com/manifest.m3u8",
				"https://cdndirector.dailymotion.com/manifest.m3u8",
			},
		},
	}

	for _, tc := range outboundCases {
		for _, h := range tc.validHosts {
			if !tc.parser.AllowedOutboundHost(h) {
				t.Fatalf("%s: expected outbound host %q to be allowed", tc.platform, h)
			}
		}
		for _, h := range tc.invalidHosts {
			if tc.parser.AllowedOutboundHost(h) {
				t.Fatalf("%s: expected outbound host %q to be rejected", tc.platform, h)
			}
		}
		for _, m := range tc.validMedia {
			if !tc.parser.AllowedMediaURL(m) {
				t.Fatalf("%s: expected media URL %q to be allowed", tc.platform, m)
			}
		}
		for _, m := range tc.invalidMedia {
			if tc.parser.AllowedMediaURL(m) {
				t.Fatalf("%s: expected media URL %q to be rejected", tc.platform, m)
			}
		}
	}
}

func loadFixtureInfo(t *testing.T, filename string) *ytengine.Info {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", filename))
	if err != nil {
		t.Fatalf("read fixture %s: %v", filename, err)
	}
	var info ytengine.Info
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode fixture %s: %v", filename, err)
	}
	return &info
}

func TestP0FixturesFacebookTumblrDailymotion(t *testing.T) {
	const now int64 = 1762300800
	publicAddr := []netip.Addr{netip.MustParseAddr("93.184.216.34")}
	pubResolver := stubResolver{
		ips: map[string][]netip.Addr{
			"www.facebook.com":             publicAddr,
			"video-nrt6-1.xx.fbcdn.net":    publicAddr,
			"scontent-nrt1-2.xx.fbcdn.net": publicAddr,
			"maskofthedragon.tumblr.com":   publicAddr,
			"va.media.tumblr.com":          publicAddr,
			"64.media.tumblr.com":          publicAddr,
			"www.dailymotion.com":          publicAddr,
			"vod2.cf.dmcdn.net":            publicAddr,
			"vod3.cf.dmcdn.net":            publicAddr,
			"s2.dmcdn.net":                 publicAddr,
			"graphql.api.dailymotion.com":  publicAddr,
			"cdndirector.dailymotion.com":  publicAddr,
		},
	}

	// 1. Facebook P0 fixture: codec-less progressive MP4 SD + HD, HD selected as default
	t.Run("facebook", func(t *testing.T) {
		spec, _ := LookupSite("facebook")
		parser := NewWithResolver(spec, &stubExtractor{
			enabled: true,
			extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
				return loadFixtureInfo(t, "facebook.json"), nil
			},
		}, func(ctx context.Context, rawURL string) (int64, bool) {
			if strings.Contains(rawURL, "sample_hd.mp4") {
				return 18450000, true
			}
			if strings.Contains(rawURL, "sample_sd.mp4") {
				return 7210000, true
			}
			return 0, false
		}, pubResolver)

		payload, err := parser.Parse(context.Background(), "https://www.facebook.com/watch/?v=647537299265662", now)
		if err != nil {
			t.Fatalf("facebook Parse failed: %v", err)
		}
		for name, env := range map[string]unified.Object{
			"raw":    payload,
			"normal": unified.PublicView(payload, false, now),
			"debug":  unified.PublicView(payload, true, now),
		} {
			if err := unified.ValidateEnvelopeAt(env, now); err != nil {
				t.Fatalf("facebook %s ValidateEnvelopeAt failed: %v", name, err)
			}
		}
		pub := unified.PublicView(payload, false, now)
		data := unified.Map(pub["data"])
		if unified.Str(data["platform"]) != "facebook" || unified.Str(data["content_id"]) != "647537299265662" {
			t.Fatalf("unexpected facebook data: %+v", data)
		}
		item := unified.Map(unified.Slice(data["items"])[0])
		def := unified.Map(item["default"])
		if def == nil || !strings.Contains(unified.Str(def["url"]), "sample_hd.mp4") {
			t.Fatalf("expected facebook default to select sample_hd.mp4, got %+v", def)
		}
		variants := unified.Slice(item["variants"])
		if len(variants) != 2 {
			t.Fatalf("expected exactly 2 progressive MP4 variants (HD, SD), got %d: %+v", len(variants), variants)
		}
		v0, v1 := unified.Map(variants[0]), unified.Map(variants[1])
		if unified.Str(v0["label"]) != "HD" || unified.Str(v1["label"]) != "SD" {
			t.Fatalf("expected labels [HD, SD], got [%s, %s]", unified.Str(v0["label"]), unified.Str(v1["label"]))
		}
	})

	// 2. Tumblr P0 fixture: codec-less progressive MP4 720p (hd) + 480p (sd), 720p selected as default
	t.Run("tumblr", func(t *testing.T) {
		spec, _ := LookupSite("tumblr")
		parser := NewWithResolver(spec, &stubExtractor{
			enabled: true,
			extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
				return loadFixtureInfo(t, "tumblr.json"), nil
			},
		}, func(ctx context.Context, rawURL string) (int64, bool) {
			if strings.Contains(rawURL, "_720.mp4") {
				return 1428784, true
			}
			if strings.Contains(rawURL, "_480.mp4") {
				return 812000, true
			}
			return 0, false
		}, pubResolver)

		payload, err := parser.Parse(context.Background(), "https://maskofthedragon.tumblr.com/post/626907179849564160/mona-talking-in-english", now)
		if err != nil {
			t.Fatalf("tumblr Parse failed: %v", err)
		}
		for name, env := range map[string]unified.Object{
			"raw":    payload,
			"normal": unified.PublicView(payload, false, now),
			"debug":  unified.PublicView(payload, true, now),
		} {
			if err := unified.ValidateEnvelopeAt(env, now); err != nil {
				t.Fatalf("tumblr %s ValidateEnvelopeAt failed: %v", name, err)
			}
		}
		pub := unified.PublicView(payload, false, now)
		data := unified.Map(pub["data"])
		if unified.Str(data["platform"]) != "tumblr" || unified.Str(data["content_id"]) != "626907179849564160" {
			t.Fatalf("unexpected tumblr data: %+v", data)
		}
		item := unified.Map(unified.Slice(data["items"])[0])
		def := unified.Map(item["default"])
		if def == nil || !strings.Contains(unified.Str(def["url"]), "_720.mp4") {
			t.Fatalf("expected tumblr default to select _720.mp4, got %+v", def)
		}
		variants := unified.Slice(item["variants"])
		if len(variants) != 2 {
			t.Fatalf("expected 2 progressive MP4 variants (720p, 480p), got %d: %+v", len(variants), variants)
		}
		v0, v1 := unified.Map(variants[0]), unified.Map(variants[1])
		if unified.Str(v0["label"]) != "720p" || unified.Str(v1["label"]) != "480p" {
			t.Fatalf("expected labels [720p, 480p], got [%s, %s]", unified.Str(v0["label"]), unified.Str(v1["label"]))
		}
	})

	// 3. Dailymotion P0 fixture: outputs ONLY verified single-stream combined fMP4 HLS (scope == "single")
	t.Run("dailymotion", func(t *testing.T) {
		spec, _ := LookupSite("dailymotion")
		parser := NewWithResolver(spec, &stubExtractor{
			enabled: true,
			extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
				info := loadFixtureInfo(t, "dailymotion.json")
				// Inject extra non-verified formats to ensure Dailymotion never outputs anything else
				w, h := int64(1280), int64(720)
				info.Formats = append(info.Formats,
					ytengine.Format{
						FormatID: "http-720",
						URL:      "https://vod2.cf.dmcdn.net/progressive/720.mp4",
						Ext:      "mp4",
						Protocol: "https",
						Width:    &w,
						Height:   &h,
						VCodec:   "avc1.64001f",
						ACodec:   "mp4a.40.2",
					},
					ytengine.Format{
						FormatID: "hls-video-only",
						URL:      "https://vod2.cf.dmcdn.net/sec2(tok)/cloud/fmp4/718473886/video_only/manifest.m3u8",
						Ext:      "mp4",
						Protocol: "m3u8_native",
						Width:    &w,
						Height:   &h,
						VCodec:   "avc1.64001f",
						ACodec:   "none",
					},
					ytengine.Format{
						FormatID: "hls-legacy-ts",
						URL:      "https://vod3.cf.dmcdn.net/sec2(tok)/video/790/233/154332097_mp4_h264_aac_hd.m3u8",
						Ext:      "mp4",
						Protocol: "m3u8_native",
						Width:    &w,
						Height:   &h,
						VCodec:   "avc1.64001f",
						ACodec:   "mp4a.40.2",
					},
				)
				return info, nil
			},
		}, nil, pubResolver)

		payload, err := parser.Parse(context.Background(), "https://www.dailymotion.com/video/xbvrdzy", now)
		if err != nil {
			t.Fatalf("dailymotion Parse failed: %v", err)
		}
		for name, env := range map[string]unified.Object{
			"raw":    payload,
			"normal": unified.PublicView(payload, false, now),
			"debug":  unified.PublicView(payload, true, now),
		} {
			if err := unified.ValidateEnvelopeAt(env, now); err != nil {
				t.Fatalf("dailymotion %s ValidateEnvelopeAt failed: %v", name, err)
			}
		}
		pub := unified.PublicView(payload, false, now)
		data := unified.Map(pub["data"])
		if unified.Str(data["platform"]) != "dailymotion" || unified.Str(data["content_id"]) != "xbvrdzy" {
			t.Fatalf("unexpected dailymotion data: %+v", data)
		}
		item := unified.Map(unified.Slice(data["items"])[0])
		if item["default"] != nil || unified.Str(item["default_unavailable_reason"]) != "processing_required" {
			t.Fatalf("expected null default + processing_required, got default=%v reason=%v", item["default"], item["default_unavailable_reason"])
		}
		variants := unified.Slice(item["variants"])
		if len(variants) != 4 {
			t.Fatalf("expected exactly 4 single-stream HLS variants, got %d: %+v", len(variants), variants)
		}
		wantLabels := []string{"1080p60", "720p60", "480p", "288p"}
		for idx, rawV := range variants {
			v := unified.Map(rawV)
			if unified.Str(v["label"]) != wantLabels[idx] {
				t.Fatalf("variant[%d] label=%q, want %q", idx, unified.Str(v["label"]), wantLabels[idx])
			}
			delivery := unified.Map(v["delivery"])
			if unified.Str(delivery["kind"]) != "hls" || unified.Str(delivery["scope"]) != "single" {
				t.Fatalf("variant[%d] delivery=%+v, want kind=hls scope=single", idx, delivery)
			}
			if _, exists := delivery["selection"]; exists {
				t.Fatalf("variant[%d] must not carry selection: %+v", idx, delivery)
			}
			output := unified.Map(delivery["output"])
			if unified.Str(output["container"]) != "mp4" || unified.Str(output["extension"]) != "mp4" || unified.Str(output["mime_type"]) != "video/mp4" {
				t.Fatalf("variant[%d] unexpected output: %+v", idx, output)
			}
		}

		// Legacy MPEG-TS-only Dailymotion video and live Dailymotion stream must return NO_MEDIA rather than unverified formats.
		legacyParser := NewWithResolver(spec, &stubExtractor{
			enabled: true,
			extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
				info := loadFixtureInfo(t, "dailymotion.json")
				w, h := int64(1280), int64(720)
				if strings.Contains(rawURL, "live") {
					isLive := true
					info.IsLive = &isLive
					return info, nil
				}
				info.Formats = []ytengine.Format{
					{
						FormatID: "hls-720",
						URL:      "https://vod3.cf.dmcdn.net/sec2(tok)/video/790/233/154332097_mp4_h264_aac_hd.m3u8",
						Ext:      "mp4",
						Protocol: "m3u8_native",
						Width:    &w,
						Height:   &h,
						VCodec:   "avc1.64001f",
						ACodec:   "mp4a.40.2",
					},
				}
				return info, nil
			},
		}, nil, pubResolver)
		for _, badURL := range []string{
			"https://www.dailymotion.com/video/x2jvvep",
			"https://www.dailymotion.com/video/xlive123",
		} {
			res, err := legacyParser.Parse(context.Background(), badURL, now)
			if err != nil {
				t.Fatalf("dailymotion(%s) Parse failed: %v", badURL, err)
			}
			if err := unified.ValidateEnvelopeAt(res, now); err != nil {
				t.Fatalf("dailymotion(%s) ValidateEnvelopeAt failed: %v", badURL, err)
			}
			if unified.Str(res["code"]) != "NO_MEDIA" {
				t.Fatalf("dailymotion(%s) code=%q, want NO_MEDIA", badURL, unified.Str(res["code"]))
			}
			if badURL == "https://www.dailymotion.com/video/x2jvvep" {
				msg := unified.Str(unified.Map(res["error"])["message"])
				if !strings.Contains(msg, "不支持") {
					t.Fatalf("expected non-fMP4 TS-HLS error message to mention 不支持, got %q", msg)
				}
			}
		}
	})
}

func TestBareMobileDomainNormalizationAndShortLinks(t *testing.T) {
	const now int64 = 1762300800
	publicAddr := []netip.Addr{netip.MustParseAddr("93.184.216.34")}
	pubResolver := stubResolver{
		ips: map[string][]netip.Addr{
			"www.facebook.com":             publicAddr,
			"fb.watch":                     publicAddr,
			"video-nrt6-1.xx.fbcdn.net":    publicAddr,
			"scontent-nrt1-2.xx.fbcdn.net": publicAddr,
			"www.tumblr.com":               publicAddr,
			"va.media.tumblr.com":          publicAddr,
			"64.media.tumblr.com":          publicAddr,
			"www.dailymotion.com":          publicAddr,
			"vod2.cf.dmcdn.net":            publicAddr,
			"vod3.cf.dmcdn.net":            publicAddr,
			"s2.dmcdn.net":                 publicAddr,
		},
	}

	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"facebook", "tumblr", "dailymotion"} {
		spec, _ := LookupSite(id)
		if err := c.Register(spec.CatalogEntry()); err != nil {
			t.Fatal(err)
		}
	}

	for _, badCatalogURL := range []string{
		"https://evildai.ly/xbvrf7u",
		"https://dai.ly.evil.com/xbvrf7u",
		"https://www.dai.ly/xbvrf7u",
		"https://sub.dai.ly/xbvrf7u",
		"https://user:pass@dai.ly/xbvrf7u",
		"https://dai.ly:8080/xbvrf7u",
		"https://dai.ly:8443/xbvrf7u",
		"https://evilfb.watch/abc123",
		"https://fb.watch.evil.com/abc123",
		"https://www.fb.watch/abc123",
		"https://sub.fb.watch/abc123",
		"https://user:pass@fb.watch/abc123",
		"https://fb.watch:8080/abc123",
		"https://fb.watch:8443/abc123",
		"https://web.facebook.com/watch/?v=1",
		"https://graphql.api.dailymotion.com/video/xbvrf7u",
	} {
		if got := c.PlatformForURL(badCatalogURL); got != "" {
			t.Fatalf("catalog.PlatformForURL(%q) = %q, want empty", badCatalogURL, got)
		}
	}

	// 1. Bare/mobile domain normalization + dai.ly zero-network local rewrite.
	t.Run("dailymotion-bare-and-daily-zero-network", func(t *testing.T) {
		dmSpec, _ := LookupSite("dailymotion")
		var extractedURL string
		httpCalls := 0
		policyP := New(dmSpec, nil, nil)
		prober := ytengine.NewSizeProber(executorFunc(func(_ context.Context, r fetch.Request) (fetch.Response, error) {
			httpCalls++
			t.Fatalf("dai.ly / dailymotion must make ZERO HTTP preflight requests, got %s %s", r.Method, r.URL)
			return fetch.Response{}, nil
		}), policyP.AllowedMediaURL)
		p := NewWithResolver(dmSpec, &stubExtractor{
			enabled: true,
			extract: func(_ context.Context, rawURL string) (*ytengine.Info, error) {
				extractedURL = rawURL
				return loadFixtureInfo(t, "dailymotion.json"), nil
			},
		}, prober, pubResolver)

		if p.AllowedOutboundHost("dai.ly") || p.AllowedOutboundHost("dailymotion.com") || p.AllowedTargetURL("https://dai.ly/xbvrdzy") || p.AllowedTargetURL("https://dailymotion.com/video/xbvrdzy") {
			t.Fatal("dai.ly and bare dailymotion.com must never enter yt-dlp target or outbound allowlist")
		}

		for _, input := range []string{
			"https://dailymotion.com/video/xbvrdzy?playlist=x1",
			"https://dai.ly/xbvrdzy",
			"http://dai.ly/xbvrdzy/",
		} {
			extractedURL = ""
			res, err := p.Parse(context.Background(), input, now)
			if err != nil || unified.Str(res["code"]) != "OK" {
				t.Fatalf("Parse(%q) failed: res=%v err=%v", input, res, err)
			}
			wantCanonical := "https://www.dailymotion.com/video/xbvrdzy"
			if strings.Contains(input, "playlist=x1") {
				wantCanonical += "?playlist=x1"
			}
			if extractedURL != wantCanonical {
				t.Fatalf("Parse(%q) passed %q to yt-dlp, want %q", input, extractedURL, wantCanonical)
			}
			if httpCalls != 0 {
				t.Fatalf("expected 0 HTTP calls, got %d", httpCalls)
			}
		}
	})

	t.Run("tumblr-bare-domain-normalized-to-www", func(t *testing.T) {
		tbSpec, _ := LookupSite("tumblr")
		var extractedURL string
		p := NewWithResolver(tbSpec, &stubExtractor{
			enabled: true,
			extract: func(_ context.Context, rawURL string) (*ytengine.Info, error) {
				extractedURL = rawURL
				return loadFixtureInfo(t, "tumblr.json"), nil
			},
		}, nil, pubResolver)
		if p.AllowedOutboundHost("tumblr.com") || p.AllowedTargetURL("https://tumblr.com/maskofthedragon/626907179849564160") {
			t.Fatal("bare tumblr.com must never enter yt-dlp target or outbound allowlist")
		}
		res, err := p.Parse(context.Background(), "https://tumblr.com/maskofthedragon/626907179849564160", now)
		if err != nil || unified.Str(res["code"]) != "OK" {
			t.Fatalf("tumblr bare Parse failed: res=%v err=%v", res, err)
		}
		if extractedURL != "https://www.tumblr.com/maskofthedragon/626907179849564160" {
			t.Fatalf("extractedURL = %q, want canonical www.tumblr.com", extractedURL)
		}
	})

	t.Run("facebook-bare-mobile-and-fbwatch-local-hint-zero-outbound", func(t *testing.T) {
		fbSpec, _ := LookupSite("facebook")
		if testP := New(fbSpec, nil, nil); testP.AllowedOutboundHost("fb.watch") || testP.AllowedOutboundHost("facebook.com") || testP.AllowedOutboundHost("m.facebook.com") || testP.AllowedTargetURL("https://fb.watch/abc123/") || testP.AllowedTargetURL("https://m.facebook.com/watch/?v=1") {
			t.Fatal("fb.watch, facebook.com, m.facebook.com must NEVER enter yt-dlp target or outbound allowlists")
		}

		for _, input := range []string{
			"https://facebook.com/watch/?v=647537299265662",
			"https://m.facebook.com/watch/?v=647537299265662",
		} {
			var extractedURL string
			p := NewWithResolver(fbSpec, &stubExtractor{
				enabled: true,
				extract: func(_ context.Context, rawURL string) (*ytengine.Info, error) {
					extractedURL = rawURL
					return loadFixtureInfo(t, "facebook.json"), nil
				},
			}, nil, pubResolver)
			res, err := p.Parse(context.Background(), input, now)
			if err != nil || unified.Str(res["code"]) != "OK" {
				t.Fatalf("Parse(%q) failed: res=%v err=%v", input, res, err)
			}
			if extractedURL != "https://www.facebook.com/watch/?v=647537299265662" {
				t.Fatalf("Parse(%q) passed %q to yt-dlp, want https://www.facebook.com/watch/?v=647537299265662", input, extractedURL)
			}
		}

		// fb.watch short links must still be recognized as Facebook in catalog,
		// return 400 INVALID_INPUT locally with actionable copy, and make ZERO outbound calls.
		httpCalls := 0
		ytdlpCalls := 0
		dnsCalls := 0
		countingResolver := stubResolver{
			ips:   pubResolver.ips,
			calls: &dnsCalls,
		}
		policyP := New(fbSpec, nil, nil)
		prober := ytengine.NewSizeProber(executorFunc(func(_ context.Context, req fetch.Request) (fetch.Response, error) {
			httpCalls++
			t.Fatalf("fb.watch must make ZERO outbound HTTP requests, got %s %s", req.Method, req.URL)
			return fetch.Response{}, nil
		}), policyP.AllowedMediaURL)
		fbWatchParser := NewWithResolver(fbSpec, &stubExtractor{
			enabled: true,
			extract: func(_ context.Context, rawURL string) (*ytengine.Info, error) {
				ytdlpCalls++
				t.Fatalf("fb.watch must make ZERO yt-dlp invocations, got %q", rawURL)
				return nil, nil
			},
		}, prober, countingResolver)

		for _, shortURL := range []string{
			"https://fb.watch/vtkorrRM0p/",
			"http://fb.watch/vv4KcJ16Ft/",
			"https://FB.WATCH:443/v/abc123/?mibextid=1",
		} {
			if got := c.PlatformForURL(shortURL); got != "facebook" {
				t.Fatalf("catalog.PlatformForURL(%q) = %q, want facebook", shortURL, got)
			}
			res, err := fbWatchParser.Parse(context.Background(), shortURL, now)
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", shortURL, err)
			}
			if err := unified.ValidateEnvelopeAt(res, now); err != nil {
				t.Fatalf("Parse(%q) ValidateEnvelopeAt failed: %v", shortURL, err)
			}
			errObj := unified.Map(res["error"])
			msg := unified.Str(errObj["message"])
			if unified.Str(res["code"]) != "INVALID_INPUT" || errObj["retryable"] == true {
				t.Fatalf("Parse(%q) = %v, want INVALID_INPUT non-retryable", shortURL, res)
			}
			if msg != fbWatchLocalHint || !strings.Contains(msg, "facebook.com 的完整视频链接") {
				t.Fatalf("Parse(%q) message = %q, want %q", shortURL, msg, fbWatchLocalHint)
			}
			if httpCalls != 0 || ytdlpCalls != 0 || dnsCalls != 0 {
				t.Fatalf("Parse(%q) outbound calls: http=%d ytdlp=%d dns=%d, want 0/0/0", shortURL, httpCalls, ytdlpCalls, dnsCalls)
			}
		}

		// Lookalikes, subdomains, userinfo, non-default ports, and bare root are still rejected with 0 outbound calls.
		for _, rejectedURL := range []string{
			"https://evilfb.watch/abc123/",
			"https://fb.watch.evil.com/abc123/",
			"https://www.fb.watch/abc123/",
			"https://sub.fb.watch/abc123/",
			"https://user:pass@fb.watch/abc123/",
			"https://fb.watch:8080/abc123/",
			"https://fb.watch:8443/abc123/",
			"https://fb.watch/",
		} {
			res, err := fbWatchParser.Parse(context.Background(), rejectedURL, now)
			if err != nil || unified.Str(res["code"]) != "INVALID_INPUT" {
				t.Fatalf("Parse(%q) = %v (err=%v), want INVALID_INPUT", rejectedURL, res, err)
			}
			if httpCalls != 0 || ytdlpCalls != 0 || dnsCalls != 0 {
				t.Fatalf("Parse(%q) outbound calls: http=%d ytdlp=%d dns=%d, want 0/0/0", rejectedURL, httpCalls, ytdlpCalls, dnsCalls)
			}
		}
	})
}
