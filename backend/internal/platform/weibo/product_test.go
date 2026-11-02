package weibo

import (
	"context"
	"os"
	"strings"
	"testing"

	media "github.com/Brilliant666/DokiTool-Go/backend/internal/media/weibo"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// The private responses are independent frozen PHP 8.3.6 formatter results;
// never generate adapter expectations from Go itself.
func TestWeiboProductAdapterConsumesPHPFormatter(t *testing.T) {
	body, err := os.ReadFile("testdata/php-formatter-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := unified.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if fixture["php_source_commit"] != "25beaee107f8c1a208ced9deb3c637068445e316" {
		t.Fatal("wrong PHP baseline")
	}
	cases := unified.Slice(fixture["cases"])
	if len(cases) != 29 {
		t.Fatalf("lost formatter cases: %d", len(cases))
	}
	for _, value := range cases {
		entry := unified.Map(value)
		t.Run(unified.Str(entry["id"]), func(t *testing.T) {
			legacy := unified.Clone(unified.Map(entry["expected"]))
			now := int64(1730000000)
			output := adaptLegacy(legacy, "https://weibo.com/1234567890/A1B2C3D4", now)
			if err := unified.ValidateEnvelopeAt(output, now); err != nil {
				t.Fatalf("adapter/schema mismatch: %v", err)
			}
			if output["code"] == "OK" {
				data := unified.Map(output["data"])
				if data["platform"] != "weibo" || len(unified.Slice(data["items"])) == 0 {
					t.Fatal("invalid work inventory")
				}
			}
			if !unified.Equal(legacy, entry["expected"]) {
				t.Fatal("private parser output mutated")
			}
		})
	}
}

func TestWeiboSigningKeepsStableSourceAndDefault(t *testing.T) {
	const raw = "https://f.video.weibocdn.com/hd.mp4?token=fixed"
	now := int64(1730000000)
	key := strings.Repeat("a", 64)
	legacy := unified.Object{"code": 200, "data": unified.Object{
		"type": "video", "url": raw,
		"video_backup": unified.Array{
			unified.Object{"url": raw, "quality": "1080p", "width": 1920, "height": 1080},
			unified.Object{"url": "https://example.com/foreign.mp4", "quality": "360p"},
		},
	}}
	original := adaptLegacy(legacy, "https://weibo.com/1234567/AbC", now)
	snapshot := unified.Clone(original)
	prepared, err := PreparePublic(key)(context.Background(), "weibo", original, now)
	if err != nil {
		t.Fatal(err)
	}
	item := unified.Map(unified.Slice(unified.Map(prepared["data"])["items"])[0])
	variants := unified.Slice(item["variants"])
	if len(variants) != 2 {
		t.Fatalf("lost variant candidates: %d", len(variants))
	}
	first := unified.Map(variants[0])
	source := unified.Map(unified.Slice(unified.Map(first["delivery"])["sources"])[0])
	ticket := strings.TrimPrefix(unified.Str(source["url"]), "/api/media/weibo?ticket=")
	decoded, valid := media.Verify(ticket, key, now)
	if !valid || decoded["u"] != raw || source["access"] != "relay" {
		t.Fatal("invalid or unbound ticket")
	}
	oldItem := unified.Map(unified.Slice(unified.Map(snapshot["data"])["items"])[0])
	oldVariant := unified.Map(unified.Slice(oldItem["variants"])[0])
	oldSource := unified.Map(unified.Slice(unified.Map(oldVariant["delivery"])["sources"])[0])
	if source["id"] != oldSource["id"] || first["id"] != oldVariant["id"] {
		t.Fatal("signing changed identity")
	}
	foreign := unified.Map(unified.Slice(unified.Map(unified.Map(variants[1])["delivery"])["sources"])[0])
	if foreign["availability"] != "unavailable" {
		t.Fatal("foreign source remained available")
	}
	if !unified.Equal(original, snapshot) {
		t.Fatal("signing mutated private inventory")
	}
	view := unified.PublicView(prepared, false, now)
	if err := unified.ValidateEnvelopeAt(view, now); err != nil {
		t.Fatal(err)
	}
	publicItem := unified.Map(unified.Slice(unified.Map(view["data"])["items"])[0])
	selected := unified.Map(publicItem["default"])
	if selected["url"] != source["url"] || selected["source_id"] != source["id"] {
		t.Fatal("default selection lost signed source")
	}
	if len(unified.Slice(publicItem["variants"])) > len(variants) {
		t.Fatal("filtered view invented variants")
	}
	missing, err := PreparePublic("")(context.Background(), "weibo", original, now)
	if err != nil || missing["code"] != "SERVICE_NOT_CONFIGURED" {
		t.Fatal("missing key must fail closed")
	}
}

// Two credential generations of a selected rendition must still address the
// same explicit choice, without ever replacing the signed delivery URL.
func TestWeiboLPRefreshPreservesExplicitFile(t *testing.T) {
	const file = "https://f.video.weibocdn.com/ab/object540.mp4?media_id=12345&template=540p&label=video"
	const now = int64(1730000000)
	key := strings.Repeat("k", 32)
	type choice struct{ variant, source string }
	publicChoices := func(urls ...string) []choice {
		t.Helper()
		var candidates unified.Array
		for _, raw := range urls {
			candidates = append(candidates, unified.Object{"url": raw, "width": 540, "height": 960, "size": 844977})
		}
		legacy := unified.Object{"code": 200, "data": unified.Object{"url": urls[0], "video_backup": candidates}}
		out, err := PreparePublic(key)(context.Background(), "weibo", adaptLegacy(legacy, "https://weibo.com/tv/show/1034:12345", now), now)
		if err != nil {
			t.Fatal(err)
		}
		out = unified.PublicView(out, false, now)
		if err := unified.ValidateEnvelopeAt(out, now); err != nil {
			t.Fatal(err)
		}
		item := unified.Map(unified.Slice(unified.Map(out["data"])["items"])[0])
		variants := unified.Slice(item["variants"])
		if len(variants) != len(urls) {
			t.Fatalf("lost candidates: got %d, want %d", len(variants), len(urls))
		}
		var got []choice
		for i, v := range variants {
			variant := unified.Map(v)
			sources := unified.Slice(unified.Map(variant["delivery"])["sources"])
			if len(sources) != 1 {
				t.Fatal("unexpected source merge")
			}
			source := unified.Map(sources[0])
			ticket := strings.TrimPrefix(unified.Str(source["url"]), "/api/media/weibo?ticket=")
			data, ok := media.Verify(ticket, key, now)
			if !ok || data["u"] != urls[i] {
				t.Fatal("delivery did not preserve the exact incoming URL or source order")
			}
			got = append(got, choice{unified.Str(variant["id"]), unified.Str(source["id"])})
		}
		return got
	}
	const old = "&lp=oldRoute&Expires=1900000010&ssig=old&KID=k"
	const fresh = "&lp=newRoute&Expires=1900000020&ssig=new&KID=k"
	const plain = "&Expires=1900000010&ssig=other&KID=k"
	first := publicChoices(file+plain, file+old)
	next := publicChoices(file+fresh, file+plain)
	if first[1] != next[0] || first[0] != next[1] {
		t.Fatalf("refresh changed a choice: before %v, reordered after %v", first, next)
	}
	for _, raw := range []string{
		strings.Replace(file, "object540", "other540", 1) + fresh,
		strings.Replace(file, "12345", "12346", 1) + fresh,
		strings.Replace(file, "540p", "other", 1) + fresh,
		file + "&extra=content" + fresh,
	} {
		if publicChoices(raw)[0] == first[1] {
			t.Fatal("unrelated content inherited explicit choice")
		}
	}
	ambiguous := publicChoices(file+old, file+fresh)
	if ambiguous[0] == ambiguous[1] || ambiguous[0] == first[1] || ambiguous[1] == first[1] {
		t.Fatal("two simultaneously distinct observations must remain distinct from the unambiguous choice")
	}
	for _, unproven := range []string{
		strings.Replace(file, "/ab/object540.mp4", "/other/shape", 1),
		strings.Replace(file, "template=", "other=", 1),
		file + "&lp=another",
	} {
		if publicChoices(unproven + old)[0] == publicChoices(unproven + fresh)[0] {
			t.Fatal("an unfamiliar or ambiguous URL shape lost its original identity")
		}
	}
}

func TestWeiboForeignOnlyFailsClosed(t *testing.T) {
	now := int64(1730000000)
	raw := unified.Object{"code": 200, "data": unified.Object{"type": "video",
		"video_backup": unified.Array{unified.Object{"url": "https://example.com/video.mp4"}}}}
	payload := adaptLegacy(raw, "https://weibo.com/1234567/AbC", now)
	result, err := PreparePublic(strings.Repeat("z", 64))(context.Background(), "weibo", payload, now)
	if err != nil || result["code"] != "UPSTREAM_INVALID_RESPONSE" {
		t.Fatal("unsupported provider not rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PreparePublic(strings.Repeat("z", 64))(ctx, "weibo", payload, now); err == nil {
		t.Fatal("canceled signing ran")
	}
}
