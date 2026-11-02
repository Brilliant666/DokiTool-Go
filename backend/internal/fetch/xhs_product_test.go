package fetch_test

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/api"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/xiaohongshu"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// The oracle is the EXISTING fixed PHP 8.3.6 product output, not a Go-derived
// expected response. Only page-only, network-response fixtures are selected;
// media fixtures remain in the unchanged full PHP/Go gate. The HTTPS server is
// synthetic, but the request traverses real TLS/HTTP1 or TLS/HTTP2 -> bounded
// fetch -> original XHS parser/adapter -> formal parse/resolve API.
func TestXHSChrome131FormalAPIThroughFrozenPHPPageFixtures(t *testing.T) {
	raw, err := os.ReadFile("../platform/xiaohongshu/testdata/php-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := unified.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	exports := unified.Array{}
	tested := 0
	for _, value := range unified.Slice(document["cases"]) {
		fixture := unified.Map(value)
		id := unified.Str(fixture["id"])
		input, expected := unified.Map(fixture["input"]), unified.Map(fixture["expected"])
		responses := unified.Slice(input["http_responses"])
		if len(responses) == 0 || id == "xiaohongshu/entry/xhs-com-alias" {
			continue
		}
		pageOnly := true
		for _, value := range responses {
			step := unified.Map(value)
			status, _ := unified.PHPInt(step["status"])
			failure, _ := unified.PHPInt(step["error"])
			if unified.Str(step["range"]) != "" || !strings.HasPrefix(unified.Str(step["url"]), "https://") || status == 0 || failure != 0 {
				pageOnly = false
			}
		}
		if !pageOnly {
			continue
		}
		tested++
		for _, h2 := range []bool{false, true} {
			for _, view := range []string{"normal", "debug"} {
				base := unified.Map(expected[view])
				choices := []string{""}
				if base["code"] == "OK" {
					for _, item := range unified.Slice(unified.Map(base["data"])["items"]) {
						choices = append(choices, unified.Str(unified.Map(item)["id"]))
					}
					choices = append(choices, "missing-item")
				}
				for _, choice := range choices {
					t.Run(fmt.Sprintf("%s/http2=%v/%s/%s", id, h2, view, choice), func(t *testing.T) {
						var mutex sync.Mutex
						next := 0
						client := fetch.XHSLoopbackClientForTest(t, h2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							mutex.Lock()
							defer mutex.Unlock()
							if next >= len(responses) {
								t.Error("unregistered page request")
								w.WriteHeader(500)
								return
							}
							step := unified.Map(responses[next])
							next++
							if "https://"+r.Host+r.RequestURI != step["url"] || r.Method != step["method"] || (r.ProtoMajor == 2) != h2 {
								t.Error("page request identity/order/protocol changed")
								w.WriteHeader(500)
								return
							}
							if r.Header.Get("Cookie") != "" || r.Header.Get("Range") != "" {
								t.Error("unexpected page credential or media request")
							}
							for key, value := range unified.Map(step["headers"]) {
								if list := unified.Slice(value); list != nil {
									for _, v := range list {
										w.Header().Add(key, unified.Str(v))
									}
								} else {
									w.Header().Set(key, unified.Str(value))
								}
							}
							status, _ := unified.PHPInt(step["status"])
							body, err := base64.StdEncoding.DecodeString(unified.Str(step["body_b64"]))
							if err != nil {
								t.Error(err)
								w.WriteHeader(500)
								return
							}
							w.WriteHeader(int(status))
							_, _ = w.Write(body)
						}))
						now, _ := unified.PHPInt(input["now"])
						handler, err := api.New(registry, map[string]api.Adapter{"xiaohongshu": xiaohongshu.New(client)}, func() time.Time { return time.Unix(now, 0) }, nil)
						if err != nil {
							t.Fatal(err)
						}
						query := url.Values{"schema_version": {"1.0"}, "url": {unified.Str(input["url"])}}
						if view == "debug" {
							query.Set("debug", "1")
						}
						route := "parse"
						want := unified.Clone(base)
						if choice != "" {
							route = "resolve"
							query.Set("item_id", choice)
							if choice == "missing-item" {
								want = unified.Object{"schema_version": "1.0", "code": "CONTENT_UNAVAILABLE", "error": unified.Object{"code": "CONTENT_UNAVAILABLE", "message": "请求的媒体条目暂时不可用"}}
							} else {
								unified.Map(want["data"])["default_item_id"] = choice
							}
						}
						recorder := httptest.NewRecorder()
						handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/"+route+"?"+query.Encode(), nil))
						got, err := unified.Decode(recorder.Body.Bytes())
						if err != nil {
							t.Fatal(err)
						}
						exports = append(exports, unified.Object{"fixture_id": id, "http2": h2, "view": view, "item_id": choice, "status": recorder.Code, "payload": got})
						if !unified.Equal(got, want) {
							a, _ := unified.Encode(got)
							b, _ := unified.Encode(want)
							t.Fatalf("actual Chrome131 API != frozen PHP: got=%s want=%s", a, b)
						}
						if err := unified.ValidateEnvelopeAt(got, now); err != nil {
							t.Fatal(err)
						}
						mutex.Lock()
						consumed := next
						mutex.Unlock()
						if consumed != len(responses) {
							t.Fatalf("unused PHP request tape: %d/%d", consumed, len(responses))
						}
						statuses := map[string]int{"OK": 200, "INVALID_INPUT": 400, "UPSTREAM_BLOCKED": 403, "RATE_LIMITED": 429, "CONTENT_UNAVAILABLE": 404, "NO_MEDIA": 404, "UPSTREAM_INVALID_RESPONSE": 502}
						if recorder.Code != statuses[unified.Str(want["code"])] {
							t.Fatal("formal HTTP error status changed")
						}
						if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
							t.Fatal("formal HTTP safety headers changed")
						}
					})
				}
			}
		}
	}
	if output := os.Getenv("DOKITOOL_GO_XHS_TRANSPORT_OUTPUT"); output != "" {
		encoded, err := unified.Encode(unified.Object{"responses": exports})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, append(encoded, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if tested < 20 {
		t.Fatalf("unexpected loss of independent page oracle coverage: %d", tested)
	}
}
