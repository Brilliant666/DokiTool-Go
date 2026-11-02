package unified

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

func TestPHPRoundDecimalHalfBoundaries(t *testing.T) {
	// Expected values were observed with the frozen PHP 8.3 runtime. These are
	// deliberate floating-point boundary cases, not a restatement of Go math.
	cases := []struct {
		value     float64
		precision int
		want      float64
	}{
		{0.5005, 3, 0.501},
		{0.5015, 3, 0.502},
		{1.005, 3, 1.005},
		{500.49999999999994, 0, 501},
		{500.49999999999, 0, 500},
	}
	for _, c := range cases {
		if got := PHPRound(c.value, c.precision); got != c.want {
			t.Errorf("PHPRound(%.17g,%d)=%g, want %g", c.value, c.precision, got, c.want)
		}
	}
}

func TestPHPRoundFrozenRuntimeBaseline(t *testing.T) {
	data, err := os.ReadFile("../../testdata/php-douyin/numeric.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		FormatVersion int `json:"format_version"`
		Cases         []struct {
			ID        string `json:"id"`
			Operation string `json:"operation"`
			Input     struct {
				Value     float64 `json:"value"`
				Precision int     `json:"precision"`
			} `json:"input"`
			Expected float64 `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil || fixture.FormatVersion != 1 || len(fixture.Cases) == 0 {
		t.Fatalf("invalid frozen PHP numeric fixture: %v", err)
	}
	for _, c := range fixture.Cases {
		t.Run(c.ID, func(t *testing.T) {
			if c.Operation != "round" {
				t.Fatalf("unsupported frozen operation %q", c.Operation)
			}
			actual := PHPRound(c.Input.Value, c.Input.Precision)
			if actual != c.Expected {
				t.Fatalf("PHP mismatch round(%.17g,%d): got %.17g, expected %.17g", c.Input.Value, c.Input.Precision, actual, c.Expected)
			}
		})
	}
}

func TestPHPRoundPreservesNonfiniteAndZero(t *testing.T) {
	for _, value := range []float64{0, math.Copysign(0, -1), math.Inf(1), math.Inf(-1)} {
		if got := PHPRound(value, 3); got != value || math.Signbit(got) != math.Signbit(value) {
			t.Errorf("special float changed: %v -> %v", value, got)
		}
	}
	if !math.IsNaN(PHPRound(math.NaN(), 0)) {
		t.Fatal("NaN was converted into a number")
	}
}
