package main

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/api"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	weibomedia "github.com/Brilliant666/DokiTool-Go/backend/internal/media/weibo"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// The adapter is synthetic; the production public-media composition and HTTP
// parse/resolve handlers are real. The frozen PHP oracle is not modified.
func TestXHSPublicMediaIntegration(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/schema/unified-v1/examples/10-mixed-live-photo-and-bgm.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := unified.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	data := unified.Map(fixture["data"])
	data["platform"] = "xiaohongshu"
	data["source_url"] = "https://www.xiaohongshu.com/explore/123"
	delete(data, "attachments")
	original := unified.Map(unified.Slice(data["items"])[1])
	items := unified.Array{}
	for i, id := range []string{"live-1", "live-2"} {
		item := unified.Clone(original)
		item["id"] = id
		item["index"] = i + 1
		variant := unified.Map(unified.Slice(item["variants"])[0])
		resources := unified.Slice(unified.Map(variant["delivery"])["resources"])
		for _, value := range resources {
			resource := unified.Map(value)
			sources := unified.Slice(unified.Map(resource["delivery"])["sources"])
			source := unified.Map(sources[0])
			if resource["role"] == "motion" {
				// A newly observed first node and an unfamiliar provider must reach
				// both the original single-file and ZIP consumers without code lists.
				hosts := []string{"sns-v28.rednotecdn.com", "media.next-provider.example.test"}
				source["url"] = "http://" + hosts[i] + "/" + id + "?sig=a%2Bb+z&part=1&part=2&empty="
				source["id"] = id + "-first"
				alternates := unified.Array{source}
				for _, host := range []string{"sns-v11.rednotecdn.com", "sns-v27.rednotecdn.com"} {
					backup := unified.Clone(source)
					backup["id"] = id + "-" + host
					backup["url"] = "http://" + host + "/" + id + "?sig=backup%2Btoken"
					alternates = append(alternates, backup)
				}
				unified.Map(resource["delivery"])["sources"] = alternates
			} else {
				source["url"] = "https://sns-webpic.xhscdn.com/" + id
			}
		}
		items = append(items, item)
	}
	data["items"] = items
	data["default_item_id"] = "live-1"
	registry, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	adapter := api.AdapterFunc(func(context.Context, string, int64) (unified.Object, error) { return unified.Clone(fixture), nil })
	h, err := api.New(registry, map[string]api.Adapter{"xiaohongshu": adapter}, func() time.Time { return time.Unix(1793448000, 0) }, preparePublicMedia("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	outputs := unified.Array{}
	for _, route := range []string{"parse", "resolve"} {
		for _, debug := range []string{"0", "1"} {
			q := url.Values{"schema_version": {"1.0"}, "url": {"https://www.xiaohongshu.com/explore/123"}, "debug": {debug}}
			if route == "resolve" {
				q.Set("item_id", "live-2")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/api/"+route+"?"+q.Encode(), nil))
			if w.Code != 200 {
				t.Fatalf("%s/%s: %s", route, debug, w.Body.String())
			}
			out, err := unified.Decode(w.Body.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if err = unified.ValidateEnvelopeAt(out, 1793448000); err != nil {
				t.Fatal(err)
			}
			got := unified.Map(out["data"])
			if route == "resolve" && got["default_item_id"] != "live-2" {
				t.Fatal("resolve lost selected item")
			}
			gotItems := unified.Slice(got["items"])
			if len(gotItems) != 2 {
				t.Fatal("lost live pair")
			}
			for i, value := range gotItems {
				item := unified.Map(value)
				res := unified.Slice(unified.Map(unified.Map(unified.Slice(item["variants"])[0])["delivery"])["resources"])
				for _, v := range res {
					r := unified.Map(v)
					if r["role"] != "motion" {
						continue
					}
					sources := unified.Slice(unified.Map(r["delivery"])["sources"])
					originalItem := unified.Map(items[i])
					originalVariant := unified.Map(unified.Slice(originalItem["variants"])[0])
					originalResources := unified.Slice(unified.Map(originalVariant["delivery"])["resources"])
					var originalSources unified.Array
					for _, candidate := range originalResources {
						if resource := unified.Map(candidate); resource["role"] == "motion" {
							originalSources = unified.Slice(unified.Map(resource["delivery"])["sources"])
						}
					}
					if len(sources) != 3 || len(originalSources) != 3 {
						t.Fatal("source count changed")
					}
					for n, value := range sources {
						gotSource, originalSource := unified.Map(value), unified.Map(originalSources[n])
						want := "https" + unified.Str(originalSource["url"])[4:]
						if gotSource["url"] != want || gotSource["id"] != originalSource["id"] {
							t.Fatalf("source %d changed identity/order or was not upgraded", n)
						}
					}
				}
			}
			outputs = append(outputs, out)
		}
	}
	if dest := os.Getenv("DOKITOOL_XHS_PUBLIC_INTEGRATION_OUTPUT"); dest != "" {
		bytes, err := unified.Encode(outputs)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, bytes, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCombinedPublicMediaKeepsWeiboSigning(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/schema/unified-v1/examples/01-douyin-quality-and-cdn.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := unified.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	data := unified.Map(fixture["data"])
	data["platform"] = "weibo"
	data["source_url"] = "https://weibo.com/123/AbC"
	data["generated_at"] = time.Unix(1793448000, 0).UTC().Format(time.RFC3339)
	item := unified.Map(unified.Slice(data["items"])[0])
	item["id"] = "video"
	variants := unified.Slice(item["variants"])
	item["variants"] = variants[:1]
	variant := unified.Map(variants[0])
	delivery := unified.Map(variant["delivery"])
	sources := unified.Slice(delivery["sources"])
	delivery["sources"] = sources[:1]
	const upstream = "https://f.video.weibocdn.com/file.mp4?x=1"
	unified.Map(sources[0])["url"] = upstream
	unified.Map(item["default"])["url"] = upstream
	key := strings.Repeat("k", 64)
	prepared, err := preparePublicMedia(key)(context.Background(), "weibo", fixture, 1793448000)
	if err != nil {
		t.Fatal(err)
	}
	item = unified.Map(unified.Slice(unified.Map(prepared["data"])["items"])[0])
	d := unified.Map(item["default"])
	u := unified.Str(d["url"])
	if d["access"] != "relay" || !strings.HasPrefix(u, "/api/media/weibo?ticket=") {
		t.Fatal("signed relay missing")
	}
	ticket, ok := weibomedia.Verify(strings.TrimPrefix(u, "/api/media/weibo?ticket="), key, 1793448000)
	if !ok || ticket["u"] != upstream {
		t.Fatal("signed relay invalid")
	}
}
