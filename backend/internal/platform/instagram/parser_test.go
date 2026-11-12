package instagram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
					bodyStr := string(req.Body)
					if strings.Contains(bodyStr, "fetch_comment_count") || strings.Contains(bodyStr, "has_threaded_comments") {
						t.Fatalf("expected GraphQL variables to omit comment fields, got body=%q", bodyStr)
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

func TestParseInstagramGraphQLFailurePathsFallbackToYtDlpError(t *testing.T) {
	const now int64 = 1762300800

	failureCases := []struct {
		name          string
		graphqlStatus int
		graphqlBody   []byte
		ytDlpErr      *ytengine.Error
		wantCode      string
		wantMessage   string
	}{
		{
			name:          "200-with-error-json-falls-back-to-ytdlp-error",
			graphqlStatus: http.StatusOK,
			graphqlBody:   []byte(`{"errors":[{"message":"Unsupported get request. Object with ID does not exist"}],"status":"fail"}`),
			ytDlpErr: &ytengine.Error{
				Code:      ytengine.CodeUpstreamBlocked,
				Message:   "该内容受访问限制（如私密账号或年龄限制），无法无凭据解析",
				Retryable: false,
			},
			wantCode:    "UPSTREAM_BLOCKED",
			wantMessage: "该内容受访问限制（如私密账号或年龄限制），无法无凭据解析",
		},
		{
			name:          "200-with-html-falls-back-to-ytdlp-error",
			graphqlStatus: http.StatusOK,
			graphqlBody:   []byte(`<!DOCTYPE html><html><head><title>Login • Instagram</title></head><body>Please log in</body></html>`),
			ytDlpErr: &ytengine.Error{
				Code:      ytengine.CodeContentUnavailable,
				Message:   "未找到可用的视频或该推文已不可访问",
				Retryable: false,
			},
			wantCode:    "CONTENT_UNAVAILABLE",
			wantMessage: "未找到可用的视频或该推文已不可访问",
		},
		{
			name:          "4xx-status-falls-back-to-ytdlp-error",
			graphqlStatus: http.StatusForbidden,
			graphqlBody:   []byte(`{"message":"CSRF token missing or incorrect","status":"fail"}`),
			ytDlpErr: &ytengine.Error{
				Code:      ytengine.CodeUpstreamBlocked,
				Message:   "该内容受访问限制（如私密账号或年龄限制），无法无凭据解析",
				Retryable: false,
			},
			wantCode:    "UPSTREAM_BLOCKED",
			wantMessage: "该内容受访问限制（如私密账号或年龄限制），无法无凭据解析",
		},
	}

	for _, tc := range failureCases {
		t.Run(tc.name, func(t *testing.T) {
			var hitGraphQL bool
			p := NewWithFetcher(
				&stubExtractor{
					enabled: true,
					extract: func(context.Context, string) (*ytengine.Info, error) {
						return nil, tc.ytDlpErr
					},
				},
				nil,
				fakeFetcher{
					fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
						if strings.HasSuffix(req.URL, "/embed/captioned/") {
							return fetch.Response{
								StatusCode: http.StatusOK,
								Body:       []byte(`<html><body>No media in embed</body></html>`),
								URL:        req.URL,
							}, nil
						}
						if req.URL == "https://www.instagram.com/api/graphql" {
							hitGraphQL = true
							return fetch.Response{
								StatusCode: tc.graphqlStatus,
								Body:       tc.graphqlBody,
								URL:        req.URL,
							}, nil
						}
						return fetch.Response{StatusCode: http.StatusNotFound}, nil
					},
				},
			)

			env, err := p.Parse(context.Background(), "https://www.instagram.com/reel/CTApqC8BGWd/", now)
			if err != nil {
				t.Fatalf("expected Parse not to return raw error, got %v", err)
			}
			if !hitGraphQL {
				t.Fatalf("expected GraphQL tier to be attempted in %s", tc.name)
			}
			if gotCode := unified.Str(env["code"]); gotCode != tc.wantCode {
				t.Fatalf("expected code=%q, got %q (env=%+v)", tc.wantCode, gotCode, env)
			}
			errObj := unified.Map(env["error"])
			if gotMsg := unified.Str(errObj["message"]); gotMsg != tc.wantMessage {
				t.Fatalf("expected error.message=%q, got %q", tc.wantMessage, gotMsg)
			}
		})
	}
}

func TestAllowedPageURLHTTPSOnly(t *testing.T) {
	allowed := []string{
		"https://www.instagram.com/p/CTApqC8BGWd/embed/captioned/",
		"https://www.instagram.com:443/api/graphql",
		"https://instagram.com/p/CTApqC8BGWd/embed/captioned/",
		"https://i.instagram.com/api/v1/media/123/info/",
	}
	for _, raw := range allowed {
		if !AllowedPageURL(raw) {
			t.Fatalf("expected AllowedPageURL(%q) == true", raw)
		}
	}

	rejected := []string{
		"http://www.instagram.com/p/CTApqC8BGWd/embed/captioned/",
		"http://www.instagram.com:80/p/CTApqC8BGWd/embed/captioned/",
		"http://instagram.com/api/graphql",
		"https://www.instagram.com:8443/p/CTApqC8BGWd/embed/captioned/",
		"https://user:pass@www.instagram.com/p/CTApqC8BGWd/embed/captioned/",
		"https://evil.example.com/p/CTApqC8BGWd/embed/captioned/",
	}
	for _, raw := range rejected {
		if AllowedPageURL(raw) {
			t.Fatalf("expected AllowedPageURL(%q) == false", raw)
		}
	}
}

func TestInstagramSingleImageMultiImageMixedCompletionAndLoginBlocked(t *testing.T) {
	const now int64 = 1762300800

	// 1. Single-image post: Embed lacks contextJSON (has srcset), GraphQL supplies full metadata + max-area 1080x1350 over trailing 1080x1080 crop.
	singleEmbedHTML := `<img class="EmbeddedMediaImage" src="https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/s640.jpg" srcset="https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/s640.jpg 640w, https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/s1080.jpg 1080w" />`
	singleGraphQL, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"xdt_shortcode_media": map[string]any{
				"__typename": "XDTGraphImage",
				"id":         "4100001",
				"shortcode":  "BqvsDleB3lV",
				"dimensions": map[string]any{"width": 1080, "height": 1350},
				"display_resources": []any{
					map[string]any{"src": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/single_640x800.jpg", "config_width": 640, "config_height": 800},
					map[string]any{"src": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/single_1080x1350_orig.jpg", "config_width": 1080, "config_height": 1350},
					map[string]any{"src": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/single_1080x1080_crop.jpg", "config_width": 1080, "config_height": 1080},
				},
				"edge_media_to_caption": map[string]any{
					"edges": []any{
						map[string]any{"node": map[string]any{"text": "Single Photo Portrait"}},
					},
				},
			},
		},
	})
	singleParser := NewWithFetcher(
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
					return fetch.Response{StatusCode: http.StatusOK, Body: []byte(singleEmbedHTML), URL: req.URL}, nil
				}
				return fetch.Response{StatusCode: http.StatusOK, Body: singleGraphQL, URL: req.URL}, nil
			},
		},
	)
	singleEnv, err := singleParser.Parse(context.Background(), "https://www.instagram.com/p/BqvsDleB3lV/", now)
	if err != nil {
		t.Fatalf("single image Parse failed: %v", err)
	}
	singleView := unified.PublicView(singleEnv, false, now)
	if err := unified.ValidateEnvelopeAt(singleView, now); err != nil {
		t.Fatalf("single image ValidateEnvelopeAt failed: %v", err)
	}
	singleItem := unified.Map(unified.Slice(unified.Map(singleView["data"])["items"])[0])
	if !strings.Contains(unified.Str(unified.Map(singleItem["default"])["url"]), "single_1080x1350_orig.jpg") {
		t.Fatalf("expected uncropped 1080x1350 candidate over trailing 1080x1080 square crop, got %+v", singleItem["default"])
	}
	singleMeta := unified.Map(unified.Map(unified.Slice(singleItem["variants"])[0])["metadata"])
	if singleMeta["width"] != int64(1080) || singleMeta["height"] != int64(1350) {
		t.Fatalf("expected 1080x1350 metadata on single image, got %+v", singleMeta)
	}

	// 2. Pure multi-image carousel fixture (3 photos via image_versions2.candidates max-area selection).
	multiImageGraphQL, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"xdt_shortcode_media": map[string]any{
				"__typename": "XDTGraphSidecar",
				"id":         "4100002",
				"shortcode":  "DaBUYYOjNgA",
				"edge_sidecar_to_children": map[string]any{
					"edges": []any{
						map[string]any{
							"node": map[string]any{
								"__typename": "GraphImage",
								"id":         "c1",
								"image_versions2": map[string]any{
									"candidates": []any{
										map[string]any{"url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/c1_720.webp", "width": 720, "height": 900},
										map[string]any{"url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/c1_1440x1800.webp", "width": 1440, "height": 1800},
									},
								},
							},
						},
						map[string]any{
							"node": map[string]any{
								"__typename":  "GraphImage",
								"id":          "c2",
								"display_url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/c2_1440x1800.jpg",
								"dimensions":  map[string]any{"width": 1440, "height": 1800},
							},
						},
						map[string]any{
							"node": map[string]any{
								"__typename":  "GraphImage",
								"id":          "c3",
								"display_url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/c3_1440x1800.png",
								"dimensions":  map[string]any{"width": 1440, "height": 1800},
							},
						},
					},
				},
			},
		},
	})
	multiImageParser := NewWithFetcher(
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
				return fetch.Response{StatusCode: http.StatusOK, Body: multiImageGraphQL, URL: req.URL}, nil
			},
		},
	)
	multiEnv, err := multiImageParser.Parse(context.Background(), "https://www.instagram.com/p/DaBUYYOjNgA/", now)
	if err != nil {
		t.Fatalf("multi-image carousel Parse failed: %v", err)
	}
	multiView := unified.PublicView(multiEnv, false, now)
	if err := unified.ValidateEnvelopeAt(multiView, now); err != nil {
		t.Fatalf("multi-image carousel ValidateEnvelopeAt failed: %v", err)
	}
	multiItems := unified.Slice(unified.Map(multiView["data"])["items"])
	if len(multiItems) != 3 || !strings.Contains(unified.Str(unified.Map(unified.Map(multiItems[0])["default"])["url"]), "c1_1440x1800.webp") {
		t.Fatalf("expected 3 full-res carousel images starting with c1_1440x1800.webp, got %+v", multiItems)
	}

	// 3. Mixed carousel where yt-dlp only returns the 1 video item (omitting the 2 image slides):
	// native pipeline completes all 3 slides while preserving yt-dlp's richer video variants on slide 1.
	mixedSidecarCtx := map[string]any{
		"gql_data": map[string]any{
			"shortcode_media": map[string]any{
				"__typename": "GraphSidecar",
				"id":         "4100003",
				"shortcode":  "DIqghhpok2K",
				"edge_sidecar_to_children": map[string]any{
					"edges": []any{
						map[string]any{
							"node": map[string]any{
								"__typename":     "GraphVideo",
								"id":             "vid-slide-1",
								"is_video":       true,
								"display_url":    "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/vid1_poster.jpg",
								"video_url":      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t16/f2/m69/vid1_native.mp4",
								"video_duration": 12.0,
								"dimensions":     map[string]any{"width": 720, "height": 1280},
							},
						},
						map[string]any{
							"node": map[string]any{
								"__typename":  "GraphImage",
								"id":          "img-slide-2",
								"display_url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/img2_1440.jpg",
								"dimensions":  map[string]any{"width": 1440, "height": 1800},
							},
						},
						map[string]any{
							"node": map[string]any{
								"__typename":  "GraphImage",
								"id":          "img-slide-3",
								"display_url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/img3_1440.jpg",
								"dimensions":  map[string]any{"width": 1440, "height": 1800},
							},
						},
					},
				},
			},
		},
	}
	rawMixedCtx, _ := json.Marshal(mixedSidecarCtx)
	escapedMixedCtx, _ := json.Marshal(string(rawMixedCtx))
	mixedEmbedHTML := `<script>window.__data = {"contextJSON":` + string(escapedMixedCtx) + `};</script>`

	mixedCompletionParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return &ytengine.Info{
					ID:    "DIqghhpok2K",
					Title: "Mixed Post Video Only From YTDLP",
					Formats: []ytengine.Format{
						{
							FormatID: "0",
							URL:      "https://scontent-nrt1-2.cdninstagram.com/o1/v/t16/f2/m69/vid1_ytdlp_probed.mp4",
							Ext:      "mp4",
							Protocol: "https",
							Width:    int64Ptr(720),
							Height:   int64Ptr(1280),
							Filesize: int64Ptr(4096000),
						},
					},
				}, nil
			},
		},
		nil,
		fakeFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: []byte(mixedEmbedHTML), URL: req.URL}, nil
			},
		},
	)
	completedEnv, err := mixedCompletionParser.Parse(context.Background(), "https://www.instagram.com/p/DIqghhpok2K/", now)
	if err != nil {
		t.Fatalf("mixed completion Parse failed: %v", err)
	}
	completedView := unified.PublicView(completedEnv, false, now)
	if err := unified.ValidateEnvelopeAt(completedView, now); err != nil {
		t.Fatalf("mixed completion ValidateEnvelopeAt failed: %v", err)
	}
	completedData := unified.Map(completedView["data"])
	completedItems := unified.Slice(completedData["items"])
	if len(completedItems) != 3 {
		t.Fatalf("expected 3 completed carousel items (1 video + 2 images), got %d", len(completedItems))
	}
	if !strings.Contains(unified.Str(unified.Map(unified.Map(completedItems[0])["default"])["url"]), "vid1_ytdlp_probed.mp4") {
		t.Fatalf("expected slide 1 video to preserve yt-dlp variant URL, got %+v", unified.Map(completedItems[0])["default"])
	}
	if unified.Str(unified.Map(completedItems[1])["type"]) != "image" || unified.Str(unified.Map(completedItems[2])["type"]) != "image" {
		t.Fatalf("expected slides 2 and 3 to be completed image items, got %+v", completedItems)
	}

	// 4. Stories, private long-token posts, and login-gated posts explicitly return "需要登录，暂不支持".
	loginBlockedParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return nil, &ytengine.Error{
					Code:      ytengine.CodeUpstreamUnavailable,
					Message:   "当前平台解析服务暂不可用，请稍后重试",
					Retryable: true,
					Cause:     errors.New("ERROR: [Instagram] CVm3L6qjykFPSee: Instagram sent an empty media response. Check if this post is accessible in your browser without being logged-in."),
				}
			},
		},
		nil,
		fakeFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				if strings.HasSuffix(req.URL, "/embed/captioned/") {
					return fetch.Response{StatusCode: http.StatusOK, Body: []byte(`<html><body>BrokenMedia</body></html>`), URL: req.URL}, nil
				}
				return fetch.Response{StatusCode: http.StatusOK, Body: []byte(`{"data":{"xdt_shortcode_media":null},"status":"ok"}`), URL: req.URL}, nil
			},
		},
	)
	for _, loginURL := range []string{
		"https://www.instagram.com/stories/instagram/1234567890123456789/",
		"https://www.instagram.com/stories/highlights/17900000000000000/",
		"https://www.instagram.com/p/C1234567890abcdef1234567890abcdef1234567/",
		"https://www.instagram.com/p/CVm3L6qjykFPSee/",
	} {
		blockedEnv, err := loginBlockedParser.Parse(context.Background(), loginURL, now)
		if err != nil {
			t.Fatalf("loginBlocked Parse(%q) returned Go error: %v", loginURL, err)
		}
		if err := unified.ValidateEnvelopeAt(blockedEnv, now); err != nil {
			t.Fatalf("loginBlocked ValidateEnvelopeAt(%q) failed: %v", loginURL, err)
		}
		errObj := unified.Map(blockedEnv["error"])
		if unified.Str(blockedEnv["code"]) != "UPSTREAM_BLOCKED" ||
			unified.Str(errObj["message"]) != "需要登录，暂不支持" ||
			errObj["retryable"] == true {
			t.Fatalf("Parse(%q) = %+v, want UPSTREAM_BLOCKED '需要登录，暂不支持' (non-retryable)", loginURL, blockedEnv)
		}
	}
}

func TestParseInstagramPostVideoParallelNativeSupplementTimeoutAndErrorFallback(t *testing.T) {
	const now int64 = 1762300800
	const shortcode = "DIqghhpok2K"
	const postURL = "https://www.instagram.com/p/" + shortcode + "/"

	sampleYTVideo := func() *ytengine.Info {
		return &ytengine.Info{
			ID:        shortcode,
			Title:     "Instagram /p/ Video Post",
			Thumbnail: "https://scontent-nrt1-2.cdninstagram.com/v/t51.2885-15/post_thumb.jpg",
			Formats: []ytengine.Format{
				{
					FormatID: "0",
					URL:      "https://scontent-nrt1-2.cdninstagram.com/o1/v/t16/f2/m69/post_video_720.mp4",
					Ext:      "mp4",
					Protocol: "https",
					Width:    int64Ptr(720),
					Height:   int64Ptr(1280),
					Filesize: int64Ptr(4096000),
				},
			},
		}
	}

	// 1. Default native supplement timeout is bounded to 3 seconds (~3s per PR #65 follow-up).
	defaultParser := NewWithFetcher(&stubExtractor{enabled: true}, nil, fakeFetcher{})
	if got := defaultParser.effectiveSupplementTimeout(); got != 3*time.Second {
		t.Fatalf("effectiveSupplementTimeout() = %v, want 3s", got)
	}

	// 2. Deterministic barrier handshake proving yt-dlp and native supplement run concurrently on /p/,
	// and completed carousel images remain preserved even when yt-dlp finishes after supplementTimeout.
	ytEntered := make(chan struct{})
	nativeEntered := make(chan struct{})
	bothRunning := make(chan struct{})
	go func() {
		<-ytEntered
		<-nativeEntered
		close(bothRunning)
	}()

	mixedSidecarCtx := map[string]any{
		"gql_data": map[string]any{
			"shortcode_media": map[string]any{
				"__typename": "GraphSidecar",
				"id":         "5100001",
				"shortcode":  shortcode,
				"edge_sidecar_to_children": map[string]any{
					"edges": []any{
						map[string]any{
							"node": map[string]any{
								"__typename":     "GraphVideo",
								"id":             "vid-slide-1",
								"is_video":       true,
								"display_url":    "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/vid1_poster.jpg",
								"video_url":      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t16/f2/m69/vid1_native.mp4",
								"video_duration": 12.0,
								"dimensions":     map[string]any{"width": 720, "height": 1280},
							},
						},
						map[string]any{
							"node": map[string]any{
								"__typename":  "GraphImage",
								"id":          "img-slide-2",
								"display_url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/img2_1440.jpg",
								"dimensions":  map[string]any{"width": 1440, "height": 1800},
							},
						},
					},
				},
			},
		},
	}
	rawMixedCtx, _ := json.Marshal(mixedSidecarCtx)
	escapedMixedCtx, _ := json.Marshal(string(rawMixedCtx))
	mixedEmbedHTML := `<script>window.__data = {"contextJSON":` + string(escapedMixedCtx) + `};</script>`

	parallelParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(ctx context.Context, _ string) (*ytengine.Info, error) {
				close(ytEntered)
				select {
				case <-bothRunning:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				time.Sleep(60 * time.Millisecond)
				return sampleYTVideo(), nil
			},
		},
		nil,
		fakeFetcher{
			fn: func(reqCtx context.Context, req fetch.Request) (fetch.Response, error) {
				close(nativeEntered)
				select {
				case <-bothRunning:
				case <-reqCtx.Done():
					return fetch.Response{}, reqCtx.Err()
				}
				return fetch.Response{StatusCode: http.StatusOK, Body: []byte(mixedEmbedHTML), URL: req.URL}, nil
			},
		},
	)
	parallelParser.supplementTimeout = 30 * time.Millisecond

	parallelCtx, cancelParallel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelParallel()
	parallelEnv, err := parallelParser.Parse(parallelCtx, postURL, now)
	if err != nil {
		t.Fatalf("parallel mixed /p/ Parse failed: %v", err)
	}
	parallelItems := unified.Slice(unified.Map(unified.PublicView(parallelEnv, false, now)["data"])["items"])
	if len(parallelItems) != 2 || unified.Str(unified.Map(parallelItems[0])["type"]) != "video" || unified.Str(unified.Map(parallelItems[1])["type"]) != "image" {
		t.Fatalf("expected [video, image] from parallel mixed /p/ carousel, got %+v", parallelItems)
	}

	// 3. Hung native supplement on a /p/ video post is cancelled at supplementTimeout without
	// entering Tier 2 GraphQL, and returns the yt-dlp video result directly.
	embedCanceled := make(chan struct{})
	var graphqlCalled atomic.Bool
	timeoutParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return sampleYTVideo(), nil
			},
		},
		nil,
		fakeFetcher{
			fn: func(reqCtx context.Context, req fetch.Request) (fetch.Response, error) {
				if req.URL == "https://www.instagram.com/api/graphql" {
					graphqlCalled.Store(true)
					return fetch.Response{StatusCode: http.StatusInternalServerError}, nil
				}
				<-reqCtx.Done()
				close(embedCanceled)
				return fetch.Response{}, reqCtx.Err()
			},
		},
	)
	timeoutParser.supplementTimeout = 80 * time.Millisecond

	start := time.Now()
	videoEnv, err := timeoutParser.Parse(context.Background(), postURL, now)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("timeout fallback Parse returned Go error: %v", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("expected native supplement timeout to cut off promptly (<1s), took %v", elapsed)
	}
	select {
	case <-embedCanceled:
	case <-time.After(time.Second):
		t.Fatal("expected hung native embed request context to be cancelled on supplement timeout")
	}
	if graphqlCalled.Load() {
		t.Fatal("expected Tier 2 GraphQL not to start after native supplement context cancellation")
	}
	videoView := unified.PublicView(videoEnv, false, now)
	if err := unified.ValidateEnvelopeAt(videoView, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt failed on /p/ video fallback envelope: %v", err)
	}
	if unified.Str(videoView["code"]) != "OK" {
		t.Fatalf("expected code=OK video result on native supplement timeout, got %+v", videoView)
	}
	videoItems := unified.Slice(unified.Map(videoView["data"])["items"])
	if len(videoItems) != 1 || !strings.Contains(unified.Str(unified.Map(unified.Map(videoItems[0])["default"])["url"]), "post_video_720.mp4") {
		t.Fatalf("expected yt-dlp post_video_720.mp4 result on supplement timeout, got %+v", videoItems)
	}

	// 4. Native supplement failures (HTTP 403/429/500, login-wall HTML/JSON, malformed body)
	// on a /p/ video post return the yt-dlp video result directly without failing.
	nativeFailureCases := []struct {
		name    string
		fetchFn func(context.Context, fetch.Request) (fetch.Response, error)
	}{
		{
			name: "http-429-both-tiers",
			fetchFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusTooManyRequests, URL: req.URL}, nil
			},
		},
		{
			name: "http-500-both-tiers",
			fetchFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusInternalServerError, URL: req.URL}, nil
			},
		},
		{
			name: "login-wall-on-native-when-ytdlp-succeeded",
			fetchFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				if strings.HasSuffix(req.URL, "/embed/captioned/") {
					return fetch.Response{StatusCode: http.StatusOK, Body: []byte(`<html>Log in to watch</html>`), URL: req.URL}, nil
				}
				return fetch.Response{StatusCode: http.StatusOK, Body: []byte(`{"data":{"xdt_shortcode_media":null},"status":"ok"}`), URL: req.URL}, nil
			},
		},
		{
			name: "malformed-body",
			fetchFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: []byte(`{not valid`), URL: req.URL}, nil
			},
		},
	}
	for _, tc := range nativeFailureCases {
		t.Run("post-video-ignores-native-"+tc.name, func(t *testing.T) {
			p := NewWithFetcher(
				&stubExtractor{
					enabled: true,
					extract: func(context.Context, string) (*ytengine.Info, error) {
						return sampleYTVideo(), nil
					},
				},
				nil,
				fakeFetcher{fn: tc.fetchFn},
			)
			env, err := p.Parse(context.Background(), postURL, now)
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if unified.Str(env["code"]) != "OK" {
				t.Fatalf("expected code=OK video result despite native %s, got %+v", tc.name, env)
			}
		})
	}

	// 5. Pure-image /p/ post where yt-dlp returns CONTENT_UNAVAILABLE is NOT cut off by
	// supplementTimeout and reuses the single in-flight native fetch without duplicating requests.
	var embedHits atomic.Int32
	pureImageSidecar := map[string]any{
		"gql_data": map[string]any{
			"shortcode_media": map[string]any{
				"__typename": "GraphSidecar",
				"id":         "5100009",
				"shortcode":  "BoHk1haB5tM",
				"edge_sidecar_to_children": map[string]any{
					"edges": []any{
						map[string]any{
							"node": map[string]any{
								"__typename":  "GraphImage",
								"id":          "slow-img-1",
								"display_url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/slow_img1.jpg",
								"dimensions":  map[string]any{"width": 1080, "height": 1350},
							},
						},
						map[string]any{
							"node": map[string]any{
								"__typename":  "GraphImage",
								"id":          "slow-img-2",
								"display_url": "https://scontent-nrt1-1.cdninstagram.com/v/t51.29350-15/slow_img2.jpg",
								"dimensions":  map[string]any{"width": 1080, "height": 1350},
							},
						},
					},
				},
			},
		},
	}
	rawPureCtx, _ := json.Marshal(pureImageSidecar)
	escapedPureCtx, _ := json.Marshal(string(rawPureCtx))
	pureEmbedHTML := `<script>window.__data = {"contextJSON":` + string(escapedPureCtx) + `};</script>`

	slowPureImageParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				time.Sleep(15 * time.Millisecond)
				return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "There is no video in this post"}
			},
		},
		nil,
		fakeFetcher{
			fn: func(reqCtx context.Context, req fetch.Request) (fetch.Response, error) {
				embedHits.Add(1)
				select {
				case <-time.After(90 * time.Millisecond):
					return fetch.Response{StatusCode: http.StatusOK, Body: []byte(pureEmbedHTML), URL: req.URL}, nil
				case <-reqCtx.Done():
					return fetch.Response{}, reqCtx.Err()
				}
			},
		},
	)
	slowPureImageParser.supplementTimeout = 30 * time.Millisecond

	slowPureEnv, err := slowPureImageParser.Parse(context.Background(), "https://www.instagram.com/p/BoHk1haB5tM/", now)
	if err != nil || unified.Str(slowPureEnv["code"]) != "OK" {
		t.Fatalf("expected pure-image /p/ post slower than supplementTimeout to succeed, got env=%+v err=%v", slowPureEnv, err)
	}
	if gotHits := embedHits.Load(); gotHits != 1 {
		t.Fatalf("expected exactly 1 native embed fetch (reused from parallel task), got %d", gotHits)
	}
	if gotItems := len(unified.Slice(unified.Map(slowPureEnv["data"])["items"])); gotItems != 2 {
		t.Fatalf("expected 2 image items, got %d", gotItems)
	}
}
