package zuiyou

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

func TestCallerCancellationStopsZuiyou(t *testing.T) {
	for _, stage := range []string{"before", "head", "detail", "deadline"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if stage == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			if stage == "before" {
				cancel()
			}
			calls := 0
			p := New(executorFunc(func(requestContext context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				if stage == "deadline" {
					<-requestContext.Done()
				} else {
					cancel()
				}
				if request.Method == http.MethodHead {
					return fetch.Response{StatusCode: 302, Header: http.Header{"Location": {"https://www.izuiyou.com/?pid=123456789"}}}, nil
				}
				// Even a complete successful response cannot override a caller's
				// cancellation or deadline after the request was issued.
				return fetch.Response{StatusCode: 200, Body: []byte(`{"data":{"post":{"videos":{"42":{"url":"https://media.example/cancel.mp4"}}}}}`)}, nil
			}))
			input := "https://www.izuiyou.com/detail/123456789"
			if stage == "head" {
				input = "https://share.izuiyou.com/synthetic"
			}
			result, err := p.Parse(ctx, input, 1793361600)
			wantCalls := 1
			if stage == "before" {
				wantCalls = 0
			}
			wantError := context.Canceled
			if stage == "deadline" {
				wantError = context.DeadlineExceeded
			}
			if result != nil || !errors.Is(err, wantError) || calls != wantCalls {
				t.Fatalf("caller cancellation was hidden or started another request: calls=%d result=%v err=%v", calls, result, err)
			}
		})
	}
}

func TestZuiyouRejectsOversizedDetailResponse(t *testing.T) {
	p := New(executorFunc(func(context.Context, fetch.Request) (fetch.Response, error) {
		return fetch.Response{StatusCode: 200, Body: []byte(strings.Repeat(" ", (8<<20)+1))}, nil
	}))
	result, err := p.Parse(context.Background(), "https://www.izuiyou.com/detail/123456789", 1793361600)
	if err != nil || result["code"] != "UPSTREAM_INVALID_RESPONSE" {
		t.Fatalf("oversized response was accepted: result=%v err=%v", result, err)
	}
}

func TestZuiyouFixedAPIOutboundBoundary(t *testing.T) {
	if !allowedDetail(detailURL) || !allowedDetail(detailURL+"?request=synthetic") {
		t.Fatal("fixed detail endpoint was rejected")
	}
	for _, input := range []string{
		"http://share.xiaochuankeji.cn/planck/share/post/detail_h5",
		"https://share.xiaochuankeji.cn/other",
		"https://share.xiaochuankeji.cn/planck/share/post/detail_h5#fragment",
		"https://share.xiaochuankeji.cn/planck/share/post/detail_h5/../other",
		"https://share.xiaochuankeji.cn:8080/planck/share/post/detail_h5",
		"https://user@share.xiaochuankeji.cn/planck/share/post/detail_h5",
		"https://share.xiaochuankeji.cn.example/planck/share/post/detail_h5",
		"https://127.0.0.1/planck/share/post/detail_h5",
	} {
		if allowedDetail(input) {
			t.Fatal("metadata redirect escaped the fixed HTTPS detail endpoint", input)
		}
	}
}

func TestZuiyouShareXiaochuankejiExactHostAndPagePID(t *testing.T) {
	const now int64 = 1793361600
	detailResp := `{"data":{"post":{"id":163566379,"content":"最右分享页测试","videos":{"88":{"url":"http://web-v01.izuiyou.com/zyvd/sample?auth_key=1"}}}}}`

	// 1. Direct ?pid= and /detail/<pid> on share.xiaochuankeji.cn make only the fixed detail_h5 POST.
	for _, link := range []string{
		"https://share.xiaochuankeji.cn/hybrid/share/post?pid=163566379",
		"https://share.xiaochuankeji.cn/detail/163566379",
	} {
		calls := 0
		p := New(executorFunc(func(_ context.Context, r fetch.Request) (fetch.Response, error) {
			calls++
			if r.Method != http.MethodPost || r.URL != detailURL {
				t.Fatalf("expected direct detail POST for %q, got %s %s", link, r.Method, r.URL)
			}
			return fetch.Response{StatusCode: 200, Body: []byte(detailResp)}, nil
		}))
		res, err := p.Parse(context.Background(), link, now)
		if err != nil || res["code"] != "OK" || calls != 1 {
			t.Fatalf("Parse(%q) failed: calls=%d res=%v err=%v", link, calls, res, err)
		}
	}

	// 2. 200 HTML page on share.xiaochuankeji.cn without query pid extracts post.id from page HTML (no redirect follow).
	htmlCalls := 0
	htmlParser := New(executorFunc(func(_ context.Context, r fetch.Request) (fetch.Response, error) {
		htmlCalls++
		if htmlCalls == 1 {
			if r.Method != http.MethodGet || r.Policy.FollowRedirects || r.Header.Get("Cookie") != "" {
				t.Fatalf("unexpected share page request: method=%s follow=%v cookie=%q", r.Method, r.Policy.FollowRedirects, r.Header.Get("Cookie"))
			}
			return fetch.Response{
				StatusCode: 200,
				Body:       []byte(`<!doctype html><script>window.__INITIAL_STATE__={"sharePost":{"postDetail":{"post":{"id":163566379,"content":"hello"}}}};</script>`),
				URL:        r.URL,
			}, nil
		}
		if r.Method != http.MethodPost || r.URL != detailURL || !strings.Contains(string(r.Body), `"pid":163566379`) {
			t.Fatalf("unexpected detail request: %s %s body=%s", r.Method, r.URL, string(r.Body))
		}
		return fetch.Response{StatusCode: 200, Body: []byte(detailResp)}, nil
	}))
	res, err := htmlParser.Parse(context.Background(), "https://share.xiaochuankeji.cn/s/abcXYZ", now)
	if err != nil || res["code"] != "OK" || htmlCalls != 2 {
		t.Fatalf("HTML page Parse failed: calls=%d res=%v err=%v", htmlCalls, res, err)
	}

	// 3. Mandatory negative tests: subdomains, lookalikes, userinfo, non-default ports, and /planck/ API path must be rejected before any network call.
	rejectParser := New(executorFunc(func(_ context.Context, r fetch.Request) (fetch.Response, error) {
		t.Fatalf("rejected input triggered network request: %s %s", r.Method, r.URL)
		return fetch.Response{}, nil
	}))
	for _, bad := range []string{
		"https://sub.share.xiaochuankeji.cn/hybrid/share/post?pid=163566379",
		"https://www.share.xiaochuankeji.cn/hybrid/share/post?pid=163566379",
		"https://xiaochuankeji.cn/hybrid/share/post?pid=163566379",
		"https://evilshare.xiaochuankeji.cn/hybrid/share/post?pid=163566379",
		"https://share.xiaochuankeji.cn.evil.com/hybrid/share/post?pid=163566379",
		"https://user:pass@share.xiaochuankeji.cn/hybrid/share/post?pid=163566379",
		"https://share.xiaochuankeji.cn:8080/hybrid/share/post?pid=163566379",
		"https://share.xiaochuankeji.cn:8443/hybrid/share/post?pid=163566379",
		"https://share.xiaochuankeji.cn/planck/share/post/detail_h5",
	} {
		got, err := rejectParser.Parse(context.Background(), bad, now)
		if err != nil || got["code"] != "INVALID_INPUT" {
			t.Fatalf("Parse(%q) = (%v, %v), want INVALID_INPUT with 0 requests", bad, got, err)
		}
	}
}
