package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	weiboplatform "github.com/Brilliant666/DokiTool-Go/backend/internal/platform/weibo"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var weiboOracleNonce = regexp.MustCompile(`([?&]_rand=)[^&]*`)

// The independent PHP oracle executes the original parser, entry encoder,
// internal adapter and actual public gateway. Only upstream IO and the clock
// are controlled. No expected ID, signature or public field is produced by Go.
type weiboSignedOracleTape struct {
	t                   *testing.T
	responses, requests unified.Array
	next                int
}

func (f *weiboSignedOracleTape) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return fetch.Response{}, err
	}
	if _, ok := ctx.Deadline(); !ok {
		f.t.Fatal("Weibo metadata lost its total deadline")
	}
	if request.Policy.Purpose != "weibo-metadata" || request.Policy.FollowRedirects || request.Policy.MaxBytes != 2<<20 ||
		request.Policy.AllowedURL == nil || !request.Policy.AllowedURL(request.URL) || request.Policy.AllowedURL("https://example.com/") ||
		request.Policy.Timeout <= 0 || request.Policy.Timeout > 20*time.Second {
		f.t.Fatal("Weibo metadata lost its approved outbound policy")
	}
	if f.next >= len(f.responses) || f.next >= len(f.requests) {
		f.t.Fatalf("unexpected metadata request: %s %s", request.Method, request.URL)
	}
	want := unified.Map(f.responses[f.next])
	headers := unified.Object{}
	for name, values := range request.Header {
		// PHP's cURL adds this Content-Type for CURLOPT_POSTFIELDS. Its option
		// recorder contains explicit headers only; Go must supply the same wire
		// value itself. No other header difference is permitted.
		if strings.EqualFold(name, "Content-Type") && request.Method == http.MethodPost {
			if strings.Join(values, ", ") != "application/x-www-form-urlencoded" {
				f.t.Fatal("incorrect form content type")
			}
			continue
		}
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	var post any
	if request.Method == http.MethodPost {
		if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			f.t.Fatal("POST omitted its form content type")
		}
		post = string(request.Body)
	} else if len(request.Body) != 0 {
		f.t.Fatal("GET request unexpectedly has a body")
	}
	observed := unified.Object{"url": weiboOracleNonce.ReplaceAllString(request.URL, "${1}<nonce>"), "method": request.Method, "post": post, "headers": headers}
	weiboSignedSameJSON(f.t, observed, f.requests[f.next])
	if observed["url"] != want["url"] || observed["method"] != want["method"] || !unified.Equal(post, want["post"]) {
		f.t.Fatal("PHP request observation disagrees with controlled upstream tape")
	}
	f.next++
	if want["error"] == true {
		return fetch.Response{}, errors.New("synthetic metadata transport failure")
	}
	status, _ := unified.Numeric(want["status"])
	return fetch.Response{StatusCode: int(status), Header: http.Header{}, Body: []byte(unified.Str(want["body"])), URL: request.URL}, nil
}

func weiboSignedSameJSON(t *testing.T, got, want any) {
	t.Helper()
	if !unified.Equal(got, want) {
		actual, _ := unified.Encode(got)
		expected, _ := unified.Encode(want)
		t.Fatalf("signed Weibo PHP oracle differs:\ngot:  %s\nwant: %s", actual, expected)
	}
}

func TestWeiboHTTPSignedPHPOracle(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "platform", "weibo", "testdata", "php-signed-baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := unified.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	fixtures := unified.Slice(document["cases"])
	if document["baseline_commit"] != "25beaee107f8c1a208ced9deb3c637068445e316" || len(fixtures) != 35 {
		t.Fatal("unexpected signed PHP oracle inventory")
	}
	registry, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	exports := unified.Array{}
	for _, raw := range fixtures {
		fixture := unified.Map(raw)
		id := unified.Str(fixture["id"])
		if id == "" || seen[id] {
			t.Fatal("empty or duplicate signed oracle ID")
		}
		seen[id] = true
		input, expected := unified.Map(fixture["input"]), unified.Map(fixture["expected"])
		now, validNow := unified.PHPInt(input["now"])
		if !validNow {
			t.Fatal("invalid frozen clock")
		}
		exported := unified.Object{"id": id}
		for _, view := range []string{"normal", "debug"} {
			t.Run(id+"/"+view, func(t *testing.T) {
				tape := &weiboSignedOracleTape{t: t, responses: unified.Slice(input["http_responses"]), requests: unified.Slice(expected["http_requests"])}
				handler, err := New(registry, map[string]Adapter{"weibo": weiboplatform.New(tape)},
					func() time.Time { return time.Unix(now, 0) }, weiboplatform.PreparePublic(unified.Str(input["key"])))
				if err != nil {
					t.Fatal(err)
				}
				query := url.Values{"schema_version": {"1.0"}, "url": {unified.Str(input["url"])}}
				if view == "debug" {
					query.Set("debug", "1")
				}
				if input["route"] == "resolve" {
					query.Set("item_id", unified.Str(input["item_id"]))
				}
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/"+unified.Str(input["route"])+"?"+query.Encode(), nil))
				if tape.next != len(tape.responses) || tape.next != len(tape.requests) {
					t.Fatalf("unused upstream tape: consumed %d, responses %d, requests %d", tape.next, len(tape.responses), len(tape.requests))
				}
				for name, want := range map[string]string{"Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Access-Control-Allow-Origin": ""} {
					if recorder.Header().Get(name) != want {
						t.Fatalf("incorrect public header %s", name)
					}
				}
				payload, err := unified.Decode(recorder.Body.Bytes())
				if err != nil {
					t.Fatal(err)
				}
				weiboSignedSameJSON(t, unified.Object{"status": recorder.Code, "payload": payload},
					unified.Object{"status": expected[view+"_status"], "payload": expected[view]})
				if err := unified.ValidateEnvelopeAt(payload, now); err != nil {
					t.Fatal(err)
				}
				exported[view] = payload
			})
		}
		exports = append(exports, exported)
	}
	if path := os.Getenv("DOKITOOL_GO_WEIBO_OUTPUT"); path != "" && !t.Failed() {
		data, err := unified.Encode(unified.Object{"cases": exports})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
