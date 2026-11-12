package douyin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type mockWorkResolver struct {
	workFn func(context.Context, string) (*ShanghaiWork, error)
	origFn func(context.Context, string) ([]unified.Object, error)
}

func (m mockWorkResolver) ResolveWork(ctx context.Context, awemeID string) (*ShanghaiWork, error) {
	if m.workFn == nil {
		return nil, ErrProtocolV1
	}
	return m.workFn(ctx, awemeID)
}

func (m mockWorkResolver) ResolveOriginal(ctx context.Context, vid string) ([]unified.Object, error) {
	if m.origFn == nil {
		return nil, errors.New("unavailable")
	}
	return m.origFn(ctx, vid)
}

func TestShanghaiAllRenditionsPrependDomesticAndEnforceConstraints(t *testing.T) {
	const (
		workID   = "7704056737233449610"
		vid      = "v0200fg10000dbl4sn7og65ijmfp8hm0"
		origKey  = "/video/tos/cn/tos-cn-v-0015c001-alinc2/oOrig4K"
		key4K    = "/video/tos/cn/tos-cn-v-0015c001-alinc2/oTranscode4K"
		key1080  = "/video/tos/cn/tos-cn-v-0015c001-alinc2/oNormal1080"
		key720   = "/video/tos/cn/tos-cn-v-0015c001-alinc2/oNormal720"
		keyDash  = "/video/tos/cn/tos-cn-v-0015c001-alinc2/oDash1080Track"
		keyExtra = "/video/tos/cn/tos-cn-v-0015c001-alinc2/oShanghaiOnlyExtra"
	)
	ovURL := func(host, sig, objKey string) string {
		return "https://" + host + "/" + strings.Repeat(sig, 32) + "/6a000001" + objKey + "/?a=6383&mime_type=video_mp4"
	}
	webRedirect := "https://www.douyin.com/aweme/v1/play/?video_id=" + vid + "&line=0"

	detail := object{
		"aweme_id": workID,
		"desc":     "phase2 test",
		"video": object{
			"duration":        81067,
			"cdn_url_expired": 1800003600,
			"play_addr":       object{"uri": vid},
			"bit_rate": array{
				object{
					"gear_name": "adapt_lowest_4_1",
					"format":    "mp4",
					"bit_rate":  3127000,
					"is_h265":   1,
					"play_addr": object{
						"uri":       vid,
						"file_hash": "hash4k",
						"width":     3840,
						"height":    2160,
						"data_size": 31680000,
						"url_list":  array{ovURL("v3-dy-ov.zjcdn.com", "1", key4K), webRedirect},
					},
				},
				object{
					"gear_name": "normal_1080_0",
					"format":    "mp4",
					"bit_rate":  2107000,
					"is_h265":   0,
					"play_addr": object{
						"uri":       vid,
						"file_hash": "hash1080",
						"width":     1920,
						"height":    1080,
						"data_size": 21353927,
						"url_list":  array{ovURL("v3-dy-ov.zjcdn.com", "2", key1080), ovURL("v9-dy-ov.zjcdn.com", "3", key1080), webRedirect},
					},
				},
				object{
					"gear_name": "normal_720_0",
					"format":    "mp4",
					"bit_rate":  1613000,
					"is_h265":   0,
					"play_addr": object{
						"uri":       vid,
						"file_hash": "hash720",
						"width":     1280,
						"height":    720,
						"data_size": 16350000,
						"url_list":  array{ovURL("v3-dy-ov.zjcdn.com", "4", key720), webRedirect},
					},
				},
				object{
					"gear_name":   "normal_1080_0",
					"format":      "dash",
					"bit_rate":    2042000,
					"is_h265":     0,
					"video_extra": `{"format":"dash","audio_file_id":"aud1"}`,
					"play_addr": object{
						"uri":       vid,
						"file_hash": "hashDash1080",
						"width":     1920,
						"height":    1080,
						"data_size": 20690000,
						"url_list":  array{ovURL("v3-dy-ov.zjcdn.com", "5", keyDash)},
					},
				},
			},
			"bit_rate_audio": array{
				object{
					"audio_meta": object{
						"file_id":    "aud1",
						"media_type": "audio",
						"bitrate":    61728,
						"size":       625332,
						"url_list":   array{"https://v3-dy-ov.zjcdn.com/" + strings.Repeat("6", 32) + "/6a000001/video/tos/cn/tos-cn-v-0015/oAudio1/?a=6383"},
					},
				},
			},
		},
	}

	// Capture Japan-only baseline variant count and IDs (without 4K original).
	baseParser := NewWithOriginalResolver(nil, nil, originalResolverFunc(func(context.Context, string) ([]unified.Object, error) {
		return nil, nil
	}))
	baseRaw, err := baseParser.formatData(context.Background(), detail, workID)
	if err != nil {
		t.Fatal(err)
	}
	baseView := unified.PublicView(adapt(baseRaw, "https://www.douyin.com/jingxuan?modal_id="+workID, 1800000000), false, 1800000000)
	baseVariants := unified.Slice(child(first(child(baseView, "data", "items")), "variants"))

	// Shanghai returns raw v26-web (must be rewritten to v26-luna), v11-weba,
	// plus disallowed/mismatched entries that MUST be dropped:
	// - zjcdn.com URL (Constraint A: drop non-douyinvod.com even though AllowedMediaURL allows zjcdn.com)
	// - www.douyin.com URL (not in AllowedMediaURL)
	// - unapproved host URL (not in AllowedMediaURL)
	// - mismatched DouyinStoragePath URL under key1080
	// - keyExtra object not present in Japan's detail (Constraint B: no new qualities)
	// - keyDash pure video_track object (Constraint B: lr=default tracks keep existing rules)
	sh4kV26Web := ovURL("v26-web.douyinvod.com", "a", key4K)
	sh4kLuna := ovURL("v26-luna.douyinvod.com", "a", key4K)
	sh1080V26Web := ovURL("v26-web.douyinvod.com", "b", key1080)
	sh1080Luna := ovURL("v26-luna.douyinvod.com", "b", key1080)
	sh1080Weba := ovURL("v11-weba.douyinvod.com", "c", key1080)
	sh720V26Web := ovURL("v26-web.douyinvod.com", "d", key720)
	sh720Luna := ovURL("v26-luna.douyinvod.com", "d", key720)
	sh720Weba := ovURL("v11-weba.douyinvod.com", "e", key720)

	origCandidate := object{
		"url":           ovURL("v26-cold.douyinvod.com", "f", origKey),
		"metadata":      object{"metadata_source": "mp4", "width": json.Number("3840"), "height": json.Number("2160"), "size": json.Number("394316272"), "bit_rate": json.Number("38912000"), "duration": json.Number("81.067"), "codec": "hevc", "has_audio": true},
		"object_key":    origKey,
		"source_key":    "v26-cold.douyinvod.com",
		"prefix_sha256": strings.Repeat("9", 64),
	}

	probes := 0
	japanProber := proberFunc(func(context.Context, string) (object, error) {
		probes++
		return nil, nil
	})
	resolver := mockWorkResolver{
		workFn: func(_ context.Context, gotID string) (*ShanghaiWork, error) {
			if gotID != workID {
				t.Fatalf("unexpected aweme_id %q", gotID)
			}
			return &ShanghaiWork{
				VideoID:    vid,
				Candidates: []unified.Object{origCandidate},
				Renditions: map[string]array{
					key4K: {object{"url": sh4kV26Web}},
					key1080: {
						object{"url": "https://evil.example.com/bad" + key1080 + "/?v26-web=1"},
						object{"url": webRedirect},
						object{"url": ovURL("v3-dy-o.zjcdn.com", "7", key1080)},
						object{"url": ovURL("v11-weba.douyinvod.com", "8", key720)}, // wrong DouyinStoragePath for key1080!
						object{"url": sh1080V26Web},
						object{"url": sh1080Weba},
					},
					key720:   {object{"url": sh720V26Web}, object{"url": sh720Weba}},
					keyDash:  {object{"url": ovURL("v11-weba.douyinvod.com", "9", keyDash)}},
					keyExtra: {object{"url": ovURL("v11-weba.douyinvod.com", "0", keyExtra)}},
				},
			}, nil
		},
	}

	parser := NewWithOriginalResolver(nil, japanProber, resolver)
	raw, err := parser.formatData(context.Background(), detail, workID)
	if err != nil {
		t.Fatal(err)
	}
	view := unified.PublicView(adapt(raw, "https://www.douyin.com/jingxuan?modal_id="+workID, 1800000000), false, 1800000000)
	if err := unified.ValidateEnvelopeAt(view, 1800000000); err != nil {
		t.Fatal(err)
	}
	if probes != 0 {
		t.Fatalf("expected 0 Japan probes when Shanghai work succeeded, got %d", probes)
	}

	variants := unified.Slice(child(first(child(view, "data", "items")), "variants"))
	// Exactly 1 original_endpoint + len(baseVariants) declared variants (no extra qualities added!).
	if len(variants) != 1+len(baseVariants) {
		t.Fatalf("unexpected variant count: got %d want %d", len(variants), 1+len(baseVariants))
	}

	encodedView, _ := json.Marshal(view)
	for _, forbidden := range []string{"evil.example.com", "v26-web.douyinvod.com", "v3-dy-o.zjcdn.com", keyExtra} {
		if strings.Contains(string(encodedView), forbidden) {
			t.Fatalf("forbidden or un-rewritten URL %q leaked into public envelope", forbidden)
		}
	}

	findFileVariantByHeight := func(h int64) object {
		for _, v := range variants {
			vm := unified.Map(v)
			if vm["provenance"] == "original_endpoint" || child(vm, "delivery", "kind") != "file" {
				continue
			}
			gotH, _ := unified.PHPInt(child(vm, "metadata", "height"))
			if gotH == h {
				return vm
			}
		}
		t.Fatalf("missing file variant with height=%d", h)
		return nil
	}

	v4k := findFileVariantByHeight(2160)
	s4k := unified.Slice(child(v4k, "delivery", "sources"))
	if child(s4k[0], "url") != sh4kLuna {
		t.Fatalf("2160p transcode sources[0] = %v, want %s", child(s4k[0], "url"), sh4kLuna)
	}

	v1080 := findFileVariantByHeight(1080)
	s1080 := unified.Slice(child(v1080, "delivery", "sources"))
	if len(s1080) != 5 { // 2 domestic (v26-luna, v11-weba) + 3 Japan declared (2 zjcdn + www.douyin.com)
		t.Fatalf("1080p sources count = %d, want 5: %#v", len(s1080), s1080)
	}
	if child(s1080[0], "url") != sh1080Luna || child(s1080[1], "url") != sh1080Weba {
		t.Fatalf("1080p leading domestic sources wrong: got [%v, %v]", child(s1080[0], "url"), child(s1080[1], "url"))
	}
	if !strings.Contains(unified.Str(child(s1080[2], "url")), "v3-dy-ov.zjcdn.com") {
		t.Fatalf("1080p Japan overseas backup was not preserved behind domestic sources: %v", s1080[2])
	}

	v720 := findFileVariantByHeight(720)
	s720 := unified.Slice(child(v720, "delivery", "sources"))
	if child(s720[0], "url") != sh720Luna || child(s720[1], "url") != sh720Weba {
		t.Fatalf("720p leading domestic sources wrong: got [%v, %v]", child(s720[0], "url"), child(s720[1], "url"))
	}
}

func TestShanghaiParallelParseAlbumCancelAndFallbacks(t *testing.T) {
	const (
		workID  = "7704056737233449610"
		vid     = "v0200fg10000dbl4sn7og65ijmfp8hm0"
		key1080 = "/video/tos/cn/tos-cn-v-0015/o1080"
		ov1080  = "https://v3-dy-ov.zjcdn.com/11111111111111111111111111111111/6a000001" + key1080 + "/?a=6383&mime_type=video_mp4"
		sh1080  = "https://v26-web.douyinvod.com/22222222222222222222222222222222/6a000001" + key1080 + "/?a=6383&mime_type=video_mp4"
	)
	videoDetailJSON := `{"status_code":0,"aweme_detail":{"aweme_id":"` + workID + `","video":{"duration":10000,"play_addr":{"uri":"` + vid + `"},"bit_rate":[{"gear_name":"normal_1080_0","format":"mp4","bit_rate":2000000,"play_addr":{"uri":"` + vid + `","file_hash":"h1","width":1920,"height":1080,"data_size":2500000,"url_list":["` + ov1080 + `"]}}]}}}`

	// 1. Deterministic barrier proving Japan fetchDetail and Shanghai ResolveWork run concurrently.
	detailEntered := make(chan struct{})
	shanghaiEntered := make(chan struct{})
	exec := executorFunc(func(_ context.Context, req fetch.Request) (fetch.Response, error) {
		close(detailEntered)
		select {
		case <-shanghaiEntered:
		case <-time.After(2 * time.Second):
			t.Error("fetchDetail and ResolveWork did not overlap in parallel")
		}
		return fetch.Response{StatusCode: 200, Body: []byte(videoDetailJSON)}, nil
	})
	parallelResolver := mockWorkResolver{
		workFn: func(_ context.Context, id string) (*ShanghaiWork, error) {
			close(shanghaiEntered)
			select {
			case <-detailEntered:
			case <-time.After(2 * time.Second):
				t.Error("ResolveWork did not overlap with fetchDetail")
			}
			return &ShanghaiWork{
				VideoID:    vid,
				Renditions: map[string]array{key1080: {object{"url": sh1080}}},
			}, nil
		},
	}
	data, err := NewWithOriginalResolver(exec, nil, parallelResolver).Parse(context.Background(), "https://www.douyin.com/jingxuan?modal_id="+workID, 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	defURL := unified.Str(child(first(child(data, "data", "items")), "default", "url"))
	if !strings.HasPrefix(defURL, "https://v26-luna.douyinvod.com/") {
		t.Fatalf("parallel parse did not select rewritten v26-luna domestic URL: %s", defURL)
	}

	// 2. Album / live-photo immediately cancels Shanghai context without waiting.
	albumJSON := `{"status_code":0,"aweme_detail":{"aweme_id":"` + workID + `","images":[{"url_list":["https://p3-sign.douyinpic.com/img1.jpeg"]}]}}`
	shCancelled := make(chan struct{})
	albumExec := executorFunc(func(context.Context, fetch.Request) (fetch.Response, error) {
		return fetch.Response{StatusCode: 200, Body: []byte(albumJSON)}, nil
	})
	albumResolver := mockWorkResolver{
		workFn: func(ctx context.Context, _ string) (*ShanghaiWork, error) {
			<-ctx.Done()
			close(shCancelled)
			return nil, ctx.Err()
		},
	}
	albumData, err := NewWithOriginalResolver(albumExec, nil, albumResolver).Parse(context.Background(), "https://www.douyin.com/note/"+workID, 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-shCancelled:
	case <-time.After(time.Second):
		t.Fatal("album parse did not cancel Shanghai ResolveWork")
	}
	if len(unified.Slice(child(albumData, "data", "items"))) != 1 {
		t.Fatalf("unexpected album items: %#v", albumData)
	}

	// 3. Shanghai timeout / error leaves Japan result unchanged.
	plainExec := executorFunc(func(context.Context, fetch.Request) (fetch.Response, error) {
		return fetch.Response{StatusCode: 200, Body: []byte(videoDetailJSON)}, nil
	})
	japanBaseline, err := NewWithOriginalResolver(plainExec, nil, originalResolverFunc(func(context.Context, string) ([]unified.Object, error) {
		return nil, nil
	})).Parse(context.Background(), "https://www.douyin.com/video/"+workID, 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	for _, shErr := range []error{context.DeadlineExceeded, errors.New("shanghai connection refused")} {
		got, err := NewWithOriginalResolver(plainExec, nil, mockWorkResolver{
			workFn: func(context.Context, string) (*ShanghaiWork, error) { return nil, shErr },
		}).Parse(context.Background(), "https://www.douyin.com/video/"+workID, 1800000000)
		if err != nil {
			t.Fatal(err)
		}
		if !unified.Equal(got, japanBaseline) {
			t.Fatalf("Shanghai error %v altered Japan fallback result", shErr)
		}
	}
}

func TestFetchShanghaiDetailExtractsBitRateVIDAndFiltersHosts(t *testing.T) {
	const (
		workID = "7704056737233449610"
		vid    = "v0200fg10000dbl4sn7og65ijmfp8hm0"
		key1   = "/video/tos/cn/tos-cn-v-0015/oMuxed1080"
		key2   = "/video/tos/cn/tos-cn-v-0015/oDash1080"
	)
	v26Web := "https://v26-web.douyinvod.com/" + strings.Repeat("a", 32) + "/6a000001" + key1 + "/?a=6383"
	v11Weba := "https://v11-weba.douyinvod.com/" + strings.Repeat("b", 32) + "/6a000001" + key1 + "/?a=6383"
	zjcdn := "https://v3-dy-ov.zjcdn.com/" + strings.Repeat("c", 32) + "/6a000001" + key1 + "/?a=6383"
	www := "https://www.douyin.com/aweme/v1/play/?video_id=" + vid
	dashV26 := "https://v26-web.douyinvod.com/" + strings.Repeat("d", 32) + "/6a000001" + key2 + "/?a=6383"

	// Top-level video.uri is null and play_addr is omitted; vid is inside bit_rate[0].play_addr.uri.
	payload := `{"status_code":0,"aweme_detail":{"aweme_id":"` + workID + `","video":{"uri":null,"bit_rate":[` +
		`{"gear_name":"normal_1080_0","format":"mp4","play_addr":{"uri":"` + vid + `","url_list":["` + v26Web + `","` + v11Weba + `","` + zjcdn + `","` + www + `"]}},` +
		`{"gear_name":"normal_1080_0","format":"dash","play_addr":{"uri":"` + vid + `","url_list":["` + dashV26 + `"]}}` +
		`]}}}`

	exec := executorFunc(func(_ context.Context, req fetch.Request) (fetch.Response, error) {
		if req.Policy.Purpose != "douyin-detail" {
			t.Fatalf("unexpected purpose %q", req.Policy.Purpose)
		}
		return fetch.Response{StatusCode: 200, Body: []byte(payload)}, nil
	})
	got, err := FetchShanghaiDetail(context.Background(), exec, workID)
	if err != nil {
		t.Fatal(err)
	}
	if got.VideoID != vid || got.IsAlbum {
		t.Fatalf("unexpected VideoID=%q IsAlbum=%v", got.VideoID, got.IsAlbum)
	}
	if len(got.Renditions) != 1 {
		t.Fatalf("expected only 1 muxed rendition object (dash excluded), got %#v", got.Renditions)
	}
	urls := got.Renditions[key1]
	wantV26Luna := strings.Replace(v26Web, "://v26-web.douyinvod.com", "://v26-luna.douyinvod.com", 1)
	if len(urls) != 2 || urls[0] != wantV26Luna || urls[1] != v11Weba {
		t.Fatalf("unexpected extracted URLs (want v26-luna + v11-weba only): %#v", urls)
	}
}
