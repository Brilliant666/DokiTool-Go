package kuaishou

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
)

type executorFunc func(context.Context, fetch.Request) (fetch.Response, error)

func (f executorFunc) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	return f(ctx, request)
}

func TestCallerCancellationStopsAfterHEAD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	parser := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
		calls++
		if request.Method != http.MethodHead {
			t.Fatal("page request started after caller cancellation")
		}
		cancel()
		return fetch.Response{StatusCode: 200, URL: "https://www.kuaishou.com/short-video/test"}, nil
	}))
	payload, err := parser.Parse(ctx, "https://v.kuaishou.com/test", 1793361600)
	if !errors.Is(err, context.Canceled) || payload != nil || calls != 1 {
		t.Fatalf("cancellation: calls=%d payload=%v error=%v", calls, payload, err)
	}
}

func TestRejectedInputNeverStartsTransport(t *testing.T) {
	for _, input := range []string{"https://kuaishou.com.attacker.example/short-video/test", "https://user@www.kuaishou.com/short-video/test", "https://www.kuaishou.com:8080/short-video/test", "http://127.0.0.1/short-video/test"} {
		t.Run(input, func(t *testing.T) {
			parser := New(executorFunc(func(context.Context, fetch.Request) (fetch.Response, error) {
				t.Fatal("unapproved input started transport")
				return fetch.Response{}, nil
			}))
			payload, err := parser.Parse(context.Background(), input, 1793361600)
			if err != nil || payload["code"] != "INVALID_INPUT" {
				t.Fatalf("invalid input: %v %v", payload, err)
			}
		})
	}
}

func TestUntrustedEffectiveURLCannotStartPage(t *testing.T) {
	calls := 0
	parser := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
		calls++
		return fetch.Response{StatusCode: 200, URL: "https://example.invalid/private"}, nil
	}))
	payload, err := parser.Parse(context.Background(), "https://v.kuaishou.com/test", 1793361600)
	if err != nil || payload["code"] != "INVALID_INPUT" || calls != 1 {
		t.Fatalf("effective URL escaped authorization: calls=%d payload=%v err=%v", calls, payload, err)
	}
}

func TestParseKeepsHEADAndGETRequestBoundaries(t *testing.T) {
	const target = "https://www.kuaishou.com/short-video/test"
	calls := 0
	parser := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 40*time.Second || request.Policy.Timeout != 20*time.Second || request.Policy.MaxBytes != 8<<20 || !request.Policy.FollowRedirects || request.Policy.MaxRedirects != 5 {
			t.Error("original bounded request semantics changed")
		}
		if calls == 1 {
			if request.Method != http.MethodHead || request.Policy.AutoReferer || len(request.Header.Values("Cookie")) != 0 {
				t.Error("HEAD acquired page settings")
			}
			return fetch.Response{StatusCode: 200, URL: target}, nil
		}
		if request.Method != http.MethodGet || !request.Policy.AutoReferer || request.Header.Get("Cookie") != "" || len(request.Header.Values("Cookie")) != 1 || request.URL != target {
			t.Error("GET lost effective target, automatic Referer or empty configured Cookie")
		}
		return fetch.Response{StatusCode: 200, URL: target, Body: []byte("<html>no metadata</html>")}, nil
	}))
	payload, err := parser.Parse(context.Background(), "https://v.kuaishou.com/test", 1793361600)
	if err != nil || payload["code"] != "CONTENT_UNAVAILABLE" || calls != 2 {
		t.Fatalf("bounded flow: calls=%d payload=%v err=%v", calls, payload, err)
	}
}

func TestMalformedRequiredShapesAreNotEmptySuccess(t *testing.T) {
	for _, raw := range []string{
		"null", "true",
		`{"tusjoh-a":{"photo":{"photoType":"VIDEO","mainMvUrls":"not-an-array"}}}`,
		`{"tusjoh-a":{"photo":{"ext_params":{"atlas":{"list":"not-an-array"}}}}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			payload, err := fromInitState("<script>window.INIT_STATE = " + raw + ";</script>")
			if err == nil || payload != nil {
				t.Fatalf("required shape error was swallowed: %v %v", payload, err)
			}
		})
	}
}

func TestOversizedInjectedPageRemainsRejected(t *testing.T) {
	const target = "https://www.kuaishou.com/short-video/test"
	calls := 0
	parser := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
		calls++
		response := fetch.Response{StatusCode: 200, URL: target}
		if request.Method == http.MethodGet {
			response.Body = []byte(strings.Repeat("x", (8<<20)+1))
		}
		return response, nil
	}))
	payload, err := parser.Parse(context.Background(), target, 1793361600)
	if err != nil || payload["code"] != "UPSTREAM_UNAVAILABLE" || calls != 2 {
		t.Fatalf("oversized metadata was parsed: calls=%d payload=%v err=%v", calls, payload, err)
	}
}
