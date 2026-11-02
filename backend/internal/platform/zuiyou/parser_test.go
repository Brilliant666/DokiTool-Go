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
