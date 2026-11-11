package douyin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type originalResolverFunc func(context.Context, string) ([]unified.Object, error)

func (f originalResolverFunc) ResolveOriginal(ctx context.Context, vid string) ([]unified.Object, error) {
	return f(ctx, vid)
}

func TestShanghaiOriginalSelectionAndRefresh(t *testing.T) {
	const vid = "v0200fg10000example"
	const key = "/video/tos/cn/tos-cn-x/object"
	const smaller = "https://example.net/transcode.mp4"
	detail := object{"video": object{"duration": 13667, "play_addr": object{"uri": vid}, "bit_rate": array{object{"play_addr": object{"width": 2160, "height": 3840, "data_size": 8662403, "url_list": array{smaller}}, "bit_rate": 5000000}}}}
	candidate := func(host, sig string) object {
		return object{"url": "https://" + host + "/" + strings.Repeat(sig, 32) + "/6a000001" + key + "/?ver=1", "metadata": object{"metadata_source": "mp4", "width": json.Number("2160"), "height": json.Number("3840"), "size": json.Number("93733891"), "bit_rate": json.Number("54730256"), "duration": json.Number("13.667"), "codec": "hevc", "has_audio": true}, "object_key": key, "source_key": host, "prefix_sha256": strings.Repeat("a", 64), "expires_at": "2099-01-01T00:00:00Z"}
	}
	a, b := candidate("v3-dy-o.zjcdn.com", "a"), candidate("v11-cold.douyinvod.com", "b")
	var remote []unified.Object
	var remoteErr error
	var japanResult object
	probes := 0
	japan := proberFunc(func(context.Context, string) (object, error) { probes++; return japanResult, nil })
	sh := originalResolverFunc(func(_ context.Context, v string) ([]unified.Object, error) {
		if v != vid {
			t.Fatalf("requested %s", v)
		}
		return remote, remoteErr
	})
	parser := NewWithOriginalResolver(nil, japan, sh)
	views := []object{}
	observe := func() (object, array) {
		t.Helper()
		raw, err := parser.formatData(context.Background(), detail, "7673404708262453110")
		if err != nil {
			t.Fatal(err)
		}
		view := unified.PublicView(adapt(raw, "https://www.douyin.com/video/7673404708262453110", 1800000000), false, 1800000000)
		if err := unified.ValidateEnvelopeAt(view, 1800000000); err != nil {
			t.Fatal(err)
		}
		views = append(views, view)
		item := unified.Map(first(child(view, "data", "items")))
		d := unified.Map(item["default"])
		for _, v := range unified.Slice(item["variants"]) {
			if child(v, "id") == d["variant_id"] {
				return d, unified.Slice(child(v, "delivery", "sources"))
			}
		}
		t.Fatal("missing default")
		return nil, nil
	}
	remote = []unified.Object{a, b}
	firstDefault, sources := observe()
	if child(first(child(first(child(views[0], "data", "items")), "variants")), "provenance") != "original_endpoint" {
		t.Fatal("source not presented as original")
	}
	if len(sources) != 2 || firstDefault["url"] != a["url"] || firstDefault["expires_at"] != a["expires_at"] || child(sources[0], "id") == child(sources[1], "id") || probes != 0 {
		t.Fatalf("incorrect initial selection: %#v %#v", firstDefault, sources)
	}
	remote = []unified.Object{candidate("v11-cold.douyinvod.com", "c")}
	renewed, remaining := observe()
	if len(remaining) != 1 || renewed["variant_id"] != firstDefault["variant_id"] || renewed["source_id"] != child(sources[1], "id") || renewed["source_id"] == firstDefault["source_id"] {
		t.Fatalf("wrong renewal %#v", renewed)
	}
	swapped := candidate("v11-cold.douyinvod.com", "d")
	swapped["object_key"] = key + "Changed"
	swapped["url"] = strings.ReplaceAll(stringValue(swapped["url"]), key, key+"Changed")
	remote = []unified.Object{swapped}
	changed, _ := observe()
	if changed["variant_id"] == firstDefault["variant_id"] {
		t.Fatal("new object reused the old selection")
	}
	transcode := candidate("v3-dy-o.zjcdn.com", "a")
	unified.Map(transcode["metadata"])["size"] = json.Number("8662403")
	remote = []unified.Object{transcode}
	fallback, fallbackSources := observe()
	if probes != 0 || len(fallbackSources) != 1 || fallback["variant_id"] == firstDefault["variant_id"] || !strings.HasPrefix(stringValue(fallback["url"]), smaller) {
		t.Fatalf("size-only match merged or probed Japan: probes=%d sources=%#v default=%#v", probes, fallbackSources, fallback)
	}
	remote = nil
	remoteErr = errors.New("remote unavailable")
	observe()
	if probes != 1 {
		t.Fatalf("failed remote did not use Japan: probes=%d", probes)
	}
	japanResult = candidate("japan.zjcdn.com", "e")
	japanResult["metadata"] = object{"metadata_source": "mp4", "width": int64(2160), "height": int64(3840), "size": int64(93733891), "bit_rate": int64(54730256), "duration": 13.667, "codec": "hevc"}
	jp, _ := observe()
	if jp["variant_id"] != firstDefault["variant_id"] || jp["source_id"] == firstDefault["source_id"] {
		t.Fatal("regional fallback changed file identity or borrowed vanished route")
	}
	japanResult = transcode
	jpCompressed, _ := observe()
	if jpCompressed["variant_id"] == firstDefault["variant_id"] || !strings.HasPrefix(stringValue(jpCompressed["url"]), smaller) {
		t.Fatal("Japan fallback mislabeled a known transcode")
	}
	for _, testcase := range []int{3, 4, 6} {
		for _, v := range unified.Slice(child(first(child(views[testcase], "data", "items")), "variants")) {
			if child(v, "provenance") == "original_endpoint" {
				t.Fatal("no original exists: a variant still advertises one")
			}
		}
	}
	remoteErr = nil
	replacement := candidate("v3-dy-o.zjcdn.com", "f")
	replacement["prefix_sha256"] = strings.Repeat("b", 64)
	remote = []object{replacement}
	replaced, _ := observe()
	if replaced["variant_id"] == firstDefault["variant_id"] {
		t.Fatal("changed file prefix reused original identity")
	}
	if path := os.Getenv("DOKITOOL_SHANGHAI_EXAMPLES"); path != "" {
		b, err := json.Marshal(views)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDispatcherNeverBecomesRegionalFileSource(t *testing.T) {
	const media = "https://cdn.example.org/play.mp4"
	for _, onlyDispatcher := range []bool{true, false} {
		urls := array{OriginalPlayURL("vid"), OriginalPlayURL("vid") + "&other=1"}
		if !onlyDispatcher {
			urls = append(urls, media)
		}
		detail := object{"video": object{"play_addr": object{"uri": "vid", "url_list": urls}}}
		resolver := originalResolverFunc(func(context.Context, string) ([]unified.Object, error) { return nil, nil })
		parser := NewWithOriginalResolver(nil, nil, resolver)
		raw, err := parser.formatData(context.Background(), detail, "123")
		if err != nil {
			t.Fatal(err)
		}
		view := adapt(raw, "https://www.douyin.com/video/7673404708262453110", 1800000000)
		if onlyDispatcher {
			if view["code"] != "NO_MEDIA" {
				t.Fatal("Unverified dispatcher became a file", view)
			}
			continue
		}
		item := first(child(view, "data", "items"))
		if !strings.HasPrefix(unified.Str(child(item, "default", "url")), media) || len(unified.Slice(child(item, "variants"))) != 1 {
			t.Fatal("lost real candidate or retained a dispatcher", view)
		}
	}
}

func TestShanghaiSameObjectPrependsDomesticSourcesAndSkipsJapanProbe(t *testing.T) {
	const (
		vid      = "v0200fg10000sameobj"
		sameKey  = "/video/tos/cn/tos-cn-v-0015/sameObject4K"
		otherKey = "/video/tos/cn/tos-cn-v-0015/otherObject1080"
		ov1      = "https://v3-web-ov.douyinvod.com/11111111111111111111111111111111/6a000001" + sameKey + "/?a=6383"
		ov2      = "https://v26-web-ov.douyinvod.com/22222222222222222222222222222222/6a000001" + sameKey + "/?a=6383"
		ov1080   = "https://v3-web-ov.douyinvod.com/33333333333333333333333333333333/6a000001" + otherKey + "/?a=6383"
	)
	detail := object{
		"video": object{
			"duration":        13667,
			"cdn_url_expired": 1800003600,
			"play_addr":       object{"uri": vid},
			"bit_rate": array{
				object{
					"gear_name": "2160p",
					"bit_rate":  54730256,
					"is_h265":   1,
					"play_addr": object{
						"uri":       vid,
						"file_hash": "hash4k",
						"width":     2160,
						"height":    3840,
						"data_size": 93733891,
						"url_list":  array{ov1, ov2},
					},
				},
				object{
					"gear_name": "normal_1080_0",
					"bit_rate":  4000000,
					"is_h265":   0,
					"play_addr": object{
						"uri":       vid,
						"file_hash": "hash1080",
						"width":     1080,
						"height":    1920,
						"data_size": 6800000,
						"url_list":  array{ov1080},
					},
				},
			},
		},
	}
	shCandidate := func(host, sig, objKey string, size int64, dur string) object {
		return object{
			"url":           "https://" + host + "/" + strings.Repeat(sig, 32) + "/6a000001" + objKey + "/?ver=1",
			"metadata":      object{"metadata_source": "mp4", "width": json.Number("2160"), "height": json.Number("3840"), "size": json.Number(stringValue(size)), "bit_rate": json.Number("54730256"), "duration": json.Number(dur), "codec": "hevc", "has_audio": true},
			"object_key":    objKey,
			"source_key":    host,
			"prefix_sha256": strings.Repeat("c", 64),
			"expires_at":    "2099-01-01T00:00:00Z",
		}
	}

	// Baseline without Shanghai observations (empty return + nil prober) to capture exact declared shape.
	baseParser := NewWithOriginalResolver(nil, nil, originalResolverFunc(func(context.Context, string) ([]unified.Object, error) {
		return nil, nil
	}))
	baseRaw, err := baseParser.formatData(context.Background(), detail, "7673404708262453110")
	if err != nil {
		t.Fatal(err)
	}
	baseView := unified.PublicView(adapt(baseRaw, "https://www.douyin.com/video/7673404708262453110", 1800000000), false, 1800000000)
	baseItem := unified.Map(first(child(baseView, "data", "items")))
	baseVariants := unified.Slice(baseItem["variants"])
	if len(baseVariants) != 2 {
		t.Fatalf("expected 2 declared variants, got %d", len(baseVariants))
	}
	base4K := unified.Map(baseVariants[0])
	baseOrigSources := unified.Slice(child(base4K, "delivery", "sources"))

	// 1. Same object_key: Shanghai URLs prepended first, original overseas URLs kept behind, Japan prober called 0 times.
	sh1 := shCandidate("v3-dy-o.zjcdn.com", "a", sameKey, 93733891, "13.667")
	sh2 := shCandidate("v11-cold.douyinvod.com", "b", sameKey, 93733891, "13.667")
	probes := 0
	japan := proberFunc(func(context.Context, string) (object, error) {
		probes++
		return nil, nil
	})
	var remote []unified.Object
	var remoteErr error
	parser := NewWithOriginalResolver(nil, japan, originalResolverFunc(func(context.Context, string) ([]unified.Object, error) {
		return remote, remoteErr
	}))

	remote = []unified.Object{sh1, sh2}
	raw, err := parser.formatData(context.Background(), detail, "7673404708262453110")
	if err != nil {
		t.Fatal(err)
	}
	view := unified.PublicView(adapt(raw, "https://www.douyin.com/video/7673404708262453110", 1800000000), false, 1800000000)
	if err := unified.ValidateEnvelopeAt(view, 1800000000); err != nil {
		t.Fatal(err)
	}
	if probes != 0 {
		t.Fatalf("expected 0 Japan probes for validated same-object response, got %d", probes)
	}
	item := unified.Map(first(child(view, "data", "items")))
	variants := unified.Slice(item["variants"])
	if len(variants) != 2 {
		t.Fatalf("same-object added or removed variants: got %d want 2", len(variants))
	}
	got4K := unified.Map(variants[0])
	if got4K["id"] != base4K["id"] || got4K["label"] != base4K["label"] || got4K["provenance"] != nil || !unified.Equal(got4K["metadata"], base4K["metadata"]) {
		t.Fatalf("declared rendition identity, label, provenance or metadata changed:\ngot:  %#v\nwant: %#v", got4K, base4K)
	}
	if child(item, "default", "variant_id") != child(baseItem, "default", "variant_id") {
		t.Fatalf("default variant selection changed: got %v want %v", child(item, "default", "variant_id"), child(baseItem, "default", "variant_id"))
	}
	gotSources := unified.Slice(child(got4K, "delivery", "sources"))
	if len(gotSources) != 2+len(baseOrigSources) {
		t.Fatalf("unexpected merged sources count: got %d want %d", len(gotSources), 2+len(baseOrigSources))
	}
	if child(gotSources[0], "url") != sh1["url"] || child(gotSources[1], "url") != sh2["url"] {
		t.Fatalf("Shanghai candidates not placed at head of sources: %#v", gotSources)
	}
	for i, orig := range baseOrigSources {
		if !unified.Equal(gotSources[2+i], orig) {
			t.Fatalf("original backup source %d altered:\ngot:  %#v\nwant: %#v", i, gotSources[2+i], orig)
		}
	}
	if child(item, "default", "url") != sh1["url"] || child(item, "default", "source_id") != child(gotSources[0], "id") {
		t.Fatalf("default projection did not point to leading Shanghai source: %#v", item["default"])
	}

	// 2. Same size only (different object_key): must NOT merge URLs, and must still skip Japan probe (probes == 0).
	diffKeySameSize := shCandidate("v3-dy-o.zjcdn.com", "d", "/video/tos/cn/tos-cn-v-0015/differentObject", 93733891, "13.667")
	remote = []unified.Object{diffKeySameSize}
	raw, err = parser.formatData(context.Background(), detail, "7673404708262453110")
	if err != nil {
		t.Fatal(err)
	}
	sizeOnlyView := unified.PublicView(adapt(raw, "https://www.douyin.com/video/7673404708262453110", 1800000000), false, 1800000000)
	if probes != 0 || !unified.Equal(sizeOnlyView, baseView) {
		t.Fatalf("size-only match should neither merge nor probe Japan: probes=%d", probes)
	}

	// 3. Shanghai error / empty / duration mismatch / AllowedMediaURL rejection: falls back to Japan probe (1 call each), output unchanged.
	badHost := shCandidate("v3-dy-o.zjcdn.com", "e", sameKey, 93733891, "13.667")
	badHost["url"] = "https://unapproved.example.com/" + strings.Repeat("e", 32) + "/6a000001" + sameKey + "/?ver=1"
	for idx, tc := range []struct {
		name string
		rows []unified.Object
		err  error
	}{
		{"error", nil, errors.New("shanghai timeout")},
		{"empty", nil, nil},
		{"duration_mismatch", []unified.Object{shCandidate("v3-dy-o.zjcdn.com", "f", sameKey, 93733891, "16.000")}, nil},
		{"disallowed_media_url", []unified.Object{badHost}, nil},
	} {
		remote, remoteErr = tc.rows, tc.err
		raw, err = parser.formatData(context.Background(), detail, "7673404708262453110")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		gotView := unified.PublicView(adapt(raw, "https://www.douyin.com/video/7673404708262453110", 1800000000), false, 1800000000)
		if probes != idx+1 || !unified.Equal(gotView, baseView) {
			t.Fatalf("%s: expected fallback probe #%d and unchanged view, got probes=%d", tc.name, idx+1, probes)
		}
	}

	// 4. resolver == nil: byte-for-byte identical to New(nil, japan).
	nilCalls := 0
	nilJapan := proberFunc(func(context.Context, string) (object, error) {
		nilCalls++
		return object{"url": ov1, "metadata": object{"width": int64(2160), "height": int64(3840), "size": int64(93733891), "bit_rate": int64(54730256), "codec": "hevc"}}, nil
	})
	nilRawA, err := New(nil, nilJapan).formatData(context.Background(), detail, "7673404708262453110")
	if err != nil {
		t.Fatal(err)
	}
	nilRawB, err := NewWithOriginalResolver(nil, nilJapan, nil).formatData(context.Background(), detail, "7673404708262453110")
	if err != nil {
		t.Fatal(err)
	}
	aBytes, _ := unified.Encode(adapt(nilRawA, "https://www.douyin.com/video/7673404708262453110", 1800000000))
	bBytes, _ := unified.Encode(adapt(nilRawB, "https://www.douyin.com/video/7673404708262453110", 1800000000))
	if nilCalls != 2 || string(aBytes) != string(bBytes) {
		t.Fatalf("nil resolver diverged: calls=%d", nilCalls)
	}
}
