package weibo

import (
	"encoding/json"
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
	var rawDocument map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawDocument); err != nil {
		t.Fatal(err)
	}
	var rawCases []map[string]json.RawMessage
	if err := json.Unmarshal(rawDocument["cases"], &rawCases); err != nil {
		t.Fatal(err)
	}
	cases := unified.Slice(fixture["cases"])
	if len(cases) != 48 {
		t.Fatalf("incomplete PHP candidate oracle: got %d, expected 48", len(cases))
	}
	counts := map[string]int{}
	for index, value := range cases {
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
				if candidate := legacyWeiboCandidate(input["url"], unified.Str(input["label"]), unified.Map(input["info"])); candidate != nil {
					got = candidate
				}
			case "candidates":
				var rawInput map[string]json.RawMessage
				if err := json.Unmarshal(rawCases[index]["input"], &rawInput); err != nil {
					t.Fatal(err)
				}
				keys, err := weiboURLKeysFromRaw(rawInput["video_info"], "urls")
				if err != nil {
					t.Fatal(err)
				}
				expectedKeys := unified.Slice(input["url_keys"])
				if len(keys) != len(expectedKeys) {
					t.Fatalf("ordered URL key count differs from PHP: %#v vs %#v", keys, expectedKeys)
				}
				for i, key := range keys {
					if key != unified.Str(expectedKeys[i]) {
						t.Fatalf("URL key position %d differs from PHP: %#v vs %#v", i, keys, expectedKeys)
					}
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

func TestWeiboURLKeysFromRawDocument(t *testing.T) {
	raw := []byte("{\"data\":{\"Component_Play_Playinfo\":{\"urls\":{\"SD\":\"a\",\"HD\":\"b\",\"SD\":\"c\"}}}}")
	keys, err := weiboURLKeysFromRaw(raw, "data", "Component_Play_Playinfo", "urls")
	if err != nil || len(keys) != 2 || keys[0] != "SD" || keys[1] != "HD" {
		t.Fatalf("PHP source insertion order lost on duplicate keys: keys=%#v err=%v", keys, err)
	}
	for _, raw := range [][]byte{[]byte("{"), []byte("{\"urls\":[\"one\"]}"), []byte("{\"urls\":true}")} {
		if _, err := weiboURLKeysFromRaw(raw, "urls"); err == nil {
			t.Fatalf("malformed or unsupported ordered source must fail: %s", raw)
		}
	}
}
