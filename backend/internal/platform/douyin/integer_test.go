package douyin

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/testsupport/publicoracle"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func TestPHPIntegerBoundaryBaseline(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "php-integer-boundaries.json"))
	if err != nil {
		t.Fatal(err)
	}
	if actual := fmt.Sprintf("%x", sha256.Sum256(data)); actual != "725412ca898a7ae20d632395c80803716b05865e9e01908520caa2a6c96e1046" {
		t.Fatal("frozen PHP integer oracle bytes changed; review and regenerate explicitly")
	}
	manifest, err := unified.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if manifest["baseline_commit"] != "665bd55134982b9fe107aa1cd00373ba8da8ff3c" ||
		manifest["baseline_tree"] != "93744d4d718c3299f82640d2888e93252106db70" ||
		manifest["php_version"] != "8.3.6" || len(unified.Slice(manifest["cases"])) != 13 ||
		len(unified.Slice(manifest["direct_cases"])) != 12 || len(unified.Map(manifest["source_sha256"])) != 12 {
		t.Fatal("incomplete archived PHP integer boundary oracle")
	}
	producers := unified.Map(manifest["producer_sha256"])
	if len(producers) != 2 {
		t.Fatal("missing PHP integer oracle producers")
	}
	for name, expected := range producers {
		content, err := os.ReadFile(filepath.Join("..", "..", "..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(content)) != expected {
			t.Fatalf("PHP integer oracle producer changed: %s", name)
		}
	}
	seen := make(map[string]bool)
	for _, group := range []string{"cases", "direct_cases"} {
		for _, value := range unified.Slice(manifest[group]) {
			c := unified.Map(value)
			id := unified.Str(c["id"])
			if id == "" || seen[id] || unified.Map(c["input"]) == nil || c["expected"] == nil {
				t.Fatal("invalid PHP integer oracle inventory")
			}
			seen[id] = true
		}
	}
	oracle, err := publicoracle.New()
	if err != nil {
		t.Fatal(err)
	}
	exported := unified.Array{}
	completedChecks, expectedChecks := 0, 0
	for _, value := range unified.Slice(manifest["cases"]) {
		c := unified.Map(value)
		expectedChecks += 2
		if unified.Map(c["expected"])["formatter"] != nil {
			expectedChecks++
		}
		t.Run(unified.Str(c["id"]), func(t *testing.T) {
			input, expected := unified.Map(c["input"]), unified.Map(c["expected"])
			now, _ := unified.PHPInt(input["now"])
			executor := &fixtureExecutor{t: t, responses: unified.Slice(input["http_responses"])}
			prober := &fixtureProber{t: t, results: unified.Slice(input["probe_results"])}
			actual, err := New(executor, prober).Parse(context.Background(), unified.Str(input["url"]), now)
			if err != nil {
				t.Fatal(err)
			}
			entry := unified.Object{"id": c["id"]}
			for _, view := range []string{"normal", "debug"} {
				t.Run(view, func(t *testing.T) {
					public := unified.PublicView(actual, view == "debug", now)
					want, err := oracle.Expected(publicoracle.IntegerFixture, unified.Str(c["id"]), view, expected[view])
					if err != nil {
						t.Fatal(err)
					}
					requireSameWire(t, public, want)
					if err := unified.ValidateEnvelopeAt(public, now); err != nil {
						t.Fatal(err)
					}
					entry[view] = public
					completedChecks++
				})
			}
			if expected["formatter"] != nil {
				t.Run("formatter", func(t *testing.T) {
					formatted, err := New(nil, nil).formatData(context.Background(), unified.Map(input["detail"]), "7628438784602389247")
					if err != nil {
						t.Fatal(err)
					}
					requireSameWire(t, formatted, expected["formatter"])
					completedChecks++
				})
			}
			if executor.position != len(executor.responses) || prober.position != len(prober.results) {
				t.Fatal("integer regression did not follow the archived PHP request path")
			}
			exported = append(exported, entry)
		})
	}
	for _, value := range unified.Slice(manifest["direct_cases"]) {
		c := unified.Map(value)
		expectedChecks++
		t.Run(unified.Str(c["id"]), func(t *testing.T) {
			input := unified.Map(c["input"])
			var got any
			switch c["operation"] {
			case "video_metadata":
				got = videoMetadata(input).values
			case "audio_candidates":
				got = audioCandidates(unified.Slice(input["entries"]), input["expires_at"])
			default:
				t.Fatal("unknown PHP oracle operation")
			}
			requireSameWire(t, got, c["expected"])
			completedChecks++
		})
	}
	if completedChecks == expectedChecks {
		if err := oracle.RequireConsumed(publicoracle.IntegerFixture); err != nil {
			t.Fatal(err)
		}
	}
	if destination := os.Getenv("DOKITOOL_GO_INTEGER_OUTPUT"); destination != "" && !t.Failed() {
		if len(exported) != 13 || completedChecks != expectedChecks {
			t.Fatal("Go integer output export requires all PHP integer baseline subtests")
		}
		if !filepath.IsAbs(destination) {
			t.Fatal("DOKITOOL_GO_INTEGER_OUTPUT must be an absolute path")
		}
		encoded, err := unified.Encode(unified.Object{"cases": exported})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, append(encoded, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
