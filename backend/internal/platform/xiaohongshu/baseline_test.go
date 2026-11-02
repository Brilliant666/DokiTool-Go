package xiaohongshu

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type oracleTape struct {
	t                   *testing.T
	responses, requests unified.Array
	position            int
}

func (f *oracleTape) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return fetch.Response{}, err
	}
	if _, present := ctx.Deadline(); !present {
		f.t.Fatal("missing request deadline")
	}
	if request.Policy.AllowedURL == nil || !request.Policy.AllowedURL(request.URL) || request.Policy.FollowRedirects || request.Method != http.MethodGet || len(request.Body) != 0 {
		f.t.Fatal("lost fixed request policy")
	}
	if f.position >= len(f.responses) || f.position >= len(f.requests) {
		f.t.Fatalf("unexpected XHS request %s", request.URL)
	}
	step := unified.Map(f.responses[f.position])
	wanted := unified.Clone(unified.Map(f.requests[f.position]))
	headers := unified.Object{}
	for key, values := range request.Header {
		headers[strings.ToLower(key)] = strings.Join(values, ", ")
	}
	byteRange := strings.TrimPrefix(request.Header.Get("Range"), "bytes=")
	observed := unified.Object{"url": request.URL, "method": request.Method, "headers": headers, "body": string(request.Body),
		"follow_redirects": request.Policy.FollowRedirects, "timeout_ms": int64(request.Policy.Timeout / time.Millisecond), "range": byteRange}
	if byteRange == "" {
		if request.Policy.Purpose != "xiaohongshu-page" || request.Policy.MaxBytes != 8<<20 || request.Policy.Timeout <= 0 || request.Policy.Timeout > 10*time.Second || request.Policy.AllowedURL("https://example.com/") {
			f.t.Fatal("page budget/host boundary changed")
		}
		// CURLOPT_ENCODING="" lets cURL advertise compiled-in decoders. Go
		// deliberately supports the existing bounded gzip/deflate decoder set.
		if unified.Map(wanted["headers"])["accept-encoding"] != "" {
			f.t.Fatal("PHP decoder negotiation changed")
		}
		unified.Map(wanted["headers"])["accept-encoding"] = "gzip,deflate"
	} else {
		if request.Policy.Purpose != "media-probe" || request.Policy.MaxBytes != 65536 || request.Policy.MaxHeaderBytes != 16<<10 || !request.Policy.StrictMediaHeaders || request.Policy.Timeout <= 0 || request.Policy.Timeout > 6*time.Second || request.Policy.AllowedURL("https://127.0.0.1/private") {
			f.t.Fatal("container probe budget/public-address boundary changed")
		}
		// PHP's frozen microtime is constant; Go's actual cancellation clock
		// advances. Assert its tighter remaining bound, then compare every wire
		// field without pretending elapsed sub-millisecond values are identical.
		if limit, _ := unified.Numeric(wanted["timeout_ms"]); float64(request.Policy.Timeout/time.Millisecond) > limit {
			f.t.Fatal("Go probe exceeded the PHP time budget")
		}
		delete(observed, "timeout_ms")
		delete(wanted, "timeout_ms")
	}
	oracleEqual(f.t, observed, wanted)
	if request.URL != step["url"] || request.Method != step["method"] || byteRange != step["range"] {
		f.t.Fatal("PHP observation and upstream response tape differ")
	}
	f.position++
	if code, _ := unified.Numeric(step["error"]); code != 0 {
		return fetch.Response{}, errors.New("synthetic transport failure")
	}
	body, err := base64.StdEncoding.DecodeString(unified.Str(step["body_b64"]))
	if err != nil {
		f.t.Fatal(err)
	}
	status, _ := unified.Numeric(step["status"])
	reply := fetch.Response{StatusCode: int(status), Header: http.Header{}, Body: body, URL: request.URL}
	for key, value := range unified.Map(step["headers"]) {
		if values := unified.Slice(value); values != nil {
			for _, value := range values {
				reply.Header.Add(key, unified.Str(value))
			}
		} else {
			reply.Header.Set(key, unified.Str(value))
		}
	}
	return reply, nil
}

func oracleEqual(t *testing.T, got, want any) {
	t.Helper()
	if !unified.Equal(got, want) {
		actual, _ := unified.Encode(got)
		expected, _ := unified.Encode(want)
		t.Fatalf("XHS frozen PHP mismatch:\ngot: %s\nwant: %s", actual, expected)
	}
}

func fixtureParser(t *testing.T, input, expected unified.Object) (*Parser, *oracleTape) {
	t.Helper()
	tape := &oracleTape{t: t, responses: unified.Slice(input["http_responses"]), requests: unified.Slice(expected["http_requests"])}
	return NewWithClock(tape, func() time.Time { return time.Unix(100, 0) }), tape
}

func TestPHPXiaohongshuBaseline(t *testing.T) {
	body, err := os.ReadFile("testdata/php-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := unified.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	cases := unified.Slice(document["cases"])
	if document["baseline_commit"] != "25beaee107f8c1a208ced9deb3c637068445e316" || len(cases) != 37 {
		t.Fatal("wrong XHS oracle inventory")
	}
	exports := unified.Array{}
	for _, raw := range cases {
		entry := unified.Map(raw)
		t.Run(unified.Str(entry["id"]), func(t *testing.T) {
			input, expected := unified.Map(entry["input"]), unified.Map(entry["expected"])
			now, _ := unified.PHPInt(input["now"])
			parser, tape := fixtureParser(t, input, expected)
			full, err := parser.Parse(context.Background(), unified.Str(input["url"]), now)
			if err != nil {
				t.Fatal(err)
			}
			if tape.position != len(tape.responses) || tape.position != len(tape.requests) {
				t.Fatalf("unused upstream tape: consumed %d of %d", tape.position, len(tape.responses))
			}
			actual := unified.Object{"id": entry["id"]}
			for _, view := range []string{"normal", "debug"} {
				t.Run(view, func(t *testing.T) {
					payload := unified.PublicView(full, view == "debug", now)
					oracleEqual(t, payload, expected[view])
					if err := unified.ValidateEnvelopeAt(payload, now); err != nil {
						t.Fatal(err)
					}
					actual[view] = payload
				})
			}
			exports = append(exports, actual)
			t.Run("legacy-wire", func(t *testing.T) {
				parser, tape := fixtureParser(t, input, expected)
				legacy, err := parser.parseLegacy(context.Background(), unified.Str(input["url"]))
				if err != nil {
					t.Fatal(err)
				}
				wire, err := legacyWire(legacy)
				if err != nil {
					t.Fatal(err)
				}
				oracleEqual(t, wire, expected["legacy_response"])
				if tape.position != len(tape.responses) {
					t.Fatal("legacy parser left upstream tape unused")
				}
			})
		})
	}
	if destination := os.Getenv("DOKITOOL_GO_XIAOHONGSHU_OUTPUT"); destination != "" && !t.Failed() {
		if !filepath.IsAbs(destination) || len(exports) != len(cases) {
			t.Fatal("invalid/incomplete oracle export")
		}
		data, err := unified.Encode(unified.Object{"cases": exports})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
