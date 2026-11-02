package douyin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/probe"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// This executor replays bytes emitted by the archived PHP product path. It
// verifies each requested slice; it never calls a network or manufactures media.
type php25Executor struct {
	t        *testing.T
	steps    array
	bodies   object
	position int
}

func (e *php25Executor) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	e.t.Helper()
	if err := ctx.Err(); err != nil {
		return fetch.Response{}, err
	}
	if e.position >= len(e.steps) {
		e.t.Fatalf("unexpected request beyond PHP trace: %s", request.URL)
	}
	step := unified.Map(e.steps[e.position])
	e.position++
	offset, _ := unified.Numeric(step["offset"])
	length, _ := unified.Numeric(step["length"])
	wantRange := fmt.Sprintf("bytes=%d-%d", int64(offset), int64(offset+length)-1)
	if request.Method != http.MethodGet || request.URL != unified.Str(step["url"]) || request.Header.Get("Range") != wantRange {
		e.t.Fatalf("request %d differs from PHP: %s %s %s; expected %s %s", e.position, request.Method, request.URL, request.Header.Get("Range"), unified.Str(step["url"]), wantRange)
	}
	if request.Policy.AllowedURL == nil || !request.Policy.AllowedURL(request.URL) || request.Policy.FollowRedirects || request.Policy.MaxBytes <= 0 || request.Policy.MaxBytes > probe.MaxBytes {
		e.t.Fatal("missing probe request bounds")
	}
	response := unified.Map(step["response"])
	if response == nil {
		return fetch.Response{}, errors.New("fixed PHP transport unavailable")
	}
	key := unified.Str(response["body_sha256"])
	body, err := base64.StdEncoding.DecodeString(unified.Str(e.bodies[key]))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(body)) != key {
		e.t.Fatal("missing or corrupt frozen response body: " + key)
	}
	status, _ := unified.Numeric(response["status"])
	return fetch.Response{StatusCode: int(status), Header: fixtureHeader(unified.Map(response["headers"])), Body: body, URL: request.URL}, nil
}

type php25Prober struct {
	t        *testing.T
	actual   *probe.Probe
	input    object
	observed object
	calls    int
}

func (p *php25Prober) Probe(ctx context.Context, raw string) (object, error) {
	p.t.Helper()
	if raw != unified.Str(p.input["probe_url"]) {
		p.t.Fatal("formatter changed the PHP original endpoint")
	}
	p.calls++
	if override := unified.Str(child(p.input, "spec", "entry")); override != "" {
		raw = override
	}
	var err error
	p.observed, err = p.actual.Probe(ctx, raw)
	return p.observed, err
}

func TestPHP25ProbeProductPath(t *testing.T) {
	path := filepath.Join("..", "..", "..", "testdata", "php-25beaee", "cases.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := unified.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	cases := unified.Slice(fixture["cases"])
	if len(cases) != 12 {
		t.Fatalf("expected all 12 PHP 25beaee product cases, got %d", len(cases))
	}
	exported := array{}
	for _, value := range cases {
		c := unified.Map(value)
		t.Run(unified.Str(c["id"]), func(t *testing.T) {
			input, expected := unified.Map(c["input"]), unified.Map(c["expected"])
			executor := &php25Executor{t: t, steps: unified.Slice(expected["transport"]), bodies: unified.Map(fixture["body_base64_by_sha256"])}
			prober := &php25Prober{t: t, actual: probe.New(executor, AllowedMediaURL), input: input}
			parser := New(nil, prober)
			raw, err := parser.formatData(context.Background(), unified.Map(input["detail"]), unified.Str(input["video_id"]))
			if err != nil {
				t.Fatal(err)
			}
			requireSameWire(t, raw, expected["formatter"])
			if prober.calls != 1 || executor.position != len(executor.steps) {
				t.Fatalf("probe calls/trace consumption differs: %d / %d of %d", prober.calls, executor.position, len(executor.steps))
			}
			wantProbe := unified.CloneValue(expected["probe"])
			if object := unified.Map(wantProbe); object != nil {
				if empty, ok := object["metadata"].([]any); ok && len(empty) == 0 {
					object["metadata"] = unified.Object{}
				}
			}
			var actualProbe any
			if prober.observed != nil {
				actualProbe = prober.observed
			}
			requireSameWire(t, actualProbe, wantProbe)
			now, _ := unified.Numeric(input["now"])
			full := adapt(raw, unified.Str(input["url"]), int64(now))
			actual := object{"id": c["id"]}
			for _, debug := range []bool{false, true} {
				view := "normal"
				if debug {
					view = "debug"
				}
				got := unified.PublicView(full, debug, int64(now))
				requireSameWire(t, got, expected[view])
				if err := unified.ValidateEnvelopeAt(got, int64(now)); err != nil {
					t.Fatal(err)
				}
				actual[view] = got
			}
			exported = append(exported, actual)
		})
	}
	if destination := os.Getenv("DOKITOOL_GO_PHP25_OUTPUT"); destination != "" && !t.Failed() {
		if !filepath.IsAbs(destination) || len(exported) != len(cases) {
			t.Fatal("complete PHP25 export requires an absolute destination")
		}
		encoded, err := unified.Encode(object{"cases": exported, "php_baseline": "25beaee107f8c1a208ced9deb3c637068445e316"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, append(encoded, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
