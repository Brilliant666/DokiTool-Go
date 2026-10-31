package douyin

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

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/testsupport/publicoracle"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type fixtureExecutor struct {
	t         *testing.T
	responses array
	requests  array
	position  int
}

func fixtureHeader(fields object) http.Header {
	result := http.Header{}
	for key, value := range fields {
		if values := unified.Slice(value); values != nil {
			for _, value := range values {
				result.Add(key, unified.Str(value))
			}
		} else {
			result.Set(key, unified.Str(value))
		}
	}
	return result
}

func (f *fixtureExecutor) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return fetch.Response{}, err
	}
	if request.Policy.Purpose == "" || request.Policy.AllowedURL == nil || !request.Policy.AllowedURL(request.URL) || request.Policy.Timeout <= 0 || request.Policy.MaxBytes <= 0 {
		f.t.Fatalf("missing or violated request bounds: %s %s", request.Method, request.URL)
	}
	headers := object{}
	for key, values := range request.Header {
		headers[strings.ToLower(key)] = strings.Join(values, ", ")
	}
	f.requests = append(f.requests, object{"method": request.Method, "url": request.URL, "headers": headers, "follow_redirects": request.Policy.FollowRedirects})
	if f.position >= len(f.responses) {
		f.t.Fatalf("unexpected outbound request: %s %s", request.Method, request.URL)
	}
	want := unified.Map(f.responses[f.position])
	f.position++
	if request.Method != unified.Str(want["method"]) || request.URL != unified.Str(want["url"]) {
		f.t.Fatalf("request %d: got %s %s; want %s %s", f.position, request.Method, request.URL, unified.Str(want["method"]), unified.Str(want["url"]))
	}
	if message := unified.Str(want["error"]); message != "" {
		return fetch.Response{}, errors.New(message)
	}
	status, _ := unified.Numeric(want["status"])
	response := fetch.Response{StatusCode: int(status), Header: fixtureHeader(unified.Map(want["headers"])), Body: []byte(unified.Str(want["body"])), URL: request.URL}
	if value := unified.Str(want["final_url"]); value != "" {
		response.URL = value
	}
	for _, value := range unified.Slice(want["history"]) {
		response.History = append(response.History, unified.Str(value))
	}
	return response, nil
}

type fixtureProber struct {
	t        *testing.T
	results  array
	requests array
	position int
}

func (p *fixtureProber) Probe(ctx context.Context, raw string) (object, error) {
	p.t.Helper()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.requests = append(p.requests, raw)
	if p.position >= len(p.results) {
		p.t.Fatalf("unexpected probe: %s", raw)
	}
	want := unified.Map(p.results[p.position])
	p.position++
	if raw != unified.Str(want["url"]) {
		p.t.Fatalf("probe %d: got %s; want %s", p.position, raw, unified.Str(want["url"]))
	}
	if message := unified.Str(want["error"]); message != "" {
		return nil, errors.New(message)
	}
	return unified.Clone(unified.Map(want["result"])), nil
}

func phpDouyinFixtures(t *testing.T) array {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "php-douyin", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := unified.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if !unified.Equal(manifest["format_version"], 1) {
		t.Fatal("unsupported fixture manifest")
	}
	return unified.Slice(manifest["cases"])
}

func firstDifference(got, want any, path string) string {
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
				return fmt.Sprintf("%s.%s: present got=%v want=%v", path, key, ap, bp)
			}
			if difference := firstDifference(av, bv, path+"."+key); difference != "" {
				return difference
			}
		}
	}
	x, y := unified.Slice(got), unified.Slice(want)
	if x != nil && y != nil {
		if len(x) != len(y) {
			return fmt.Sprintf("%s: array length got=%d want=%d", path, len(x), len(y))
		}
		for i := range x {
			if difference := firstDifference(x[i], y[i], fmt.Sprintf("%s[%d]", path, i)); difference != "" {
				return difference
			}
		}
	}
	return fmt.Sprintf("%s: got %#v (%T); want %#v (%T)", path, got, got, want, want)
}

func requireSameWire(t *testing.T, got, want any) {
	t.Helper()
	if difference := firstDifference(got, want, "$"); difference != "" {
		t.Fatal(difference)
	}
}

func TestPHPDouyinBaseline(t *testing.T) {
	cases := phpDouyinFixtures(t)
	if len(cases) < 70 {
		t.Fatalf("missing PHP migration fixtures: %d", len(cases))
	}
	oracle, err := publicoracle.New()
	if err != nil {
		t.Fatal(err)
	}
	exported := array{}
	checkedViews := 0
	for _, value := range cases {
		c := unified.Map(value)
		t.Run(unified.Str(c["id"]), func(t *testing.T) {
			input, expected := unified.Map(c["input"]), unified.Map(c["expected"])
			now, _ := unified.Numeric(input["now"])
			executor := &fixtureExecutor{t: t, responses: unified.Slice(input["http_responses"]), requests: array{}}
			prober := &fixtureProber{t: t, results: unified.Slice(input["probe_results"]), requests: array{}}
			parser := New(executor, prober)
			full, err := parser.Parse(context.Background(), unified.Str(input["url"]), int64(now))
			if err != nil {
				t.Fatal(err)
			}
			actual := object{"id": c["id"]}
			for _, debug := range []bool{false, true} {
				view := "normal"
				if debug {
					view = "debug"
				}
				t.Run(view, func(t *testing.T) {
					got := unified.PublicView(full, debug, int64(now))
					actual[view] = got
					want, err := oracle.Expected(publicoracle.DouyinFixture, unified.Str(c["id"]), view, expected[view])
					if err != nil {
						t.Fatal(err)
					}
					requireSameWire(t, got, want)
					checkedViews++
					if err := unified.ValidateEnvelopeAt(got, int64(now)); err != nil {
						t.Fatal(err)
					}
				})
			}
			exported = append(exported, actual)
			t.Run("requests", func(t *testing.T) {
				wanted := array{}
				for _, value := range unified.Slice(expected["http_requests"]) {
					request := unified.Clone(unified.Map(value))
					delete(request, "transport") // The Go executor replaces PHP curl/get_headers.
					wanted = append(wanted, request)
				}
				requireSameWire(t, executor.requests, wanted)
				requireSameWire(t, prober.requests, expected["probe_requests"])
				if executor.position != len(executor.responses) || prober.position != len(prober.results) {
					t.Fatal("fixture response was not consumed")
				}
			})
			if expected["formatter"] != nil {
				t.Run("formatter", func(t *testing.T) {
					responses := array{}
					for _, value := range unified.Slice(input["http_responses"]) {
						request := unified.Map(value)
						if unified.Str(request["method"]) == "HEAD" && AllowedMediaURL(unified.Str(request["url"])) {
							responses = append(responses, request)
						}
					}
					formatter := New(&fixtureExecutor{t: t, responses: responses}, &fixtureProber{t: t, results: unified.Slice(input["probe_results"])})
					got, err := formatter.formatData(context.Background(), unified.Map(input["detail"]), stringValue(child(input, "detail", "aweme_id")))
					if err != nil {
						t.Fatal(err)
					}
					requireSameWire(t, got, expected["formatter"])
				})
			}
		})
	}
	if checkedViews == 2*len(cases) {
		if err := oracle.RequireConsumed(publicoracle.DouyinFixture); err != nil {
			t.Fatal(err)
		}
	}
	if destination := os.Getenv("DOKITOOL_GO_DOUYIN_OUTPUT"); destination != "" && !t.Failed() {
		if len(exported) != len(cases) || checkedViews != 2*len(cases) {
			t.Fatal("Go output export requires all PHP Douyin baseline cases")
		}
		if !filepath.IsAbs(destination) {
			t.Fatal("DOKITOOL_GO_DOUYIN_OUTPUT must be an absolute path")
		}
		manifestBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "php-douyin", "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := unified.Decode(manifestBytes)
		if err != nil {
			t.Fatal(err)
		}
		payload := object{"cases": exported, "renewal_groups": unified.CloneValue(manifest["renewal_groups"])}
		encoded, err := unified.Encode(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, append(encoded, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
