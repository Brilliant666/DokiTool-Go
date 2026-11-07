package instagram

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

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

func int64Ptr(v int64) *int64 { return &v }

func TestExtractPostTargetValidation(t *testing.T) {
	valid := []struct {
		raw       string
		wantID    string
		wantCanon string
	}{
		{
			raw:       "https://www.instagram.com/reel/CTApqC8BGWd/?igsh=abc",
			wantID:    "CTApqC8BGWd",
			wantCanon: "https://www.instagram.com/reel/CTApqC8BGWd/",
		},
		{
			raw:       "https://www.instagram.com/p/aye83DjauH/",
			wantID:    "aye83DjauH",
			wantCanon: "https://www.instagram.com/p/aye83DjauH/",
		},
		{
			raw:       "https://www.instagram.com/someuser/reels/CTApqC8BGWd",
			wantID:    "CTApqC8BGWd",
			wantCanon: "https://www.instagram.com/reel/CTApqC8BGWd/",
		},
	}
	for _, tc := range valid {
		id, canon, ok := ExtractPostTarget(tc.raw)
		if !ok || id != tc.wantID || canon != tc.wantCanon {
			t.Fatalf("ExtractPostTarget(%q) = (%q, %q, %v), want (%q, %q, true)",
				tc.raw, id, canon, ok, tc.wantID, tc.wantCanon)
		}
		if !AllowedTargetURL(canon) {
			t.Fatalf("AllowedTargetURL(%q) = false, want true", canon)
		}
	}
}

func TestParseInstagramReelCodecLessProgressiveMP4(t *testing.T) {
	const now int64 = 1762300800
	sourceURL := "https://www.instagram.com/reel/CTApqC8BGWd/"

	// Real yt-dlp 2026.10.26 Instagram Reel shape: formats "1", "2", "3" have nil vcodec/acodec/width/height
	// while top-level Info has Width=720, Height=1280 and dash-* formats have explicit codecs.
	info := &ytengine.Info{
		ID:        "CTApqC8BGWd",
		Title:     "Video by instagram",
		Uploader:  "Instagram",
		Width:     int64Ptr(720),
		Height:    int64Ptr(1280),
		Thumbnail: "https://scontent-nrt1-2.cdninstagram.com/v/t51.2885-15/thumb.jpg",
		Formats: []ytengine.Format{
			{
				FormatID: "dash-600443267638332a",
				URL:      "https://scontent-nrt1-2.cdninstagram.com/v/t50.2886-16/audio.m4a",
				Ext:      "m4a",
				Protocol: "https",
				VCodec:   "none",
				ACodec:   "mp4a.40.5",
			},
			{
				FormatID: "1",
				URL:      "https://scontent-nrt1-2.cdninstagram.com/o1/v/t16/f2/m69/progressive720.mp4",
				Ext:      "mp4",
				Protocol: "https",
			},
			{
				FormatID: "2",
				URL:      "https://scontent-nrt1-2.cdninstagram.com/o1/v/t16/f2/m69/progressive720.mp4",
				Ext:      "mp4",
				Protocol: "https",
			},
			{
				FormatID: "3",
				URL:      "https://scontent-nrt1-2.cdninstagram.com/o1/v/t16/f2/m69/progressive720.mp4",
				Ext:      "mp4",
				Protocol: "https",
			},
			{
				FormatID: "dash-1450327211989271v",
				URL:      "https://scontent-nrt1-2.cdninstagram.com/o1/v/t16/f2/m69/dash720.mp4",
				Ext:      "mp4",
				Protocol: "https",
				Width:    int64Ptr(720),
				Height:   int64Ptr(1280),
				VCodec:   "avc1.64001F",
				ACodec:   "none",
			},
		},
	}

	parser := NewWithSizeProber(&stubExtractor{
		enabled: true,
		extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
			return info, nil
		},
	}, func(ctx context.Context, rawURL string) (int64, bool) {
		if strings.Contains(rawURL, "progressive720.mp4") {
			return 3362175, true
		}
		return 0, false
	})

	payload, err := parser.Parse(context.Background(), sourceURL, now)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if err := unified.ValidateEnvelopeAt(payload, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt failed: %v", err)
	}
	pub := unified.PublicView(payload, false, now)
	if err := unified.ValidateEnvelopeAt(pub, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt(PublicView) failed: %v", err)
	}

	item := unified.Map(unified.Slice(unified.Map(pub["data"])["items"])[0])
	def := unified.Map(item["default"])
	if def == nil || !strings.Contains(unified.Str(def["url"]), "progressive720.mp4") {
		t.Fatalf("expected progressive720.mp4 as default, got %+v", def)
	}
	variants := unified.Slice(item["variants"])
	firstVariant := unified.Map(variants[0])
	if unified.Str(firstVariant["label"]) != "720p" {
		t.Fatalf("expected inherited 720p label on progressive Reel MP4, got %v", firstVariant["label"])
	}
	firstMeta := unified.Map(firstVariant["metadata"])
	if _, exists := firstMeta["has_audio"]; exists {
		t.Fatalf("expected codec-less progressive MP4 with empty ACodec to omit has_audio (unknown), got %v", firstMeta["has_audio"])
	}
	src := unified.Map(unified.Slice(unified.Map(firstVariant["delivery"])["sources"])[0])
	if src["size_bytes"] != int64(3362175) {
		t.Fatalf("expected probed size_bytes=3362175, got %v", src["size_bytes"])
	}
}

func TestInstagramSourceIdentityPreservedAcrossCDNTokenRenewal(t *testing.T) {
	const now int64 = 1762300800
	sourceURL := "https://www.instagram.com/reel/C8abc123XYZ/"

	url1 := "https://scontent-lax3-1.cdninstagram.com/o1/v/t16/f2/m69/AQM123/progressive720.mp4?efg=eyJ0ZXN0IjoxfQ&_nc_ht=scontent-lax3-1.cdninstagram.com&_nc_cat=104&oh=00_AYB111&oe=690D9EC0"
	url2 := "https://scontent-sjc3-1.cdninstagram.com/o1/v/t16/f2/m69/AQM123/progressive720.mp4?efg=eyJ0ZXN0IjoyfQ&_nc_ht=scontent-sjc3-1.cdninstagram.com&_nc_cat=108&oh=00_AYB222&oe=690EEFA0"
	urlDiffAsset := "https://scontent-sjc3-1.cdninstagram.com/o1/v/t16/f2/m69/AQM999_OTHER/progressive720.mp4?oh=00_AYB222&oe=690EEFA0"

	runParse := func(mediaURL string, ts int64) unified.Object {
		p := New(&stubExtractor{
			enabled: true,
			extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
				return &ytengine.Info{
					ID:    "C8abc123XYZ",
					Title: "Instagram Reel",
					Formats: []ytengine.Format{
						{
							FormatID: "0",
							URL:      mediaURL,
							Ext:      "mp4",
							Protocol: "https",
							Width:    int64Ptr(720),
							Height:   int64Ptr(1280),
							Filesize: int64Ptr(3362175),
						},
					},
				}, nil
			},
		})
		out, err := p.Parse(context.Background(), sourceURL, ts)
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		return unified.PublicView(out, false, ts)
	}

	pub1 := runParse(url1, now)
	pub2 := runParse(url2, now+60)
	pubDiff := runParse(urlDiffAsset, now+60)

	def1 := unified.Map(unified.Map(unified.Slice(unified.Map(pub1["data"])["items"])[0])["default"])
	def2 := unified.Map(unified.Map(unified.Slice(unified.Map(pub2["data"])["items"])[0])["default"])
	defDiff := unified.Map(unified.Map(unified.Slice(unified.Map(pubDiff["data"])["items"])[0])["default"])

	if unified.Str(def1["variant_id"]) != unified.Str(def2["variant_id"]) || unified.Str(def1["source_id"]) != unified.Str(def2["source_id"]) {
		t.Fatalf("expected Instagram variant_id and source_id to stay stable across CDN token renewal: %+v vs %+v", def1, def2)
	}
	if unified.Str(def2["url"]) != url2 {
		t.Fatalf("expected renewed Instagram URL bytes to be preserved, got %q", unified.Str(def2["url"]))
	}
	if unified.Str(def1["source_id"]) == unified.Str(defDiff["source_id"]) {
		t.Fatalf("expected Instagram source_id to change when asset path changes")
	}
}

func TestParseInstagramReelDimensionlessUnknownMP4PreferredOverSilentDASH(t *testing.T) {
	const now int64 = 1762300800
	sourceURL := "https://www.instagram.com/reel/CTApqC8BGWd/"

	// Matches real live CTApqC8BGWd response where top-level Width/Height are nil,
	// progressive format "1" has no width/height/vcodec/acodec, and 5 DASH video-only
	// formats have explicit dimensions and ACodec="none".
	info := &ytengine.Info{
		ID:        "CTApqC8BGWd",
		Title:     "Video by mrcigar",
		Uploader:  "RON GOH",
		Thumbnail: "https://scontent-nrt1-1.cdninstagram.com/v/t51.71878-15/thumb.jpg",
		Formats: []ytengine.Format{
			{
				FormatID: "dash-high-1",
				URL:      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t2/f2/m86/dash720.mp4",
				Ext:      "mp4",
				Protocol: "https",
				Width:    int64Ptr(720),
				Height:   int64Ptr(1280),
				VCodec:   "avc1.64001F",
				ACodec:   "none",
			},
			{
				FormatID: "dash-high-2",
				URL:      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t2/f2/m86/dash480.mp4",
				Ext:      "mp4",
				Protocol: "https",
				Width:    int64Ptr(480),
				Height:   int64Ptr(854),
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
				FormatID: "dash-audio-1",
				URL:      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t16/f2/m69/audio.mp4",
				Ext:      "m4a",
				Protocol: "https",
				VCodec:   "none",
				ACodec:   "mp4a.40.5",
			},
		},
	}

	parser := NewWithSizeProber(&stubExtractor{
		enabled: true,
		extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
			return info, nil
		},
	}, func(ctx context.Context, rawURL string) (int64, bool) {
		if strings.Contains(rawURL, "progressive_default.mp4") {
			return 3362175, true
		}
		return 0, false
	})

	payload, err := parser.Parse(context.Background(), sourceURL, now)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	for _, debug := range []bool{false, true} {
		view := unified.PublicView(payload, debug, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("ValidateEnvelopeAt(debug=%v) failed: %v", debug, err)
		}
		item := unified.Map(unified.Slice(unified.Map(view["data"])["items"])[0])
		def := unified.Map(item["default"])
		if def == nil || !strings.Contains(unified.Str(def["url"]), "progressive_default.mp4") {
			t.Fatalf("debug=%v: expected progressive_default.mp4 as default over 720p silent DASH, got %+v", debug, def)
		}
		variants := unified.Slice(item["variants"])
		if len(variants) != 4 {
			t.Fatalf("debug=%v: expected 4 variants (2 silent DASH + 1 unknown MP4 + 1 M4A), got %d", debug, len(variants))
		}
		progVariant := unified.Map(variants[2])
		if _, exists := unified.Map(progVariant["metadata"])["has_audio"]; exists {
			t.Fatalf("debug=%v: expected unknown-audio progressive MP4 to omit has_audio", debug)
		}
		dash720 := unified.Map(variants[0])
		if unified.Map(dash720["metadata"])["width"] != int64(720) || unified.Map(dash720["metadata"])["height"] != int64(1280) {
			t.Fatalf("debug=%v: expected 720p silent DASH variant to retain true width/height", debug)
		}
	}
}

type fakeFetcher struct {
	fn func(context.Context, fetch.Request) (fetch.Response, error)
}

func (f fakeFetcher) Do(ctx context.Context, req fetch.Request) (fetch.Response, error) {
	return f.fn(ctx, req)
}

func TestParseInstagramNativeEmbedAndGraphQLThreeTierPipeline(t *testing.T) {
	const now int64 = 1762300800

	// 1. Photo + Video Carousel (/p/aye83DjauH/) where yt-dlp fails with CONTENT_UNAVAILABLE ("no video")
	// and Tier 1 Embed Captioned returns a GraphSidecar with 2 photos + 1 video.
	sidecarContextObj := map[string]any{
		"gql_data": map[string]any{
			"shortcode_media": map[string]any{
				"__typename":         "GraphSidecar",
				"id":                 "3000000000000000001",
				"shortcode":          "aye83DjauH",
				"display_url":        "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/slide1.jpg",
				"taken_at_timestamp": 1762000000,
				"owner": map[string]any{
					"id":              "12345",
					"username":        "doki_creator",
					"full_name":       "Doki Creator",
					"profile_pic_url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.2885-19/avatar.jpg",
				},
				"edge_media_to_caption": map[string]any{
					"edges": []any{
						map[string]any{
							"node": map[string]any{"text": "Carousel Post Title\nSecond line"},
						},
					},
				},
				"edge_media_preview_like": map[string]any{"count": 1024},
				"edge_media_to_comment":   map[string]any{"count": 42},
				"edge_sidecar_to_children": map[string]any{
					"edges": []any{
						map[string]any{
							"node": map[string]any{
								"__typename":  "GraphImage",
								"id":          "child-img-1",
								"display_url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/slide1.jpg",
								"dimensions":  map[string]any{"width": 1080, "height": 1350},
							},
						},
						map[string]any{
							"node": map[string]any{
								"__typename":  "GraphImage",
								"id":          "child-img-2",
								"display_url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/slide2.webp",
								"dimensions":  map[string]any{"width": 1080, "height": 1350},
							},
						},
						map[string]any{
							"node": map[string]any{
								"__typename":     "GraphVideo",
								"id":             "child-vid-3",
								"is_video":       true,
								"display_url":    "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/slide3_cover.jpg",
								"video_url":      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t16/f2/m69/slide3_video.mp4",
								"video_duration": 14.2,
								"dimensions":     map[string]any{"width": 1080, "height": 1920},
							},
						},
					},
				},
			},
		},
	}
	rawCtxBytes, _ := json.Marshal(sidecarContextObj)
	escapedCtxBytes, _ := json.Marshal(string(rawCtxBytes))
	embedSidecarHTML := `<script>window.__data = {"contextJSON":` + string(escapedCtxBytes) + `};</script>`

	carouselParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "There is no video in this post"}
			},
		},
		func(_ context.Context, rawURL string) (int64, bool) {
			if strings.Contains(rawURL, "slide3_video.mp4") {
				return 5120000, true
			}
			return 0, false
		},
		fakeFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				if strings.HasSuffix(req.URL, "/embed/captioned/") {
					return fetch.Response{
						StatusCode: http.StatusOK,
						Body:       []byte(embedSidecarHTML),
						URL:        req.URL,
					}, nil
				}
				t.Fatalf("unexpected request URL in Tier 1 success case: %s", req.URL)
				return fetch.Response{}, nil
			},
		},
	)

	carouselEnv, err := carouselParser.Parse(context.Background(), "https://www.instagram.com/p/aye83DjauH/", now)
	if err != nil {
		t.Fatalf("carousel Parse failed: %v", err)
	}
	for _, debug := range []bool{false, true} {
		view := unified.PublicView(carouselEnv, debug, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("carousel ValidateEnvelopeAt(debug=%v) failed: %v", debug, err)
		}
		data := unified.Map(view["data"])
		items := unified.Slice(data["items"])
		if len(items) != 3 {
			t.Fatalf("expected 3 items in carousel, got %d", len(items))
		}
		if unified.Str(unified.Map(items[0])["type"]) != "image" ||
			unified.Str(unified.Map(items[1])["type"]) != "image" ||
			unified.Str(unified.Map(items[2])["type"]) != "video" {
			t.Fatalf("unexpected item types in carousel: %+v", items)
		}
		groups := unified.Slice(data["groups"])
		if len(groups) != 1 || len(unified.Slice(unified.Map(groups[0])["item_ids"])) != 2 {
			t.Fatalf("expected album group with 2 image item_ids, got %+v", groups)
		}
	}

	// 2. Reel URL (/reel/CTApqC8BGWd/) where yt-dlp is login-blocked, Tier 1 Embed only has a poster image
	// (no video_url), and Tier 2 Anonymous GraphQL recovers the video with audio.
	embedPosterOnlyHTML := `<img class="EmbeddedMediaImage" src="https://scontent-nrt1-1.cdninstagram.com/v/t51.2885-15/poster_only.jpg" />`
	graphqlVideoResp, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"xdt_shortcode_media": map[string]any{
				"__typename":      "XDTGraphVideo",
				"id":              "2662500000000000000",
				"shortcode":       "CTApqC8BGWd",
				"is_video":        true,
				"display_url":     "https://scontent-nrt1-1.cdninstagram.com/v/t51.2885-15/poster_only.jpg",
				"video_url":       "https://scontent-nrt1-2.cdninstagram.com/o1/v/t16/f2/m69/graphql_reel_720.mp4",
				"video_duration":  9.8,
				" dimensions":     map[string]any{"width": 720, "height": 1280},
				"original_width":  720,
				"original_height": 1280,
				"owner": map[string]any{
					"id":       "999",
					"username": "mrcigar",
				},
			},
		},
	})

	var hitEmbed, hitGraphQL bool
	reelFallbackParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return nil, &ytengine.Error{Code: ytengine.CodeUpstreamBlocked, Message: "login required"}
			},
		},
		nil,
		fakeFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				if strings.HasSuffix(req.URL, "/embed/captioned/") {
					hitEmbed = true
					return fetch.Response{
						StatusCode: http.StatusOK,
						Body:       []byte(embedPosterOnlyHTML),
						URL:        req.URL,
					}, nil
				}
				if req.URL == "https://www.instagram.com/api/graphql" && req.Method == http.MethodPost {
					hitGraphQL = true
					if req.Header.Get("X-IG-App-ID") != instagramAppID {
						t.Fatalf("missing X-IG-App-ID header on GraphQL request")
					}
					return fetch.Response{
						StatusCode: http.StatusOK,
						Body:       graphqlVideoResp,
						URL:        req.URL,
					}, nil
				}
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
				return fetch.Response{}, nil
			},
		},
	)

	reelEnv, err := reelFallbackParser.Parse(context.Background(), "https://www.instagram.com/reel/CTApqC8BGWd/", now)
	if err != nil {
		t.Fatalf("reel fallback Parse failed: %v", err)
	}
	if !hitEmbed || !hitGraphQL {
		t.Fatalf("expected Reel poster-only Embed to fall through to GraphQL: hitEmbed=%v hitGraphQL=%v", hitEmbed, hitGraphQL)
	}
	if err := unified.ValidateEnvelopeAt(unified.PublicView(reelEnv, false, now), now); err != nil {
		t.Fatalf("reel GraphQL fallback failed ValidateEnvelopeAt: %v", err)
	}
	reelItem := unified.Map(unified.Slice(unified.Map(reelEnv["data"])["items"])[0])
	if unified.Str(reelItem["type"]) != "video" || !strings.Contains(unified.Str(unified.Map(reelItem["default"])["url"]), "graphql_reel_720.mp4") {
		t.Fatalf("expected graphql_reel_720.mp4 video default, got %+v", reelItem)
	}

	// 3. LocalBusy on yt-dlp must return RATE_LIMITED immediately without invoking native fetcher
	busyParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return nil, &ytengine.Error{
					Code:       ytengine.CodeRateLimited,
					Message:    "本地解析引擎繁忙，请稍后重试",
					Retryable:  true,
					RetryAfter: 1,
					LocalBusy:  true,
				}
			},
		},
		nil,
		fakeFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				t.Fatalf("native fetcher must not be called when yt-dlp returns LocalBusy: %s", req.URL)
				return fetch.Response{}, nil
			},
		},
	)
	busyEnv, err := busyParser.Parse(context.Background(), "https://www.instagram.com/reel/CTApqC8BGWd/", now)
	if err != nil || unified.Str(busyEnv["code"]) != "RATE_LIMITED" {
		t.Fatalf("expected RATE_LIMITED on LocalBusy, got env=%v err=%v", busyEnv, err)
	}
}

func TestInstagramNativeEnvelopeDefaultSelectionEligibilityWithPR38Rules(t *testing.T) {
	const now int64 = 1762300800

	// Case 1: Native video omitting has_audio -> eligible under #38 Instagram default rules
	videoGraphQL, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"xdt_shortcode_media": map[string]any{
				"__typename":     "XDTGraphVideo",
				"id":             "3100001",
				"shortcode":      "CTApqC8BGWd",
				"is_video":       true,
				"display_url":    "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/cover.jpg",
				"video_url":      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t16/f2/m69/native_video_unknown_audio.mp4",
				"video_duration": 10.8,
				"dimensions":     map[string]any{"width": 720, "height": 1280},
			},
		},
	})
	videoParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return nil, &ytengine.Error{Code: ytengine.CodeUpstreamBlocked, Message: "login required"}
			},
		},
		nil,
		fakeFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				if strings.HasSuffix(req.URL, "/embed/captioned/") {
					return fetch.Response{StatusCode: http.StatusNotFound}, nil
				}
				return fetch.Response{StatusCode: http.StatusOK, Body: videoGraphQL, URL: req.URL}, nil
			},
		},
	)
	videoEnv, err := videoParser.Parse(context.Background(), "https://www.instagram.com/reel/CTApqC8BGWd/", now)
	if err != nil {
		t.Fatalf("native video Parse failed: %v", err)
	}
	for _, debug := range []bool{false, true} {
		view := unified.PublicView(videoEnv, debug, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("native video ValidateEnvelopeAt(debug=%v) failed: %v", debug, err)
		}
		item := unified.Map(unified.Slice(unified.Map(view["data"])["items"])[0])
		def := unified.Map(item["default"])
		if def == nil || !strings.Contains(unified.Str(def["url"]), "native_video_unknown_audio.mp4") {
			t.Fatalf("debug=%v: expected native_video_unknown_audio.mp4 as default, got %+v", debug, def)
		}
		variant := unified.Map(unified.Slice(item["variants"])[0])
		if _, hasAudioSet := unified.Map(variant["metadata"])["has_audio"]; hasAudioSet {
			t.Fatalf("debug=%v: native video must omit has_audio, got %+v", debug, variant["metadata"])
		}
	}

	// Case 2: Single photo post via Tier 1 Embed HTML
	singleImageHTML := `<img class="EmbeddedMediaImage" src="https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/single_photo_1080.jpg" />`
	imageParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "There is no video in this post"}
			},
		},
		nil,
		fakeFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: []byte(singleImageHTML), URL: req.URL}, nil
			},
		},
	)
	imageEnv, err := imageParser.Parse(context.Background(), "https://www.instagram.com/p/CsinglePhoto1/", now)
	if err != nil {
		t.Fatalf("single image Parse failed: %v", err)
	}
	for _, debug := range []bool{false, true} {
		view := unified.PublicView(imageEnv, debug, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("single image ValidateEnvelopeAt(debug=%v) failed: %v", debug, err)
		}
		data := unified.Map(view["data"])
		item := unified.Map(unified.Slice(data["items"])[0])
		if unified.Str(item["type"]) != "image" {
			t.Fatalf("debug=%v: expected item.type=image, got %+v", debug, item["type"])
		}
		def := unified.Map(item["default"])
		if def == nil || !strings.Contains(unified.Str(def["url"]), "single_photo_1080.jpg") {
			t.Fatalf("debug=%v: expected single_photo_1080.jpg default, got %+v", debug, def)
		}
	}

	// Case 3: Mixed image + video carousel album via Tier 2 GraphQL
	mixedCarouselGraphQL, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"xdt_shortcode_media": map[string]any{
				"__typename":  "XDTGraphSidecar",
				"id":          "3100003",
				"shortcode":   "aye83DjauH",
				"display_url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/album_slide1.jpg",
				"edge_sidecar_to_children": map[string]any{
					"edges": []any{
						map[string]any{
							"node": map[string]any{
								"__typename":  "GraphImage",
								"id":          "album-img-1",
								"display_url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/album_slide1.jpg",
								"dimensions":  map[string]any{"width": 1080, "height": 1080},
							},
						},
						map[string]any{
							"node": map[string]any{
								"__typename":     "GraphVideo",
								"id":             "album-vid-2",
								"is_video":       true,
								"display_url":    "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/album_slide2_poster.jpg",
								"video_url":      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t16/f2/m69/album_slide2_video.mp4",
								"video_duration": 8.0,
								"dimensions":     map[string]any{"width": 720, "height": 1280},
							},
						},
					},
				},
			},
		},
	})
	albumParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "There is no video in this post"}
			},
		},
		nil,
		fakeFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				if strings.HasSuffix(req.URL, "/embed/captioned/") {
					return fetch.Response{StatusCode: http.StatusNotFound}, nil
				}
				return fetch.Response{StatusCode: http.StatusOK, Body: mixedCarouselGraphQL, URL: req.URL}, nil
			},
		},
	)
	albumEnv, err := albumParser.Parse(context.Background(), "https://www.instagram.com/p/aye83DjauH/", now)
	if err != nil {
		t.Fatalf("mixed carousel Parse failed: %v", err)
	}
	for _, debug := range []bool{false, true} {
		view := unified.PublicView(albumEnv, debug, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("mixed carousel ValidateEnvelopeAt(debug=%v) failed: %v", debug, err)
		}
		data := unified.Map(view["data"])
		items := unified.Slice(data["items"])
		if len(items) != 2 {
			t.Fatalf("debug=%v: expected 2 items in mixed carousel, got %d", debug, len(items))
		}
		item0 := unified.Map(items[0])
		item1 := unified.Map(items[1])
		if unified.Str(data["default_item_id"]) != unified.Str(item0["id"]) {
			t.Fatalf("debug=%v: expected default_item_id=%s, got %s", debug, unified.Str(item0["id"]), unified.Str(data["default_item_id"]))
		}
		groups := unified.Slice(data["groups"])
		if len(groups) != 1 || unified.Str(unified.Map(groups[0])["kind"]) != "album" {
			t.Fatalf("debug=%v: expected album group, got %+v", debug, groups)
		}
		if !strings.Contains(unified.Str(unified.Map(item0["default"])["url"]), "album_slide1.jpg") {
			t.Fatalf("debug=%v: expected item-1 default to point to album_slide1.jpg, got %+v", debug, item0["default"])
		}
		if !strings.Contains(unified.Str(unified.Map(item1["default"])["url"]), "album_slide2_video.mp4") {
			t.Fatalf("debug=%v: expected item-2 default to point to album_slide2_video.mp4, got %+v", debug, item1["default"])
		}
		vidVar := unified.Map(unified.Slice(item1["variants"])[0])
		if _, hasAudioSet := unified.Map(vidVar["metadata"])["has_audio"]; hasAudioSet {
			t.Fatalf("debug=%v: expected carousel video variant to omit has_audio, got %+v", debug, vidVar["metadata"])
		}
	}
}
