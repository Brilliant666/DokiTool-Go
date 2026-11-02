package unified

import (
	"encoding/json"
	"testing"
)

// Expectations were reproduced with snapWorkMetadata/snapSource on the frozen
// PHP baseline; this catches integer precision loss before JSON publication.
func TestWorkAndSourcesPreserveExactIntegerFacts(t *testing.T) {
	for _, raw := range []string{"9007199254740993", "9223372036854775800"} {
		for _, input := range []any{json.Number(raw), raw} {
			work := WorkMetadata("douyin", Object{"extra": Object{"statistics": Object{"digg_count": input}}}, "https://www.douyin.com/video/7628438784602389247")
			if !Equal(Map(Map(work["statistics"])["likes"])["value"], json.Number(raw)) {
				t.Fatalf("integer statistic lost: %s", raw)
			}
			source := Source(Object{"url": "https://media.example.test/file.mp4", "size": input})
			if !Equal(source["size_bytes"], json.Number(raw)) {
				t.Fatal("integer source size lost")
			}
		}
	}
}

func TestExplicitArrayAuthorDoesNotUseRootAvatar(t *testing.T) {
	for _, author := range []any{Array{}, Array{"unused"}, Object{}} {
		work := WorkMetadata("douyin", Object{"author": author, "avatar": "https://images.example/root-avatar.jpg"}, "https://www.douyin.com/video/7628438784602389247")
		if _, ok := work["author"]; ok {
			t.Fatal("explicit array author incorrectly fell back to root avatar")
		}
	}
}

func TestNumericIntegerCastPaths(t *testing.T) {
	for _, tc := range []struct {
		input any
		want  int64
	}{
		{"+9007199254740993", 9007199254740993}, {" 7.9 ", 7}, {"-7.9", -7}, {"1e3", 1000},
		{"9223372036854775808", 9223372036854775807}, {json.Number("9223372036854775808"), -9223372036854775808},
		{"-9223372036854775809", -9223372036854775808}, {json.Number("18446744073709551616"), 0},
	} {
		if got, ok := PHPInt(tc.input); !ok || got != tc.want {
			t.Fatalf("cast %v => %d want %d", tc.input, got, tc.want)
		}
	}
}
