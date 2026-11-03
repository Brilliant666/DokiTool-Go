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
	unified.Map(transcode["metadata"])["size"] = 8662403
	remote = []unified.Object{transcode}
	fallback, _ := observe()
	if probes != 1 || fallback["variant_id"] == firstDefault["variant_id"] || !strings.HasPrefix(stringValue(fallback["url"]), smaller) {
		t.Fatalf("known transcode advertised as original: %#v", fallback)
	}
	remote = nil
	remoteErr = errors.New("remote unavailable")
	observe()
	if probes != 2 {
		t.Fatal("failed remote did not use Japan")
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
