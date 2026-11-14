package x

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

	// 6. Explicit /video/<N> URL tries syndication first and falls back to yt-dlp when syndication fails.
	var videoOnlySynCalls atomic.Int32
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
				videoOnlySynCalls.Add(1)
				return fetch.Response{StatusCode: http.StatusServiceUnavailable, Body: []byte(`unavailable`), URL: req.URL}, nil
			},
		},
	)
	videoOnlyEnv, err := videoOnlyParser.Parse(context.Background(), tweetURL+"/video/1", now)
	if err != nil || unified.Str(videoOnlyEnv["code"]) != "OK" {
		t.Fatalf("/video/1 Parse failed: env=%+v err=%v", videoOnlyEnv, err)
	}
	if videoOnlySynCalls.Load() != 1 {
		t.Fatalf("expected 1 syndication attempt before yt-dlp fallback on /video/1, got %d", videoOnlySynCalls.Load())
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
		p.supplementTimeout = 15 * time.Millisecond

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
		p.supplementTimeout = 15 * time.Millisecond
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
		var ytCalled atomic.Bool
		p := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					ytCalled.Store(true)
					return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video", Retryable: false}
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{StatusCode: http.StatusOK, Body: extlinkPhotoSyndicationBody, URL: req.URL}, nil
				},
			},
		)
		env, err := p.Parse(context.Background(), "https://x.com/lalala9529/status/"+extlinkTweetID+"/video/1", now)
		if err != nil || unified.Str(env["code"]) != "CONTENT_UNAVAILABLE" {
			t.Fatalf("expected CONTENT_UNAVAILABLE for /video/1 on pure-photo tweet, got env=%+v err=%v", env, err)
		}
		if ytCalled.Load() {
			t.Fatal("expected /video/1 on pure-photo tweet to reject directly without spawning yt-dlp")
		}
	})
}

func TestFxTwitterAllowlistAndOutboundHostIsolation(t *testing.T) {
	// Requirement 2: api.fxtwitter.com MUST NEVER be added to yt-dlp AllowedOutboundHost.
	for _, forbiddenHost := range []string{
		"api.fxtwitter.com",
		"fxtwitter.com",
		"d.fxtwitter.com",
		"mosaic.fxtwitter.com",
	} {
		if AllowedOutboundHost(forbiddenHost) {
			t.Fatalf("AllowedOutboundHost(%q) = true, want false (fxtwitter must never enter yt-dlp allowlist)", forbiddenHost)
		}
	}

	validURLs := []string{
		"https://api.fxtwitter.com/i/status/1",
		"https://api.fxtwitter.com/i/status/2119994769057132696",
		"https://API.FXTWITTER.COM/i/status/12345678901234567890",
	}
	for _, u := range validURLs {
		if !AllowedFxTwitterURL(u) {
			t.Fatalf("AllowedFxTwitterURL(%q) = false, want true", u)
		}
	}

	invalidURLs := []string{
		"",
		"http://api.fxtwitter.com/i/status/2119994769057132696",
		"https://api.fxtwitter.com:443/i/status/2119994769057132696",
		"https://api.fxtwitter.com:8443/i/status/2119994769057132696",
		"https://api.fxtwitter.com:80/i/status/2119994769057132696",
		"https://user:pass@api.fxtwitter.com/i/status/2119994769057132696",
		"https://user@api.fxtwitter.com/i/status/2119994769057132696",
		"https://api.fxtwitter.com.evil/i/status/2119994769057132696",
		"https://evilapi.fxtwitter.com/i/status/2119994769057132696",
		"https://sub.api.fxtwitter.com/i/status/2119994769057132696",
		"https://api.fxtwitter.com./i/status/2119994769057132696",
		"https://fxtwitter.com/i/status/2119994769057132696",
		"https://api.fxtwitter.com/i/status/2119994769057132696?foo=1",
		"https://api.fxtwitter.com/i/status/2119994769057132696#frag",
		"https://api.fxtwitter.com/i/status/2119994769057132696/",
		"https://api.fxtwitter.com/i/status/2119994769057132696/photo/1",
		"https://api.fxtwitter.com/MiuMiu99999999/status/2119994769057132696",
		"https://api.fxtwitter.com/i/status/02119994769057132696",
		"https://api.fxtwitter.com/i/status/0",
		"https://api.fxtwitter.com/i/status/123456789012345678901",
		"https://api.fxtwitter.com/i/status/abc",
		"https://api.fxtwitter.com/i/status/..%2f2119994769057132696",
	}
	for _, u := range invalidURLs {
		if AllowedFxTwitterURL(u) {
			t.Fatalf("AllowedFxTwitterURL(%q) = true, want false", u)
		}
	}
}

func assertFxTwitterRequestHygiene(t *testing.T, req fetch.Request, wantTweetID string) {
	t.Helper()
	wantURL := "https://api.fxtwitter.com/i/status/" + wantTweetID
	if req.Method != http.MethodGet {
		t.Fatalf("fxtwitter Method = %q, want GET", req.Method)
	}
	if req.URL != wantURL {
		t.Fatalf("fxtwitter URL = %q, want %q", req.URL, wantURL)
	}
	if req.Policy.AllowedURL == nil || !req.Policy.AllowedURL(req.URL) {
		t.Fatalf("fxtwitter AllowedURL rejected %q", req.URL)
	}
	if req.Policy.FollowRedirects || req.Policy.ReadRedirectBody || req.Policy.AutoReferer || req.Policy.MaxRedirects != 0 {
		t.Fatalf("fxtwitter redirect/referer policy must disable redirects and auto-referer: %+v", req.Policy)
	}
	if req.Policy.MaxBytes != 256<<10 {
		t.Fatalf("fxtwitter MaxBytes = %d, want %d (256 KiB)", req.Policy.MaxBytes, 256<<10)
	}
	if req.Policy.Timeout != 4*time.Second {
		t.Fatalf("fxtwitter Timeout = %v, want 4s", req.Policy.Timeout)
	}
	if gotCookie := req.Header.Get("Cookie"); gotCookie != "" {
		t.Fatalf("fxtwitter request must never carry Cookie, got %q", gotCookie)
	}
	if gotRef := req.Header.Get("Referer"); gotRef != "" {
		t.Fatalf("fxtwitter request must never carry Referer, got %q", gotRef)
	}
	if gotAuth := req.Header.Get("Authorization"); gotAuth != "" {
		t.Fatalf("fxtwitter request must never carry Authorization, got %q", gotAuth)
	}
	if gotXFF := req.Header.Get("X-Forwarded-For"); gotXFF != "" {
		t.Fatalf("fxtwitter request must never forward client IP, got %q", gotXFF)
	}
	if gotUA := req.Header.Get("User-Agent"); gotUA != "DokiTool-Go (+https://snap.dokitool.com)" {
		t.Fatalf("fxtwitter User-Agent = %q, want %q", gotUA, "DokiTool-Go (+https://snap.dokitool.com)")
	}
	if gotAccept := req.Header.Get("Accept"); gotAccept != "application/json" {
		t.Fatalf("fxtwitter Accept = %q, want application/json", gotAccept)
	}
	if len(req.Header) != 2 {
		t.Fatalf("fxtwitter request must only send User-Agent and Accept headers, got %+v", req.Header)
	}
}

func TestParseXTombstoneFxTwitterMediaMatrix(t *testing.T) {
	const now int64 = 1762300800
	tombstoneSynBody := []byte(`{"__typename":"TweetTombstone","tombstone":{"text":{"text":"Age-restricted adult content."}}}`)

	// 1. Single restricted photo (Issue #88 real sample 2119994769057132696, 2448x3264).
	t.Run("tombstone-single-photo-2119994769057132696", func(t *testing.T) {
		const tweetID = "2119994769057132696"
		const sourceURL = "https://x.com/MiuMiu99999999/status/" + tweetID
		longFirstLine := strings.Repeat("喵", 95)
		fxBody, _ := json.Marshal(map[string]any{
			"code":    200,
			"message": "OK",
			"tweet": map[string]any{
				"id":          tweetID,
				"text":        longFirstLine + "\x00\r\nSecond line https://t.co/Restricted123",
				"likes":       99999,
				"secret_leak": "must_never_appear_in_envelope",
				"author": map[string]any{
					"name":        "MiuMiu\x07\nChannel",
					"screen_name": "MiuMiu99999999",
					"avatar_url":  "https://fxtwitter.com/spoof-avatar.jpg",
					"banner_url":  "https://fxtwitter.com/spoof-banner.jpg",
				},
				"media": map[string]any{
					"mosaic": map[string]any{
						"type": "mosaic_photo",
						"formats": map[string]any{
							"jpeg": "https://mosaic.fxtwitter.com/jpeg/" + tweetID,
						},
					},
					"photos": []map[string]any{
						{
							"type":   "photo",
							"url":    "https://pbs.twimg.com/media/HCpE9UZaAAAHlX5.jpg?name=orig",
							"width":  2448,
							"height": 3264,
						},
					},
				},
			},
		})

		var synCalls, fxCalls atomic.Int32
		baseParser := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{
						Code:      ytengine.CodeUpstreamBlocked,
						Message:   "Age-restricted adult content",
						Retryable: false,
					}
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					if strings.HasPrefix(req.URL, syndicationEndpointURL) {
						synCalls.Add(1)
						return fetch.Response{StatusCode: http.StatusOK, Body: tombstoneSynBody, URL: req.URL}, nil
					}
					fxCalls.Add(1)
					assertFxTwitterRequestHygiene(t, req, tweetID)
					return fetch.Response{StatusCode: http.StatusOK, Body: fxBody, URL: req.URL}, nil
				},
			},
		)
		p, err := baseParser.WithTombstoneFallback(TombstoneFallbackFxTwitter)
		if err != nil {
			t.Fatalf("WithTombstoneFallback failed: %v", err)
		}

		env, err := p.Parse(context.Background(), sourceURL, now)
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		if synCalls.Load() != 1 || fxCalls.Load() != 1 {
			t.Fatalf("expected synCalls=1 fxCalls=1, got syn=%d fx=%d", synCalls.Load(), fxCalls.Load())
		}
		if err := unified.ValidateEnvelopeAt(env, now); err != nil {
			t.Fatalf("ValidateEnvelopeAt failed: %v", err)
		}
		view := unified.PublicView(env, false, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("ValidateEnvelopeAt(PublicView) failed: %v", err)
		}
		encoded, _ := unified.Encode(view)
		if strings.Contains(string(encoded), "must_never_appear") ||
			strings.Contains(string(encoded), "fxtwitter.com") ||
			strings.Contains(string(encoded), `"relay"`) {
			t.Fatalf("third-party URL/field or relay leaked into public envelope: %s", string(encoded))
		}

		data := unified.Map(view["data"])
		if len([]rune(unified.Str(data["title"]))) != 80 {
			t.Fatalf("expected title truncated to 80 runes, got %d (%q)", len([]rune(unified.Str(data["title"]))), unified.Str(data["title"]))
		}
		author := unified.Map(data["author"])
		if unified.Str(author["id"]) != "MiuMiu99999999" ||
			unified.Str(author["name"]) != "MiuMiu Channel" ||
			unified.Str(author["url"]) != "https://x.com/MiuMiu99999999" ||
			author["avatar"] != nil {
			t.Fatalf("unexpected sanitized author: %+v", author)
		}
		items := unified.Slice(data["items"])
		if len(items) != 1 {
			t.Fatalf("expected 1 photo item, got %d", len(items))
		}
		item := unified.Map(items[0])
		def := unified.Map(item["default"])
		const wantOrigURL = "https://pbs.twimg.com/media/HCpE9UZaAAAHlX5?format=jpg&name=orig"
		if unified.Str(def["url"]) != wantOrigURL {
			t.Fatalf("default.url = %q, want %q", unified.Str(def["url"]), wantOrigURL)
		}
		meta := unified.Map(unified.Map(unified.Slice(item["variants"])[0])["metadata"])
		if meta["width"] != int64(2448) || meta["height"] != int64(3264) {
			t.Fatalf("expected 2448x3264 metadata, got %+v", meta)
		}
	})

	// 2. Four-photo tombstone album + /photo/N selection + out-of-bounds /photo/N.
	t.Run("tombstone-four-photos-and-photo-index-selection", func(t *testing.T) {
		const tweetID = "1900000000000000004"
		fxFourBody, _ := json.Marshal(map[string]any{
			"code": 200,
			"tweet": map[string]any{
				"text":   "Four restricted photos https://t.co/AlbumLink",
				"author": map[string]any{"name": "Artist", "screen_name": "artist_x"},
				"media": map[string]any{
					"photos": []map[string]any{
						{"url": "https://pbs.twimg.com/media/TombImg1.jpg?name=orig", "width": 2000, "height": 1500},
						{"url": "https://pbs.twimg.com/media/TombImg2.png?name=orig", "width": 1920, "height": 1080},
						{"url": "https://pbs.twimg.com/media/TombImg3?format=webp&name=large", "width": 1440, "height": 1800},
						{"url": "https://pbs.twimg.com:443/media/TombImg4.jpeg", "width": 1200, "height": 1200},
						{"url": "https://pbs.twimg.com/media/TombImg5Ignored.jpg", "width": 800, "height": 800},
					},
				},
			},
		})

		var fxCalls atomic.Int32
		var ytCalls atomic.Int32
		p, err := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					ytCalls.Add(1)
					return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video", Retryable: false}
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					if strings.HasPrefix(req.URL, syndicationEndpointURL) {
						return fetch.Response{StatusCode: http.StatusOK, Body: tombstoneSynBody, URL: req.URL}, nil
					}
					fxCalls.Add(1)
					assertFxTwitterRequestHygiene(t, req, tweetID)
					return fetch.Response{StatusCode: http.StatusOK, Body: fxFourBody, URL: req.URL}, nil
				},
			},
		).WithTombstoneFallback(TombstoneFallbackFxTwitter)
		if err != nil {
			t.Fatal(err)
		}

		// Full status URL -> 4 photos + album group.
		fullEnv, err := p.Parse(context.Background(), "https://x.com/artist_x/status/"+tweetID, now)
		if err != nil {
			t.Fatalf("full 4-photo Parse failed: %v", err)
		}
		fullView := unified.PublicView(fullEnv, false, now)
		if err := unified.ValidateEnvelopeAt(fullView, now); err != nil {
			t.Fatalf("4-photo ValidateEnvelopeAt failed: %v", err)
		}
		if fxCalls.Load() != 1 {
			t.Fatalf("expected exactly 1 fxtwitter call, got %d", fxCalls.Load())
		}
		fullData := unified.Map(fullView["data"])
		items := unified.Slice(fullData["items"])
		if len(items) != 4 {
			t.Fatalf("expected 4 capped photo items, got %d", len(items))
		}
		wantURLs := []string{
			"https://pbs.twimg.com/media/TombImg1?format=jpg&name=orig",
			"https://pbs.twimg.com/media/TombImg2?format=png&name=orig",
			"https://pbs.twimg.com/media/TombImg3?format=webp&name=orig",
			"https://pbs.twimg.com/media/TombImg4?format=jpg&name=orig",
		}
		for i, want := range wantURLs {
			got := unified.Str(unified.Map(unified.Map(items[i])["default"])["url"])
			if got != want {
				t.Fatalf("item[%d] default.url = %q, want %q", i, got, want)
			}
		}
		groups := unified.Slice(fullData["groups"])
		if len(groups) != 1 || len(unified.Slice(unified.Map(groups[0])["item_ids"])) != 4 {
			t.Fatalf("expected 4-item album group, got %+v", groups)
		}

		// /photo/2 -> bypasses yt-dlp, returns only the 2nd photo.
		ytCallsBefore := ytCalls.Load()
		photo2Env, err := p.Parse(context.Background(), "https://x.com/artist_x/status/"+tweetID+"/photo/2", now)
		if err != nil {
			t.Fatalf("/photo/2 Parse failed: %v", err)
		}
		if ytCalls.Load() != ytCallsBefore {
			t.Fatalf("expected /photo/2 to bypass yt-dlp, ytCalls changed %d -> %d", ytCallsBefore, ytCalls.Load())
		}
		photo2View := unified.PublicView(photo2Env, false, now)
		if err := unified.ValidateEnvelopeAt(photo2View, now); err != nil {
			t.Fatalf("/photo/2 ValidateEnvelopeAt failed: %v", err)
		}
		photo2Items := unified.Slice(unified.Map(photo2View["data"])["items"])
		if len(photo2Items) != 1 || unified.Str(unified.Map(unified.Map(photo2Items[0])["default"])["url"]) != wantURLs[1] {
			t.Fatalf("unexpected /photo/2 items: %+v", photo2Items)
		}

		// Out-of-bounds /photo/3 on a 2-photo tombstone tweet -> CONTENT_UNAVAILABLE.
		fxTwoBody, _ := json.Marshal(map[string]any{
			"code": 200,
			"tweet": map[string]any{
				"text": "Two photos",
				"media": map[string]any{
					"photos": []map[string]any{
						{"url": "https://pbs.twimg.com/media/TombImg1.jpg", "width": 1000, "height": 1000},
						{"url": "https://pbs.twimg.com/media/TombImg2.jpg", "width": 1000, "height": 1000},
					},
				},
			},
		})
		pTwo, _ := NewWithFetcher(
			&stubExtractor{enabled: true},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					if strings.HasPrefix(req.URL, syndicationEndpointURL) {
						return fetch.Response{StatusCode: http.StatusOK, Body: tombstoneSynBody, URL: req.URL}, nil
					}
					return fetch.Response{StatusCode: http.StatusOK, Body: fxTwoBody, URL: req.URL}, nil
				},
			},
		).WithTombstoneFallback(TombstoneFallbackFxTwitter)
		oobEnv, err := pTwo.Parse(context.Background(), "https://x.com/artist_x/status/"+tweetID+"/photo/3", now)
		if err != nil || unified.Str(oobEnv["code"]) != "CONTENT_UNAVAILABLE" {
			t.Fatalf("expected CONTENT_UNAVAILABLE for out-of-bounds /photo/3, got env=%+v err=%v", oobEnv, err)
		}
	})

	// 3. Tombstone video (progressive MP4s + master .m3u8 + SizeProber + /video/1 + HLS-only).
	t.Run("tombstone-video-and-hls-only", func(t *testing.T) {
		const tweetID = "1900000000000000005"
		const mp4Best = "https://video.twimg.com/amplify_video/1900000000000000001/vid/avc1/728x720/best720.mp4?tag=14"
		const mp4Low = "https://video.twimg.com/amplify_video/1900000000000000001/vid/avc1/364x360/low360.mp4?tag=14"
		const thumbURL = "https://pbs.twimg.com/amplify_video_thumb/1900000000000000001/img/cover.jpg"

		fxVideoBody, _ := json.Marshal(map[string]any{
			"code": 200,
			"tweet": map[string]any{
				"text":   "Restricted video post https://t.co/VidLink",
				"author": map[string]any{"name": "VideoAuthor", "screen_name": "vid_author"},
				"media": map[string]any{
					"videos": []map[string]any{
						{
							"id":            "1900000000000000001",
							"type":          "video",
							"url":           mp4Best,
							"thumbnail_url": thumbURL,
							"duration":      15.488,
							"width":         728,
							"height":        720,
							"formats": []map[string]any{
								{
									"url":       "https://video.twimg.com/amplify_video/1900000000000000001/pl/master.m3u8?tag=14",
									"container": "m3u8",
								},
								{
									"url":       mp4Low,
									"bitrate":   832000,
									"container": "mp4",
									"codec":     "h264",
								},
								{
									"url":       mp4Best,
									"bitrate":   2176000,
									"container": "mp4",
									"codec":     "h264",
								},
							},
						},
					},
				},
			},
		})

		for _, targetURL := range []string{
			"https://x.com/vid_author/status/" + tweetID,
			"https://x.com/vid_author/status/" + tweetID + "/video/1",
		} {
			var fxCalls atomic.Int32
			p, err := NewWithFetcher(
				&stubExtractor{
					enabled: true,
					extract: func(context.Context, string) (*ytengine.Info, error) {
						return nil, &ytengine.Error{Code: ytengine.CodeUpstreamBlocked, Message: "NSFW tweet requires login", Retryable: false}
					},
				},
				func(_ context.Context, rawURL string) (int64, bool) {
					if rawURL == mp4Best {
						return 4194304, true
					}
					return 0, false
				},
				fakeXFetcher{
					fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
						if strings.HasPrefix(req.URL, syndicationEndpointURL) {
							return fetch.Response{StatusCode: http.StatusOK, Body: tombstoneSynBody, URL: req.URL}, nil
						}
						fxCalls.Add(1)
						assertFxTwitterRequestHygiene(t, req, tweetID)
						return fetch.Response{StatusCode: http.StatusOK, Body: fxVideoBody, URL: req.URL}, nil
					},
				},
			).WithTombstoneFallback(TombstoneFallbackFxTwitter)
			if err != nil {
				t.Fatal(err)
			}

			env, err := p.Parse(context.Background(), targetURL, now)
			if err != nil {
				t.Fatalf("Parse(%s) failed: %v", targetURL, err)
			}
			if fxCalls.Load() != 1 {
				t.Fatalf("Parse(%s): fxCalls = %d, want 1", targetURL, fxCalls.Load())
			}
			view := unified.PublicView(env, false, now)
			if err := unified.ValidateEnvelopeAt(view, now); err != nil {
				t.Fatalf("Parse(%s) ValidateEnvelopeAt failed: %v", targetURL, err)
			}
			data := unified.Map(view["data"])
			if unified.Str(unified.Map(data["cover"])["url"]) != thumbURL {
				t.Fatalf("unexpected cover: %+v", data["cover"])
			}
			items := unified.Slice(data["items"])
			if len(items) != 1 {
				t.Fatalf("expected 1 video item, got %d", len(items))
			}
			item := unified.Map(items[0])
			if unified.Str(item["type"]) != "video" || item["duration_ms"] != int64(15488) {
				t.Fatalf("unexpected video item fields: %+v", item)
			}
			def := unified.Map(item["default"])
			if unified.Str(def["url"]) != mp4Best {
				t.Fatalf("expected default MP4 %q, got %+v", mp4Best, def)
			}
			variants := unified.Slice(item["variants"])
			if len(variants) != 2 {
				t.Fatalf("expected 2 progressive MP4 variants (master m3u8 skipped), got %d: %+v", len(variants), variants)
			}
			bestMeta := unified.Map(unified.Map(variants[0])["metadata"])
			if bestMeta["width"] != int64(728) || bestMeta["height"] != int64(720) || bestMeta["bitrate_bps"] != int64(2176000) || bestMeta["has_audio"] != true {
				t.Fatalf("unexpected 720p metadata: %+v", bestMeta)
			}
			bestSrc := unified.Map(unified.Slice(unified.Map(unified.Map(variants[0])["delivery"])["sources"])[0])
			if bestSrc["size_bytes"] != int64(4194304) {
				t.Fatalf("expected probed size_bytes=4194304, got %+v", bestSrc)
			}
		}

		// HLS-only tombstone video succeeds when no MP4 is available.
		fxHLSOnlyBody, _ := json.Marshal(map[string]any{
			"code": 200,
			"tweet": map[string]any{
				"text": "HLS only restricted video",
				"media": map[string]any{
					"videos": []map[string]any{
						{
							"id":            "1900000000000000099",
							"type":          "video",
							"url":           "https://video.twimg.com/amplify_video/1900000000000000099/pl/1280x720/playlist.m3u8",
							"thumbnail_url": thumbURL,
							"duration":      10.0,
						},
					},
				},
			},
		})
		pHLS, _ := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{Code: ytengine.CodeUpstreamBlocked, Message: "blocked", Retryable: false}
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					if strings.HasPrefix(req.URL, syndicationEndpointURL) {
						return fetch.Response{StatusCode: http.StatusOK, Body: tombstoneSynBody, URL: req.URL}, nil
					}
					return fetch.Response{StatusCode: http.StatusOK, Body: fxHLSOnlyBody, URL: req.URL}, nil
				},
			},
		).WithTombstoneFallback(TombstoneFallbackFxTwitter)
		hlsEnv, err := pHLS.Parse(context.Background(), "https://x.com/vid_author/status/"+tweetID, now)
		if err != nil {
			t.Fatalf("HLS-only Parse failed: %v", err)
		}
		hlsView := unified.PublicView(hlsEnv, false, now)
		if err := unified.ValidateEnvelopeAt(hlsView, now); err != nil {
			t.Fatalf("HLS-only ValidateEnvelopeAt failed: %v", err)
		}
		if unified.Str(hlsView["code"]) != "OK" {
			t.Fatalf("expected OK on HLS-only tombstone video, got %+v", hlsView)
		}
	})

	// 4. Tombstone GIF (type="gif", silent /tweet_video/<key>.mp4, duration=0, bitrate=0).
	t.Run("tombstone-gif", func(t *testing.T) {
		const tweetID = "1900000000000000006"
		const gifURL = "https://video.twimg.com/tweet_video/GifKeyAbc123.mp4"
		const gifThumb = "https://pbs.twimg.com/tweet_video_thumb/GifKeyAbc123.jpg"

		fxGIFBody, _ := json.Marshal(map[string]any{
			"code": 200,
			"tweet": map[string]any{
				"text":   "Restricted animated GIF",
				"author": map[string]any{"name": "GifMaker", "screen_name": "gif_maker"},
				"media": map[string]any{
					"videos": []map[string]any{
						{
							"id":            "1900000000000000002",
							"type":          "gif",
							"url":           gifURL,
							"thumbnail_url": gifThumb,
							"duration":      0,
							"width":         498,
							"height":        280,
							"formats": []map[string]any{
								{
									"url":       gifURL,
									"bitrate":   0,
									"container": "mp4",
									"codec":     "h264",
								},
							},
						},
					},
				},
			},
		})

		p, err := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{Code: ytengine.CodeUpstreamBlocked, Message: "restricted", Retryable: false}
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					if strings.HasPrefix(req.URL, syndicationEndpointURL) {
						return fetch.Response{StatusCode: http.StatusOK, Body: tombstoneSynBody, URL: req.URL}, nil
					}
					assertFxTwitterRequestHygiene(t, req, tweetID)
					return fetch.Response{StatusCode: http.StatusOK, Body: fxGIFBody, URL: req.URL}, nil
				},
			},
		).WithTombstoneFallback(TombstoneFallbackFxTwitter)
		if err != nil {
			t.Fatal(err)
		}

		env, err := p.Parse(context.Background(), "https://x.com/gif_maker/status/"+tweetID, now)
		if err != nil {
			t.Fatalf("GIF Parse failed: %v", err)
		}
		view := unified.PublicView(env, false, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("GIF ValidateEnvelopeAt failed: %v", err)
		}
		item := unified.Map(unified.Slice(unified.Map(view["data"])["items"])[0])
		if item["duration_ms"] != nil {
			t.Fatalf("GIF with duration=0 must omit duration_ms, got %v", item["duration_ms"])
		}
		if unified.Str(unified.Map(item["default"])["url"]) != gifURL {
			t.Fatalf("expected GIF default.url=%q, got %+v", gifURL, item["default"])
		}
		meta := unified.Map(unified.Map(unified.Slice(item["variants"])[0])["metadata"])
		if meta["has_audio"] != false || meta["width"] != int64(498) || meta["height"] != int64(280) {
			t.Fatalf("unexpected GIF metadata: %+v", meta)
		}
	})
}

func TestParseXTombstoneSpoofedMediaHostsDiscarded(t *testing.T) {
	const now int64 = 1762300800
	const tweetID = "1900000000000000007"
	tombstoneSynBody := []byte(`{"__typename":"TweetTombstone"}`)

	spoofedPhotos := []map[string]any{
		{"url": "https://pbs.twimg.com.evil/media/Spoof1.jpg", "width": 1000, "height": 1000},
		{"url": "https://evilpbs.twimg.com/media/Spoof2.jpg", "width": 1000, "height": 1000},
		{"url": "https://sub.pbs.twimg.com/media/Spoof3.jpg", "width": 1000, "height": 1000},
		{"url": "https://user:secret@pbs.twimg.com/media/Spoof4.jpg", "width": 1000, "height": 1000},
		{"url": "https://pbs.twimg.com:8443/media/Spoof5.jpg", "width": 1000, "height": 1000},
		{"url": "https://pbs.twimg.com:80/media/Spoof6.jpg", "width": 1000, "height": 1000},
		{"url": "http://pbs.twimg.com/media/Spoof7.jpg", "width": 1000, "height": 1000},
		{"url": "https://video.twimg.com/media/Spoof8.jpg", "width": 1000, "height": 1000},
		{"url": "https://mosaic.fxtwitter.com/jpeg/Spoof9.jpg", "width": 1000, "height": 1000},
	}

	// 1a. All photos spoofed -> 403 UPSTREAM_BLOCKED.
	allSpoofPhotoBody, _ := json.Marshal(map[string]any{
		"code": 200,
		"tweet": map[string]any{
			"text":  "All spoofed photos",
			"media": map[string]any{"photos": spoofedPhotos},
		},
	})
	makeParser := func(fxBody []byte) *Parser {
		p, err := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{Code: ytengine.CodeUpstreamBlocked, Message: "blocked", Retryable: false}
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					if strings.HasPrefix(req.URL, syndicationEndpointURL) {
						return fetch.Response{StatusCode: http.StatusOK, Body: tombstoneSynBody, URL: req.URL}, nil
					}
					return fetch.Response{StatusCode: http.StatusOK, Body: fxBody, URL: req.URL}, nil
				},
			},
		).WithTombstoneFallback(TombstoneFallbackFxTwitter)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	env, err := makeParser(allSpoofPhotoBody).Parse(context.Background(), "https://x.com/u/status/"+tweetID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := unified.ValidateEnvelopeAt(env, now); err != nil {
		t.Fatal(err)
	}
	if unified.Str(env["code"]) != "UPSTREAM_BLOCKED" || unified.Map(env["error"])["retryable"] == true {
		t.Fatalf("expected UPSTREAM_BLOCKED when all photo URLs are spoofed, got %+v", env)
	}

	// 1b. Spoofed photos mixed with 1 valid pbs.twimg.com photo -> discards spoofed entries, keeps only valid photo.
	mixedPhotos := append([]map[string]any{}, spoofedPhotos...)
	mixedPhotos = append(mixedPhotos, map[string]any{
		"url":    "https://pbs.twimg.com/media/ValidSafeKey.png?name=large",
		"width":  1600,
		"height": 900,
	})
	mixedSpoofPhotoBody, _ := json.Marshal(map[string]any{
		"code": 200,
		"tweet": map[string]any{
			"text":  "Mixed spoofed and valid photo",
			"media": map[string]any{"photos": mixedPhotos},
		},
	})
	validPhotoEnv, err := makeParser(mixedSpoofPhotoBody).Parse(context.Background(), "https://x.com/u/status/"+tweetID, now)
	if err != nil {
		t.Fatal(err)
	}
	validPhotoView := unified.PublicView(validPhotoEnv, false, now)
	if err := unified.ValidateEnvelopeAt(validPhotoView, now); err != nil {
		t.Fatal(err)
	}
	photoItems := unified.Slice(unified.Map(validPhotoView["data"])["items"])
	if len(photoItems) != 1 || unified.Str(unified.Map(unified.Map(photoItems[0])["default"])["url"]) != "https://pbs.twimg.com/media/ValidSafeKey?format=png&name=orig" {
		t.Fatalf("expected only the 1 valid pbs.twimg.com photo to survive, got %+v", photoItems)
	}

	// 2a. All video stream URLs spoofed -> 403 UPSTREAM_BLOCKED.
	spoofedFormats := []map[string]any{
		{"url": "https://video.twimg.com.evil/ext_tw_video/1/pu/vid/1280x720/a.mp4", "bitrate": 2176000, "container": "mp4"},
		{"url": "https://evilvideo.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.mp4", "bitrate": 2176000, "container": "mp4"},
		{"url": "https://sub.video.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.mp4", "bitrate": 2176000, "container": "mp4"},
		{"url": "https://user:pass@video.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.mp4", "bitrate": 2176000, "container": "mp4"},
		{"url": "https://video.twimg.com:8443/ext_tw_video/1/pu/vid/1280x720/a.mp4", "bitrate": 2176000, "container": "mp4"},
		{"url": "https://video.twimg.com:80/ext_tw_video/1/pu/vid/1280x720/a.mp4", "bitrate": 2176000, "container": "mp4"},
		{"url": "http://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.mp4", "bitrate": 2176000, "container": "mp4"},
		{"url": "https://pbs.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.mp4", "bitrate": 2176000, "container": "mp4"},
		{"url": "https://d.fxtwitter.com/ext_tw_video/1/pu/vid/1280x720/a.mp4", "bitrate": 2176000, "container": "mp4"},
		{"url": "https://video.twimg.com/ext_tw_video/..%2fsecret/a.mp4", "bitrate": 2176000, "container": "mp4"},
		{"url": "https://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.html", "bitrate": 2176000, "container": "mp4"},
	}
	allSpoofVideoBody, _ := json.Marshal(map[string]any{
		"code": 200,
		"tweet": map[string]any{
			"text": "All spoofed videos",
			"media": map[string]any{
				"videos": []map[string]any{
					{
						"id":            "1",
						"type":          "video",
						"url":           "https://d.fxtwitter.com/spoof.mp4",
						"thumbnail_url": "https://pbs.twimg.com/ext_tw_video_thumb/1/pu/img/ok.jpg",
						"formats":       spoofedFormats,
					},
				},
			},
		},
	})
	spoofVidEnv, err := makeParser(allSpoofVideoBody).Parse(context.Background(), "https://x.com/u/status/"+tweetID, now)
	if err != nil {
		t.Fatal(err)
	}
	if unified.Str(spoofVidEnv["code"]) != "UPSTREAM_BLOCKED" {
		t.Fatalf("expected UPSTREAM_BLOCKED when all video stream URLs are spoofed, got %+v", spoofVidEnv)
	}

	// 2b. Valid video.twimg.com stream + spoofed cover URLs -> spoofed cover discarded, valid video kept without cover.
	for _, badCover := range []string{
		"https://pbs.twimg.com.evil/thumb.jpg",
		"https://evilpbs.twimg.com/thumb.jpg",
		"https://user:pass@pbs.twimg.com/thumb.jpg",
		"https://pbs.twimg.com:8443/thumb.jpg",
		"http://pbs.twimg.com/thumb.jpg",
		"https://video.twimg.com/thumb.jpg",
		"https://pbs.twimg.com/thumb.html",
	} {
		formatsWithOneValid := append([]map[string]any{}, spoofedFormats...)
		formatsWithOneValid = append(formatsWithOneValid, map[string]any{
			"url":       "https://video.twimg.com:443/ext_tw_video/1/pu/vid/1280x720/clean.mp4?tag=12",
			"bitrate":   2176000,
			"container": "mp4",
			"codec":     "h264",
		})
		bodyWithBadCover, _ := json.Marshal(map[string]any{
			"code": 200,
			"tweet": map[string]any{
				"text": "Valid stream with spoofed cover",
				"media": map[string]any{
					"videos": []map[string]any{
						{
							"id":            "1",
							"type":          "video",
							"url":           "https://d.fxtwitter.com/ignored.mp4",
							"thumbnail_url": badCover,
							"duration":      5.0,
							"formats":       formatsWithOneValid,
						},
					},
				},
			},
		})
		cleanVidEnv, err := makeParser(bodyWithBadCover).Parse(context.Background(), "https://x.com/u/status/"+tweetID, now)
		if err != nil {
			t.Fatal(err)
		}
		cleanVidView := unified.PublicView(cleanVidEnv, false, now)
		if err := unified.ValidateEnvelopeAt(cleanVidView, now); err != nil {
			t.Fatalf("badCover=%q ValidateEnvelopeAt failed: %v", badCover, err)
		}
		data := unified.Map(cleanVidView["data"])
		if data["cover"] != nil {
			t.Fatalf("badCover=%q leaked into data.cover: %+v", badCover, data["cover"])
		}
		item := unified.Map(unified.Slice(data["items"])[0])
		if item["cover"] != nil {
			t.Fatalf("badCover=%q leaked into item.cover: %+v", badCover, item["cover"])
		}
		if gotURL := unified.Str(unified.Map(item["default"])["url"]); gotURL != "https://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/clean.mp4?tag=12" {
			t.Fatalf("unexpected clean video URL: %q", gotURL)
		}
	}
}

func TestParseXTombstoneFallbackErrorSemanticsAndTriggerBoundaries(t *testing.T) {
	const now int64 = 1762300800
	const tweetID = "2119994769057132696"
	const tweetURL = "https://x.com/MiuMiu99999999/status/" + tweetID
	tombstoneSynBody := []byte(`{"__typename":"TweetTombstone"}`)

	// 1. Fallback response failure matrix: 404 -> CONTENT_UNAVAILABLE; everything else -> 403 UPSTREAM_BLOCKED.
	oversizedBody := append([]byte(`{"code":200,"pad":"`), append([]byte(strings.Repeat("a", (256<<10)+32)), []byte(`"}`)...)...)
	failureCases := []struct {
		name     string
		fxFn     func(context.Context, fetch.Request) (fetch.Response, error)
		wantCode string
	}{
		{
			name: "fx-404-not-found",
			fxFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusNotFound, Body: []byte(`{"code":404,"message":"NOT_FOUND"}`), URL: req.URL}, nil
			},
			wantCode: "CONTENT_UNAVAILABLE",
		},
		{
			name: "fx-redirect-302-status",
			fxFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				hdr := make(http.Header)
				hdr.Set("Location", "https://fxtwitter.com/i/status/"+tweetID)
				return fetch.Response{StatusCode: http.StatusFound, Header: hdr, URL: req.URL}, nil
			},
			wantCode: "UPSTREAM_BLOCKED",
		},
		{
			name: "fx-redirect-301-status",
			fxFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusMovedPermanently, URL: req.URL}, nil
			},
			wantCode: "UPSTREAM_BLOCKED",
		},
		{
			name: "fx-err-redirect",
			fxFn: func(context.Context, fetch.Request) (fetch.Response, error) {
				return fetch.Response{}, fetch.ErrRedirect
			},
			wantCode: "UPSTREAM_BLOCKED",
		},
		{
			name: "fx-oversized-err-limit",
			fxFn: func(context.Context, fetch.Request) (fetch.Response, error) {
				return fetch.Response{}, fetch.ErrLimit
			},
			wantCode: "UPSTREAM_BLOCKED",
		},
		{
			name: "fx-oversized-raw-body-over-256kib",
			fxFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: oversizedBody, URL: req.URL}, nil
			},
			wantCode: "UPSTREAM_BLOCKED",
		},
		{
			name: "fx-timeout",
			fxFn: func(context.Context, fetch.Request) (fetch.Response, error) {
				return fetch.Response{}, context.DeadlineExceeded
			},
			wantCode: "UPSTREAM_BLOCKED",
		},
		{
			name: "fx-policy-blocked",
			fxFn: func(context.Context, fetch.Request) (fetch.Response, error) {
				return fetch.Response{}, fetch.ErrPolicy
			},
			wantCode: "UPSTREAM_BLOCKED",
		},
		{
			name: "fx-non-json-html",
			fxFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: []byte(`<!DOCTYPE html><html>challenge</html>`), URL: req.URL}, nil
			},
			wantCode: "UPSTREAM_BLOCKED",
		},
		{
			name: "fx-malformed-json",
			fxFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: []byte(`{"code":200,"tweet":`), URL: req.URL}, nil
			},
			wantCode: "UPSTREAM_BLOCKED",
		},
		{
			name: "fx-500-server-error",
			fxFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusInternalServerError, Body: []byte(`{"code":500}`), URL: req.URL}, nil
			},
			wantCode: "UPSTREAM_BLOCKED",
		},
		{
			name: "fx-403-blocked",
			fxFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusForbidden, Body: []byte(`forbidden`), URL: req.URL}, nil
			},
			wantCode: "UPSTREAM_BLOCKED",
		},
		{
			name: "fx-200-without-media",
			fxFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: []byte(`{"code":200,"tweet":{"text":"text only restricted post"}}`), URL: req.URL}, nil
			},
			wantCode: "UPSTREAM_BLOCKED",
		},
	}

	for _, tc := range failureCases {
		t.Run(tc.name, func(t *testing.T) {
			var fxCalls atomic.Int32
			p, err := NewWithFetcher(
				&stubExtractor{
					enabled: true,
					extract: func(context.Context, string) (*ytengine.Info, error) {
						return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video", Retryable: false}
					},
				},
				nil,
				fakeXFetcher{
					fn: func(ctx context.Context, req fetch.Request) (fetch.Response, error) {
						if strings.HasPrefix(req.URL, syndicationEndpointURL) {
							return fetch.Response{StatusCode: http.StatusOK, Body: tombstoneSynBody, URL: req.URL}, nil
						}
						fxCalls.Add(1)
						assertFxTwitterRequestHygiene(t, req, tweetID)
						return tc.fxFn(ctx, req)
					},
				},
			).WithTombstoneFallback(TombstoneFallbackFxTwitter)
			if err != nil {
				t.Fatal(err)
			}

			env, err := p.Parse(context.Background(), tweetURL, now)
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if fxCalls.Load() != 1 {
				t.Fatalf("expected exactly 1 fxtwitter call, got %d", fxCalls.Load())
			}
			if err := unified.ValidateEnvelopeAt(env, now); err != nil {
				t.Fatalf("ValidateEnvelopeAt failed: %v", err)
			}
			if got := unified.Str(env["code"]); got != tc.wantCode {
				t.Fatalf("code = %q, want %q (env=%+v)", got, tc.wantCode, env)
			}
			errObj := unified.Map(env["error"])
			if errObj["retryable"] == true {
				t.Fatalf("expected non-retryable error, got %+v", errObj)
			}
			if tc.wantCode == "UPSTREAM_BLOCKED" && unified.Str(errObj["message"]) != tombstoneBlockedMessage {
				t.Fatalf("expected platform-neutral blocked message %q, got %q", tombstoneBlockedMessage, unified.Str(errObj["message"]))
			}
		})
	}

	// 2. Switch "off" (both default and explicit): NEVER calls fxtwitter, returns 403 UPSTREAM_BLOCKED on Tombstone.
	t.Run("switch-off-never-requests-fxtwitter-and-returns-upstream-blocked", func(t *testing.T) {
		defaultP := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video", Retryable: false}
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					if !strings.HasPrefix(req.URL, syndicationEndpointURL) {
						t.Fatalf("switch off must never request fxtwitter, got %s", req.URL)
					}
					return fetch.Response{StatusCode: http.StatusOK, Body: tombstoneSynBody, URL: req.URL}, nil
				},
			},
		)
		explicitOffP, err := defaultP.WithTombstoneFallback(TombstoneFallbackOff)
		if err != nil {
			t.Fatal(err)
		}

		for _, p := range []*Parser{defaultP, explicitOffP} {
			for _, u := range []string{tweetURL, tweetURL + "/photo/1"} {
				env, err := p.Parse(context.Background(), u, now)
				if err != nil {
					t.Fatalf("Parse(%s) error: %v", u, err)
				}
				if err := unified.ValidateEnvelopeAt(env, now); err != nil {
					t.Fatal(err)
				}
				if unified.Str(env["code"]) != "UPSTREAM_BLOCKED" ||
					unified.Str(unified.Map(env["error"])["message"]) != tombstoneBlockedMessage ||
					unified.Map(env["error"])["retryable"] == true {
					t.Fatalf("Parse(%s) with switch off = %+v, want UPSTREAM_BLOCKED", u, env)
				}
			}
		}
	})

	// 3. Normal tweets, syndication errors, and Tombstone-when-yt-dlp-got-video NEVER request fxtwitter.
	t.Run("normal-tweets-and-syndication-errors-never-request-fxtwitter", func(t *testing.T) {
		noFxCases := []struct {
			name    string
			ytFn    func(context.Context, string) (*ytengine.Info, error)
			synFn   func(context.Context, fetch.Request) (fetch.Response, error)
			wantErr string
		}{
			{
				name: "normal-photo-tweet",
				ytFn: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video", Retryable: false}
				},
				synFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{
						StatusCode: http.StatusOK,
						Body:       []byte(`{"__typename":"Tweet","text":"Normal photo","photos":[{"url":"https://pbs.twimg.com/media/NormalKey.jpg","width":800,"height":600}]}`),
						URL:        req.URL,
					}, nil
				},
				wantErr: "OK",
			},
			{
				name: "normal-text-only-tweet",
				ytFn: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video", Retryable: false}
				},
				synFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{
						StatusCode: http.StatusOK,
						Body:       []byte(`{"__typename":"Tweet","text":"Just text"}`),
						URL:        req.URL,
					}, nil
				},
				wantErr: "NO_MEDIA",
			},
			{
				name: "pure-text-tweet-with-external-link",
				ytFn: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "ERROR: [twitter] 123: No video could be found in this tweet", Retryable: false}
				},
				synFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{
						StatusCode: http.StatusOK,
						Body:       []byte(`{"__typename":"Tweet","text":"Read this article https://t.co/abcDEF123"}`),
						URL:        req.URL,
					}, nil
				},
				wantErr: "NO_MEDIA",
			},
			{
				name: "pure-text-tweet-empty-info",
				ytFn: func(context.Context, string) (*ytengine.Info, error) {
					return &ytengine.Info{ID: tweetID, Title: "Just text"}, nil
				},
				synFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{
						StatusCode: http.StatusOK,
						Body:       []byte(`{"__typename":"Tweet","text":"Just text"}`),
						URL:        req.URL,
					}, nil
				},
				wantErr: "NO_MEDIA",
			},
			{
				name: "syndication-network-error",
				ytFn: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video", Retryable: false}
				},
				synFn: func(context.Context, fetch.Request) (fetch.Response, error) {
					return fetch.Response{}, errors.New("dial tcp: connection reset")
				},
				wantErr: "UPSTREAM_UNAVAILABLE",
			},
			{
				name: "syndication-timeout",
				ytFn: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video", Retryable: false}
				},
				synFn: func(context.Context, fetch.Request) (fetch.Response, error) {
					return fetch.Response{}, context.DeadlineExceeded
				},
				wantErr: "UPSTREAM_TIMEOUT",
			},
			{
				name: "syndication-404",
				ytFn: func(context.Context, string) (*ytengine.Info, error) {
					return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "not found", Retryable: false}
				},
				synFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{StatusCode: http.StatusNotFound, Body: []byte(`not found`), URL: req.URL}, nil
				},
				wantErr: "CONTENT_UNAVAILABLE",
			},
			{
				name: "tombstone-but-ytdlp-already-obtained-video",
				ytFn: func(context.Context, string) (*ytengine.Info, error) {
					return &ytengine.Info{
						ID:        "999",
						DisplayID: tweetID,
						Title:     "Primary yt-dlp video",
						Formats: []ytengine.Format{
							{
								FormatID: "http-2176",
								URL:      "https://video.twimg.com/ext_tw_video/999/pu/vid/1280x720/v.mp4",
								Ext:      "mp4",
								Protocol: "https",
								Width:    int64Ptr(1280),
								Height:   int64Ptr(720),
								VCodec:   "h264",
							},
						},
					}, nil
				},
				synFn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{StatusCode: http.StatusOK, Body: tombstoneSynBody, URL: req.URL}, nil
				},
				wantErr: "OK",
			},
		}

		for _, tc := range noFxCases {
			t.Run(tc.name, func(t *testing.T) {
				p, err := NewWithFetcher(
					&stubExtractor{enabled: true, extract: tc.ytFn},
					nil,
					fakeXFetcher{
						fn: func(ctx context.Context, req fetch.Request) (fetch.Response, error) {
							if !strings.HasPrefix(req.URL, syndicationEndpointURL) {
								t.Fatalf("%s must NEVER request fxtwitter, got %s", tc.name, req.URL)
							}
							return tc.synFn(ctx, req)
						},
					},
				).WithTombstoneFallback(TombstoneFallbackFxTwitter)
				if err != nil {
					t.Fatal(err)
				}
				env, err := p.Parse(context.Background(), tweetURL, now)
				if err != nil {
					t.Fatalf("%s Go error: %v", tc.name, err)
				}
				if got := unified.Str(env["code"]); got != tc.wantErr {
					t.Fatalf("%s code = %q, want %q (env=%+v)", tc.name, got, tc.wantErr, env)
				}
			})
		}
	})
}

func TestParseXSyndicationVideoFastPathMatrix(t *testing.T) {
	const now int64 = 1762300800

	// 1a. Single amplify_video tweet where original_info (886x876) differs from actual MP4 path dimensions
	// (728x720, 364x360, 272x270): fast-path builds unified-v1 directly without invoking yt-dlp,
	// probes MP4 byte sizes only (never m3u8), and normalizes pbs.twimg.com cover with ?name=orig.
	t.Run("single-amplify-video-zero-ytdlp-and-size-prober", func(t *testing.T) {
		const tweetID = "1790637656616943991"
		const sourceURL = "https://x.com/historyinmemes/status/" + tweetID
		const mp4720 = "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/kF5AeH6MK-1qDlBg.mp4?tag=14"
		const mp4360 = "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/364x360/MhZUmBoG84GGC0fb.mp4?tag=14"
		const mp4270 = "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/272x270/du7PkQofwIDrjAEz.mp4?tag=14"
		const m3u8Master = "https://video.twimg.com/amplify_video/1790637589910654976/pl/osiKl6ALz2B8l9Cw.m3u8?tag=14&v=cfc"

		synBody, _ := json.Marshal(map[string]any{
			"__typename":         "Tweet",
			"id_str":             tweetID,
			"text":               "One of the most intense moments in history https://t.co/Zgzhvix8ES",
			"created_at":         "2024-05-15T06:57:40.000Z",
			"favorite_count":     132404,
			"conversation_count": 827,
			"user": map[string]any{
				"name":        "Historic Vids",
				"screen_name": "historyinmemes",
			},
			"mediaDetails": []map[string]any{
				{
					"type":            "video",
					"media_url_https": "https://pbs.twimg.com/amplify_video_thumb/1790637589910654976/img/jyhLQQZkq-lJJ9fN.jpg",
					"original_info":   map[string]any{"width": 886, "height": 876},
					"sizes":           map[string]any{"large": map[string]any{"w": 886, "h": 876}},
					"video_info": map[string]any{
						"duration_millis": 15488,
						"variants": []map[string]any{
							{"content_type": "application/x-mpegURL", "url": m3u8Master},
							{"bitrate": 288000, "content_type": "video/mp4", "url": mp4270},
							{"bitrate": 832000, "content_type": "video/mp4", "url": mp4360},
							{"bitrate": 2176000, "content_type": "video/mp4", "url": mp4720},
						},
					},
				},
			},
		})

		var ytCalls atomic.Int32
		var probedMu sync.Mutex
		probedURLs := make([]string, 0, 3)

		p := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					ytCalls.Add(1)
					return nil, errors.New("yt-dlp must not be spawned on syndication video fast-path")
				},
			},
			func(_ context.Context, rawURL string) (int64, bool) {
				probedMu.Lock()
				probedURLs = append(probedURLs, rawURL)
				probedMu.Unlock()
				if rawURL == mp4720 {
					return 4212736, true
				}
				return 0, false
			},
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{StatusCode: http.StatusOK, Body: synBody, URL: req.URL}, nil
				},
			},
		)

		env, err := p.Parse(context.Background(), sourceURL, now)
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		if ytCalls.Load() != 0 {
			t.Fatalf("expected 0 yt-dlp calls on fast-path, got %d", ytCalls.Load())
		}
		for _, u := range probedURLs {
			if strings.Contains(u, ".m3u8") {
				t.Fatalf("SizeProber must never probe HLS master playlist: %q", u)
			}
		}

		view := unified.PublicView(env, false, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("ValidateEnvelopeAt failed: %v", err)
		}
		data := unified.Map(view["data"])
		if got := unified.Str(data["title"]); got != "Historic Vids - One of the most intense moments in history" {
			t.Fatalf("unexpected title: %q", got)
		}
		if got := unified.Str(data["published_at"]); got != "2024-05-15T06:57:40Z" {
			t.Fatalf("unexpected published_at: %q", got)
		}
		wantCover := "https://pbs.twimg.com/amplify_video_thumb/1790637589910654976/img/jyhLQQZkq-lJJ9fN.jpg?name=orig"
		if got := unified.Str(unified.Map(data["cover"])["url"]); got != wantCover {
			t.Fatalf("cover = %q, want %q", got, wantCover)
		}

		items := unified.Slice(data["items"])
		if len(items) != 1 {
			t.Fatalf("expected 1 video item, got %d", len(items))
		}
		item := unified.Map(items[0])
		if item["duration_ms"] != int64(15488) {
			t.Fatalf("duration_ms = %v, want 15488", item["duration_ms"])
		}
		if got := unified.Str(unified.Map(item["default"])["url"]); got != mp4720 {
			t.Fatalf("default.url = %q, want %q", got, mp4720)
		}
		variants := unified.Slice(item["variants"])
		if len(variants) != 3 {
			t.Fatalf("expected 3 progressive MP4 variants, got %d", len(variants))
		}
		wantDims := [][3]int64{
			{728, 720, 2176000},
			{364, 360, 832000},
			{272, 270, 288000},
		}
		wantLabels := []string{"720p", "360p", "270p"}
		for i, v := range variants {
			vm := unified.Map(v)
			if got := unified.Str(vm["label"]); got != wantLabels[i] {
				t.Fatalf("variant[%d] label = %q, want %q", i, got, wantLabels[i])
			}
			meta := unified.Map(vm["metadata"])
			if meta["width"] != wantDims[i][0] || meta["height"] != wantDims[i][1] || meta["bitrate_bps"] != wantDims[i][2] || meta["has_audio"] != true {
				t.Fatalf("variant[%d] metadata mismatch: %+v", i, meta)
			}
		}
		src0 := unified.Map(unified.Slice(unified.Map(unified.Map(variants[0])["delivery"])["sources"])[0])
		if src0["size_bytes"] != int64(4212736) {
			t.Fatalf("expected probed size_bytes=4212736 on 720p variant, got %+v", src0)
		}
	})

	// 1b. Multi-video playlist (4 videos) + /video/3 selection + out-of-bounds /video/4 on 2-video tweet.
	t.Run("multi-video-playlist-and-video-index-selection", func(t *testing.T) {
		const tweetID = "1577719286659006464"
		makeEntry := func(mediaID string, durMS int64) map[string]any {
			return map[string]any{
				"id_str":          mediaID,
				"type":            "video",
				"media_url_https": "https://pbs.twimg.com/ext_tw_video_thumb/" + mediaID + "/pu/img/thumb.jpg",
				"original_info":   map[string]any{"width": 1280, "height": 720},
				"sizes":           map[string]any{"large": map[string]any{"w": 1280, "h": 720}},
				"video_info": map[string]any{
					"duration_millis": durMS,
					"variants": []map[string]any{
						{"content_type": "application/x-mpegURL", "url": "https://video.twimg.com/ext_tw_video/" + mediaID + "/pu/pl/master.m3u8?tag=12"},
						{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/" + mediaID + "/pu/vid/1280x720/v720.mp4?tag=12"},
						{"bitrate": 832000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/" + mediaID + "/pu/vid/640x360/v360.mp4?tag=12"},
					},
				},
			}
		}

		fourVidBody, _ := json.Marshal(map[string]any{
			"__typename": "Tweet",
			"id_str":     tweetID,
			"text":       "Test https://t.co/Y3KEZD7Dad",
			"user":       map[string]any{"name": "Ultima", "screen_name": "UltimaShadowX"},
			"mediaDetails": []map[string]any{
				makeEntry("1577719236209922048", 4266),
				makeEntry("1577719252450267136", 2100),
				makeEntry("1577719262554365953", 4766),
				makeEntry("1577719274625572864", 3700),
			},
		})

		var ytCalls atomic.Int32
		p := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					ytCalls.Add(1)
					return nil, errors.New("unexpected yt-dlp call")
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{StatusCode: http.StatusOK, Body: fourVidBody, URL: req.URL}, nil
				},
			},
		)

		fullEnv, err := p.Parse(context.Background(), "https://x.com/UltimaShadowX/status/"+tweetID, now)
		if err != nil {
			t.Fatalf("4-video Parse failed: %v", err)
		}
		fullView := unified.PublicView(fullEnv, false, now)
		if err := unified.ValidateEnvelopeAt(fullView, now); err != nil {
			t.Fatalf("ValidateEnvelopeAt failed: %v", err)
		}
		items := unified.Slice(unified.Map(fullView["data"])["items"])
		if len(items) != 4 {
			t.Fatalf("expected 4 video items, got %d", len(items))
		}
		wantTitles := []string{"Ultima - Test #1", "Ultima - Test #2", "Ultima - Test #3", "Ultima - Test #4"}
		for i, wantTitle := range wantTitles {
			if got := unified.Str(unified.Map(items[i])["title"]); got != wantTitle {
				t.Fatalf("item[%d].title = %q, want %q", i, got, wantTitle)
			}
		}

		// /video/3 selects the 3rd video as default_item_id without calling yt-dlp.
		selEnv, err := p.Parse(context.Background(), "https://x.com/UltimaShadowX/status/"+tweetID+"/video/3", now)
		if err != nil {
			t.Fatalf("/video/3 Parse failed: %v", err)
		}
		selView := unified.PublicView(selEnv, false, now)
		if err := unified.ValidateEnvelopeAt(selView, now); err != nil {
			t.Fatalf("/video/3 ValidateEnvelopeAt failed: %v", err)
		}
		selData := unified.Map(selView["data"])
		selItems := unified.Slice(selData["items"])
		if unified.Str(selData["default_item_id"]) != unified.Str(unified.Map(selItems[2])["id"]) {
			t.Fatalf("expected /video/3 default_item_id to select 3rd item, got %q vs %q",
				unified.Str(selData["default_item_id"]), unified.Str(unified.Map(selItems[2])["id"]))
		}
		if ytCalls.Load() != 0 {
			t.Fatalf("expected 0 yt-dlp calls across 4-video tests, got %d", ytCalls.Load())
		}
	})

	// 1c. Mixed video + photo tweet: fast-path handles bare status URL ([video, image]), /video/1 ([video] only), and /photo/2 ([image] only) with 0 yt-dlp calls.
	t.Run("mixed-video-photo-fast-path-and-selectors", func(t *testing.T) {
		const tweetID = "1577855540407197696"
		mixedBody, _ := json.Marshal(map[string]any{
			"__typename": "Tweet",
			"id_str":     tweetID,
			"text":       "gm ✨️ now I can post image and video. nice update. https://t.co/cG7XgiINOm",
			"user":       map[string]any{"name": "Oshtru", "screen_name": "oshtru"},
			"photos": []map[string]any{
				{"url": "https://pbs.twimg.com/media/FeWrKrMaYAELAxK.jpg", "width": 1536, "height": 1920},
			},
			"mediaDetails": []map[string]any{
				{
					"id_str":          "1577855447914409984",
					"type":            "video",
					"media_url_https": "https://pbs.twimg.com/ext_tw_video_thumb/1577855447914409984/pu/img/5V20pjPHBu3nz6jb.jpg",
					"original_info":   map[string]any{"width": 720, "height": 900},
					"sizes":           map[string]any{"large": map[string]any{"w": 720, "h": 900}},
					"video_info": map[string]any{
						"duration_millis": 30030,
						"variants": []map[string]any{
							{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/1577855447914409984/pu/vid/720x900/Dn3XhZkvCK_gMg0d.mp4?tag=12"},
							{"bitrate": 950000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/1577855447914409984/pu/vid/480x600/7-QYdEW4oBKK4vSr.mp4?tag=12"},
						},
					},
				},
				{
					"type":            "photo",
					"media_url_https": "https://pbs.twimg.com/media/FeWrKrMaYAELAxK.jpg",
					"original_info":   map[string]any{"width": 1536, "height": 1920},
				},
			},
		})

		var ytCalls atomic.Int32
		p := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					ytCalls.Add(1)
					return nil, errors.New("unexpected yt-dlp call on complete mixed tweet")
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{StatusCode: http.StatusOK, Body: mixedBody, URL: req.URL}, nil
				},
			},
		)

		// Bare status URL -> [video, image] + album group.
		bareEnv, err := p.Parse(context.Background(), "https://x.com/oshtru/status/"+tweetID, now)
		if err != nil {
			t.Fatal(err)
		}
		bareView := unified.PublicView(bareEnv, false, now)
		if err := unified.ValidateEnvelopeAt(bareView, now); err != nil {
			t.Fatal(err)
		}
		bareItems := unified.Slice(unified.Map(bareView["data"])["items"])
		if len(bareItems) != 2 || unified.Str(unified.Map(bareItems[0])["type"]) != "video" || unified.Str(unified.Map(bareItems[1])["type"]) != "image" {
			t.Fatalf("expected [video, image] on bare URL, got %+v", bareItems)
		}

		// /video/1 -> [video] only (no photos appended when selectedIndex > 0).
		v1Env, err := p.Parse(context.Background(), "https://x.com/oshtru/status/"+tweetID+"/video/1", now)
		if err != nil {
			t.Fatal(err)
		}
		v1View := unified.PublicView(v1Env, false, now)
		if err := unified.ValidateEnvelopeAt(v1View, now); err != nil {
			t.Fatal(err)
		}
		v1Items := unified.Slice(unified.Map(v1View["data"])["items"])
		if len(v1Items) != 1 || unified.Str(unified.Map(v1Items[0])["type"]) != "video" {
			t.Fatalf("expected 1 video item on /video/1, got %+v", v1Items)
		}

		// /photo/2 -> 2nd attachment (image) only.
		p2Env, err := p.Parse(context.Background(), "https://x.com/oshtru/status/"+tweetID+"/photo/2", now)
		if err != nil {
			t.Fatal(err)
		}
		p2View := unified.PublicView(p2Env, false, now)
		if err := unified.ValidateEnvelopeAt(p2View, now); err != nil {
			t.Fatal(err)
		}
		p2Items := unified.Slice(unified.Map(p2View["data"])["items"])
		if len(p2Items) != 1 || unified.Str(unified.Map(p2Items[0])["type"]) != "image" {
			t.Fatalf("expected 1 image item on /photo/2, got %+v", p2Items)
		}
		if ytCalls.Load() != 0 {
			t.Fatalf("expected 0 yt-dlp calls across mixed fast-path tests, got %d", ytCalls.Load())
		}
	})

	// 1d. animated_gif tweet (duration_millis: 0, bitrate: 0, single /tweet_video/<Key>.mp4 using original_info dimensions)
	// and 1e. unified_card video tweet.
	t.Run("animated-gif-and-unified-card-fast-path", func(t *testing.T) {
		gifBody, _ := json.Marshal(map[string]any{
			"__typename": "Tweet",
			"id_str":     "1800000000000000001",
			"text":       "Looping GIF https://t.co/GifLoop",
			"user":       map[string]any{"name": "GifBot", "screen_name": "gifbot"},
			"mediaDetails": []map[string]any{
				{
					"id_str":          "1800000000000000099",
					"type":            "animated_gif",
					"media_url_https": "https://pbs.twimg.com/tweet_video_thumb/F1a2B3c4D5e6.jpg",
					"original_info":   map[string]any{"width": 498, "height": 280},
					"sizes":           map[string]any{"large": map[string]any{"w": 498, "h": 280}},
					"video_info": map[string]any{
						"duration_millis": 0,
						"variants": []map[string]any{
							{"bitrate": 0, "content_type": "video/mp4", "url": "https://video.twimg.com/tweet_video/F1a2B3c4D5e6.mp4"},
						},
					},
				},
			},
		})

		ucInner, _ := json.Marshal(map[string]any{
			"type": "video_app",
			"media_entities": map[string]any{
				"4_2120003298589655040": map[string]any{
					"id_str":          "2120003298589655040",
					"type":            "photo",
					"media_url_https": "https://pbs.twimg.com/card_img/2120003298589655040/9fR3xxQR?format=jpg&name=orig",
					"original_info":   map[string]any{"width": 246, "height": 246},
				},
				"13_2001841416071450628": map[string]any{
					"id_str":          "2001841416071450628",
					"type":            "video",
					"media_url_https": "https://pbs.twimg.com/amplify_video_thumb/2001841416071450628/img/hpy5KpJh4pO17b65.jpg",
					"original_info":   map[string]any{"width": 1080, "height": 1080},
					"sizes":           map[string]any{"large": map[string]any{"w": 1080, "h": 1080}},
					"video_info": map[string]any{
						"duration_millis": 30278,
						"variants": []map[string]any{
							{"content_type": "application/x-mpegURL", "url": "https://video.twimg.com/amplify_video/2001841416071450628/pl/vCiP2cksCHPAxSVw.m3u8?tag=14"},
							{"bitrate": 1280000, "content_type": "video/mp4", "url": "https://video.twimg.com/amplify_video/2001841416071450628/vid/avc1/720x720/1L5-pvqqQlx9qW8I.mp4?tag=14"},
							{"bitrate": 832000, "content_type": "video/mp4", "url": "https://video.twimg.com/amplify_video/2001841416071450628/vid/avc1/540x540/fQMDaamZ4e37ZvAN.mp4?tag=14"},
						},
					},
				},
			},
		})
		ucBody, _ := json.Marshal(map[string]any{
			"__typename": "Tweet",
			"id_str":     "2001950365332455490",
			"text":       "Square unified_card video",
			"user":       map[string]any{"name": "Top Heroes", "screen_name": "TopHeroes_"},
			"card": map[string]any{
				"name": "unified_card",
				"binding_values": map[string]any{
					"unified_card": map[string]any{"string_value": string(ucInner)},
				},
			},
		})

		for _, tc := range []struct {
			name      string
			url       string
			body      []byte
			wantAudio bool
			wantW     int64
			wantH     int64
		}{
			{"animated-gif", "https://x.com/gifbot/status/1800000000000000001", gifBody, false, 498, 280},
			{"unified-card", "https://x.com/TopHeroes_/status/2001950365332455490", ucBody, true, 720, 720},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var ytCalls atomic.Int32
				p := NewWithFetcher(
					&stubExtractor{
						enabled: true,
						extract: func(context.Context, string) (*ytengine.Info, error) {
							ytCalls.Add(1)
							return nil, errors.New("unexpected yt-dlp call")
						},
					},
					nil,
					fakeXFetcher{
						fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
							return fetch.Response{StatusCode: http.StatusOK, Body: tc.body, URL: req.URL}, nil
						},
					},
				)
				env, err := p.Parse(context.Background(), tc.url, now)
				if err != nil {
					t.Fatal(err)
				}
				if ytCalls.Load() != 0 {
					t.Fatalf("expected 0 yt-dlp calls for %s, got %d", tc.name, ytCalls.Load())
				}
				view := unified.PublicView(env, false, now)
				if err := unified.ValidateEnvelopeAt(view, now); err != nil {
					t.Fatalf("ValidateEnvelopeAt(%s) failed: %v", tc.name, err)
				}
				item := unified.Map(unified.Slice(unified.Map(view["data"])["items"])[0])
				if !tc.wantAudio && item["duration_ms"] != nil {
					t.Fatalf("expected duration_ms=nil for 0ms GIF, got %v", item["duration_ms"])
				}
				meta := unified.Map(unified.Map(unified.Slice(item["variants"])[0])["metadata"])
				if meta["has_audio"] != tc.wantAudio || meta["width"] != tc.wantW || meta["height"] != tc.wantH {
					t.Fatalf("%s metadata mismatch: %+v", tc.name, meta)
				}
			})
		}
	})
}

func TestParseXSyndicationVideoCompletenessAndFallbackToYTDLP(t *testing.T) {
	const now int64 = 1762300800
	const tweetID = "1790637656616943991"
	const tweetURL = "https://x.com/historyinmemes/status/" + tweetID

	fallbackYTInfo := func() *ytengine.Info {
		return &ytengine.Info{
			ID:        "1790637589910654976",
			DisplayID: tweetID,
			Title:     "Fallback yt-dlp video",
			Duration:  float64Ptr(15.488),
			Formats: []ytengine.Format{
				{
					FormatID: "http-2176",
					URL:      "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/fallback.mp4?tag=14",
					Ext:      "mp4",
					Protocol: "https",
					Width:    int64Ptr(728),
					Height:   int64Ptr(720),
					TBR:      float64Ptr(2176.0),
				},
			},
		}
	}

	mustJSON := func(m map[string]any) []byte {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	cases := []struct {
		name string
		body []byte
	}{
		{
			name: "missing-video-info",
			body: mustJSON(map[string]any{
				"__typename": "Tweet",
				"text":       "Missing video_info",
				"mediaDetails": []map[string]any{
					{"id_str": "1", "type": "video", "media_url_https": "https://pbs.twimg.com/ext_tw_video_thumb/1/pu/img/t.jpg"},
				},
			}),
		},
		{
			name: "empty-variants",
			body: mustJSON(map[string]any{
				"__typename": "Tweet",
				"text":       "Empty variants",
				"mediaDetails": []map[string]any{
					{
						"id_str":     "1",
						"type":       "video",
						"video_info": map[string]any{"duration_millis": 5000, "variants": []map[string]any{}},
					},
				},
			}),
		},
		{
			name: "hls-only-no-progressive-mp4",
			body: mustJSON(map[string]any{
				"__typename": "Tweet",
				"text":       "HLS only",
				"mediaDetails": []map[string]any{
					{
						"id_str": "1",
						"type":   "video",
						"video_info": map[string]any{
							"duration_millis": 5000,
							"variants": []map[string]any{
								{"content_type": "application/x-mpegURL", "url": "https://video.twimg.com/amplify_video/1/pl/master.m3u8"},
							},
						},
					},
				},
			}),
		},
		{
			name: "multi-mp4-missing-path-dimensions",
			body: mustJSON(map[string]any{
				"__typename": "Tweet",
				"text":       "Two MP4s without WxH in path",
				"mediaDetails": []map[string]any{
					{
						"id_str":        "1",
						"type":          "video",
						"original_info": map[string]any{"width": 1280, "height": 720},
						"video_info": map[string]any{
							"duration_millis": 5000,
							"variants": []map[string]any{
								{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/amplify_video/1/vid/high.mp4"},
								{"bitrate": 832000, "content_type": "video/mp4", "url": "https://video.twimg.com/amplify_video/1/vid/low.mp4"},
							},
						},
					},
				},
			}),
		},
		{
			name: "single-mp4-missing-both-path-and-original-info-dimensions",
			body: mustJSON(map[string]any{
				"__typename": "Tweet",
				"text":       "Single MP4 without any dimensions",
				"mediaDetails": []map[string]any{
					{
						"id_str": "1",
						"type":   "video",
						"video_info": map[string]any{
							"duration_millis": 5000,
							"variants": []map[string]any{
								{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/amplify_video/1/vid/nodim.mp4"},
							},
						},
					},
				},
			}),
		},
		{
			name: "zero-duration-on-video-type",
			body: mustJSON(map[string]any{
				"__typename": "Tweet",
				"text":       "Video with zero duration",
				"mediaDetails": []map[string]any{
					{
						"id_str": "1",
						"type":   "video",
						"video_info": map[string]any{
							"duration_millis": 0,
							"variants": []map[string]any{
								{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/amplify_video/1/vid/1280x720/v.mp4"},
							},
						},
					},
				},
			}),
		},
		{
			name: "partial-multi-video-never-drops-second-video",
			body: mustJSON(map[string]any{
				"__typename": "Tweet",
				"text":       "Two videos where 2nd lacks MP4",
				"mediaDetails": []map[string]any{
					{
						"id_str": "1",
						"type":   "video",
						"video_info": map[string]any{
							"duration_millis": 4000,
							"variants": []map[string]any{
								{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/v1.mp4"},
							},
						},
					},
					{
						"id_str": "2",
						"type":   "video",
						"video_info": map[string]any{
							"duration_millis": 4000,
							"variants": []map[string]any{
								{"content_type": "application/x-mpegURL", "url": "https://video.twimg.com/ext_tw_video/2/pu/pl/master.m3u8"},
							},
						},
					},
				},
			}),
		},
		{
			name: "root-video-without-media-details",
			body: mustJSON(map[string]any{
				"__typename": "Tweet",
				"text":       "Root video without mediaDetails bitrates",
				"video": map[string]any{
					"durationMs": 5000,
					"variants": []map[string]any{
						{"type": "video/mp4", "src": "https://video.twimg.com/amplify_video/1/vid/1280x720/v.mp4"},
					},
				},
			}),
		},
		{
			name: "unsupported-broadcast-card",
			body: mustJSON(map[string]any{
				"__typename": "Tweet",
				"text":       "Live broadcast",
				"card":       map[string]any{"name": "2586390716:broadcast"},
			}),
		},
		{
			name: "unsupported-audiospace-card",
			body: mustJSON(map[string]any{
				"__typename": "Tweet",
				"text":       "X Space",
				"card":       map[string]any{"name": "3691233323:audiospace"},
			}),
		},
		{
			name: "unsupported-amplify-card",
			body: mustJSON(map[string]any{
				"__typename": "Tweet",
				"text":       "Legacy amplify card",
				"card":       map[string]any{"name": "amplify"},
			}),
		},
		{
			name: "quoted-tweet-with-video",
			body: mustJSON(map[string]any{
				"__typename": "Tweet",
				"text":       "Quoting a video post",
				"quoted_tweet": map[string]any{
					"mediaDetails": []map[string]any{{"type": "video"}},
				},
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ytCalls atomic.Int32
			p := NewWithFetcher(
				&stubExtractor{
					enabled: true,
					extract: func(context.Context, string) (*ytengine.Info, error) {
						ytCalls.Add(1)
						return fallbackYTInfo(), nil
					},
				},
				nil,
				fakeXFetcher{
					fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
						return fetch.Response{StatusCode: http.StatusOK, Body: tc.body, URL: req.URL}, nil
					},
				},
			)
			env, err := p.Parse(context.Background(), tweetURL, now)
			if err != nil {
				t.Fatalf("%s Go error: %v", tc.name, err)
			}
			if ytCalls.Load() != 1 {
				t.Fatalf("%s: expected seamless fallback to yt-dlp (ytCalls=1), got %d", tc.name, ytCalls.Load())
			}
			view := unified.PublicView(env, false, now)
			if err := unified.ValidateEnvelopeAt(view, now); err != nil {
				t.Fatalf("%s ValidateEnvelopeAt failed: %v", tc.name, err)
			}
			if unified.Str(view["code"]) != "OK" {
				t.Fatalf("%s expected code=OK from yt-dlp fallback, got %+v", tc.name, view)
			}
		})
	}
}

func TestParseXSyndicationVideoSpoofedHostsRejected(t *testing.T) {
	const now int64 = 1762300800
	const tweetID = "1790637656616943991"
	const tweetURL = "https://x.com/historyinmemes/status/" + tweetID

	spoofedVariants := []map[string]any{
		{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com.evil/ext_tw_video/1/pu/vid/1280x720/a.mp4"},
		{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://evilvideo.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.mp4"},
		{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://sub.video.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.mp4"},
		{"bitrate": 2176000, "content_type": "video/mp4", "url": "http://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.mp4"},
		{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com:8443/ext_tw_video/1/pu/vid/1280x720/a.mp4"},
		{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com:80/ext_tw_video/1/pu/vid/1280x720/a.mp4"},
		{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://user:pass@video.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.mp4"},
		{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://pbs.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.mp4"},
		{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/..%2fsecret/1280x720/a.mp4"},
		{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/a.html"},
	}

	// 1. All variants forged -> fast-path rejects, falls back to yt-dlp, and never leaks any non-allowlisted host.
	allSpoofBody, _ := json.Marshal(map[string]any{
		"__typename": "Tweet",
		"text":       "Spoofed syndication variants",
		"mediaDetails": []map[string]any{
			{
				"id_str":          "1",
				"type":            "video",
				"media_url_https": "https://pbs.twimg.com.evil/ext_tw_video_thumb/1/pu/img/spoof.jpg",
				"video_info": map[string]any{
					"duration_millis": 5000,
					"variants":        spoofedVariants,
				},
			},
		},
	})
	var ytCalled atomic.Bool
	allSpoofParser := NewWithFetcher(
		&stubExtractor{
			enabled: true,
			extract: func(context.Context, string) (*ytengine.Info, error) {
				ytCalled.Store(true)
				return nil, &ytengine.Error{Code: ytengine.CodeContentUnavailable, Message: "no video", Retryable: false}
			},
		},
		nil,
		fakeXFetcher{
			fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: http.StatusOK, Body: allSpoofBody, URL: req.URL}, nil
			},
		},
	)
	spoofEnv, err := allSpoofParser.Parse(context.Background(), tweetURL, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ytCalled.Load() {
		t.Fatal("expected fallback to yt-dlp when all syndication variants are forged")
	}
	encodedSpoof, _ := unified.Encode(spoofEnv)
	if strings.Contains(string(encodedSpoof), "evil") || strings.Contains(string(encodedSpoof), "8443") {
		t.Fatalf("forged host leaked into envelope: %s", string(encodedSpoof))
	}

	// 2. Forged variants + forged covers mixed with 1 valid video.twimg.com MP4 -> strips all forged URLs, keeps valid MP4 on fast-path.
	for _, badCover := range []string{
		"https://pbs.twimg.com.evil/thumb.jpg",
		"https://sub.pbs.twimg.com/thumb.jpg",
		"http://pbs.twimg.com/thumb.jpg",
		"https://pbs.twimg.com:8443/thumb.jpg",
		"https://user:pass@pbs.twimg.com/thumb.jpg",
		"https://video.twimg.com/thumb.jpg",
		"https://pbs.twimg.com/thumb.html",
	} {
		mixedVariants := append([]map[string]any{}, spoofedVariants...)
		mixedVariants = append(mixedVariants, map[string]any{
			"bitrate":      2176000,
			"content_type": "video/mp4",
			"url":          "https://video.twimg.com:443/ext_tw_video/1/pu/vid/1280x720/clean.mp4?tag=12",
		})
		mixedBody, _ := json.Marshal(map[string]any{
			"__typename": "Tweet",
			"text":       "Valid MP4 with forged siblings",
			"mediaDetails": []map[string]any{
				{
					"id_str":          "1",
					"type":            "video",
					"media_url_https": badCover,
					"video_info": map[string]any{
						"duration_millis": 5000,
						"variants":        mixedVariants,
					},
				},
			},
		})
		p := NewWithFetcher(
			&stubExtractor{
				enabled: true,
				extract: func(context.Context, string) (*ytengine.Info, error) {
					t.Fatalf("yt-dlp must not run when 1 valid allowlisted MP4 exists (badCover=%s)", badCover)
					return nil, nil
				},
			},
			nil,
			fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{StatusCode: http.StatusOK, Body: mixedBody, URL: req.URL}, nil
				},
			},
		)
		env, err := p.Parse(context.Background(), tweetURL, now)
		if err != nil {
			t.Fatal(err)
		}
		view := unified.PublicView(env, false, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("badCover=%q ValidateEnvelopeAt failed: %v", badCover, err)
		}
		data := unified.Map(view["data"])
		if data["cover"] != nil {
			t.Fatalf("forged cover %q leaked into data.cover: %+v", badCover, data["cover"])
		}
		item := unified.Map(unified.Slice(data["items"])[0])
		if item["cover"] != nil {
			t.Fatalf("forged cover %q leaked into item.cover: %+v", badCover, item["cover"])
		}
		variants := unified.Slice(item["variants"])
		if len(variants) != 1 {
			t.Fatalf("expected only 1 allowlisted variant to survive, got %d", len(variants))
		}
		if gotURL := unified.Str(unified.Map(item["default"])["url"]); gotURL != "https://video.twimg.com/ext_tw_video/1/pu/vid/1280x720/clean.mp4?tag=12" {
			t.Fatalf("unexpected clean default.url: %q", gotURL)
		}
	}
}

func TestSyndicationFastPathEquivalenceWithYTDLP(t *testing.T) {
	const now int64 = 1762300800

	type eqCase struct {
		name      string
		tweetID   string
		sourceURL string
		synBody   map[string]any
		ytInfo    ytengine.Info
	}

	tsSingle := float64(1715756260) // 2024-05-15T06:57:40Z
	likesSingle := int64(132404)
	commentsSingle := int64(827)
	viewsSingle := int64(34496944)
	repostsSingle := int64(6776)

	tsLong := float64(1664477766) // 2022-09-29T18:56:06Z
	likesLong := int64(10884)
	commentsLong := int64(305)

	tsMulti := float64(1664992565) // 2022-10-05T17:56:05Z
	likesMulti := int64(2092)
	commentsMulti := int64(33)

	tsMixed := float64(1665025050) // 2022-10-06T02:57:30Z
	likesMixed := int64(39)
	commentsMixed := int64(11)

	cases := []eqCase{
		{
			name:      "single-amplify-video-1790637656616943991",
			tweetID:   "1790637656616943991",
			sourceURL: "https://x.com/historyinmemes/status/1790637656616943991",
			synBody: map[string]any{
				"__typename":         "Tweet",
				"id_str":             "1790637656616943991",
				"text":               "One of the most intense moments in history https://t.co/Zgzhvix8ES",
				"created_at":         "2024-05-15T06:57:40.000Z",
				"favorite_count":     likesSingle,
				"conversation_count": commentsSingle,
				"user":               map[string]any{"name": "Historic Vids", "screen_name": "historyinmemes"},
				"mediaDetails": []map[string]any{
					{
						"type":            "video",
						"media_url_https": "https://pbs.twimg.com/amplify_video_thumb/1790637589910654976/img/jyhLQQZkq-lJJ9fN.jpg",
						"original_info":   map[string]any{"width": 886, "height": 876},
						"sizes":           map[string]any{"large": map[string]any{"w": 886, "h": 876}},
						"video_info": map[string]any{
							"duration_millis": 15488,
							"variants": []map[string]any{
								{"content_type": "application/x-mpegURL", "url": "https://video.twimg.com/amplify_video/1790637589910654976/pl/osiKl6ALz2B8l9Cw.m3u8?tag=14&v=cfc"},
								{"bitrate": 288000, "content_type": "video/mp4", "url": "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/272x270/du7PkQofwIDrjAEz.mp4?tag=14"},
								{"bitrate": 832000, "content_type": "video/mp4", "url": "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/364x360/MhZUmBoG84GGC0fb.mp4?tag=14"},
								{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/kF5AeH6MK-1qDlBg.mp4?tag=14"},
							},
						},
					},
				},
			},
			ytInfo: ytengine.Info{
				ID:           "1790637589910654976",
				DisplayID:    "1790637656616943991",
				Title:        "Historic Vids - One of the most intense moments in history",
				Description:  "One of the most intense moments in history https://t.co/Zgzhvix8ES",
				Uploader:     "Historic Vids",
				UploaderID:   "historyinmemes",
				UploaderURL:  "https://twitter.com/historyinmemes",
				Timestamp:    &tsSingle,
				Duration:     float64Ptr(15.488),
				LikeCount:    &likesSingle,
				CommentCount: &commentsSingle,
				ViewCount:    &viewsSingle,
				RepostCount:  &repostsSingle,
				Thumbnail:    "https://pbs.twimg.com/amplify_video_thumb/1790637589910654976/img/jyhLQQZkq-lJJ9fN.jpg?name=orig",
				Formats: []ytengine.Format{
					{FormatID: "hls-716", URL: "https://video.twimg.com/amplify_video/1790637589910654976/pl/avc1/728x720/hls.m3u8", Ext: "mp4", Protocol: "m3u8_native", Width: int64Ptr(728), Height: int64Ptr(720), TBR: float64Ptr(716.185), VCodec: "avc1.64001F", ACodec: "none"},
					{FormatID: "http-288", URL: "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/272x270/du7PkQofwIDrjAEz.mp4?tag=14", Ext: "mp4", Protocol: "https", Width: int64Ptr(272), Height: int64Ptr(270), TBR: float64Ptr(288.0)},
					{FormatID: "http-832", URL: "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/364x360/MhZUmBoG84GGC0fb.mp4?tag=14", Ext: "mp4", Protocol: "https", Width: int64Ptr(364), Height: int64Ptr(360), TBR: float64Ptr(832.0)},
					{FormatID: "http-2176", URL: "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/kF5AeH6MK-1qDlBg.mp4?tag=14", Ext: "mp4", Protocol: "https", Width: int64Ptr(728), Height: int64Ptr(720), TBR: float64Ptr(2176.0)},
				},
			},
		},
		{
			name:      "landscape-long-caption-tags-1575560063510810624",
			tweetID:   "1575560063510810624",
			sourceURL: "https://x.com/MesoMax919/status/1575560063510810624",
			synBody: map[string]any{
				"__typename":         "Tweet",
				"id_str":             "1575560063510810624",
				"text":               "Absolutely heartbreaking footage captured by our surge probe of catastrophic storm surge washing away homes. #HurricaneIan\n\nFULL VIDEO - https://t.co/DOJJn2VThV https://t.co/iPBUyVKw4s",
				"created_at":         "2022-09-29T18:56:06.000Z",
				"favorite_count":     likesLong,
				"conversation_count": commentsLong,
				"user":               map[string]any{"name": "Max Olson", "screen_name": "MesoMax919"},
				"entities":           map[string]any{"hashtags": []map[string]any{{"text": "HurricaneIan"}}},
				"mediaDetails": []map[string]any{
					{
						"id_str":          "1575559336759263233",
						"type":            "video",
						"media_url_https": "https://pbs.twimg.com/ext_tw_video_thumb/1575559336759263233/pu/img/Kvc2iofyjHrt1DmI.jpg",
						"original_info":   map[string]any{"width": 1280, "height": 720},
						"sizes":           map[string]any{"large": map[string]any{"w": 1280, "h": 720}},
						"video_info": map[string]any{
							"duration_millis": 21321,
							"variants": []map[string]any{
								{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/1575559336759263233/pu/vid/1280x720/hd.mp4?tag=12"},
								{"bitrate": 832000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/1575559336759263233/pu/vid/640x360/md.mp4?tag=12"},
								{"bitrate": 256000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/1575559336759263233/pu/vid/480x270/sd.mp4?tag=12"},
							},
						},
					},
				},
			},
			ytInfo: ytengine.Info{
				ID:           "1575559336759263233",
				DisplayID:    "1575560063510810624",
				Title:        "Max Olson - Absolutely heartbreaking footage captured by our surge probe of catas...",
				Description:  "Absolutely heartbreaking footage captured by our surge probe of catastrophic storm surge washing away homes. #HurricaneIan  FULL VIDEO - https://t.co/DOJJn2VThV https://t.co/iPBUyVKw4s",
				Uploader:     "Max Olson",
				UploaderID:   "MesoMax919",
				UploaderURL:  "https://twitter.com/MesoMax919",
				Timestamp:    &tsLong,
				Duration:     float64Ptr(21.321),
				LikeCount:    &likesLong,
				CommentCount: &commentsLong,
				Tags:         []string{"HurricaneIan"},
				Thumbnail:    "https://pbs.twimg.com/ext_tw_video_thumb/1575559336759263233/pu/img/Kvc2iofyjHrt1DmI.jpg?name=orig",
				Formats: []ytengine.Format{
					{FormatID: "http-2176", URL: "https://video.twimg.com/ext_tw_video/1575559336759263233/pu/vid/1280x720/hd.mp4?tag=12", Ext: "mp4", Protocol: "https", Width: int64Ptr(1280), Height: int64Ptr(720), TBR: float64Ptr(2176.0)},
					{FormatID: "http-832", URL: "https://video.twimg.com/ext_tw_video/1575559336759263233/pu/vid/640x360/md.mp4?tag=12", Ext: "mp4", Protocol: "https", Width: int64Ptr(640), Height: int64Ptr(360), TBR: float64Ptr(832.0)},
					{FormatID: "http-256", URL: "https://video.twimg.com/ext_tw_video/1575559336759263233/pu/vid/480x270/sd.mp4?tag=12", Ext: "mp4", Protocol: "https", Width: int64Ptr(480), Height: int64Ptr(270), TBR: float64Ptr(256.0)},
				},
			},
		},
		{
			name:      "multi-video-playlist-with-video-2-selection",
			tweetID:   "1577719286659006464",
			sourceURL: "https://x.com/UltimaShadowX/status/1577719286659006464/video/2",
			synBody: map[string]any{
				"__typename":         "Tweet",
				"id_str":             "1577719286659006464",
				"text":               "Test https://t.co/Y3KEZD7Dad",
				"created_at":         "2022-10-05T17:56:05.000Z",
				"favorite_count":     likesMulti,
				"conversation_count": commentsMulti,
				"user":               map[string]any{"name": "Ultima", "screen_name": "UltimaShadowX"},
				"mediaDetails": []map[string]any{
					{
						"id_str":          "1577719236209922048",
						"type":            "video",
						"media_url_https": "https://pbs.twimg.com/ext_tw_video_thumb/1577719236209922048/pu/img/_3it9Ld7aLBKLEOF.jpg",
						"sizes":           map[string]any{"large": map[string]any{"w": 1280, "h": 720}},
						"video_info": map[string]any{
							"duration_millis": 4266,
							"variants": []map[string]any{
								{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/1577719236209922048/pu/vid/1280x720/v1.mp4?tag=12"},
							},
						},
					},
					{
						"id_str":          "1577719252450267136",
						"type":            "video",
						"media_url_https": "https://pbs.twimg.com/ext_tw_video_thumb/1577719252450267136/pu/img/thumb2.jpg",
						"sizes":           map[string]any{"large": map[string]any{"w": 1280, "h": 720}},
						"video_info": map[string]any{
							"duration_millis": 2100,
							"variants": []map[string]any{
								{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/1577719252450267136/pu/vid/1280x720/v2.mp4?tag=12"},
							},
						},
					},
				},
			},
			ytInfo: ytengine.Info{
				ID:           "1577719286659006464",
				DisplayID:    "1577719286659006464",
				Type:         "playlist",
				Title:        "Ultima - Test",
				Description:  "Test https://t.co/Y3KEZD7Dad",
				Uploader:     "Ultima",
				UploaderID:   "UltimaShadowX",
				UploaderURL:  "https://twitter.com/UltimaShadowX",
				Timestamp:    &tsMulti,
				LikeCount:    &likesMulti,
				CommentCount: &commentsMulti,
				Entries: []ytengine.Info{
					{
						ID:        "1577719236209922048",
						DisplayID: "1577719286659006464",
						Title:     "Ultima - Test #1",
						Duration:  float64Ptr(4.266),
						Thumbnail: "https://pbs.twimg.com/ext_tw_video_thumb/1577719236209922048/pu/img/_3it9Ld7aLBKLEOF.jpg?name=orig",
						Formats: []ytengine.Format{
							{FormatID: "http-2176", URL: "https://video.twimg.com/ext_tw_video/1577719236209922048/pu/vid/1280x720/v1.mp4?tag=12", Ext: "mp4", Protocol: "https", Width: int64Ptr(1280), Height: int64Ptr(720), TBR: float64Ptr(2176.0)},
						},
					},
					{
						ID:        "1577719252450267136",
						DisplayID: "1577719286659006464",
						Title:     "Ultima - Test #2",
						Duration:  float64Ptr(2.100),
						Thumbnail: "https://pbs.twimg.com/ext_tw_video_thumb/1577719252450267136/pu/img/thumb2.jpg?name=orig",
						Formats: []ytengine.Format{
							{FormatID: "http-2176", URL: "https://video.twimg.com/ext_tw_video/1577719252450267136/pu/vid/1280x720/v2.mp4?tag=12", Ext: "mp4", Protocol: "https", Width: int64Ptr(1280), Height: int64Ptr(720), TBR: float64Ptr(2176.0)},
						},
					},
				},
			},
		},
		{
			name:      "mixed-vertical-video-and-photo-1577855540407197696",
			tweetID:   "1577855540407197696",
			sourceURL: "https://x.com/oshtru/status/1577855540407197696",
			synBody: map[string]any{
				"__typename":         "Tweet",
				"id_str":             "1577855540407197696",
				"text":               "gm ✨️ now I can post image and video. nice update. https://t.co/cG7XgiINOm",
				"created_at":         "2022-10-06T02:57:30.000Z",
				"favorite_count":     likesMixed,
				"conversation_count": commentsMixed,
				"user":               map[string]any{"name": "Oshtru", "screen_name": "oshtru"},
				"photos": []map[string]any{
					{"url": "https://pbs.twimg.com/media/FeWrKrMaYAELAxK.jpg", "width": 1536, "height": 1920},
				},
				"mediaDetails": []map[string]any{
					{
						"type":            "photo",
						"media_url_https": "https://pbs.twimg.com/media/FeWrKrMaYAELAxK.jpg",
						"original_info":   map[string]any{"width": 1536, "height": 1920},
					},
					{
						"id_str":          "1577855447914409984",
						"type":            "video",
						"media_url_https": "https://pbs.twimg.com/ext_tw_video_thumb/1577855447914409984/pu/img/5V20pjPHBu3nz6jb.jpg",
						"sizes":           map[string]any{"large": map[string]any{"w": 720, "h": 900}},
						"video_info": map[string]any{
							"duration_millis": 30030,
							"variants": []map[string]any{
								{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/1577855447914409984/pu/vid/720x900/Dn3XhZkvCK_gMg0d.mp4?tag=12"},
								{"bitrate": 950000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/1577855447914409984/pu/vid/480x600/7-QYdEW4oBKK4vSr.mp4?tag=12"},
								{"bitrate": 632000, "content_type": "video/mp4", "url": "https://video.twimg.com/ext_tw_video/1577855447914409984/pu/vid/320x400/HYijkv-UtUL9d8qo.mp4?tag=12"},
							},
						},
					},
				},
			},
			ytInfo: ytengine.Info{
				ID:           "1577855447914409984",
				DisplayID:    "1577855540407197696",
				Title:        "Oshtru - gm ✨️ now I can post image and video. nice update.",
				Description:  "gm ✨️ now I can post image and video. nice update. https://t.co/cG7XgiINOm",
				Uploader:     "Oshtru",
				UploaderID:   "oshtru",
				UploaderURL:  "https://twitter.com/oshtru",
				Timestamp:    &tsMixed,
				Duration:     float64Ptr(30.030),
				LikeCount:    &likesMixed,
				CommentCount: &commentsMixed,
				Thumbnail:    "https://pbs.twimg.com/ext_tw_video_thumb/1577855447914409984/pu/img/5V20pjPHBu3nz6jb.jpg?name=orig",
				Formats: []ytengine.Format{
					{FormatID: "http-2176", URL: "https://video.twimg.com/ext_tw_video/1577855447914409984/pu/vid/720x900/Dn3XhZkvCK_gMg0d.mp4?tag=12", Ext: "mp4", Protocol: "https", Width: int64Ptr(720), Height: int64Ptr(900), TBR: float64Ptr(2176.0)},
					{FormatID: "http-950", URL: "https://video.twimg.com/ext_tw_video/1577855447914409984/pu/vid/480x600/7-QYdEW4oBKK4vSr.mp4?tag=12", Ext: "mp4", Protocol: "https", Width: int64Ptr(480), Height: int64Ptr(600), TBR: float64Ptr(950.0)},
					{FormatID: "http-632", URL: "https://video.twimg.com/ext_tw_video/1577855447914409984/pu/vid/320x400/HYijkv-UtUL9d8qo.mp4?tag=12", Ext: "mp4", Protocol: "https", Width: int64Ptr(320), Height: int64Ptr(400), TBR: float64Ptr(632.0)},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synBytes, err := json.Marshal(tc.synBody)
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, selectedIdx, ok := extractTweetTarget(tc.sourceURL)
			if !ok {
				t.Fatalf("invalid sourceURL %q", tc.sourceURL)
			}

			// Path A: Syndication fast-path (must NOT invoke yt-dlp).
			fastParser := NewWithFetcher(
				&stubExtractor{
					enabled: true,
					extract: func(context.Context, string) (*ytengine.Info, error) {
						t.Fatalf("yt-dlp must not be invoked for %s", tc.name)
						return nil, nil
					},
				},
				nil,
				fakeXFetcher{
					fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
						return fetch.Response{StatusCode: http.StatusOK, Body: synBytes, URL: req.URL}, nil
					},
				},
			)
			synEnvRaw, err := fastParser.Parse(context.Background(), tc.sourceURL, now)
			if err != nil {
				t.Fatalf("fast-path Parse failed: %v", err)
			}
			synView := unified.PublicView(synEnvRaw, false, now)
			if err := unified.ValidateEnvelopeAt(synView, now); err != nil {
				t.Fatalf("fast-path ValidateEnvelopeAt failed: %v", err)
			}

			// Path B: Existing yt-dlp AdaptInfo path (+ syndication photo supplement for mixed tweets).
			st, err := fetchSyndicationTweet(context.Background(), fakeXFetcher{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					return fetch.Response{StatusCode: http.StatusOK, Body: synBytes, URL: req.URL}, nil
				},
			}, tc.tweetID)
			if err != nil {
				t.Fatal(err)
			}
			ytCopy := tc.ytInfo
			var ytEnvRaw unified.Object
			if selectedIdx == 0 && len(st.Photos) > 0 {
				ytEnvRaw = adaptInfoWithPhotos(&ytCopy, st.Photos, tc.tweetID, tc.sourceURL, selectedIdx, now)
			} else {
				ytEnvRaw = AdaptInfo(&ytCopy, tc.tweetID, tc.sourceURL, selectedIdx, now)
			}
			ytView := unified.PublicView(ytEnvRaw, false, now)
			if err := unified.ValidateEnvelopeAt(ytView, now); err != nil {
				t.Fatalf("yt-dlp ValidateEnvelopeAt failed: %v", err)
			}

			synData := unified.Map(synView["data"])
			ytData := unified.Map(ytView["data"])
			for _, field := range []string{"platform", "content_id", "source_url", "refresh_url", "title", "description", "published_at", "default_item_id", "author", "cover", "tags", "groups"} {
				if !reflect.DeepEqual(synData[field], ytData[field]) {
					t.Fatalf("%s data.%s mismatch:\n  syn=%#v\n   yt=%#v", tc.name, field, synData[field], ytData[field])
				}
			}
			synStats := unified.Map(synData["statistics"])
			ytStats := unified.Map(ytData["statistics"])
			for _, statKey := range []string{"likes", "comments"} {
				if !reflect.DeepEqual(synStats[statKey], ytStats[statKey]) {
					t.Fatalf("%s statistics.%s mismatch: syn=%#v yt=%#v", tc.name, statKey, synStats[statKey], ytStats[statKey])
				}
			}

			synItems := unified.Slice(synData["items"])
			ytItems := unified.Slice(ytData["items"])
			if len(synItems) != len(ytItems) {
				t.Fatalf("%s items count mismatch: syn=%d yt=%d", tc.name, len(synItems), len(ytItems))
			}
			for i := range synItems {
				si := unified.Map(synItems[i])
				yi := unified.Map(ytItems[i])
				for _, ik := range []string{"id", "type", "index", "title", "duration_ms", "cover", "default", "resolve_url"} {
					if !reflect.DeepEqual(si[ik], yi[ik]) {
						t.Fatalf("%s item[%d].%s mismatch:\n  syn=%#v\n   yt=%#v", tc.name, i, ik, si[ik], yi[ik])
					}
				}
				sVars := unified.Slice(si["variants"])
				var yFileVars []unified.Object
				for _, yv := range unified.Slice(yi["variants"]) {
					yvm := unified.Map(yv)
					if unified.Str(unified.Map(yvm["delivery"])["kind"]) == "file" {
						yFileVars = append(yFileVars, yvm)
					}
				}
				if len(sVars) != len(yFileVars) {
					t.Fatalf("%s item[%d] progressive file variants mismatch: syn=%d yt=%d", tc.name, i, len(sVars), len(yFileVars))
				}
				for vi := range sVars {
					svm := unified.Map(sVars[vi])
					yvm := yFileVars[vi]
					if !reflect.DeepEqual(svm["id"], yvm["id"]) || !reflect.DeepEqual(svm["label"], yvm["label"]) || !reflect.DeepEqual(svm["delivery"], yvm["delivery"]) {
						t.Fatalf("%s item[%d] variant[%d] id/label/delivery mismatch:\n  syn=%#v\n   yt=%#v", tc.name, i, vi, svm, yvm)
					}
					sMeta := unified.Map(svm["metadata"])
					yMeta := unified.Map(yvm["metadata"])
					for _, mk := range []string{"container", "extension", "mime_type", "width", "height", "bitrate_bps", "has_audio"} {
						if !reflect.DeepEqual(sMeta[mk], yMeta[mk]) {
							t.Fatalf("%s item[%d] variant[%d].metadata.%s mismatch: syn=%#v yt=%#v", tc.name, i, vi, mk, sMeta[mk], yMeta[mk])
						}
					}
				}
			}
		})
	}
}

type countingExtractor struct {
	inner ytengine.Extractor
	calls atomic.Int64
}

func (c *countingExtractor) Enabled() bool { return c.inner != nil && c.inner.Enabled() }

func (c *countingExtractor) Extract(ctx context.Context, rawURL string) (*ytengine.Info, error) {
	c.calls.Add(1)
	return c.inner.Extract(ctx, rawURL)
}

func setupLocalFakeXBenchmarkHarness(tb testing.TB) (fastParser *Parser, legacyParser *Parser, fastCounter *countingExtractor, legacyCounter *countingExtractor, cleanup func()) {
	tb.Helper()
	if runtime.GOOS == "windows" {
		tb.Skip("shell script fake yt-dlp fixture requires unix")
	}

	const tweetID = "1790637656616943991"
	completeSynBody, err := json.Marshal(map[string]any{
		"__typename":         "Tweet",
		"id_str":             tweetID,
		"text":               "One of the most intense moments in history https://t.co/Zgzhvix8ES",
		"created_at":         "2024-05-15T06:57:40.000Z",
		"favorite_count":     132404,
		"conversation_count": 827,
		"user":               map[string]any{"name": "Historic Vids", "screen_name": "historyinmemes"},
		"mediaDetails": []map[string]any{
			{
				"type":            "video",
				"media_url_https": "https://pbs.twimg.com/amplify_video_thumb/1790637589910654976/img/jyhLQQZkq-lJJ9fN.jpg",
				"original_info":   map[string]any{"width": 886, "height": 876},
				"sizes":           map[string]any{"large": map[string]any{"w": 886, "h": 876}},
				"video_info": map[string]any{
					"duration_millis": 15488,
					"variants": []map[string]any{
						{"content_type": "application/x-mpegURL", "url": "https://video.twimg.com/amplify_video/1790637589910654976/pl/osiKl6ALz2B8l9Cw.m3u8?tag=14&v=cfc"},
						{"bitrate": 288000, "content_type": "video/mp4", "url": "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/272x270/du7PkQofwIDrjAEz.mp4?tag=14"},
						{"bitrate": 832000, "content_type": "video/mp4", "url": "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/364x360/MhZUmBoG84GGC0fb.mp4?tag=14"},
						{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/kF5AeH6MK-1qDlBg.mp4?tag=14"},
					},
				},
			},
		},
	})
	if err != nil {
		tb.Fatal(err)
	}

	legacyPhotoOnlySynBody, err := json.Marshal(map[string]any{
		"__typename": "Tweet",
		"id_str":     tweetID,
		"text":       "One of the most intense moments in history https://t.co/Zgzhvix8ES",
		"mediaDetails": []map[string]any{
			{"type": "video", "media_url_https": "https://pbs.twimg.com/amplify_video_thumb/1790637589910654976/img/jyhLQQZkq-lJJ9fN.jpg"},
		},
	})
	if err != nil {
		tb.Fatal(err)
	}

	synSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/legacy" {
			_, _ = w.Write(legacyPhotoOnlySynBody)
			return
		}
		_, _ = w.Write(completeSynBody)
	}))

	dir := tb.TempDir()
	binPath := filepath.Join(dir, "yt-dlp-fake")
	script := "#!/bin/sh\nset -eu\ncat <<'JSON'\n" +
		`{"id":"1790637589910654976","display_id":"1790637656616943991","_type":"video","title":"Historic Vids - One of the most intense moments in history","description":"One of the most intense moments in history https://t.co/Zgzhvix8ES","uploader":"Historic Vids","uploader_id":"historyinmemes","uploader_url":"https://twitter.com/historyinmemes","timestamp":1715756260,"duration":15.488,"like_count":132404,"comment_count":827,"thumbnail":"https://pbs.twimg.com/amplify_video_thumb/1790637589910654976/img/jyhLQQZkq-lJJ9fN.jpg?name=orig","formats":[{"format_id":"http-288","url":"https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/272x270/du7PkQofwIDrjAEz.mp4?tag=14","ext":"mp4","protocol":"https","width":272,"height":270,"tbr":288.0},{"format_id":"http-832","url":"https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/364x360/MhZUmBoG84GGC0fb.mp4?tag=14","ext":"mp4","protocol":"https","width":364,"height":360,"tbr":832.0},{"format_id":"http-2176","url":"https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/kF5AeH6MK-1qDlBg.mp4?tag=14","ext":"mp4","protocol":"https","width":728,"height":720,"tbr":2176.0}]}` +
		"\nJSON\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		synSrv.Close()
		tb.Fatal(err)
	}
	sum := sha256.Sum256([]byte(script))
	digest := hex.EncodeToString(sum[:])

	realEng, err := ytengine.New(ytengine.Config{
		BinaryPath:     binPath,
		AllowedHashes:  map[string]string{digest: "yt-dlp-fake"},
		AllowedURL:     AllowedTargetURL,
		MaxConcurrency: 4,
	})
	if err != nil {
		synSrv.Close()
		tb.Fatalf("ytengine.New failed: %v", err)
	}

	httpClient := synSrv.Client()
	makeLoopbackFetcher := func(targetPath string) fetch.Executor {
		return fakeXFetcher{
			fn: func(reqCtx context.Context, req fetch.Request) (fetch.Response, error) {
				if req.Policy.AllowedURL != nil && !req.Policy.AllowedURL(req.URL) {
					return fetch.Response{}, fetch.ErrPolicy
				}
				u, err := url.Parse(req.URL)
				if err != nil {
					return fetch.Response{}, err
				}
				localReq, err := http.NewRequestWithContext(reqCtx, req.Method, synSrv.URL+targetPath+"?"+u.RawQuery, nil)
				if err != nil {
					return fetch.Response{}, err
				}
				for k, vals := range req.Header {
					for _, v := range vals {
						localReq.Header.Add(k, v)
					}
				}
				resp, err := httpClient.Do(localReq)
				if err != nil {
					return fetch.Response{}, err
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(io.LimitReader(resp.Body, req.Policy.MaxBytes+1))
				if err != nil {
					return fetch.Response{}, err
				}
				return fetch.Response{
					StatusCode: resp.StatusCode,
					Header:     resp.Header.Clone(),
					Body:       body,
					URL:        req.URL,
				}, nil
			},
		}
	}

	fastCounter = &countingExtractor{inner: realEng}
	legacyCounter = &countingExtractor{inner: realEng}
	fastParser = NewWithFetcher(fastCounter, nil, makeLoopbackFetcher("/tweet-result"))
	legacyParser = NewWithFetcher(legacyCounter, nil, makeLoopbackFetcher("/legacy"))
	return fastParser, legacyParser, fastCounter, legacyCounter, synSrv.Close
}

func BenchmarkXVideoSyndicationFastPathVsYTDLP(b *testing.B) {
	const now int64 = 1762300800
	const targetURL = "https://x.com/historyinmemes/status/1790637656616943991"

	fastParser, legacyParser, fastCounter, legacyCounter, cleanup := setupLocalFakeXBenchmarkHarness(b)
	defer cleanup()

	b.Run("SyndicationFastPath", func(b *testing.B) {
		fastCounter.calls.Store(0)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			env, err := fastParser.Parse(context.Background(), targetURL, now)
			if err != nil || unified.Str(env["code"]) != "OK" {
				b.Fatalf("fast-path Parse failed: env=%+v err=%v", env, err)
			}
		}
		b.StopTimer()
		if got := fastCounter.calls.Load(); got != 0 {
			b.Fatalf("expected 0 yt-dlp subprocess spawns on SyndicationFastPath, got %d", got)
		}
	})

	b.Run("LegacyYTDLPSubprocessPath", func(b *testing.B) {
		legacyCounter.calls.Store(0)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			env, err := legacyParser.Parse(context.Background(), targetURL, now)
			if err != nil || unified.Str(env["code"]) != "OK" {
				b.Fatalf("legacy Parse failed: env=%+v err=%v", env, err)
			}
		}
		b.StopTimer()
		if got := legacyCounter.calls.Load(); got != int64(b.N) {
			b.Fatalf("expected %d yt-dlp subprocess spawns on LegacyYTDLPSubprocessPath, got %d", b.N, got)
		}
	})
}
