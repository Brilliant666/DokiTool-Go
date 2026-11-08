package instagram

import (
	"context"
	"strings"
	"testing"

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
