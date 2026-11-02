package weibo

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func TestWeiboCountPreservesExactIntegers(t *testing.T) {
	for _, value := range []any{
		int64(9007199254740993), json.Number("9007199254740993"),
		"9007199254740993", "9,007,199,254,740,993",
	} {
		got, valid := legacyWeiboCount(value)
		if !valid || got != 9007199254740993 {
			t.Errorf("exact count rounded: input=%#v got=%d valid=%v", value, got, valid)
		}
	}
	for _, value := range []any{int64(math.MaxInt64), json.Number("9223372036854775807"), "9223372036854775807"} {
		got, valid := legacyWeiboCount(value)
		if !valid || got != math.MaxInt64 {
			t.Errorf("maximum integer changed: input=%#v got=%d valid=%v", value, got, valid)
		}
	}
}

func TestWeiboCountStillRejectsNonNumericNegativeAndNonFinite(t *testing.T) {
	for _, value := range []any{
		nil, true, false, "", "NaN", "INF", "1e309", "-1", -1,
		-0.1, math.NaN(), math.Inf(1), math.Inf(-1), "1,00", []any{1},
	} {
		if got, valid := legacyWeiboCount(value); valid {
			t.Errorf("invalid count accepted: input=%#v got=%d", value, got)
		}
	}
	for _, value := range []any{0, "0", "-0", math.Copysign(0, -1), "0.9", 0.9} {
		if got, valid := legacyWeiboCount(value); !valid || got != 0 {
			t.Errorf("known zero omitted: input=%#v got=%d valid=%v", value, got, valid)
		}
	}
}

// This additional oracle is generated only by the unchanged PHP 8.3.6
// weiboCount function. raw_json preserves integer-vs-float token spelling so
// a PHP re-encoding cannot accidentally change the input type under test.
func TestPHPWeiboCountBoundaries(t *testing.T) {
	body, err := os.ReadFile("testdata/php-count-boundaries.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := unified.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if fixture["php_source_commit"] != "25beaee107f8c1a208ced9deb3c637068445e316" || fixture["php_runtime"] != "8.3.6" {
		t.Fatal("unexpected PHP count oracle provenance")
	}
	inputBytes, err := os.ReadFile("testdata/count-boundary-inputs.json")
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := unified.Decode(inputBytes)
	if err != nil {
		t.Fatal(err)
	}
	cases := unified.Slice(fixture["cases"])
	inputCases := unified.Slice(inputs["cases"])
	if len(cases) != 35 || len(cases) != len(inputCases) {
		t.Fatalf("incomplete PHP boundary oracle: %d results, %d inputs", len(cases), len(inputCases))
	}
	for index, rawCase := range cases {
		entry := unified.Map(rawCase)
		input := unified.Map(inputCases[index])
		if entry["id"] != input["id"] || entry["raw_json"] != input["raw_json"] {
			t.Fatalf("oracle input changed at case %d", index)
		}
		t.Run(unified.Str(entry["id"]), func(t *testing.T) {
			value, err := unified.DecodeValue([]byte(unified.Str(input["raw_json"])))
			if err != nil {
				t.Fatal(err)
			}
			var got any
			if count, valid := legacyWeiboCount(value); valid {
				got = count
			}
			if !unified.Equal(got, entry["expected"]) {
				t.Fatalf("Go/PHP count mismatch: input=%s PHP type=%v Go=%#v PHP=%#v", input["raw_json"], entry["php_type"], got, entry["expected"])
			}
		})
	}
}
