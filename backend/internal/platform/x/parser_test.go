package x

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
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

func TestSizeProberPopulatesMissingMP4SizesAndFailsSoft(t *testing.T) {
	const now int64 = 1762300800
	sourceURL := "https://x.com/historyinmemes/status/1790637656616943991"

	var probeMu sync.Mutex
	probed := make(map[string]int)
	prober := func(ctx context.Context, rawURL string) (int64, bool) {
		probeMu.Lock()
		probed[rawURL]++
		probeMu.Unlock()
		if strings.Contains(rawURL, "kF5AeH6MK-1qDlBg.mp4") {
			return 1404023, true
		}
		// Simulate fail-soft on vid270.mp4
		return 0, false
	}

	parser := NewWithSizeProber(&stubExtractor{
		enabled: true,
		extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
			return &ytengine.Info{
				ID:        "1790637589910654976",
				DisplayID: "1790637656616943991",
				Title:     "Historic Vids",
				Formats: []ytengine.Format{
					{
						FormatID: "hls-1015",
						URL:      "https://video.twimg.com/amplify_video/1790637589910654976/pl/avc1/728x720/hls720.m3u8",
						Ext:      "mp4",
						Protocol: "m3u8_native",
						Width:    int64Ptr(728),
						Height:   int64Ptr(720),
						VCodec:   "avc1.640020",
						ACodec:   "mp4a.40.2",
					},
					{
						FormatID:       "http-288",
						URL:            "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/272x270/vid270.mp4?tag=14",
						Ext:            "mp4",
						Protocol:       "https",
						Width:          int64Ptr(272),
						Height:         int64Ptr(270),
						TBR:            float64Ptr(288),
						FilesizeApprox: int64Ptr(557568),
					},
					{
						FormatID:       "http-2176",
						URL:            "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/kF5AeH6MK-1qDlBg.mp4?tag=14",
						Ext:            "mp4",
						Protocol:       "https",
						Width:          int64Ptr(728),
						Height:         int64Ptr(720),
						TBR:            float64Ptr(2176),
						FilesizeApprox: int64Ptr(4212736),
					},
				},
			}, nil
		},
	}, prober)

	payload, err := parser.Parse(context.Background(), sourceURL, now)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if err := unified.ValidateEnvelopeAt(payload, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt failed: %v", err)
	}
	// HLS URL must never be probed
	for u := range probed {
		if strings.HasSuffix(u, ".m3u8") {
			t.Fatalf("HLS URL should never be probed for size: %s", u)
		}
	}

	item := unified.Map(unified.Slice(unified.Map(payload["data"])["items"])[0])
	variants := unified.Slice(item["variants"])
	firstSrc := unified.Map(unified.Slice(unified.Map(unified.Map(variants[0])["delivery"])["sources"])[0])
	if firstSrc["size_bytes"] != int64(1404023) {
		t.Fatalf("expected probed size_bytes=1404023 on 720p variant source, got %v", firstSrc["size_bytes"])
	}
	secondSrc := unified.Map(unified.Slice(unified.Map(unified.Map(variants[1])["delivery"])["sources"])[0])
	if secondSrc["size_bytes"] != nil {
		t.Fatalf("expected nil size_bytes on failed-soft 270p variant source, got %v", secondSrc["size_bytes"])
	}
}

func TestSelectedVideoPositionPreservedAcrossFilteredEntries(t *testing.T) {
	const now int64 = 1762300800

	makeValidEntry := func(mediaID string) ytengine.Info {
		return ytengine.Info{
			ID:        mediaID,
			DisplayID: "1577719286659006464",
			Title:     "Entry " + mediaID,
			Formats: []ytengine.Format{
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

	// Case 1: Entry #1 has no deliverable formats, Entry #2 ("111") and Entry #3 ("222") are deliverable.
	// Requesting /video/2 must select Entry #2 ("video-111"), NOT Entry #3 ("video-222").
	infoFirstFiltered := &ytengine.Info{
		ID:   "1577719286659006464",
		Type: "playlist",
		Entries: []ytengine.Info{
			{ID: "000", DisplayID: "1577719286659006464", Formats: nil},
			makeValidEntry("111"),
			makeValidEntry("222"),
		},
	}
	payload1 := AdaptInfo(infoFirstFiltered, "1577719286659006464", "https://x.com/u/status/1577719286659006464/video/2", 2, now)
	if err := unified.ValidateEnvelopeAt(payload1, now); err != nil {
		t.Fatalf("Case 1 ValidateEnvelopeAt failed: %v", err)
	}
	data1 := unified.Map(payload1["data"])
	items1 := unified.Slice(data1["items"])
	if len(items1) != 2 {
		t.Fatalf("Case 1: expected 2 deliverable items, got %d", len(items1))
	}
	firstItem1 := unified.Map(items1[0])
	if unified.Str(firstItem1["title"]) != "Entry 111" || firstItem1["index"] != 2 {
		t.Fatalf("Case 1: expected first surviving item to be original entry #2 ('Entry 111', index=2), got %+v", firstItem1)
	}
	if got := unified.Str(data1["default_item_id"]); got != unified.Str(firstItem1["id"]) {
		t.Fatalf("Case 1: expected /video/2 to select original entry #2 (%q), got %q", unified.Str(firstItem1["id"]), got)
	}

	// Case 2: Entry #2 ("200") has no deliverable formats while Entry #1 ("100") and Entry #3 ("300") are deliverable.
	// Requesting /video/2 must emit an unavailable item for entry #2 and set default_item_id to its ID.
	infoSecondFiltered := &ytengine.Info{
		ID:   "1577719286659006464",
		Type: "playlist",
		Entries: []ytengine.Info{
			makeValidEntry("100"),
			{ID: "200", DisplayID: "1577719286659006464", Formats: nil},
			makeValidEntry("300"),
		},
	}
	payload2 := AdaptInfo(infoSecondFiltered, "1577719286659006464", "https://x.com/u/status/1577719286659006464/video/2", 2, now)
	if err := unified.ValidateEnvelopeAt(payload2, now); err != nil {
		t.Fatalf("Case 2 ValidateEnvelopeAt failed: %v", err)
	}
	data2 := unified.Map(payload2["data"])
	items2 := unified.Slice(data2["items"])
	if len(items2) != 3 {
		t.Fatalf("Case 2: expected 3 items (2 ready + 1 unavailable), got %d", len(items2))
	}
	unavailItem := unified.Map(items2[1])
	if unified.Str(data2["default_item_id"]) != unified.Str(unavailItem["id"]) {
		t.Fatalf("Case 2: expected /video/2 to target unavailable item %q, got %q", unified.Str(unavailItem["id"]), unified.Str(data2["default_item_id"]))
	}
	if unified.Str(unavailItem["state"]) != "unavailable" || unavailItem["default"] != nil || unified.Str(unavailItem["default_unavailable_reason"]) != "unavailable" {
		t.Fatalf("Case 2: expected second item to be unavailable with nil default, got %+v", unavailItem)
	}

	// Case 3: Only 2 entries exist, requesting /video/3 (out of bounds) must emit an unavailable item at position 3.
	infoTwoEntries := &ytengine.Info{
		ID:   "1577719286659006464",
		Type: "playlist",
		Entries: []ytengine.Info{
			makeValidEntry("100"),
			makeValidEntry("200"),
		},
	}
	payload3 := AdaptInfo(infoTwoEntries, "1577719286659006464", "https://x.com/u/status/1577719286659006464/video/3", 3, now)
	if err := unified.ValidateEnvelopeAt(payload3, now); err != nil {
		t.Fatalf("Case 3 ValidateEnvelopeAt failed: %v", err)
	}
	data3 := unified.Map(payload3["data"])
	if got := unified.Str(data3["default_item_id"]); got != "video-3" {
		t.Fatalf("Case 3: expected out-of-bounds /video/3 to target unavailable 'video-3', got %q", got)
	}
}

func TestXSourceIdentityPreservedAcrossSignatureRenewal(t *testing.T) {
	const now int64 = 1762300800
	sourceURL := "https://x.com/historyinmemes/status/1790637656616943991"

	makeInfo := func(mediaURL string, filesize int64) *ytengine.Info {
		return &ytengine.Info{
			ID:        "1790637589910654976",
			DisplayID: "1790637656616943991",
			Title:     "Historic Vids",
			Formats: []ytengine.Format{
				{
					FormatID: "http-2176",
					URL:      mediaURL,
					Ext:      "mp4",
					Protocol: "https",
					Width:    int64Ptr(728),
					Height:   int64Ptr(720),
					Filesize: int64Ptr(filesize),
				},
			},
		}
	}

	url1 := "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/kF5AeH6MK-1qDlBg.mp4?tag=14&expires=1762304400&signature=AAA"
	url2 := "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/kF5AeH6MK-1qDlBg.mp4?tag=14&expires=1762390800&signature=BBB"
	urlDifferentTag := "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/kF5AeH6MK-1qDlBg.mp4?tag=16&expires=1762390800&signature=BBB"

	p1 := unified.PublicView(AdaptInfo(makeInfo(url1, 1404023), "1790637656616943991", sourceURL, 0, now), false, now)
	p2 := unified.PublicView(AdaptInfo(makeInfo(url2, 1404023), "1790637656616943991", sourceURL, 0, now+60), false, now+60)
	infoNoSize := makeInfo(url2, 0)
	infoNoSize.Formats[0].Filesize = nil
	pNoSize := unified.PublicView(AdaptInfo(infoNoSize, "1790637656616943991", sourceURL, 0, now+120), false, now+120)
	pDiff := unified.PublicView(AdaptInfo(makeInfo(urlDifferentTag, 1404023), "1790637656616943991", sourceURL, 0, now+60), false, now+60)

	def1 := unified.Map(unified.Map(unified.Slice(unified.Map(p1["data"])["items"])[0])["default"])
	def2 := unified.Map(unified.Map(unified.Slice(unified.Map(p2["data"])["items"])[0])["default"])
	defNoSize := unified.Map(unified.Map(unified.Slice(unified.Map(pNoSize["data"])["items"])[0])["default"])
	defDiff := unified.Map(unified.Map(unified.Slice(unified.Map(pDiff["data"])["items"])[0])["default"])

	if unified.Str(def1["source_id"]) != unified.Str(def2["source_id"]) {
		t.Fatalf("expected X source_id to stay stable across signature renewal: %v vs %v", def1["source_id"], def2["source_id"])
	}
	if unified.Str(def1["source_id"]) != unified.Str(defNoSize["source_id"]) {
		t.Fatalf("expected X source_id to stay stable when optional size probe fails (Filesize=nil): %v vs %v", def1["source_id"], defNoSize["source_id"])
	}
	if unified.Str(def2["url"]) != url2 {
		t.Fatalf("expected renewed source URL bytes to be preserved, got %q", unified.Str(def2["url"]))
	}
	if unified.Str(def1["source_id"]) == unified.Str(defDiff["source_id"]) {
		t.Fatalf("expected X source_id to change when non-credential query param (tag) changes")
	}
}

type fakeXFetcher struct {
	fn func(context.Context, fetch.Request) (fetch.Response, error)
}

func (f fakeXFetcher) Do(ctx context.Context, req fetch.Request) (fetch.Response, error) {
	return f.fn(ctx, req)
}

func TestXSyndicationTokenAndURLAllowlists(t *testing.T) {
	tok1, ok1 := SyndicationToken("1790637656616943991")
	if !ok1 || tok1 == "" || strings.ContainsAny(tok1, "0.") {
		t.Fatalf("SyndicationToken(1790637656616943991) = (%q, %v), expected non-empty base36 without 0 or .", tok1, ok1)
	}
	tok2, ok2 := SyndicationToken("1546621144358391808")
	if !ok2 || tok2 == "" || tok2 == tok1 {
		t.Fatalf("SyndicationToken(1546621144358391808) = (%q, %v), expected distinct valid token", tok2, ok2)
	}
	for _, invalidID := range []string{"", "0", "0123", "abc", "-1", "123456789012345678901"} {
		if _, ok := SyndicationToken(invalidID); ok {
			t.Fatalf("SyndicationToken(%q) unexpectedly succeeded", invalidID)
		}
	}

	// Syndication outbound host allowlist: ONLY https://cdn.syndication.twimg.com/tweet-result
	validSyndication := []string{
		"https://cdn.syndication.twimg.com/tweet-result?id=1546621144358391808&token=" + tok2,
		"https://cdn.syndication.twimg.com:443/tweet-result?id=1&token=a",
	}
	for _, u := range validSyndication {
		if !AllowedSyndicationURL(u) {
			t.Fatalf("AllowedSyndicationURL(%q) = false, want true", u)
		}
	}
	invalidSyndication := []string{
		"http://cdn.syndication.twimg.com/tweet-result?id=1&token=a",
		"https://cdn.syndication.twimg.com:8443/tweet-result?id=1&token=a",
		"https://user:pass@cdn.syndication.twimg.com/tweet-result?id=1&token=a",
		"https://sub.cdn.syndication.twimg.com/tweet-result?id=1&token=a",
		"https://cdn.syndication.twimg.com.evil.example/tweet-result?id=1&token=a",
		"https://cdn.syndication.twimg.com./tweet-result?id=1&token=a",
		"https://syndication.twimg.com/tweet-result?id=1&token=a",
		"https://pbs.twimg.com/tweet-result?id=1&token=a",
		"https://cdn.syndication.twimg.com/widgets/tweet",
	}
	for _, u := range invalidSyndication {
		if AllowedSyndicationURL(u) {
			t.Fatalf("AllowedSyndicationURL(%q) = true, want false", u)
		}
	}

	// Photo URL allowlist & name=orig normalization: ONLY https://pbs.twimg.com/media/<key>
	positivePhotos := []struct {
		raw     string
		wantURL string
		wantExt string
	}{
		{
			raw:     "https://pbs.twimg.com/media/FXazugRWIAQZmml.jpg",
			wantURL: "https://pbs.twimg.com/media/FXazugRWIAQZmml?format=jpg&name=orig",
			wantExt: "jpg",
		},
		{
			raw:     "https://pbs.twimg.com:443/media/DGghkJwUwAAM42D?format=jpeg&name=small",
			wantURL: "https://pbs.twimg.com/media/DGghkJwUwAAM42D?format=jpg&name=orig",
			wantExt: "jpg",
		},
		{
			raw:     "https://pbs.twimg.com/media/AbC_123-xyz.png?name=large",
			wantURL: "https://pbs.twimg.com/media/AbC_123-xyz?format=png&name=orig",
			wantExt: "png",
		},
		{
			raw:     "https://pbs.twimg.com/media/WebpKey99?format=webp",
			wantURL: "https://pbs.twimg.com/media/WebpKey99?format=webp&name=orig",
			wantExt: "webp",
		},
	}
	for _, tc := range positivePhotos {
		gotURL, gotExt, ok := NormalizePhotoURL(tc.raw)
		if !ok || gotURL != tc.wantURL || gotExt != tc.wantExt {
			t.Fatalf("NormalizePhotoURL(%q) = (%q, %q, %v), want (%q, %q, true)",
				tc.raw, gotURL, gotExt, ok, tc.wantURL, tc.wantExt)
		}
	}

	negativePhotos := []string{
		"",
		"http://pbs.twimg.com/media/FXazugRWIAQZmml.jpg",
		"https://pbs.twimg.com:8443/media/FXazugRWIAQZmml.jpg",
		"https://pbs.twimg.com:80/media/FXazugRWIAQZmml.jpg",
		"https://user:secret@pbs.twimg.com/media/FXazugRWIAQZmml.jpg",
		"https://user@pbs.twimg.com/media/FXazugRWIAQZmml.jpg",
		"https://pbs.twimg.com.evil.example/media/FXazugRWIAQZmml.jpg",
		"https://sub.pbs.twimg.com/media/FXazugRWIAQZmml.jpg",
		"https://notpbs.twimg.com/media/FXazugRWIAQZmml.jpg",
		"https://pbs-twimg.com/media/FXazugRWIAQZmml.jpg",
		"https://pbs.twimg.com./media/FXazugRWIAQZmml.jpg",
		"https://video.twimg.com/media/FXazugRWIAQZmml.jpg",
		"https://pbs.twimg.com/ext_tw_video_thumb/123/pu/img/thumb.jpg",
		"https://pbs.twimg.com/profile_images/123/avatar.jpg",
		"https://pbs.twimg.com/media/",
		"https://pbs.twimg.com/media/FXazugRWIAQZmml/extra.jpg",
		"https://pbs.twimg.com/media/..%2fsecret.jpg",
		"https://pbs.twimg.com/media/FXazugRWIAQZmml.mp4",
		"https://pbs.twimg.com/media/FXazugRWIAQZmml#frag",
	}
	for _, raw := range negativePhotos {
		if gotURL, _, ok := NormalizePhotoURL(raw); ok {
			t.Fatalf("NormalizePhotoURL(%q) unexpectedly succeeded with %q", raw, gotURL)
		}
	}
}

func TestParseXPurePhotosMixedAndPhotoIndexSelection(t *testing.T) {
	const now int64 = 1762300800
	const tweetID = "1546621144358391808"

	fourPhotoSyndicationBody, _ := json.Marshal(map[string]any{
		"__typename": "Tweet",
		"text":       "Four original photos in one X post\nSecond line https://t.co/AbCdEf1234",
		"user":       map[string]any{"screen_name": "ignored_field", "secret": "never_read"},
		"photos": []map[string]any{
			{"url": "https://pbs.twimg.com/media/PhotoKeyOne.jpg", "width": 2048, "height": 1536},
			{"url": "https://pbs.twimg.com/media/PhotoKeyTwo.png", "width": 1920, "height": 1080},
			{"url": "https://pbs.twimg.com/media/PhotoKeyThree.webp", "width": 1440, "height": 1800},
			{"url": "https://pbs.twimg.com/media/PhotoKeyFour.jpg", "width": 1200, "height": 1200},
		},
	})

	// 1. Pure 4-photo tweet: yt-dlp reports "No video could be found", syndication returns 4 name=orig images + album group.
	purePhotoParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return nil, &ytengine.Error{
					Code:      ytengine.CodeContentUnavailable,
					Message:   "未找到可用的视频或该推文已不可访问",
					Retryable: false,
				}
			},
		},
		nil,
		fakeXFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				if req.Policy.AllowedURL == nil || !req.Policy.AllowedURL(req.URL) {
					t.Fatalf("syndication request failed AllowedURL policy: %s", req.URL)
				}
				if req.Policy.FollowRedirects {
					t.Fatalf("expected FollowRedirects=false on syndication request")
				}
				return fetch.Response{StatusCode: http.StatusOK, Body: fourPhotoSyndicationBody, URL: req.URL}, nil
			},
		},
	)

	pureEnv, err := purePhotoParser.Parse(context.Background(), "https://x.com/NASA/status/"+tweetID, now)
	if err != nil {
		t.Fatalf("pure-photo Parse failed: %v", err)
	}
	for _, debug := range []bool{false, true} {
		view := unified.PublicView(pureEnv, debug, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("pure-photo ValidateEnvelopeAt(debug=%v) failed: %v", debug, err)
		}
		rawBytes, _ := unified.Encode(view)
		if strings.Contains(string(rawBytes), "never_read") || strings.Contains(string(rawBytes), "ignored_field") {
			t.Fatalf("unread syndication fields leaked into envelope: %s", string(rawBytes))
		}
		data := unified.Map(view["data"])
		if unified.Str(data["title"]) != "Four original photos in one X post" {
			t.Fatalf("unexpected title: %q", unified.Str(data["title"]))
		}
		items := unified.Slice(data["items"])
		if len(items) != 4 {
			t.Fatalf("expected 4 image items, got %d", len(items))
		}
		wantURLs := []string{
			"https://pbs.twimg.com/media/PhotoKeyOne?format=jpg&name=orig",
			"https://pbs.twimg.com/media/PhotoKeyTwo?format=png&name=orig",
			"https://pbs.twimg.com/media/PhotoKeyThree?format=webp&name=orig",
			"https://pbs.twimg.com/media/PhotoKeyFour?format=jpg&name=orig",
		}
		for i, wantURL := range wantURLs {
			it := unified.Map(items[i])
			if unified.Str(it["type"]) != "image" {
				t.Fatalf("item[%d] type=%q, want image", i, unified.Str(it["type"]))
			}
			def := unified.Map(it["default"])
			if unified.Str(def["url"]) != wantURL {
				t.Fatalf("item[%d] default.url=%q, want %q", i, unified.Str(def["url"]), wantURL)
			}
		}
		groups := unified.Slice(data["groups"])
		if len(groups) != 1 || unified.Str(unified.Map(groups[0])["kind"]) != "album" || len(unified.Slice(unified.Map(groups[0])["item_ids"])) != 4 {
			t.Fatalf("expected album group of 4 image items, got %+v", groups)
		}
	}

	// 2. /photo/<N> only returns the N-th photo without spawning yt-dlp.
	var ytCalled bool
	photoIndexParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				ytCalled = true
				return nil, errors.New("should not call yt-dlp for /photo/N")
			},
		},
		nil,
		fakeXFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: fourPhotoSyndicationBody, URL: req.URL}, nil
			},
		},
	)
	photo3Env, err := photoIndexParser.Parse(context.Background(), "https://x.com/NASA/status/"+tweetID+"/photo/3", now)
	if err != nil {
		t.Fatalf("/photo/3 Parse failed: %v", err)
	}
	if ytCalled {
		t.Fatalf("expected /photo/3 to bypass yt-dlp and use Go fetch directly")
	}
	photo3View := unified.PublicView(photo3Env, false, now)
	if err := unified.ValidateEnvelopeAt(photo3View, now); err != nil {
		t.Fatalf("/photo/3 ValidateEnvelopeAt failed: %v", err)
	}
	photo3Items := unified.Slice(unified.Map(photo3View["data"])["items"])
	if len(photo3Items) != 1 {
		t.Fatalf("expected /photo/3 to return exactly 1 item, got %d", len(photo3Items))
	}
	if got := unified.Str(unified.Map(unified.Map(photo3Items[0])["default"])["url"]); got != "https://pbs.twimg.com/media/PhotoKeyThree?format=webp&name=orig" {
		t.Fatalf("expected 3rd photo URL on /photo/3, got %q", got)
	}

	// Requesting /photo/4 when tweet only has 2 photos returns CONTENT_UNAVAILABLE.
	twoPhotoBody, _ := json.Marshal(map[string]any{
		"__typename": "Tweet",
		"text":       "Two photos",
		"photos": []map[string]any{
			{"url": "https://pbs.twimg.com/media/PhotoKeyOne.jpg", "width": 1000, "height": 1000},
			{"url": "https://pbs.twimg.com/media/PhotoKeyTwo.jpg", "width": 1000, "height": 1000},
		},
	})
	outOfBoundsParser := NewWithFetcher(
		&stubExtractor{enabled: true},
		nil,
		fakeXFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: twoPhotoBody, URL: req.URL}, nil
			},
		},
	)
	oobEnv, err := outOfBoundsParser.Parse(context.Background(), "https://x.com/NASA/status/"+tweetID+"/photo/4", now)
	if err != nil || unified.Str(oobEnv["code"]) != "CONTENT_UNAVAILABLE" {
		t.Fatalf("expected CONTENT_UNAVAILABLE on out-of-bounds /photo/4, got %+v (err=%v)", oobEnv, err)
	}

	// 3. Mixed tweet (1 video + 2 photos): video stays first as default_item_id, photos appended after.
	mixedSyndicationBody, _ := json.Marshal(map[string]any{
		"__typename": "Tweet",
		"text":       "Mixed video and photos",
		"video":      map[string]any{"durationMs": 8000},
		"photos": []map[string]any{
			{"url": "https://pbs.twimg.com/media/MixedImg1.jpg", "width": 1600, "height": 900},
			{"url": "https://pbs.twimg.com/media/MixedImg2.png", "width": 1200, "height": 1600},
		},
		"mediaDetails": []map[string]any{
			{"type": "video", "media_url_https": "https://pbs.twimg.com/ext_tw_video_thumb/999/pu/img/thumb.jpg"},
			{"type": "photo", "media_url_https": "https://pbs.twimg.com/media/MixedImg1.jpg", "original_info": map[string]any{"width": 1600, "height": 900}},
			{"type": "photo", "media_url_https": "https://pbs.twimg.com/media/MixedImg2.png", "original_info": map[string]any{"width": 1200, "height": 1600}},
		},
	})
	mixedParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return &ytengine.Info{
					ID:        "999",
					DisplayID: tweetID,
					Title:     "Mixed Post",
					Duration:  float64Ptr(8.0),
					Formats: []ytengine.Format{
						{
							FormatID: "http-2176",
							URL:      "https://video.twimg.com/ext_tw_video/999/pu/vid/1280x720/mixed720.mp4?tag=12",
							Ext:      "mp4",
							Protocol: "https",
							Width:    int64Ptr(1280),
							Height:   int64Ptr(720),
							VCodec:   "avc1.640020",
							ACodec:   "mp4a.40.2",
						},
					},
				}, nil
			},
		},
		nil,
		fakeXFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: mixedSyndicationBody, URL: req.URL}, nil
			},
		},
	)
	mixedEnv, err := mixedParser.Parse(context.Background(), "https://x.com/NASA/status/"+tweetID, now)
	if err != nil {
		t.Fatalf("mixed tweet Parse failed: %v", err)
	}
	mixedView := unified.PublicView(mixedEnv, false, now)
	if err := unified.ValidateEnvelopeAt(mixedView, now); err != nil {
		t.Fatalf("mixed tweet ValidateEnvelopeAt failed: %v", err)
	}
	mixedData := unified.Map(mixedView["data"])
	mixedItems := unified.Slice(mixedData["items"])
	if len(mixedItems) != 3 {
		t.Fatalf("expected 3 items (1 video + 2 appended photos) in mixed tweet, got %d", len(mixedItems))
	}
	if unified.Str(unified.Map(mixedItems[0])["type"]) != "video" ||
		unified.Str(unified.Map(mixedItems[1])["type"]) != "image" ||
		unified.Str(unified.Map(mixedItems[2])["type"]) != "image" {
		t.Fatalf("expected [video, image, image] ordering, got %+v", mixedItems)
	}
	if unified.Str(mixedData["default_item_id"]) != unified.Str(unified.Map(mixedItems[0])["id"]) {
		t.Fatalf("expected video item to remain default_item_id in mixed tweet")
	}
	mixedGroups := unified.Slice(mixedData["groups"])
	if len(mixedGroups) != 1 || len(unified.Slice(unified.Map(mixedGroups[0])["item_ids"])) != 2 {
		t.Fatalf("expected album group with the 2 appended image items, got %+v", mixedGroups)
	}
}

func TestXSyndicationAbnormalAndOversizedResponses(t *testing.T) {
	const now int64 = 1762300800
	const tweetURL = "https://x.com/NASA/status/1546621144358391808"

	cases := []struct {
		name     string
		fetchFn  func(context.Context, fetch.Request) (fetch.Response, error)
		wantCode string
	}{
		{
			name: "oversized-body-exceeds-limit",
			fetchFn: func(context.Context, fetch.Request) (fetch.Response, error) {
				return fetch.Response{}, fetch.ErrLimit
			},
			wantCode: "UPSTREAM_INVALID_RESPONSE",
		},
		{
			name: "malformed-json-body",
			fetchFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: []byte(`<html>not json</html>`), URL: req.URL}, nil
			},
			wantCode: "UPSTREAM_INVALID_RESPONSE",
		},
		{
			name: "empty-200-body",
			fetchFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: nil, URL: req.URL}, nil
			},
			wantCode: "UPSTREAM_INVALID_RESPONSE",
		},
		{
			name: "not-found-404-preserves-content-unavailable",
			fetchFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusNotFound, Body: []byte(`not found`), URL: req.URL}, nil
			},
			wantCode: "CONTENT_UNAVAILABLE",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewWithFetcher(
				&stubExtractor{
					enabled: true,
					extract: func(context.Context, string) (*ytengine.Info, error) {
						return nil, &ytengine.Error{
							Code:      ytengine.CodeContentUnavailable,
							Message:   "未找到可用的视频或该推文已不可访问",
							Retryable: false,
						}
					},
				},
				nil,
				fakeXFetcher{fn: tc.fetchFn},
			)
			env, err := p.Parse(context.Background(), tweetURL, now)
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if err := unified.ValidateEnvelopeAt(env, now); err != nil {
				t.Fatalf("ValidateEnvelopeAt failed on error envelope: %v", err)
			}
			if got := unified.Str(env["code"]); got != tc.wantCode {
				t.Fatalf("got code=%q, want %q (env=%+v)", got, tc.wantCode, env)
			}
		})
	}
}

func TestParseXVideoParallelSyndicationTimeoutAndErrorFallback(t *testing.T) {
	const now int64 = 1762300800
	const tweetID = "1790637656616943991"
	const tweetURL = "https://x.com/historyinmemes/status/" + tweetID

	sampleVideoInfo := func() *ytengine.Info {
		return &ytengine.Info{
			ID:        "1790637589910654976",
			DisplayID: tweetID,
			Title:     "Historic Vids",
			Duration:  float64Ptr(15.488),
			Formats: []ytengine.Format{
				{
					FormatID: "http-2176",
					URL:      "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/kF5AeH6MK-1qDlBg.mp4?tag=14",
					Ext:      "mp4",
					Protocol: "https",
					Width:    int64Ptr(728),
					Height:   int64Ptr(720),
					VCodec:   "avc1.640020",
					ACodec:   "mp4a.40.2",
					Filesize: int64Ptr(1404023),
				},
			},
		}
	}

	// 1. Default supplement timeout is bounded to 3 seconds (~3s per PR #65 follow-up).
	defaultParser := NewWithFetcher(&stubExtractor{enabled: true}, nil, fakeXFetcher{})
	if got := defaultParser.effectiveSupplementTimeout(); got != 3*time.Second {
		t.Fatalf("effectiveSupplementTimeout() = %v, want 3s", got)
	}

	// 2. Deterministic barrier handshake proving yt-dlp and syndication run concurrently,
	// and completed syndication photos remain preserved even if yt-dlp finishes after supplementTimeout.
	ytEntered := make(chan struct{})
	synEntered := make(chan struct{})
	bothRunning := make(chan struct{})
	go func() {
		<-ytEntered
		<-synEntered
		close(bothRunning)
	}()

	mixedSyndicationBody, _ := json.Marshal(map[string]any{
		"__typename": "Tweet",
		"text":       "Parallel mixed post",
		"video":      map[string]any{"durationMs": 15488},
		"photos": []map[string]any{
			{"url": "https://pbs.twimg.com/media/ParallelImg1.jpg", "width": 1600, "height": 900},
		},
	})

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
				return sampleVideoInfo(), nil
			},
		},
		nil,
		fakeXFetcher{
			fn: func(reqCtx context.Context, req fetch.Request) (fetch.Response, error) {
				close(synEntered)
				select {
				case <-bothRunning:
				case <-reqCtx.Done():
					return fetch.Response{}, reqCtx.Err()
				}
				return fetch.Response{StatusCode: http.StatusOK, Body: mixedSyndicationBody, URL: req.URL}, nil
			},
		},
	)
	parallelParser.supplementTimeout = 30 * time.Millisecond

	parallelCtx, cancelParallel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelParallel()
	parallelEnv, err := parallelParser.Parse(parallelCtx, tweetURL, now)
	if err != nil {
		t.Fatalf("parallel mixed Parse failed: %v", err)
	}
	parallelItems := unified.Slice(unified.Map(unified.PublicView(parallelEnv, false, now)["data"])["items"])
	if len(parallelItems) != 2 || unified.Str(unified.Map(parallelItems[0])["type"]) != "video" || unified.Str(unified.Map(parallelItems[1])["type"]) != "image" {
		t.Fatalf("expected [video, image] from parallel mixed tweet even when yt-dlp outlived supplementTimeout, got %+v", parallelItems)
	}

	// 3. Hung syndication on a video tweet is cancelled at supplementTimeout and returns the video result directly.
	synCanceled := make(chan struct{})
	timeoutParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return sampleVideoInfo(), nil
			},
		},
		nil,
		fakeXFetcher{
			fn: func(reqCtx context.Context, _ fetch.Request) (fetch.Response, error) {
				<-reqCtx.Done()
				close(synCanceled)
				return fetch.Response{}, reqCtx.Err()
			},
		},
	)
	timeoutParser.supplementTimeout = 80 * time.Millisecond

	start := time.Now()
	videoEnv, err := timeoutParser.Parse(context.Background(), tweetURL, now)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("timeout fallback Parse returned Go error: %v", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("expected syndication supplement timeout to cut off promptly (<1s), took %v", elapsed)
	}
	select {
	case <-synCanceled:
	case <-time.After(time.Second):
		t.Fatal("expected hung syndication request context to be cancelled on supplement timeout")
	}
	videoView := unified.PublicView(videoEnv, false, now)
	if err := unified.ValidateEnvelopeAt(videoView, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt failed on video fallback envelope: %v", err)
	}
	if unified.Str(videoView["code"]) != "OK" {
		t.Fatalf("expected code=OK video result on syndication timeout, got %+v", videoView)
	}
	videoItems := unified.Slice(unified.Map(videoView["data"])["items"])
	if len(videoItems) != 1 || unified.Str(unified.Map(videoItems[0])["type"]) != "video" {
		t.Fatalf("expected 1 video item on syndication timeout fallback, got %+v", videoItems)
	}

	// 4. Syndication HTTP/parse errors on a video tweet never fail the video result.
	errorCases := []struct {
		name    string
		fetchFn func(context.Context, fetch.Request) (fetch.Response, error)
	}{
		{
			name: "rate-limited-429",
			fetchFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusTooManyRequests, Body: []byte(`rate limited`), URL: req.URL}, nil
			},
		},
		{
			name: "forbidden-403",
			fetchFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusForbidden, Body: []byte(`forbidden`), URL: req.URL}, nil
			},
		},
		{
			name: "server-error-500",
			fetchFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusInternalServerError, Body: []byte(`error`), URL: req.URL}, nil
			},
		},
		{
			name: "oversized-err-limit",
			fetchFn: func(context.Context, fetch.Request) (fetch.Response, error) {
				return fetch.Response{}, fetch.ErrLimit
			},
		},
		{
			name: "malformed-json",
			fetchFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: []byte(`<html>bad</html>`), URL: req.URL}, nil
			},
		},
		{
			name: "transport-deadline-exceeded",
			fetchFn: func(context.Context, fetch.Request) (fetch.Response, error) {
				return fetch.Response{}, context.DeadlineExceeded
			},
		},
	}
	for _, tc := range errorCases {
		t.Run("video-ignores-syndication-"+tc.name, func(t *testing.T) {
			p := NewWithFetcher(
				&stubExtractor{
					enabled: true,
					extract: func(context.Context, string) (*ytengine.Info, error) {
						return sampleVideoInfo(), nil
					},
				},
				nil,
				fakeXFetcher{fn: tc.fetchFn},
			)
			env, err := p.Parse(context.Background(), tweetURL, now)
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if unified.Str(env["code"]) != "OK" {
				t.Fatalf("expected video result code=OK despite syndication %s, got %+v", tc.name, env)
			}
		})
	}

	// 5. Pure-image tweet where yt-dlp returns CONTENT_UNAVAILABLE is NOT cut off by supplementTimeout.
	var purePhotoFetchCalls atomic.Int32
	purePhotoBody, _ := json.Marshal(map[string]any{
		"__typename": "Tweet",
		"text":       "Slow pure photo tweet",
		"photos": []map[string]any{
			{"url": "https://pbs.twimg.com/media/SlowPhotoOne.jpg", "width": 2048, "height": 1536},
			{"url": "https://pbs.twimg.com/media/SlowPhotoTwo.png", "width": 1920, "height": 1080},
		},
	})
	slowPurePhotoParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				time.Sleep(15 * time.Millisecond)
				return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video"}
			},
		},
		nil,
		fakeXFetcher{
			fn: func(reqCtx context.Context, req fetch.Request) (fetch.Response, error) {
				purePhotoFetchCalls.Add(1)
				select {
				case <-time.After(90 * time.Millisecond):
					return fetch.Response{StatusCode: http.StatusOK, Body: purePhotoBody, URL: req.URL}, nil
				case <-reqCtx.Done():
					return fetch.Response{}, reqCtx.Err()
				}
			},
		},
	)
	slowPurePhotoParser.supplementTimeout = 30 * time.Millisecond

	slowPureEnv, err := slowPurePhotoParser.Parse(context.Background(), tweetURL, now)
	if err != nil || unified.Str(slowPureEnv["code"]) != "OK" {
		t.Fatalf("expected pure-photo tweet slower than supplementTimeout to succeed, got env=%+v err=%v", slowPureEnv, err)
	}
	if gotCalls := purePhotoFetchCalls.Load(); gotCalls != 1 {
		t.Fatalf("expected exactly 1 syndication request on pure-photo tweet, got %d", gotCalls)
	}
	if gotItems := len(unified.Slice(unified.Map(slowPureEnv["data"])["items"])); gotItems != 2 {
		t.Fatalf("expected 2 photo items, got %d", gotItems)
	}

	// 6. Explicit /video/<N> URL does not invoke syndication at all.
	videoOnlyParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				return sampleVideoInfo(), nil
			},
		},
		nil,
		fakeXFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				t.Fatalf("unexpected syndication call for /video/1: %s", req.URL)
				return fetch.Response{}, nil
			},
		},
	)
	videoOnlyEnv, err := videoOnlyParser.Parse(context.Background(), tweetURL+"/video/1", now)
	if err != nil || unified.Str(videoOnlyEnv["code"]) != "OK" {
		t.Fatalf("/video/1 Parse failed: env=%+v err=%v", videoOnlyEnv, err)
	}
}

func TestParseXIssue73PhotoPriorityAndExtractorMatrix(t *testing.T) {
	const now int64 = 1762300800
	const extlinkTweetID = "2115900212212588814"
	const extlinkTweetURL = "https://x.com/lalala9529/status/" + extlinkTweetID + "?s=19"
	const wantPhotoURL = "https://pbs.twimg.com/media/HV0v-QhbAAAdG9e?format=jpg&name=orig"

	// Verify AllowedExtractors matches pinned yt-dlp 2026.10.26 Twitter media extractors without generic/shortener.
	gotExtractors := AllowedExtractors()
	wantExtractors := []string{"twitter", "twitter:amplify", "twitter:broadcast", "twitter:card", "twitter:spaces"}
	if strings.Join(gotExtractors, ",") != strings.Join(wantExtractors, ",") {
		t.Fatalf("AllowedExtractors() = %v, want %v", gotExtractors, wantExtractors)
	}

	extlinkPhotoSyndicationBody, _ := json.Marshal(map[string]any{
		"__typename": "Tweet",
		"text":       "Pose reference generator\nhttps://github.com/wzc520pyfm/gen-ai-pose-image\nhttps://gen-ai-pose-image.vercel.app/ https://t.co/ExtLinkPic",
		"photos": []map[string]any{
			{"url": "https://pbs.twimg.com/media/HV0v-QhbAAAdG9e.jpg", "width": 1536, "height": 2048},
		},
		"mediaDetails": []map[string]any{
			{
				"type":            "photo",
				"media_url_https": "https://pbs.twimg.com/media/HV0v-QhbAAAdG9e.jpg",
				"original_info":   map[string]any{"width": 1536, "height": 2048},
			},
		},
	})

	purePhotoNoExtlinkBody, _ := json.Marshal(map[string]any{
		"__typename": "Tweet",
		"text":       "Pure photo tweet without external links https://t.co/OnlyPhoto",
		"photos": []map[string]any{
			{"url": "https://pbs.twimg.com/media/PureNoExt1.png", "width": 1920, "height": 1080},
			{"url": "https://pbs.twimg.com/media/PureNoExt2.jpg", "width": 1080, "height": 1080},
		},
	})

	mixedVideoPhotoBody, _ := json.Marshal(map[string]any{
		"__typename": "Tweet",
		"text":       "Mixed video and photo post",
		"video":      map[string]any{"durationMs": 9500},
		"photos": []map[string]any{
			{"url": "https://pbs.twimg.com/media/MixedPhotoA.jpg", "width": 1600, "height": 900},
		},
		"mediaDetails": []map[string]any{
			{"type": "video", "media_url_https": "https://pbs.twimg.com/ext_tw_video_thumb/777/pu/img/thumb.jpg"},
			{"type": "photo", "media_url_https": "https://pbs.twimg.com/media/MixedPhotoA.jpg", "original_info": map[string]any{"width": 1600, "height": 900}},
		},
	})

	mixedAnimatedGifPhotoBody, _ := json.Marshal(map[string]any{
		"__typename": "Tweet",
		"text":       "Mixed animated_gif and photo post",
		"photos": []map[string]any{
			{"url": "https://pbs.twimg.com/media/GifCompanionPhoto.jpg", "width": 1200, "height": 1200},
		},
		"mediaDetails": []map[string]any{
			{"type": "animated_gif", "media_url_https": "https://pbs.twimg.com/tweet_video_thumb/888.jpg"},
			{"type": "photo", "media_url_https": "https://pbs.twimg.com/media/GifCompanionPhoto.jpg", "original_info": map[string]any{"width": 1200, "height": 1200}},
		},
	})

	pureVideoSyndicationBody, _ := json.Marshal(map[string]any{
		"__typename": "Tweet",
		"text":       "Pure video post",
		"video":      map[string]any{"durationMs": 12000},
		"mediaDetails": []map[string]any{
			{"type": "video", "media_url_https": "https://pbs.twimg.com/ext_tw_video_thumb/555/pu/img/thumb.jpg"},
		},
	})

	makeVideoInfo := func(tweetID, mediaID string) *ytengine.Info {
		return &ytengine.Info{
			ID:        mediaID,
			DisplayID: tweetID,
			Title:     "Sample Video",
			Duration:  float64Ptr(12.0),
			Formats: []ytengine.Format{
				{
					FormatID: "http-2176",
					URL:      "https://video.twimg.com/ext_tw_video/" + mediaID + "/pu/vid/1280x720/v720.mp4?tag=12",
					Ext:      "mp4",
					Protocol: "https",
					Width:    int64Ptr(1280),
					Height:   int64Ptr(720),
					VCodec:   "avc1.640020",
					ACodec:   "mp4a.40.2",
					Filesize: int64Ptr(2048576),
				},
			},
		}
	}

	// 1a. Pure photo + external link (syndication finishes first -> returns photo immediately and cancels yt-dlp).
	t.Run("pure-photo-with-extlink-syndication-first-cancels-ytdlp", func(t *testing.T) {
		ytStarted := make(chan struct{})
		ytCanceled := make(chan struct{})
		p := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(ytCtx context.Context, _ string) (*ytengine.Info, error) {
					close(ytStarted)
					<-ytCtx.Done()
					close(ytCanceled)
					return nil, ytCtx.Err()
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					<-ytStarted
					return fetch.Response{StatusCode: http.StatusOK, Body: extlinkPhotoSyndicationBody, URL: req.URL}, nil
				},
			},
		)

		start := time.Now()
		env, err := p.Parse(context.Background(), extlinkTweetURL, now)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if elapsed > time.Second {
			t.Fatalf("expected immediate photo return without waiting for yt-dlp timeout, took %v", elapsed)
		}
		select {
		case <-ytCanceled:
		case <-time.After(time.Second):
			t.Fatal("expected yt-dlp child context to be cancelled immediately after syndication pure-photo win")
		}

		view := unified.PublicView(env, false, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("ValidateEnvelopeAt failed: %v", err)
		}
		if unified.Str(view["code"]) != "OK" {
			t.Fatalf("expected code=OK, got %+v", view)
		}
		items := unified.Slice(unified.Map(view["data"])["items"])
		if len(items) != 1 || unified.Str(unified.Map(items[0])["type"]) != "image" {
			t.Fatalf("expected 1 image item, got %+v", items)
		}
		src := unified.Map(unified.Slice(unified.Map(unified.Map(unified.Slice(unified.Map(items[0])["variants"])[0])["delivery"])["sources"])[0])
		if unified.Str(src["url"]) != wantPhotoURL {
			t.Fatalf("photo source url = %q, want %q", unified.Str(src["url"]), wantPhotoURL)
		}
	})

	// 1b. Pure photo + external link (yt-dlp fails first with any non-LocalBusy error -> waits for full syndication and returns photo).
	t.Run("pure-photo-with-extlink-ytdlp-fails-first-ignores-error", func(t *testing.T) {
		ytErrors := []struct {
			name string
			err  error
		}{
			{
				name: "upstream-blocked-403-tunnel",
				err:  &ytengine.Error{Code: ytengine.CodeUpstreamBlocked, Message: "outbound blocked", Retryable: false},
			},
			{
				name: "upstream-unavailable-502",
				err:  &ytengine.Error{Code: ytengine.CodeUpstreamUnavailable, Message: "upstream unavailable", Retryable: true},
			},
			{
				name: "content-unavailable-disabled-extractor",
				err:  &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no suitable extractor", Retryable: false},
			},
			{
				name: "upstream-timeout",
				err:  &ytengine.Error{Code: ytengine.CodeUpstreamTimeout, Message: "timeout", Retryable: true},
			},
			{
				name: "non-engine-error",
				err:  errors.New("unexpected subprocess error"),
			},
		}
		for _, tc := range ytErrors {
			t.Run(tc.name, func(t *testing.T) {
				ytDone := make(chan struct{})
				p := NewWithFetcher(
					&stubExtractor{
						enabled: true,
						extract: func(context.Context, string) (*ytengine.Info, error) {
							defer close(ytDone)
							return nil, tc.err
						},
					},
					nil,
					fakeXFetcher{
						fn: func(reqCtx context.Context, req fetch.Request) (fetch.Response, error) {
							<-ytDone
							// Exceed supplementTimeout to verify full syndication wait is preserved.
							select {
							case <-time.After(45 * time.Millisecond):
								return fetch.Response{StatusCode: http.StatusOK, Body: extlinkPhotoSyndicationBody, URL: req.URL}, nil
							case <-reqCtx.Done():
								return fetch.Response{}, reqCtx.Err()
							}
						},
					},
				)
				p.supplementTimeout = 15 * time.Millisecond

				env, err := p.Parse(context.Background(), extlinkTweetURL, now)
				if err != nil {
					t.Fatalf("unexpected Go error: %v", err)
				}
				view := unified.PublicView(env, false, now)
				if err := unified.ValidateEnvelopeAt(view, now); err != nil {
					t.Fatalf("ValidateEnvelopeAt failed: %v", err)
				}
				if unified.Str(view["code"]) != "OK" {
					t.Fatalf("expected code=OK photo fallback on %s, got %+v", tc.name, view)
				}
				items := unified.Slice(unified.Map(view["data"])["items"])
				if len(items) != 1 || unified.Str(unified.Map(items[0])["type"]) != "image" {
					t.Fatalf("expected 1 image item, got %+v", items)
				}
			})
		}
	})

	// 1c. LocalBusy exception: yt-dlp LocalBusy is never masked by syndication photo fallback and cancels syndication.
	t.Run("local-busy-is-not-masked-by-syndication", func(t *testing.T) {
		synCanceled := make(chan struct{})
		p := NewWithFetcher(
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
			fakeXFetcher{
				fn: func(reqCtx context.Context, _ fetch.Request) (fetch.Response, error) {
					<-reqCtx.Done()
					close(synCanceled)
					return fetch.Response{}, reqCtx.Err()
				},
			},
		)
		env, err := p.Parse(context.Background(), extlinkTweetURL, now)
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if unified.Str(env["code"]) != "RATE_LIMITED" || unified.Map(env["error"])["retry_after_seconds"] != 1 {
			t.Fatalf("expected LocalBusy RATE_LIMITED with retry_after_seconds=1, got %+v", env)
		}
		select {
		case <-synCanceled:
		case <-time.After(time.Second):
			t.Fatal("expected syndication context to be cancelled on LocalBusy")
		}
	})

	// 2. Pure photo without external link.
	t.Run("pure-photo-without-extlink", func(t *testing.T) {
		p := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(ytCtx context.Context, _ string) (*ytengine.Info, error) {
					select {
					case <-time.After(80 * time.Millisecond):
						return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video"}
					case <-ytCtx.Done():
						return nil, ytCtx.Err()
					}
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{StatusCode: http.StatusOK, Body: purePhotoNoExtlinkBody, URL: req.URL}, nil
				},
			},
		)
		env, err := p.Parse(context.Background(), "https://x.com/NASA/status/1546621144358391808", now)
		if err != nil || unified.Str(env["code"]) != "OK" {
			t.Fatalf("expected code=OK on pure photo without extlink, got env=%+v err=%v", env, err)
		}
		items := unified.Slice(unified.Map(unified.PublicView(env, false, now)["data"])["items"])
		if len(items) != 2 || unified.Str(unified.Map(items[0])["type"]) != "image" || unified.Str(unified.Map(items[1])["type"]) != "image" {
			t.Fatalf("expected 2 image items, got %+v", items)
		}
	})

	// 3. Video + photo mixed (syndication finishes first with HasVideo=true -> must NOT cancel yt-dlp; returns [video, image]).
	// Also verifies animated_gif + photo is treated as HasVideo=true, and if yt-dlp fails on HasVideo=true, error is preserved.
	t.Run("video-and-photo-mixed", func(t *testing.T) {
		for _, bodyCase := range []struct {
			name string
			body []byte
		}{
			{"video-plus-photo", mixedVideoPhotoBody},
			{"animated-gif-plus-photo", mixedAnimatedGifPhotoBody},
		} {
			t.Run(bodyCase.name, func(t *testing.T) {
				synDone := make(chan struct{})
				p := NewWithFetcher(
					&stubExtractor{
						enabled: true,
						extract: func(ytCtx context.Context, _ string) (*ytengine.Info, error) {
							<-synDone
							if err := ytCtx.Err(); err != nil {
								t.Errorf("ytCtx must NOT be cancelled when syndication reports HasVideo=true: %v", err)
								return nil, err
							}
							return makeVideoInfo("1546621144358391808", "777"), nil
						},
					},
					nil,
					fakeXFetcher{
						fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
							defer close(synDone)
							return fetch.Response{StatusCode: http.StatusOK, Body: bodyCase.body, URL: req.URL}, nil
						},
					},
				)
				env, err := p.Parse(context.Background(), "https://x.com/NASA/status/1546621144358391808", now)
				if err != nil || unified.Str(env["code"]) != "OK" {
					t.Fatalf("mixed Parse failed: env=%+v err=%v", env, err)
				}
				items := unified.Slice(unified.Map(unified.PublicView(env, false, now)["data"])["items"])
				if len(items) != 2 || unified.Str(unified.Map(items[0])["type"]) != "video" || unified.Str(unified.Map(items[1])["type"]) != "image" {
					t.Fatalf("expected [video, image] items on %s, got %+v", bodyCase.name, items)
				}
			})
		}

		// When HasVideo=true and yt-dlp fails, do NOT drop the video and return partial photos; keep current error behavior.
		mixedErrParser := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{Code: ytengine.CodeUpstreamUnavailable, Message: "yt-dlp failed", Retryable: true}
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{StatusCode: http.StatusOK, Body: mixedVideoPhotoBody, URL: req.URL}, nil
				},
			},
		)
		mixedErrEnv, err := mixedErrParser.Parse(context.Background(), "https://x.com/NASA/status/1546621144358391808", now)
		if err != nil || unified.Str(mixedErrEnv["code"]) != "UPSTREAM_UNAVAILABLE" {
			t.Fatalf("expected UPSTREAM_UNAVAILABLE when HasVideo=true and yt-dlp fails, got env=%+v err=%v", mixedErrEnv, err)
		}
	})

	// 4. Pure video (syndication returns HasVideo=true, Photos=[] -> waits for yt-dlp and returns video).
	t.Run("pure-video", func(t *testing.T) {
		synDone := make(chan struct{})
		p := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(_ context.Context, _ string) (*ytengine.Info, error) {
					<-synDone
					return makeVideoInfo("1790637656616943991", "555"), nil
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					defer close(synDone)
					return fetch.Response{StatusCode: http.StatusOK, Body: pureVideoSyndicationBody, URL: req.URL}, nil
				},
			},
		)
		env, err := p.Parse(context.Background(), "https://x.com/historyinmemes/status/1790637656616943991", now)
		if err != nil || unified.Str(env["code"]) != "OK" {
			t.Fatalf("pure video Parse failed: env=%+v err=%v", env, err)
		}
		items := unified.Slice(unified.Map(unified.PublicView(env, false, now)["data"])["items"])
		if len(items) != 1 || unified.Str(unified.Map(items[0])["type"]) != "video" {
			t.Fatalf("expected 1 video item, got %+v", items)
		}
	})

	// 5. Syndication fails first + yt-dlp succeeds -> returns video.
	t.Run("syndication-fails-first-ytdlp-succeeds", func(t *testing.T) {
		synDone := make(chan struct{})
		p := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(_ context.Context, _ string) (*ytengine.Info, error) {
					<-synDone
					return makeVideoInfo("1790637656616943991", "555"), nil
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					defer close(synDone)
					return fetch.Response{StatusCode: http.StatusBadGateway, Body: []byte("bad gateway"), URL: req.URL}, nil
				},
			},
		)
		env, err := p.Parse(context.Background(), "https://x.com/historyinmemes/status/1790637656616943991", now)
		if err != nil || unified.Str(env["code"]) != "OK" {
			t.Fatalf("expected code=OK when syndication fails and yt-dlp succeeds, got env=%+v err=%v", env, err)
		}
		items := unified.Slice(unified.Map(unified.PublicView(env, false, now)["data"])["items"])
		if len(items) != 1 || unified.Str(unified.Map(items[0])["type"]) != "video" {
			t.Fatalf("expected 1 video item, got %+v", items)
		}
	})

	// 6. Both fail (syndication fails + yt-dlp fails).
	t.Run("both-fail", func(t *testing.T) {
		bothFailCases := []struct {
			name      string
			ytErr     error
			synStatus int
			wantCode  string
		}{
			{
				name:      "both-404-content-unavailable",
				ytErr:     &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "not found", Retryable: false},
				synStatus: http.StatusNotFound,
				wantCode:  "CONTENT_UNAVAILABLE",
			},
			{
				name:      "yt-content-unavailable-syn-503-surfaces-upstream-unavailable",
				ytErr:     &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video", Retryable: false},
				synStatus: http.StatusServiceUnavailable,
				wantCode:  "UPSTREAM_UNAVAILABLE",
			},
			{
				name:      "yt-blocked-syn-503-preserves-yt-blocked",
				ytErr:     &ytengine.Error{Code: ytengine.CodeUpstreamBlocked, Message: "protected", Retryable: false},
				synStatus: http.StatusServiceUnavailable,
				wantCode:  "UPSTREAM_BLOCKED",
			},
		}
		for _, tc := range bothFailCases {
			t.Run(tc.name, func(t *testing.T) {
				p := NewWithFetcher(
					&stubExtractor{
						enabled: true,
						extract: func(context.Context, string) (*ytengine.Info, error) {
							return nil, tc.ytErr
						},
					},
					nil,
					fakeXFetcher{
						fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
							return fetch.Response{StatusCode: tc.synStatus, Body: []byte("err"), URL: req.URL}, nil
						},
					},
				)
				env, err := p.Parse(context.Background(), extlinkTweetURL, now)
				if err != nil {
					t.Fatalf("unexpected Go error: %v", err)
				}
				if err := unified.ValidateEnvelopeAt(env, now); err != nil {
					t.Fatalf("ValidateEnvelopeAt failed: %v", err)
				}
				if got := unified.Str(env["code"]); got != tc.wantCode {
					t.Fatalf("code = %q, want %q (env=%+v)", got, tc.wantCode, env)
				}
			})
		}
	})

	// 7. selectedKind == "video" (/video/1) never falls back to syndication photos.
	t.Run("selected-kind-video-does-not-return-photos", func(t *testing.T) {
		p := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video", Retryable: false}
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					t.Fatalf("unexpected syndication fetch on /video/1: %s", req.URL)
					return fetch.Response{StatusCode: http.StatusOK, Body: extlinkPhotoSyndicationBody, URL: req.URL}, nil
				},
			},
		)
		env, err := p.Parse(context.Background(), "https://x.com/lalala9529/status/"+extlinkTweetID+"/video/1", now)
		if err != nil || unified.Str(env["code"]) != "CONTENT_UNAVAILABLE" {
			t.Fatalf("expected CONTENT_UNAVAILABLE for /video/1 on pure-photo tweet, got env=%+v err=%v", env, err)
		}
	})
}
