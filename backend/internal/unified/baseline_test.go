package unified

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The expected side is generated only by scripts/go-migration/produce.py from
// the pinned approved PHP commit. No PHP process is invoked by this Go test.
func TestPHPBaseline(t *testing.T) {
	directory := filepath.Join("..", "..", "testdata", "php-baseline")
	manifestBytes, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Decode(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if Str(manifest["baseline_commit"]) != "665bd55134982b9fe107aa1cd00373ba8da8ff3c" || Str(manifest["baseline_tree"]) != "93744d4d718c3299f82640d2888e93252106db70" {
		t.Fatal("unexpected PHP baseline commit/tree")
	}
	data, err := os.ReadFile(filepath.Join(directory, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != Str(Map(manifest["files"])["cases.json"]) {
		t.Fatal("PHP baseline data hash mismatch")
	}
	document, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	cases := Slice(document["cases"])
	count, ok := Numeric(manifest["case_count"])
	if !ok || int(count) != len(cases) {
		t.Fatal("PHP baseline case count mismatch")
	}
	seen := map[string]bool{}
	publicOutputs := make(Array, 0)
	for _, value := range cases {
		fixture := Map(value)
		name := Str(fixture["id"])
		if name == "" || seen[name] {
			t.Fatalf("duplicate or empty PHP baseline case %q", name)
		}
		seen[name] = true
		t.Run(name, func(t *testing.T) {
			input := Map(fixture["input"])
			number, _ := Numeric(input["now"])
			now := int64(number)
			var got any
			switch operation := Str(fixture["operation"]); operation {
			case "validate_envelope":
				err := ValidateEnvelope(Map(input["payload"]))
				got = Object{"valid": err == nil}
				if !Equal(got, fixture["expected"]) {
					t.Logf("Go validation error: %v; PHP diagnostic: %s", err, Str(fixture["php_error"]))
				}
			case "finalize_data":
				got = FinalizeData(Map(input["data"]), now)
			case "finalize_and_public_view":
				debug, _ := input["debug"].(bool)
				got = PublicView(FinalizeData(Map(input["data"]), now), debug, now)
			case "quality_compare":
				comparison, comparable := QualityCompare(Map(input["left"]), Map(input["right"]))
				if comparable {
					got = comparison
				}
			case "same_frame_rate":
				a, _ := Numeric(input["a"])
				b, _ := Numeric(input["b"])
				got = SameFrameRate(a, b)
			case "source_identity":
				got = SourceIdentity(Str(input["url"]))
			case "sources":
				got = Sources(Slice(input["sources"]), now)
			case "merge_sources":
				got = MergeSources(Slice(input["existing"]), Slice(input["additional"]), now)
			case "candidate_metadata":
				got = CandidateMetadata(Map(input["raw"]))
			case "merge_metadata":
				got = MergeMetadata(Map(input["existing"]), Map(input["additional"]))
			case "file_variants":
				got = FileVariants(Slice(input["candidates"]), now)
			default:
				t.Fatalf("unhandled PHP baseline operation %q", operation)
			}
			if operation := Str(fixture["operation"]); operation == "finalize_data" || operation == "finalize_and_public_view" {
				publicOutputs = append(publicOutputs, CloneValue(got))
			}
			if !Equal(got, fixture["expected"]) {
				want, _ := Encode(fixture["expected"])
				actual, encodeErr := Encode(got)
				if encodeErr != nil {
					t.Fatalf("cannot encode actual result: %v", encodeErr)
				}
				t.Fatalf("PHP baseline mismatch\nwant: %s\n got: %s", want, actual)
			}
		})
	}
	if len(cases) == 0 {
		t.Fatal(fmt.Errorf("empty PHP baseline"))
	}
	// Optional review output contains actual Go responses, never oracle rewrites.
	// The ordinary test run performs no file writes.
	if output := os.Getenv("DOKITOOL_GO_BASELINE_OUTPUT"); output != "" {
		if !filepath.IsAbs(output) {
			t.Fatal("DOKITOOL_GO_BASELINE_OUTPUT must be an absolute temporary path")
		}
		encoded, err := Encode(publicOutputs)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, append(encoded, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
