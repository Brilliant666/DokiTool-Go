package douyin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func TestAllowedBrowserFetchURL(t *testing.T) {
	allowed := []string{
		"https://v26-cold.douyinvod.com/ab9b7783910be75af96f7976d6864f98/69db7a3d/video/tos/cn/tos-cn-ve-15/oM0f7A27A/?a=1128",
		"https://v13-cold.douyinvod.com/61a60ed7a380f239d1cdbc8c6ba7b175/69db7a3d/video/tos/cn/tos-cn-ve-15/oM0f7A27A/?a=1128",
		"https://v26-luna.douyinvod.com/cc7bc67d4f209c00ebdb83c63718d354/69db7104/video/tos/cn/tos-cn-ve-15/o0j7sEAB5/",
		"https://v11-weba.douyinvod.com/b9e1f080ee188d9c7669533bc2e80d7e/69db7104/video/tos/cn/tos-cn-ve-15/o0j7sEAB5/",
		"https://v5-hl-mly-ov.zjcdn.com/9b433c9e95a8f6f815f4e041ba639eb0/69db78ea/video/tos/cn/tos-cn-ve-15/o0j7sEAB5/",
		"https://v3-dy-o.zjcdn.com/9b433c9e95a8f6f815f4e041ba639eb0/69db78ea/video/tos/cn/tos-cn-ve-15/o0j7sEAB5/",
	}
	for _, raw := range allowed {
		if !AllowedBrowserFetchURL(raw) {
			t.Fatalf("expected %q to allow browser_fetch", raw)
		}
	}

	rejected := []string{
		"https://aweme.snssdk.com/aweme/v1/play/?video_id=v0200fg10000dbl4sn7og65ijmfp8hm0&ratio=1080p&line=0",
		"https://www.douyin.com/aweme/v1/play/?video_id=v0200fg10000dbl4sn7og65ijmfp8hm0",
		"http://v26-cold.douyinvod.com/path/video.mp4",
		"https://douyinvod.com/path/video.mp4",
		"https://example.test/video.mp4",
	}
	for _, raw := range rejected {
		if AllowedBrowserFetchURL(raw) {
			t.Fatalf("expected %q not to allow browser_fetch", raw)
		}
	}
}

func TestPreparePublicMarksDouyinCDNSourcesAndPreservesAllRoutes(t *testing.T) {
	const (
		now     int64 = 1800000000
		vid           = "v0200fg10000dbl4sn7og65ijmfp8hm0"
		origKey       = "/video/tos/cn/tos-cn-ve-15/oM0f7A27AY6jU1aE8iBArEIQPAX36iiZA9XBE"
		normKey       = "/video/tos/cn/tos-cn-ve-15/o0j7sEAB5IQP36i"
	)

	detail := object{
		"desc": "4K route selection",
		"video": object{
			"duration":  96333,
			"play_addr": object{"uri": vid},
			"bit_rate": array{
				object{
					"gear_name": "normal_1080_0",
					"bit_rate":  1773282,
					"play_addr": object{
						"width":     1920,
						"height":    1080,
						"data_size": 21353927,
						"url_list": array{
							"https://v26-web.douyinvod.com/" + strings.Repeat("c", 32) + "/6a000001" + normKey + "/?a=1128",
							"https://v11-weba.douyinvod.com/" + strings.Repeat("d", 32) + "/6a000001" + normKey + "/?a=1128",
							"https://v5-hl-mly-ov.zjcdn.com/" + strings.Repeat("e", 32) + "/6a000001" + normKey + "/?a=1128",
							"https://www.douyin.com/aweme/v1/play/?video_id=" + vid,
						},
					},
				},
			},
		},
	}

	candidate := func(host, sig string) object {
		return object{
			"url":           "https://" + host + "/" + strings.Repeat(sig, 32) + "/6a000001" + origKey + "/?a=1128",
			"metadata":      object{"metadata_source": "mp4", "width": json.Number("3840"), "height": json.Number("2160"), "size": json.Number("394316272"), "bit_rate": json.Number("32746100"), "duration": json.Number("96.333"), "codec": "hevc", "has_audio": true},
			"object_key":    origKey,
			"source_key":    host,
			"prefix_sha256": strings.Repeat("a", 64),
			"expires_at":    "2099-01-01T00:00:00Z",
		}
	}

	sh := originalResolverFunc(func(_ context.Context, gotVid string) ([]unified.Object, error) {
		if gotVid != vid {
			t.Fatalf("unexpected vid %q", gotVid)
		}
		return []unified.Object{
			candidate("v13-cold.douyinvod.com", "1"),
			candidate("v26-cold.douyinvod.com", "2"),
		}, nil
	})
	parser := NewWithOriginalResolver(nil, nil, sh)
	raw, err := parser.formatData(context.Background(), detail, "7704056737233449610")
	if err != nil {
		t.Fatalf("formatData failed: %v", err)
	}
	envelope := unified.PublicView(adapt(raw, "https://www.douyin.com/video/7704056737233449610", now), false, now)

	out, err := PreparePublic(context.Background(), "douyin", envelope, now)
	if err != nil {
		t.Fatalf("PreparePublic returned error: %v", err)
	}
	if err := unified.ValidateEnvelopeAt(out, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt failed after PreparePublic: %v", err)
	}

	// Input envelope must not be mutated in place.
	origSources := unified.Slice(child(first(child(first(child(envelope, "data", "items")), "variants")), "delivery", "sources"))
	if unified.Map(origSources[0])["browser_fetch"] != nil {
		t.Fatalf("input envelope mutated in place")
	}

	variants := unified.Slice(child(first(child(out, "data", "items")), "variants"))
	if len(variants) != 2 {
		t.Fatalf("expected 2 variants (4K original + 1080p transcode), got %d", len(variants))
	}

	orig4K := unified.Slice(child(variants[0], "delivery", "sources"))
	if len(orig4K) != 2 {
		t.Fatalf("expected both Shanghai 4K cold sources preserved, got %d", len(orig4K))
	}
	for i, src := range orig4K {
		if got := unified.Str(unified.Map(src)["browser_fetch"]); got != "allowed" {
			t.Fatalf("4K source[%d] expected browser_fetch=allowed, got %q", i, got)
		}
	}

	transcode1080 := unified.Slice(child(variants[1], "delivery", "sources"))
	if len(transcode1080) != 4 {
		t.Fatalf("expected all 4 domestic+overseas+dispatcher 1080p sources preserved, got %d", len(transcode1080))
	}
	wantHosts := []string{"v26-luna.douyinvod.com", "v11-weba.douyinvod.com", "v5-hl-mly-ov.zjcdn.com", "www.douyin.com"}
	wantFetch := []string{"allowed", "allowed", "allowed", ""}
	for i := range transcode1080 {
		u := unified.Str(unified.Map(transcode1080[i])["url"])
		if !strings.Contains(u, wantHosts[i]) {
			t.Fatalf("1080p source[%d] url=%q does not contain %q", i, u, wantHosts[i])
		}
		if got := unified.Str(unified.Map(transcode1080[i])["browser_fetch"]); got != wantFetch[i] {
			t.Fatalf("1080p source[%d] (%s) browser_fetch=%q, want %q", i, wantHosts[i], got, wantFetch[i])
		}
	}

	// Explicit blocked / relay sources and pair resources are handled safely.
	synthetic := unified.Clone(envelope)
	sList := unified.Slice(child(first(child(first(child(synthetic, "data", "items")), "variants")), "delivery", "sources"))
	unified.Map(sList[0])["browser_fetch"] = "blocked"
	unified.Map(sList[1])["access"] = "relay"
	synOut, err := PreparePublic(context.Background(), "douyin", synthetic, now)
	if err != nil {
		t.Fatalf("PreparePublic on synthetic failed: %v", err)
	}
	synSources := unified.Slice(child(first(child(first(child(synOut, "data", "items")), "variants")), "delivery", "sources"))
	if got := unified.Str(unified.Map(synSources[0])["browser_fetch"]); got != "blocked" {
		t.Fatalf("blocked source must remain blocked, got %q", got)
	}
	if got := unified.Str(unified.Map(synSources[1])["browser_fetch"]); got != "" {
		t.Fatalf("relay source must remain unset, got %q", got)
	}

	// Non-douyin platforms pass through untouched.
	other, err := PreparePublic(context.Background(), "x", envelope, now)
	if err != nil || !unified.Equal(other, envelope) {
		t.Fatalf("expected non-douyin payload to pass through unchanged, err=%v", err)
	}
}
