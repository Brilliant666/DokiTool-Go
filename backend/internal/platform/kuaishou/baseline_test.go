package kuaishou

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// The response tape is only IO. Parser, request options, legacy
// formatting, adapter and public selection all run through production Go.
type oracleExecutor struct {
	t         *testing.T
	responses unified.Array
	requests  unified.Array
	position  int
}

func (f *oracleExecutor) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return fetch.Response{}, err
	}
	if _, ok := ctx.Deadline(); !ok {
		f.t.Fatal("missing shared parent deadline")
	}
	if request.Policy.Purpose == "" || request.Policy.AllowedURL == nil || !request.Policy.AllowedURL(request.URL) ||
		request.Policy.Timeout < time.Second || request.Policy.Timeout != 20*time.Second || !request.Policy.FollowRedirects || request.Policy.MaxRedirects != 5 || request.Policy.AutoReferer != (request.Method == http.MethodGet) {
		f.t.Fatalf("missing or violated request bounds: %s %s", request.Method, request.URL)
	}
	wantLimit := int64(8 << 20)
	if request.Policy.MaxBytes != wantLimit {
		f.t.Fatalf("response limit: got %d want %d", request.Policy.MaxBytes, wantLimit)
	}
	headers := unified.Object{}
	for key, values := range request.Header {
		headers[strings.ToLower(key)] = strings.Join(values, ", ")
	}
	f.requests = append(f.requests, unified.Object{"method": request.Method, "url": request.URL, "headers": headers,
		"body": string(request.Body), "follow_redirects": request.Policy.FollowRedirects, "auto_referer": request.Policy.AutoReferer, "max_redirects": request.Policy.MaxRedirects, "timeout_seconds": int64(request.Policy.Timeout / time.Second)})
	if f.position >= len(f.responses) {
		f.t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
	}
	want := unified.Map(f.responses[f.position])
	f.position++
	if request.Method != unified.Str(want["method"]) || request.URL != unified.Str(want["url"]) || request.Policy.FollowRedirects != want["follow_redirects"] {
		f.t.Fatalf("request %d got %s %s; want %#v", f.position, request.Method, request.URL, want)
	}
	if message := unified.Str(want["error"]); message != "" {
		return fetch.Response{}, errors.New(message)
	}
	status, _ := unified.Numeric(want["status"])
	response := fetch.Response{StatusCode: int(status), Header: http.Header{}, Body: []byte(unified.Str(want["body"])), URL: unified.Str(want["final_url"])}
	for key, raw := range unified.Map(want["headers"]) {
		if values := unified.Slice(raw); values != nil {
			for _, value := range values {
				response.Header.Add(key, unified.Str(value))
			}
		} else {
			response.Header.Set(key, unified.Str(raw))
		}
	}
	return response, nil
}

func oracleParser(t *testing.T, input unified.Object) (*Parser, *oracleExecutor) {
	t.Helper()
	executor := &oracleExecutor{t: t, responses: unified.Slice(input["http_responses"]), requests: unified.Array{}}
	return New(executor), executor
}

func oracleDifference(got, want any, path string) string {
	if unified.Equal(got, want) {
		return ""
	}
	a, b := unified.Map(got), unified.Map(want)
	if a != nil && b != nil {
		keys := map[string]bool{}
		for key := range a {
			keys[key] = true
		}
		for key := range b {
			keys[key] = true
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		for _, key := range ordered {
			av, ap := a[key]
			bv, bp := b[key]
			if ap != bp {
				return fmt.Sprintf("%s.%s: presence got=%v want=%v", path, key, ap, bp)
			}
			if difference := oracleDifference(av, bv, path+"."+key); difference != "" {
				return difference
			}
		}
	}
	x, y := unified.Slice(got), unified.Slice(want)
	if x != nil && y != nil {
		if len(x) != len(y) {
			return fmt.Sprintf("%s: length got=%d want=%d", path, len(x), len(y))
		}
		for index := range x {
			if difference := oracleDifference(x[index], y[index], fmt.Sprintf("%s[%d]", path, index)); difference != "" {
				return difference
			}
		}
	}
	return fmt.Sprintf("%s: got %#v (%T); want %#v (%T)", path, got, got, want, want)
}

func oracleEqual(t *testing.T, got, want any) {
	t.Helper()
	if difference := oracleDifference(got, want, "$"); difference != "" {
		t.Fatal(difference)
	}
}

func oracleRequests(t *testing.T, executor *oracleExecutor, expected unified.Object) {
	t.Helper()
	wanted := unified.Array{}
	for _, raw := range unified.Slice(expected["http_requests"]) {
		request := unified.Clone(unified.Map(raw))
		// PHP records explicitly configured cURL headers; Go declares this
		// transport header directly. Keep the sole difference exact and visible.
		if request["method"] == http.MethodHead {
			unified.Map(request["headers"])["accept"] = "*/*"
		}
		wanted = append(wanted, request)
	}
	oracleEqual(t, executor.requests, wanted)
	if executor.position != len(executor.responses) {
		t.Fatal("unused HTTP tape entries")
	}
}

func TestPHPKuaishouBaseline(t *testing.T) {
	encoded, err := os.ReadFile(filepath.Join("testdata", "php-baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := unified.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !unified.Equal(document["format_version"], 1) || document["baseline_commit"] != "25beaee107f8c1a208ced9deb3c637068445e316" {
		t.Fatal("unexpected PHP oracle provenance")
	}
	cases := unified.Slice(document["cases"])
	if len(cases) != 40 {
		t.Fatalf("incomplete PHP cases: %d", len(cases))
	}
	exported := unified.Array{}
	checkedViews := 0
	for _, raw := range cases {
		entry := unified.Map(raw)
		t.Run(unified.Str(entry["id"]), func(t *testing.T) {
			input, expected := unified.Map(entry["input"]), unified.Map(entry["expected"])
			number, ok := unified.Numeric(input["now"])
			if !ok {
				t.Fatal("public fixture clock missing")
			}
			now := int64(number)
			parser, executor := oracleParser(t, input)
			full, err := parser.Parse(context.Background(), unified.Str(input["url"]), now)
			if err != nil {
				t.Fatal(err)
			}
			actual := unified.Object{"id": entry["id"]}
			for _, debug := range []bool{false, true} {
				view := "normal"
				if debug {
					view = "debug"
				}
				t.Run(view, func(t *testing.T) {
					got := unified.PublicView(full, debug, now)
					actual[view] = got
					oracleEqual(t, got, expected[view])
					if err := unified.ValidateEnvelopeAt(got, now); err != nil {
						t.Fatal(err)
					}
					checkedViews++
				})
			}
			exported = append(exported, actual)
			t.Run("requests", func(t *testing.T) { oracleRequests(t, executor, expected) })
			t.Run("legacy-wire", func(t *testing.T) {
				legacyParser, legacyExecutor := oracleParser(t, input)
				legacy, err := legacyParser.parseLegacy(context.Background(), unified.Str(input["url"]))
				if err != nil {
					t.Fatal(err)
				}
				oracleEqual(t, legacy, expected["parser_response"])
				wire, err := legacyWire(legacy)
				if err != nil {
					t.Fatal(err)
				}
				oracleEqual(t, wire, expected["legacy_response"])
				oracleRequests(t, legacyExecutor, expected)
			})
		})
	}
	if destination := os.Getenv("DOKITOOL_GO_KUAISHOU_OUTPUT"); destination != "" && !t.Failed() {
		if len(exported) != len(cases) || checkedViews != len(cases)*2 || !filepath.IsAbs(destination) {
			t.Fatal("export requires all independent cases/views and an absolute destination")
		}
		body, err := unified.Encode(unified.Object{"cases": exported})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, append(body, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
