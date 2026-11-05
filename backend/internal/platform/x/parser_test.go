package x

import (
	"context"
	"errors"
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

func TestExtractTweetTargetValidation(t *testing.T) {
	valid := []struct {
		raw       string
		wantID    string
		wantCanon string
		wantIndex int
	}{
		{
			raw:       "https://x.com/historyinmemes/status/1790637656616943991",
			wantID:    "1790637656616943991",
			wantCanon: "https://x.com/i/status/1790637656616943991",
			wantIndex: 0,
		},
		{
			raw:       "https://twitter.com/UltimaShadowX/status/1577719286659006464/video/3?s=20",
			wantID:    "1577719286659006464",
			wantCanon: "https://x.com/i/status/1577719286659006464",
			wantIndex: 3,
		},
		{
			raw:       "https://mobile.twitter.com/i/web/status/1790637656616943991/",
			wantID:    "1790637656616943991",
			wantCanon: "https://x.com/i/status/1790637656616943991",
			wantIndex: 0,
		},
		{
			raw:       "https://www.x.com/i/status/1790637656616943991/photo/2",
			wantID:    "1790637656616943991",
			wantCanon: "https://x.com/i/status/1790637656616943991",
			wantIndex: 2,
		},
	}
	for _, tc := range valid {
		id, canon, idx, ok := ExtractTweetTarget(tc.raw)
		if !ok || id != tc.wantID || canon != tc.wantCanon || idx != tc.wantIndex {
			t.Fatalf("ExtractTweetTarget(%q) = (%q, %q, %d, %v), want (%q, %q, %d, true)",
				tc.raw, id, canon, idx, ok, tc.wantID, tc.wantCanon, tc.wantIndex)
		}
		if !AllowedTargetURL(canon) {
			t.Fatalf("AllowedTargetURL(%q) = false, want true", canon)
		}
	}

	invalid := []string{
		"",
		"https://x.com/",
		"https://x.com/historyinmemes",
		"https://x.com/home/status/1790637656616943991",
		"https://x.com/search?q=test",
		"https://x.com/historyinmemes/status/0123",
		"https://x.com/historyinmemes/status/abc",
		"https://x.com/historyinmemes/status/1790637656616943991/video/0",
		"https://x.com/historyinmemes/status/1790637656616943991/video/5",
		"https://x.com/historyinmemes/status/1790637656616943991/retweets",
		"https://user:pass@x.com/historyinmemes/status/1790637656616943991",
		"https://x.com:8443/historyinmemes/status/1790637656616943991",
		"https://evil-x.com/historyinmemes/status/1790637656616943991",
		"file:///etc/passwd",
	}
	for _, raw := range invalid {
		if _, _, _, ok := ExtractTweetTarget(raw); ok {
			t.Fatalf("ExtractTweetTarget(%q) unexpectedly succeeded", raw)
		}
		if AllowedTargetURL(raw) {
			t.Fatalf("AllowedTargetURL(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestParseSingleVideoTweetAndHLSDistinction(t *testing.T) {
	const now int64 = 1762300800
	sourceURL := "https://x.com/historyinmemes/status/1790637656616943991"

	info := &ytengine.Info{
		ID:           "1790637589910654976",
		DisplayID:    "1790637656616943991",
		Type:         "video",
		Title:        "Historic Vids - One of the most intense moments in history",
		Description:  "One of the most intense moments in history https://t.co/Zgzhvix8ES",
		Uploader:     "Historic Vids",
		UploaderID:   "historyinmemes",
		UploaderURL:  "https://twitter.com/historyinmemes",
		Timestamp:    float64Ptr(1715756260),
		Duration:     float64Ptr(15.488),
		Thumbnail:    "https://pbs.twimg.com/amplify_video_thumb/1790637589910654976/img/jyhLQQZkq-lJJ9fN.jpg?name=orig",
		ViewCount:    int64Ptr(34496834),
		LikeCount:    int64Ptr(132485),
		RepostCount:  int64Ptr(6779),
		CommentCount: int64Ptr(827),
		Formats: []ytengine.Format{
			{
				FormatID: "hls-audio-32000-Audio",
				URL:      "https://video.twimg.com/amplify_video/1790637589910654976/pl/mp4a/32000/audio32.m3u8",
				Ext:      "mp4",
				Protocol: "m3u8_native",
				VCodec:   "none",
				ACodec:   "mp4a.40.2",
				ABR:      float64Ptr(32),
			},
			{
				FormatID: "hls-audio-128000-Audio",
				URL:      "https://video.twimg.com/amplify_video/1790637589910654976/pl/mp4a/128000/audio128.m3u8",
				Ext:      "mp4",
				Protocol: "m3u8_native",
				VCodec:   "none",
				ACodec:   "mp4a.40.2",
				ABR:      float64Ptr(128),
			},
			{
				FormatID:     "hls-1015",
				URL:          "https://video.twimg.com/amplify_video/1790637589910654976/pl/avc1/728x720/hls720.m3u8",
				Ext:          "mp4",
				Protocol:     "m3u8_native",
				Width:        int64Ptr(728),
				Height:       int64Ptr(720),
				FPS:          float64Ptr(29.97),
				TBR:          float64Ptr(1015.709),
				DynamicRange: "SDR",
				VCodec:       "avc1.640020",
				ACodec:       "none",
			},
			{
				FormatID:     "hls-3200-1080p",
				URL:          "https://video.twimg.com/amplify_video/1790637589910654976/pl/avc1/1936x1920/hls1080.m3u8",
				Ext:          "mp4",
				Protocol:     "m3u8_native",
				Width:        int64Ptr(1936),
				Height:       int64Ptr(1920),
				FPS:          float64Ptr(29.97),
				TBR:          float64Ptr(3200.0),
				DynamicRange: "SDR",
				VCodec:       "avc1.640032",
				ACodec:       "none",
			},
			{
				FormatID:       "http-288",
				URL:            "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/272x270/vid270.mp4?tag=14",
				Ext:            "mp4",
				Protocol:       "https",
				Width:          int64Ptr(272),
				Height:         int64Ptr(270),
				TBR:            float64Ptr(288.0),
				DynamicRange:   "SDR",
				Filesize:       nil,
				FilesizeApprox: int64Ptr(557568),
				HTTPHeaders:    map[string]string{"Authorization": "Bearer secret-token", "Cookie": "guest_id=123"},
			},
			{
				FormatID:       "http-832",
				URL:            "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/364x360/vid360.mp4?tag=14",
				Ext:            "mp4",
				Protocol:       "https",
				Width:          int64Ptr(364),
				Height:         int64Ptr(360),
				TBR:            float64Ptr(832.0),
				DynamicRange:   "SDR",
				Filesize:       int64Ptr(780123),
				FilesizeApprox: int64Ptr(1610547),
			},
			{
				FormatID:       "http-2176",
				URL:            "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/kF5AeH6MK-1qDlBg.mp4?tag=14",
				Ext:            "mp4",
				Protocol:       "https",
				Width:          int64Ptr(728),
				Height:         int64Ptr(720),
				TBR:            float64Ptr(2176.0),
				DynamicRange:   "SDR",
				Filesize:       nil,
				FilesizeApprox: int64Ptr(4212736),
			},
		},
	}

	var gotExtractURL string
	parser := New(&stubExtractor{
		enabled: true,
		extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
			gotExtractURL = rawURL
			return info, nil
		},
	})

	payload, err := parser.Parse(context.Background(), sourceURL, now)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if gotExtractURL != "https://x.com/i/status/1790637656616943991" {
		t.Fatalf("unexpected canonical URL passed to ytengine: %q", gotExtractURL)
	}
	if err := unified.ValidateEnvelopeAt(payload, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt failed: %v", err)
	}

	publicPayload := unified.PublicView(payload, false, now)
	if err := unified.ValidateEnvelopeAt(publicPayload, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt(PublicView) failed: %v", err)
	}

	// Ensure private HTTPHeaders never leak into public JSON
	encoded, err := unified.Encode(publicPayload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-token") || strings.Contains(string(encoded), "guest_id") {
		t.Fatalf("private http_headers leaked into public payload: %s", string(encoded))
	}

	data := unified.Map(publicPayload["data"])
	if unified.Str(data["platform"]) != "x" || unified.Str(data["content_id"]) != "1790637656616943991" {
		t.Fatalf("unexpected platform/content_id: %v", data)
	}
	items := unified.Slice(data["items"])
	if len(items) != 1 {
		t.Fatalf("expected 1 video item, got %d", len(items))
	}
	item := unified.Map(items[0])
	def := unified.Map(item["default"])
	if def == nil || !strings.Contains(unified.Str(def["url"]), "kF5AeH6MK-1qDlBg.mp4") {
		t.Fatalf("expected default to select highest progressive MP4 (720p), got %+v", def)
	}

	// Verify variants: progressive MP4s have kind="file", HLS variants have kind="hls" and scope="adaptive"
	fileCount, hlsCount := 0, 0
	for _, vVal := range unified.Slice(item["variants"]) {
		v := unified.Map(vVal)
		delivery := unified.Map(v["delivery"])
		kind := unified.Str(delivery["kind"])
		sources := unified.Slice(delivery["sources"])
		if len(sources) != 1 {
			t.Fatalf("expected 1 source per variant, got %d", len(sources))
		}
		src := unified.Map(sources[0])
		srcURL := unified.Str(src["url"])

		switch kind {
		case "file":
			fileCount++
			if strings.HasSuffix(srcURL, ".m3u8") {
				t.Fatalf("HLS m3u8 URL %q must never be emitted with delivery.kind=file", srcURL)
			}
			// Check exact filesize vs filesize_approx boundary
			if strings.Contains(srcURL, "kF5AeH6MK-1qDlBg.mp4") || strings.Contains(srcURL, "vid270.mp4") {
				if src["size_bytes"] != nil {
					t.Fatalf("filesize_approx was erroneously mapped to size_bytes on %s: %v", srcURL, src["size_bytes"])
				}
			}
			if strings.Contains(srcURL, "vid360.mp4") {
				if src["size_bytes"] != int64(780123) {
					t.Fatalf("expected exact filesize 780123 on vid360.mp4, got %v", src["size_bytes"])
				}
			}
		case "hls":
			hlsCount++
			if unified.Str(delivery["scope"]) != "adaptive" {
				t.Fatalf("expected HLS variant scope=adaptive so single-file Worker never claims it, got %v", delivery["scope"])
			}
			sel := unified.Map(delivery["selection"])
			if unified.Str(sel["video_representation_id"]) == "" || unified.Str(sel["audio_representation_id"]) != "hls-audio-128000-Audio" {
				t.Fatalf("expected HLS selection with video_representation_id and best audio_representation_id, got %+v", sel)
			}
			if !strings.Contains(unified.Str(v["label"]), "HLS 需合流") {
				t.Fatalf("expected HLS label to indicate 'HLS 需合流', got %q", unified.Str(v["label"]))
			}
		default:
			t.Fatalf("unexpected delivery kind %q", kind)
		}
	}
	if fileCount != 3 || hlsCount != 2 {
		t.Fatalf("expected 3 file variants and 2 HLS video variants (excluding audio-only HLS), got file=%d hls=%d", fileCount, hlsCount)
	}

	// Higher-resolution HLS variant (1936x1920 > 728x720) must be listed in higher_quality_variants with requires=["hls"]
	higher := unified.Slice(item["higher_quality_variants"])
	if len(higher) != 1 {
		t.Fatalf("expected 1 higher_quality_variants entry for 1080p HLS, got %+v", higher)
	}
	hq := unified.Map(higher[0])
	reqs := unified.Slice(hq["requires"])
	if len(reqs) != 1 || unified.Str(reqs[0]) != "hls" {
		t.Fatalf("expected higher_quality_variants requires=['hls'], got %+v", hq)
	}
}

func TestParseMultiVideoTweetFourEntries(t *testing.T) {
	const now int64 = 1762300800
	sourceURL := "https://x.com/UltimaShadowX/status/1577719286659006464/video/3"

	makeEntry := func(num string, mediaID string) ytengine.Info {
		return ytengine.Info{
			ID:        mediaID,
			DisplayID: "1577719286659006464",
			Title:     "Ultima - Test #" + num,
			Duration:  float64Ptr(4.266),
			Thumbnail: "https://pbs.twimg.com/ext_tw_video_thumb/" + mediaID + "/pu/img/thumb.jpg?name=orig",
			Formats: []ytengine.Format{
				{
					FormatID: "hls-2176",
					URL:      "https://video.twimg.com/ext_tw_video/" + mediaID + "/pu/pl/1280x720/playlist.m3u8",
					Ext:      "mp4",
					Protocol: "m3u8_native",
					Width:    int64Ptr(1280),
					Height:   int64Ptr(720),
					VCodec:   "avc1.640020",
					ACodec:   "mp4a.40.2",
				},
				{
					FormatID: "http-832",
					URL:      "https://video.twimg.com/ext_tw_video/" + mediaID + "/pu/vid/640x360/v360.mp4?tag=12",
					Ext:      "mp4",
					Protocol: "https",
					Width:    int64Ptr(640),
					Height:   int64Ptr(360),
					TBR:      float64Ptr(832),
				},
				{
					FormatID: "http-2176",
					URL:      "https://video.twimg.com/ext_tw_video/" + mediaID + "/pu/vid/1280x720/v720.mp4?tag=12",
					Ext:      "mp4",
					Protocol: "https",
					Width:    int64Ptr(1280),
					Height:   int64Ptr(720),
					TBR:      float64Ptr(2176),
				},
			},
		}
	}

	playlist := &ytengine.Info{
		ID:          "1577719286659006464",
		Type:        "playlist",
		Title:       "Ultima - Test",
		Description: "Test https://t.co/Y3KEZD7Dad",
		Uploader:    "Ultima",
		UploaderID:  "UltimaShadowX",
		Timestamp:   float64Ptr(1664992565),
		LikeCount:   int64Ptr(2096),
		RepostCount: int64Ptr(433),
		Entries: []ytengine.Info{
			makeEntry("1", "1577719236209922048"),
			makeEntry("2", "1577719239326273536"),
			makeEntry("3", "1577719242006466562"),
			makeEntry("4", "1577719245114413056"),
		},
	}

	parser := New(&stubExtractor{
		enabled: true,
		extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
			if rawURL != "https://x.com/i/status/1577719286659006464" {
				t.Fatalf("expected canonical status URL without /video/3 suffix so yt-dlp returns all entries, got %q", rawURL)
			}
			return playlist, nil
		},
	})

	payload, err := parser.Parse(context.Background(), sourceURL, now)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if err := unified.ValidateEnvelopeAt(payload, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt failed: %v", err)
	}

	data := unified.Map(payload["data"])
	items := unified.Slice(data["items"])
	if len(items) != 4 {
		t.Fatalf("expected 4 video items in multi-video tweet, got %d", len(items))
	}
	thirdID := unified.Str(unified.Map(items[2])["id"])
	if unified.Str(data["default_item_id"]) != thirdID {
		t.Fatalf("expected /video/3 URL to select 3rd item %q as default_item_id, got %q", thirdID, unified.Str(data["default_item_id"]))
	}
}

func TestDegradationAndCancellation(t *testing.T) {
	const now int64 = 1762300800
	url := "https://x.com/historyinmemes/status/1790637656616943991"

	// 1. Disabled engine -> SERVICE_NOT_CONFIGURED (valid error envelope)
	disabledParser := New(ytengine.NewDisabled("missing binary"))
	payload, err := disabledParser.Parse(context.Background(), url, now)
	if err != nil {
		t.Fatalf("expected nil Go error on graceful degradation, got %v", err)
	}
	if unified.Str(payload["code"]) != "SERVICE_NOT_CONFIGURED" {
		t.Fatalf("expected SERVICE_NOT_CONFIGURED, got %v", payload["code"])
	}
	if err := unified.ValidateEnvelopeAt(payload, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt failed on error envelope: %v", err)
	}

	// 2. Upstream rate limit -> RATE_LIMITED (retryable: true)
	rlParser := New(&stubExtractor{
		enabled: true,
		extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
			return nil, &ytengine.Error{Code: ytengine.CodeRateLimited, Message: "上游访问频率受限", Retryable: true}
		},
	})
	rlPayload, err := rlParser.Parse(context.Background(), url, now)
	if err != nil || unified.Str(rlPayload["code"]) != "RATE_LIMITED" {
		t.Fatalf("expected RATE_LIMITED, got %v (err=%v)", rlPayload, err)
	}

	// 3. Context cancelled -> returns context.Canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rlParser.Parse(ctx, url, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
