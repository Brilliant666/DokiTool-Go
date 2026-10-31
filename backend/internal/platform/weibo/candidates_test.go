package weibo

import (
	"os"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// The expected values are produced by unmodified PHP weiboCount,
// weiboCandidate and weiboCandidates in the fixed PHP 8.3.6 runtime.
// A Go implementation must never write this oracle.
func TestPHPWeiboCandidateExtraction(t *testing.T) {
	body, err := os.ReadFile("testdata/php-candidates-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := unified.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if unified.Str(fixture["php_source_commit"]) != "25beaee107f8c1a208ced9deb3c637068445e316" {
		t.Fatal("unexpected frozen PHP provenance")
	}
	cases := unified.Slice(fixture["cases"])
	if len(cases) != 48 {
		t.Fatalf("incomplete PHP candidate oracle: got %d, expected 48", len(cases))
	}
	counts := map[string]int{}
	for _, value := range cases {
		entry := unified.Map(value)
		t.Run(unified.Str(entry["id"]), func(t *testing.T) {
			kind := unified.Str(entry["kind"])
			input := unified.Map(entry["input"])
			counts[kind]++
			var got any
			switch kind {
			case "count":
				if number, ok := legacyWeiboCount(input["value"]); ok {
					got = number
				}
			case "candidate":
				got = legacyWeiboCandidate(input["url"], unified.Str(input["label"]), unified.Map(input["info"]))
			case "candidates":
				keys := make([]string, 0)
				for _, key := range unified.Slice(input["url_keys"]) {
					keys = append(keys, unified.Str(key))
				}
				got = legacyWeiboCandidates(unified.Map(input["video_info"]), unified.Map(input["status"]), keys)
			default:
				t.Fatalf("unexpected fixture kind %q", kind)
			}
			if !unified.Equal(got, entry["expected"]) {
				t.Fatalf("Go/PHP candidate mismatch:\nGo  %#v\nPHP %#v", got, entry["expected"])
			}
		})
	}
	if counts["candidate"] != 23 || counts["count"] != 13 || counts["candidates"] != 12 {
		t.Fatalf("incomplete kind coverage: %#v", counts)
	}
}

func TestWeiboCandidatesPreserveOrderedEqualRankSources(t *testing.T) {
	src := unified.Object{"urls": unified.Object{
		"A": "https://f.video.weibocdn.com/a.mp4",
		"B": "https://f.video.weibocdn.com/b.mp4",
	}}
	got := legacyWeiboCandidates(src, nil, []string{"B", "A"})
	if len(got) != 2 || got[0]["label"] != "B" || got[1]["label"] != "A" {
		t.Fatalf("equal-rank candidates must retain supplied PHP order: %#v", got)
	}
}
