package xiaohongshu

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type requestFunc func(context.Context, fetch.Request) (fetch.Response, error)

func (f requestFunc) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	return f(ctx, request)
}

const controlledNote = `<script>window.__INITIAL_STATE__={"note":{"noteDetailMap":{"abc123":{"note":{"noteId":"abc123","type":"video","title":"Synthetic note","video":{"media":{"stream":{"h264":[{"masterUrl":"https://sns-video-qn.xhscdn.com/synthetic/720.mp4","width":1280,"height":720}]}},"consumer":{"originVideoKey":"synthetic/original"}}}}}}};</script>`

func TestXiaohongshuCancellationStopsPageAndProbeFallbacks(t *testing.T) {
	for _, phase := range []string{"before", "page", "probe"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			executor := requestFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				if phase == "page" || request.Policy.Purpose == "media-probe" {
					cancel()
					return fetch.Response{}, ctx.Err()
				}
				return fetch.Response{StatusCode: 200, URL: request.URL, Body: []byte(controlledNote)}, nil
			})
			if phase == "before" {
				cancel()
			}
			_, err := New(executor).Parse(ctx, "https://www.xiaohongshu.com/explore/abc123", 1793361600)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("caller cancellation was masked: %v", err)
			}
			if want := map[string]int{"before": 0, "page": 1, "probe": 2}[phase]; calls != want {
				t.Fatalf("fallback continued after cancellation: %d requests, want %d", calls, want)
			}
		})
	}
}

func TestXiaohongshuOptionalProbeFailurePreservesVerifiedStreams(t *testing.T) {
	pages, probes := 0, 0
	executor := requestFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
		if request.Policy.Purpose == "media-probe" {
			probes++
			return fetch.Response{}, context.DeadlineExceeded
		}
		pages++
		return fetch.Response{StatusCode: http.StatusOK, URL: request.URL, Body: []byte(controlledNote)}, nil
	})
	legacy, err := NewWithClock(executor, func() time.Time { return time.Unix(100, 0) }).parseLegacy(context.Background(), "https://www.xiaohongshu.com/explore/abc123")
	if err != nil || legacy["code"] != 200 || pages != 1 || probes != 4 {
		t.Fatalf("optional enhancement replaced a usable page: pages=%d probes=%d legacy=%v err=%v", pages, probes, legacy, err)
	}
	candidates := unified.Slice(unified.Map(legacy["data"])["video_candidates"])
	if len(candidates) != 2 || unified.Map(candidates[0])["width"] != int64(1280) || unified.Map(candidates[0])["height"] != int64(720) {
		t.Fatal("upstream representation was lost after optional probe failure")
	}
	original := unified.Map(candidates[1])
	if original["width"] != nil || original["height"] != nil || len(unified.Slice(original["sources"])) != 4 {
		t.Fatal("unmeasured original inferred dimensions or lost its source candidates")
	}
}

func TestXiaohongshuPageRejectsUnapprovedRedirectWithoutFetchingIt(t *testing.T) {
	calls := 0
	executor := requestFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
		calls++
		if !strings.Contains(request.URL, "xiaohongshu.com") && !strings.Contains(request.URL, "rednote.com") {
			t.Fatal("foreign redirect reached transport")
		}
		return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {"https://unapproved.example/private"}}}, nil
	})
	payload, err := New(executor).Parse(context.Background(), "https://www.xiaohongshu.com/explore/abc123", 1793361600)
	if err != nil || payload["code"] != "UPSTREAM_INVALID_RESPONSE" || calls != 2 {
		t.Fatalf("unexpected redirect result: %v %v calls=%d", payload, err, calls)
	}
}

func TestXiaohongshuMalformedConcatenatedImageCannotRecurse(t *testing.T) {
	if got := processImageURL("3https://sns-img-hw.xhscdn.com/example.jpg"); got != "" {
		t.Fatalf("malformed concatenated URL was retained: %q", got)
	}
}
