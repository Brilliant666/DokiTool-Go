package bilibili

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
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type fixtureExecutor struct {
	t *testing.T
	responses array
	requests array
	position int
}

func (f *fixtureExecutor) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil { return fetch.Response{}, err }
	if request.Policy.Purpose == "" || request.Policy.AllowedURL == nil || !request.Policy.AllowedURL(request.URL) || request.Policy.Timeout <= 0 || request.Policy.MaxBytes <= 0 {
		f.t.Fatalf("missing or violated request bounds: %s %s", request.Method, request.URL)
	}
	headers := object{}
	for key, values := range request.Header {
		// An explicit empty User-Agent suppresses net/http's default on the wire.
		// PHP's ordinary cURL request also sends no User-Agent header.
		if strings.EqualFold(key, "User-Agent") && len(values) == 1 && values[0] == "" { continue }
		headers[strings.ToLower(key)] = strings.Join(values, ", ")
	}
	f.requests = append(f.requests, object{"method": request.Method, "url": request.URL, "headers": headers, "follow_redirects": request.Policy.FollowRedirects})
	if f.position >= len(f.responses) { f.t.Fatalf("unexpected request: %s %s", request.Method, request.URL) }
	want := unified.Map(f.responses[f.position])
	f.position++
	if request.Method != unified.Str(want["method"]) || request.URL != unified.Str(want["url"]) || request.Policy.FollowRedirects != want["follow_redirects"] {
		f.t.Fatalf("request %d: got %s %s follow=%v; want %#v", f.position, request.Method, request.URL, request.Policy.FollowRedirects, want)
	}
	if message := unified.Str(want["error"]); message != "" { return fetch.Response{}, errors.New(message) }
	response := fetch.Response{StatusCode: int(intValue(want["status"])), Body: []byte(unified.Str(want["body"])), URL: request.URL, Header: http.Header{}}
	for key, value := range unified.Map(want["headers"]) { response.Header.Set(key, unified.Str(value)) }
	if final := unified.Str(want["final_url"]); final != "" {
		if request.Policy.FollowRedirects && !request.Policy.AllowedURL(final) { f.t.Fatalf("fixture redirect violates the real fetch policy: %s", final) }
		response.URL = final
	}
	return response, nil
}

func fixtures(t *testing.T) array {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "php-baseline.json"))
	if err != nil { t.Fatal(err) }
	document, err := unified.Decode(data)
	if err != nil { t.Fatal(err) }
	if !unified.Equal(document["format_version"], 1) || document["baseline_commit"] != "25beaee107f8c1a208ced9deb3c637068445e316" { t.Fatal("unexpected PHP baseline identity") }
	return unified.Slice(document["cases"])
}

func difference(got, want any, path string) string {
	if unified.Equal(got, want) { return "" }
	a, b := unified.Map(got), unified.Map(want)
	if a != nil && b != nil {
		keys := map[string]bool{}
		for key := range a { keys[key] = true }
		for key := range b { keys[key] = true }
		ordered := make([]string, 0, len(keys))
		for key := range keys { ordered = append(ordered, key) }
		sort.Strings(ordered)
		for _, key := range ordered {
			av, ap := a[key]; bv, bp := b[key]
			if ap != bp { return fmt.Sprintf("%s.%s: present got=%v want=%v", path, key, ap, bp) }
			if result := difference(av, bv, path+"."+key); result != "" { return result }
		}
	}
	x, y := unified.Slice(got), unified.Slice(want)
	if x != nil && y != nil {
		if len(x) != len(y) { return fmt.Sprintf("%s: length got=%d want=%d", path, len(x), len(y)) }
		for i := range x { if result := difference(x[i], y[i], fmt.Sprintf("%s[%d]", path, i)); result != "" { return result } }
	}
	return fmt.Sprintf("%s: got %#v (%T); want %#v (%T)", path, got, got, want, want)
}

func sameWire(t *testing.T, got, want any) {
	t.Helper()
	if result := difference(got, want, "$"); result != "" { t.Fatal(result) }
}

func compareRequests(t *testing.T, executor *fixtureExecutor, expected any) {
	t.Helper()
	wanted := array{}
	for _, value := range unified.Slice(expected) {
		request := unified.Clone(unified.Map(value))
		delete(request, "transport") // Injected Go fetch replaces cURL, not the request's business purpose.
		headers := unified.Map(request["headers"])
		// PHP's empty CURLOPT_ENCODING advertises its build's supported codecs.
		// Go advertises exactly the bounded gzip/deflate decoders in fetch.Client.
		// This sole transport difference leaves the frozen PHP oracle unchanged.
		if encoding, exists := headers["accept-encoding"]; exists && encoding == "" { headers["accept-encoding"] = "gzip,deflate" }
		wanted = append(wanted, request)
	}
	sameWire(t, executor.requests, wanted)
	if executor.position != len(executor.responses) { t.Fatal("unused upstream fixture response") }
}

func TestPHPBilibiliBaseline(t *testing.T) {
	cases := fixtures(t)
	if len(cases) < 51 { t.Fatalf("missing frozen Bilibili scenarios: %d", len(cases)) }
	exported := array{}
	checkedViews := 0
	for _, value := range cases {
		c := unified.Map(value)
		t.Run(unified.Str(c["id"]), func(t *testing.T) {
			input, expected := unified.Map(c["input"]), unified.Map(c["expected"])
			now := intValue(input["now"])
			executor := &fixtureExecutor{t: t, responses: unified.Slice(input["http_responses"]), requests: array{}}
			full, err := New(executor).Parse(context.Background(), unified.Str(input["url"]), now)
			if err != nil { t.Fatal(err) }
			actual := object{"id": c["id"]}
			for _, debug := range []bool{false, true} {
				view := "normal"
				if debug { view = "debug" }
				t.Run(view, func(t *testing.T) {
					got := unified.PublicView(full, debug, now)
					actual[view] = got
					sameWire(t, got, expected[view])
					if err := unified.ValidateEnvelopeAt(got, now); err != nil { t.Fatal(err) }
					checkedViews++
				})
			}
			exported = append(exported, actual)
			t.Run("requests", func(t *testing.T) { compareRequests(t, executor, expected["http_requests"]) })
			t.Run("legacy-wire", func(t *testing.T) {
				legacyExecutor := &fixtureExecutor{t: t, responses: unified.Slice(input["http_responses"]), requests: array{}}
				raw, err := New(legacyExecutor).parseLegacy(context.Background(), unified.Str(input["url"]))
				if err != nil { t.Fatal(err) }
				wire, err := legacyWire(raw)
				if err != nil { t.Fatal(err) }
				sameWire(t, wire, expected["legacy_response"])
				compareRequests(t, legacyExecutor, expected["http_requests"])
			})
		})
	}
	if destination := os.Getenv("DOKITOOL_GO_BILIBILI_OUTPUT"); destination != "" && !t.Failed() {
		if len(exported) != len(cases) || checkedViews != len(cases)*2 { t.Fatal("export requires every PHP Bilibili case and view") }
		if !filepath.IsAbs(destination) { t.Fatal("DOKITOOL_GO_BILIBILI_OUTPUT must be absolute") }
		encoded, err := unified.Encode(object{"cases": exported})
		if err != nil { t.Fatal(err) }
		if err := os.WriteFile(destination, append(encoded, '\n'), 0o600); err != nil { t.Fatal(err) }
	}
}
