package weibo

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// The independent expected result and exact sequence of GET/POST/Referer
// calls are emitted by the unmodified PHP weiboResolve product entrypoint.
func TestPHPWeiboFullResolveFlow(t *testing.T) {
	raw, err := os.ReadFile("testdata/php-resolve-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := unified.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if fixture["php_source_commit"] != "25beaee107f8c1a208ced9deb3c637068445e316" || !unified.Equal(fixture["schema"], 1) {
		t.Fatal("unexpected independent PHP resolver oracle")
	}
	cases := unified.Slice(fixture["cases"])
	if len(cases) != 16 {
		t.Fatalf("incomplete PHP resolver oracle: %d", len(cases))
	}
	counts := map[string]int{}
	for _, rawCase := range cases {
		entry := unified.Map(rawCase)
		t.Run(unified.Str(entry["id"]), func(t *testing.T) {
			input := unified.Map(entry["input"])
			target := unified.Map(input["target"])
			responses := unified.Slice(input["responses"])
			calls := make(unified.Array, 0, len(responses))
			index := 0
			session := newGuestSession(func(_ context.Context, address string, post *string, referer string) (unified.Object, error) {
				var body any
				if post != nil {
					body = *post
				}
				calls = append(calls, unified.Object{
					"url":  noncePattern.ReplaceAllString(address, "${1}<nonce>"),
					"post": body, "referer": referer,
				})
				if index >= len(responses) {
					return nil, errors.New("unexpected extra request")
				}
				value := unified.Map(responses[index])
				index++
				return value, nil
			})
			session.nonce = func() string { return "0.456789" }
			got, err := session.resolveLegacy(context.Background(), &Target{Kind: unified.Str(target["kind"]), ID: unified.Str(target["id"])})
			if err != nil {
				t.Fatal(err)
			}
			if index != len(responses) {
				t.Fatalf("unconsumed HTTP responses: %d of %d", index, len(responses))
			}
			output := unified.Object{"result": got, "requests": calls}
			if !unified.Equal(output, entry["expected"]) {
				t.Fatalf("frozen PHP status/TV resolver differs:\nGo  %#v\nPHP %#v", output, entry["expected"])
			}
			counts[unified.Str(target["kind"])]++
		})
	}
	if counts["status"] != 7 || counts["video"] != 9 {
		t.Fatalf("unexpected PHP resolver inventory: %#v", counts)
	}
}

func TestWeiboResolveStopsBeforeTransportWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	session := newGuestSession(func(context.Context, string, *string, string) (unified.Object, error) {
		called = true
		return nil, nil
	})
	if _, err := session.resolveLegacy(ctx, &Target{Kind: "status", ID: "123"}); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled resolver contacted upstream: %v, %v", called, err)
	}
}
