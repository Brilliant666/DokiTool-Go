package probe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// These expected values and exact requested byte ranges come from the frozen
// PHP implementation, not from the Go metadata parser or fixture constructors.
func TestFrozenPHPProbeBaseline(t *testing.T) {
	data, err := os.ReadFile("../../testdata/php-douyin/probe.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		FormatVersion int `json:"format_version"`
		Cases         []struct {
			ID        string          `json:"id"`
			Operation string          `json:"operation"`
			Input     json.RawMessage `json:"input"`
			Expected  json.RawMessage `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil || fixture.FormatVersion != 1 || len(fixture.Cases) == 0 {
		t.Fatalf("invalid frozen fixture: %v", err)
	}
	for _, c := range fixture.Cases {
		t.Run(c.ID, func(t *testing.T) {
			expected, err := unified.DecodeValue(c.Expected)
			if err != nil {
				t.Fatal(err)
			}
			var actual any
			switch c.Operation {
			case "metadata":
				var input struct {
					MoovBase64 string `json:"moov_base64"`
					FileSize   int64  `json:"file_size"`
				}
				if err := json.Unmarshal(c.Input, &input); err != nil {
					t.Fatal(err)
				}
				moov, err := base64.StdEncoding.DecodeString(input.MoovBase64)
				if err != nil {
					t.Fatal(err)
				}
				if metadata := Metadata(moov, input.FileSize); metadata != nil {
					actual = metadata
				}
			case "probe":
				var input struct {
					URL       string `json:"url"`
					Transport []struct {
						URL      string `json:"url"`
						Offset   int64  `json:"offset"`
						Length   int64  `json:"length"`
						Response *struct {
							Status     int               `json:"status"`
							Headers    map[string]string `json:"headers"`
							BodyBase64 string            `json:"body_base64"`
						} `json:"response"`
					} `json:"transport"`
				}
				if err := json.Unmarshal(c.Input, &input); err != nil {
					t.Fatal(err)
				}
				calls := 0
				executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
					if calls >= len(input.Transport) {
						t.Fatalf("made extra request beyond PHP trace: %s %s", request.URL, request.Header.Get("Range"))
					}
					step := input.Transport[calls]
					calls++
					offset, length := requestedRange(t, request)
					if request.URL != step.URL || offset != step.Offset || length != step.Length {
						t.Fatalf("request %d differs from PHP: got %s %d/%d; want %s %d/%d", calls, request.URL, offset, length, step.URL, step.Offset, step.Length)
					}
					if step.Response == nil {
						return fetch.Response{}, errors.New("frozen PHP transport failure")
					}
					body, err := base64.StdEncoding.DecodeString(step.Response.BodyBase64)
					if err != nil {
						t.Fatal(err)
					}
					header := make(http.Header, len(step.Response.Headers))
					for key, value := range step.Response.Headers {
						header.Set(key, value)
					}
					return fetch.Response{StatusCode: step.Response.Status, Header: header, Body: body, URL: request.URL}, nil
				})
				// This exact fixture-only allowlist is not the platform policy;
				// production Douyin injects douyin.AllowedMediaURL separately.
				allowed := func(raw string) bool {
					u, err := url.Parse(raw)
					if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
						return false
					}
					return u.Host == "aweme.snssdk.com" || u.Host == "v5-test.douyinvod.com" || u.Host == "v5-unavailable.douyinvod.com"
				}
				result, err := New(executor, allowed).Probe(context.Background(), input.URL)
				if err != nil {
					t.Fatal(err)
				}
				if calls != len(input.Transport) {
					t.Fatalf("used %d requests; PHP used %d", calls, len(input.Transport))
				}
				if result != nil {
					actual = result
				}
				// PHP's private unknown associative metadata serializes as [].
				// The Go Prober interface deliberately uses an empty Object here;
				// it has no effect on the public unified-v1 wire representation.
				if object := unified.Map(expected); object != nil {
					if values, ok := object["metadata"].([]any); ok && len(values) == 0 {
						object["metadata"] = unified.Object{}
					}
				}
			default:
				t.Fatalf("unsupported frozen operation %q", c.Operation)
			}
			if !unified.Equal(actual, expected) {
				got, _ := unified.Encode(actual)
				want, _ := unified.Encode(expected)
				t.Fatalf("PHP mismatch\nactual: %s\nexpected: %s", got, want)
			}
		})
	}
}
