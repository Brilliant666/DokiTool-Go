package weibo

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func requestOracle(t *testing.T, name string) unified.Object {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	bytes, err := io.ReadAll(io.LimitReader(decoder, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	value, err := unified.Decode(bytes)
	if err != nil {
		t.Fatal(err)
	}
	if value["php_source_commit"] != "25beaee107f8c1a208ced9deb3c637068445e316" || !unified.Equal(value["schema"], 1) {
		t.Fatal("unexpected PHP oracle provenance")
	}
	return value
}

var noncePattern = regexp.MustCompile(`([?&]_rand=)[^&]*`)

func TestPHPWeiboGuestRequestFlow(t *testing.T) {
	cases := unified.Slice(requestOracle(t, "php-request-baseline.json.gz")["cases"])
	if len(cases) != 28 {
		t.Fatalf("incomplete PHP request oracle: %d", len(cases))
	}
	for _, value := range cases {
		entry := unified.Map(value)
		t.Run(unified.Str(entry["id"]), func(t *testing.T) {
			input := unified.Map(entry["input"])
			canned := unified.Slice(input["responses"])
			calls := make([]any, 0, len(canned))
			next := 0
			exchange := func(_ context.Context, address string, post *string, referer string) (unified.Object, error) {
				var body any
				if post != nil {
					body = *post
				}
				calls = append(calls, unified.Object{
					"url":  noncePattern.ReplaceAllString(address, "${1}<nonce>"),
					"post": body, "referer": referer,
				})
				if next >= len(canned) {
					return nil, errors.New("unexpected extra request")
				}
				value := unified.Map(canned[next])
				next++
				return value, nil
			}
			session := newGuestSession(exchange)
			session.nonce = func() string { return "0.456789" }
			session.attempted = input["guest_attempted"] == true
			var post *string
			if value, ok := input["post"].(string); ok {
				post = &value
			}
			got := unified.Object{
				"result":          session.jsonRequest(context.Background(), unified.Str(input["url"]), post, unified.Str(input["referer"])),
				"guest_attempted": session.attempted,
				"requests":        calls,
			}
			if next != len(canned) {
				t.Errorf("unconsumed responses %d/%d", next, len(canned))
			}
			if !unified.Equal(got, entry["expected"]) {
				t.Fatalf("frozen PHP request flow mismatch:\n Go  %#v\n PHP %#v", got, entry["expected"])
			}
		})
	}
}

func TestGuestRequestDoesNotFetchWhenCanceled(t *testing.T) {
	called := false
	session := newGuestSession(func(context.Context, string, *string, string) (unified.Object, error) {
		called = true
		return unified.Object{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := session.jsonRequest(ctx, "https://weibo.com/ajax/statuses/show?id=123", nil, "https://weibo.com/")
	if called || !unified.Equal(unified.Map(got["failure"])["code"], 503) {
		t.Fatalf("canceled fetch must not reach outbound transport: called=%v result=%#v", called, got)
	}
}
