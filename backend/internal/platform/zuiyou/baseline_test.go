package zuiyou

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type oracleTape struct {
	t                   *testing.T
	responses, requests unified.Array
	next                int
}

func (f *oracleTape) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return fetch.Response{}, err
	}
	deadline, valid := ctx.Deadline()
	if !valid || time.Until(deadline) <= 0 || time.Until(deadline) > 35*time.Second {
		f.t.Fatal("Zuiyou request lost total cancellation budget")
	}
	if f.next >= len(f.responses) || f.next >= len(f.requests) {
		f.t.Fatal("unexpected metadata request", request.URL)
	}
	response, expected := unified.Map(f.responses[f.next]), unified.Clone(unified.Map(f.requests[f.next]))
	if request.Method == http.MethodHead {
		// libcurl adds this implicit wire header; the PHP recorder observes
		// explicit options. This is the sole request-comparison normalization.
		unified.Map(expected["headers"])["accept"] = "*/*"
	}
	headers := unified.Object{}
	for name, values := range request.Header {
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	got := unified.Object{"url": request.URL, "method": request.Method, "body": string(request.Body), "headers": headers,
		"follow_redirects": request.Policy.FollowRedirects, "timeout_seconds": int64(request.Policy.Timeout / time.Second)}
	oracleSame(f.t, got, expected)
	policy := request.Policy
	if policy.MaxBytes != 8<<20 || policy.AllowedURL == nil || !policy.AllowedURL(request.URL) || policy.AllowedURL("https://127.0.0.1/") || policy.AllowedURL("https://example.com/") {
		f.t.Fatal("Zuiyou metadata authorization/size boundary changed")
	}
	if request.Method == http.MethodHead {
		if policy.Purpose != "zuiyou-share" || policy.MaxRedirects != 0 || policy.FollowRedirects || policy.Timeout != 12*time.Second {
			f.t.Fatal("share request must be one bounded HEAD")
		}
	} else if request.URL != detailURL || policy.Purpose != "zuiyou-detail" || policy.MaxRedirects != 5 || !policy.FollowRedirects || policy.Timeout != 20*time.Second {
		f.t.Fatal("detail request left the fixed API policy")
	}
	f.next++
	if message := unified.Str(response["error"]); message != "" {
		return fetch.Response{}, errors.New(message)
	}
	status, _ := unified.PHPInt(response["status"])
	result := fetch.Response{StatusCode: int(status), Header: http.Header{}, Body: []byte(unified.Str(response["body"])), URL: request.URL}
	if request.Method == http.MethodHead {
		parsed, err := http.ReadResponse(bufio.NewReader(strings.NewReader(unified.Str(response["body"]))), &http.Request{Method: http.MethodHead})
		if err != nil {
			f.t.Fatal(err)
		}
		result.Header, result.Body = parsed.Header, nil
		_ = parsed.Body.Close()
	}
	return result, nil
}

func oracleSame(t *testing.T, got, want any) {
	t.Helper()
	if !unified.Equal(got, want) {
		actual, _ := unified.Encode(got)
		expected, _ := unified.Encode(want)
		t.Fatalf("Zuiyou differs from fixed PHP:\nGo  %s\nPHP %s", actual, expected)
	}
}

func TestPHPZuiyouParserAndEntry(t *testing.T) {
	body, err := os.ReadFile("testdata/php-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := unified.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	cases := unified.Slice(document["cases"])
	if document["baseline_commit"] != "25beaee107f8c1a208ced9deb3c637068445e316" || document["baseline_tree"] != "e4b9d40f5e6c12a86fff629c105553886b434dcd" || len(cases) != 48 {
		t.Fatal("unexpected PHP Zuiyou oracle provenance/inventory")
	}
	seen := map[string]bool{}
	for _, value := range cases {
		entry := unified.Map(value)
		id := unified.Str(entry["id"])
		if id == "" || seen[id] {
			t.Fatal("duplicate/missing case ID")
		}
		seen[id] = true
		t.Run(id, func(t *testing.T) {
			input, expected := unified.Map(entry["input"]), unified.Map(entry["expected"])
			tape := &oracleTape{t: t, responses: unified.Slice(input["http_responses"]), requests: unified.Slice(expected["http_requests"])}
			parser := New(tape)
			legacy, err := parser.parseLegacy(context.Background(), unified.Str(input["url"]))
			if err != nil {
				t.Fatal(err)
			}
			oracleSame(t, legacy, expected["parser_response"])
			wire, err := legacyWire(legacy)
			if err != nil {
				t.Fatal(err)
			}
			oracleSame(t, wire, expected["legacy_response"])
			if tape.next != len(tape.responses) || tape.next != len(tape.requests) {
				t.Fatal("unconsumed PHP request tape")
			}
		})
	}
}
