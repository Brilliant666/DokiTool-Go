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
	src := unified.Map(unified.Slice(unified.Map(firstVariant["delivery"])["sources"])[0])
	if src["size_bytes"] != int64(3362175) {
		t.Fatalf("expected probed size_bytes=3362175, got %v", src["size_bytes"])
	}
}
