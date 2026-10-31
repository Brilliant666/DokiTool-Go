package weibo

import (
	"os"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// Expected results are produced by the unchanged PHP8.3.6 weiboTarget().
// A Go-produced fixture or rewritten PHP expectation is never accepted.
func TestPHPWeiboTargetIdentity(t *testing.T) {
	body, err := os.ReadFile("testdata/php-target-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := unified.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if unified.Str(fixture["php_source_commit"]) != "25beaee107f8c1a208ced9deb3c637068445e316" {
		t.Fatal("unapproved PHP identity oracle")
	}
	cases := unified.Slice(fixture["cases"])
	if len(cases) != 47 {
		t.Fatalf("incomplete identity oracle: %d cases", len(cases))
	}
	for _, entryValue := range cases {
		entry := unified.Map(entryValue)
		t.Run(unified.Str(entry["id"]), func(t *testing.T) {
			input := unified.Map(entry["input"])
			target := ParseTarget(input["url"])
			var got any
			if target != nil {
				got = unified.Object{"kind": target.Kind, "id": target.ID}
			}
			if !unified.Equal(got, entry["expected"]) {
				actual, _ := unified.Encode(unified.Object{"actual": got})
				expected, _ := unified.Encode(unified.Object{"expected": entry["expected"]})
				t.Fatalf("Go/PHP Weibo target mismatch\nGo: %s\nPHP: %s", actual, expected)
			}
		})
	}
}
