package youtube

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

func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }

func TestExtractVideoTargetValidation(t *testing.T) {
	valid := []struct {
		raw       string
		wantID    string
		wantCanon string
	}{
		{
			raw:       "https://www.youtube.com/watch?v=aqz-KE-bpKQ",
			wantID:    "aqz-KE-bpKQ",
			wantCanon: "https://www.youtube.com/watch?v=aqz-KE-bpKQ",
		},
		{
			raw:       "https://youtu.be/jNQXAC9IVRw?si=test123",
			wantID:    "jNQXAC9IVRw",
			wantCanon: "https://www.youtube.com/watch?v=jNQXAC9IVRw",
		},
		{
			raw:       "https://www.youtube.com/shorts/aqz-KE-bpKQ?feature=share",
			wantID:    "aqz-KE-bpKQ",
			wantCanon: "https://www.youtube.com/watch?v=aqz-KE-bpKQ",
		},
	}
	for _, tc := range valid {
		id, canon, ok := ExtractVideoTarget(tc.raw)
		if !ok || id != tc.wantID || canon != tc.wantCanon {
			t.Fatalf("ExtractVideoTarget(%q) = (%q, %q, %v), want (%q, %q, true)",
				tc.raw, id, canon, ok, tc.wantID, tc.wantCanon)
		}
		if !AllowedTargetURL(canon) {
			t.Fatalf("AllowedTargetURL(%q) = false, want true", canon)
		}
	}

	for _, invalid := range []string{
		"",
		"https://www.youtube.com/",
		"https://www.youtube.com/watch?v=short",
		"https://www.youtube.com/playlist?list=PL123",
		"https://evil-youtube.com/watch?v=aqz-KE-bpKQ",
	} {
		if _, _, ok := ExtractVideoTarget(invalid); ok {
			t.Fatalf("ExtractVideoTarget(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestParseYouTube4KMuxAndProgressiveFile(t *testing.T) {
	const now int64 = 1762300800
	sourceURL := "https://www.youtube.com/watch?v=aqz-KE-bpKQ"

	info := &ytengine.Info{
		ID:          "aqz-KE-bpKQ",
		Title:       "Big Buck Bunny 60fps 4K - Official Blender Foundation Short Film",
		Description: "Big Buck Bunny 4K60 test",
		Uploader:    "Blender Foundation",
		UploaderID:  "@BlenderFoundation",
		Duration:    float64Ptr(634.533),
		Thumbnail:   "https://i.ytimg.com/vi_webp/aqz-KE-bpKQ/maxresdefault.webp",
		ViewCount:   int64Ptr(25000000),
		LikeCount:   int64Ptr(350000),
		Formats: []ytengine.Format{
			{
				FormatID:       "18",
				URL:            "https://rr2---sn-ogueln67.googlevideo.com/videoplayback?itag=18&mime=video%2Fmp4",
				Ext:            "mp4",
				Protocol:       "https",
				Width:          int64Ptr(640),
				Height:         int64Ptr(360),
				FPS:            float64Ptr(30),
				TBR:            float64Ptr(359.7),
				DynamicRange:   "SDR",
				VCodec:         "avc1.42001E",
				ACodec:         "mp4a.40.2",
				Filesize:       nil,
				FilesizeApprox: int64Ptr(28526904),
			},
			{
				FormatID:      "140",
				URL:           "https://rr2---sn-ogueln67.googlevideo.com/videoplayback?itag=140&mime=audio%2Fmp4",
				Ext:           "m4a",
				Protocol:      "https",
				VCodec:        "none",
				ACodec:        "mp4a.40.2",
				ABR:           float64Ptr(129.476),
				ASR:           int64Ptr(44100),
				AudioChannels: int64Ptr(2),
				Filesize:      int64Ptr(10271496),
			},
			{
				FormatID:     "299",
				URL:          "https://rr2---sn-ogueln67.googlevideo.com/videoplayback?itag=299&mime=video%2Fmp4",
				Ext:          "mp4",
				Protocol:     "https",
				Width:        int64Ptr(1920),
				Height:       int64Ptr(1080),
				FPS:          float64Ptr(60),
				VBR:          float64Ptr(3247.989),
				DynamicRange: "SDR",
				VCodec:       "avc1.64002A",
				ACodec:       "none",
				Filesize:     int64Ptr(257619653),
			},
			{
				FormatID:     "400",
				URL:          "https://rr2---sn-ogueln67.googlevideo.com/videoplayback?itag=400&mime=video%2Fmp4",
				Ext:          "mp4",
				Protocol:     "https",
				Width:        int64Ptr(2560),
				Height:       int64Ptr(1440),
				FPS:          float64Ptr(60),
				VBR:          float64Ptr(3940.591),
				DynamicRange: "SDR",
				VCodec:       "av01.0.12M.08",
				ACodec:       "none",
				Filesize:     int64Ptr(312554313),
			},
			{
				FormatID:     "401",
				URL:          "https://rr2---sn-ogueln67.googlevideo.com/videoplayback?itag=401&mime=video%2Fmp4",
				Ext:          "mp4",
				Protocol:     "https",
				Width:        int64Ptr(3840),
				Height:       int64Ptr(2160),
				FPS:          float64Ptr(60),
				VBR:          float64Ptr(8982.295),
				DynamicRange: "SDR",
				VCodec:       "av01.0.13M.08",
				ACodec:       "none",
				Filesize:     int64Ptr(712445280),
			},
		},
	}

	parser := NewWithSizeProber(&stubExtractor{
		enabled: true,
		extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
			return info, nil
		},
	}, func(ctx context.Context, rawURL string) (int64, bool) {
		if strings.Contains(rawURL, "itag=18") {
			return 28523658, true
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

	data := unified.Map(pub["data"])
	item := unified.Map(unified.Slice(data["items"])[0])
	def := unified.Map(item["default"])
	if def == nil || !strings.Contains(unified.Str(def["url"]), "itag=18") {
		t.Fatalf("expected complete audiovisual file (itag=18 360p) as default, got %+v", def)
	}

	variants := unified.Slice(item["variants"])
	if len(variants) != 5 {
		t.Fatalf("expected 5 file variants (2160p60, 1440p60, 1080p60, 360p, audio M4A), got %d: %+v", len(variants), variants)
	}
	wantLabels := []string{
		"2160p60 (4K · 仅视频无声)",
		"1440p60 (2K · 仅视频无声)",
		"1080p60 (仅视频无声)",
		"360p (含音频)",
		"仅音频 M4A (129 kbps)",
	}
	for i, want := range wantLabels {
		v := unified.Map(variants[i])
		if got := unified.Str(v["label"]); got != want {
			t.Fatalf("variant[%d] label = %q, want %q", i, got, want)
		}
		if kind := unified.Str(unified.Map(v["delivery"])["kind"]); kind != "file" {
			t.Fatalf("variant[%d] kind = %q, want file", i, kind)
		}
	}

	attachments := unified.Slice(data["attachments"])
	if len(attachments) != 1 {
		t.Fatalf("expected 1 background_audio attachment, got %d", len(attachments))
	}
	att := unified.Map(attachments[0])
	if unified.Str(att["role"]) != "background_audio" {
		t.Fatalf("expected role=background_audio, got %v", att["role"])
	}
}

func TestYouTubeSourceIdentityPreservedAcrossSignatureRenewal(t *testing.T) {
	const now int64 = 1762300800
	sourceURL := "https://www.youtube.com/watch?v=aqz-KE-bpKQ"

	makeFormat := func(urlStr, itag string, size int64) ytengine.Format {
		return ytengine.Format{
			FormatID: itag,
			URL:      urlStr,
			Ext:      "mp4",
			Protocol: "https",
			Width:    int64Ptr(640),
			Height:   int64Ptr(360),
			VCodec:   "avc1.42001E",
			ACodec:   "mp4a.40.2",
			Filesize: int64Ptr(size),
		}
	}

	url1 := "https://rr1---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762322400&ei=AAA&ip=1.2.3.4&id=o-abc123&itag=18&source=youtube&requiressl=yes&clen=28523658&dur=634.566&lmt=1700000000000000&sparams=expire%2Cei%2Cip%2Cid%2Citag%2Csource%2Crequiressl%2Cclen%2Cdur%2Clmt&sig=SIG_ONE"
	url2 := "https://rr5---sn-ogueln67.googlevideo.com/videoplayback?expire=1762350000&ei=BBB&ip=5.6.7.8&id=o-abc123&itag=18&source=youtube&requiressl=yes&clen=28523658&dur=634.566&lmt=1700000000000000&sparams=expire%2Cei%2Cip%2Cid%2Citag%2Csource%2Crequiressl%2Cclen%2Cdur%2Clmt&sig=SIG_TWO"
	urlDiffItag := "https://rr5---sn-ogueln67.googlevideo.com/videoplayback?expire=1762350000&ei=BBB&ip=5.6.7.8&id=o-abc123&itag=22&source=youtube&requiressl=yes&clen=28523658&dur=634.566&lmt=1700000000000000&sparams=expire%2Cei%2Cip%2Cid%2Citag%2Csource%2Crequiressl%2Cclen%2Cdur%2Clmt&sig=SIG_TWO"

	runParse := func(f ytengine.Format, ts int64) unified.Object {
		p := New(&stubExtractor{
			enabled: true,
			extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
				return &ytengine.Info{
					ID:      "aqz-KE-bpKQ",
					Title:   "Big Buck Bunny",
					Formats: []ytengine.Format{f},
				}, nil
			},
		})
		out, err := p.Parse(context.Background(), sourceURL, ts)
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		return unified.PublicView(out, false, ts)
	}

	pub1 := runParse(makeFormat(url1, "18", 28523658), now)
	pub2 := runParse(makeFormat(url2, "18", 28523658), now+120)
	pubDiff := runParse(makeFormat(urlDiffItag, "22", 28523658), now+120)

	def1 := unified.Map(unified.Map(unified.Slice(unified.Map(pub1["data"])["items"])[0])["default"])
	def2 := unified.Map(unified.Map(unified.Slice(unified.Map(pub2["data"])["items"])[0])["default"])
	defDiff := unified.Map(unified.Map(unified.Slice(unified.Map(pubDiff["data"])["items"])[0])["default"])

	if unified.Str(def1["variant_id"]) != unified.Str(def2["variant_id"]) || unified.Str(def1["source_id"]) != unified.Str(def2["source_id"]) {
		t.Fatalf("expected YouTube variant_id and source_id to stay stable across signature renewal: %+v vs %+v", def1, def2)
	}
	if !strings.HasPrefix(unified.Str(def2["url"]), url2) {
		t.Fatalf("expected renewed url2 prefix to be preserved in default.url, got %q", unified.Str(def2["url"]))
	}
	if unified.Str(def1["source_id"]) == unified.Str(defDiff["source_id"]) {
		t.Fatalf("expected YouTube source_id to change when itag changes")
	}
}
