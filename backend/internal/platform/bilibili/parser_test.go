package bilibili

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type executorFunc func(context.Context, fetch.Request) (fetch.Response, error)

func (f executorFunc) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	return f(ctx, request)
}

const testURL = "https://www.bilibili.com/video/BV1234567890/"
const testView = `{"code":0,"data":{"title":"Example","pages":[{"page":1,"cid":101,"part":"One"},{"page":2,"cid":102,"part":"Two"}]}}`
const testPlay = `{"code":0,"data":{"quality":64,"durl":[{"url":"https://upos-test.bilivideo.com/video.mp4","width":1280,"height":720}]}}`

func TestOrdinaryRequestWireDoesNotAddGoUserAgent(t *testing.T) {
	for _, input := range []fetch.Request{shareRequest("https://b23.tv/test"), viewRequest("BV1234567890", 20*time.Second), playRequest("BV1234567890", "101", 20*time.Second), pageRequest("https://www.bilibili.com/video/av123", 10*time.Second)} {
		t.Run(input.Policy.Purpose, func(t *testing.T) {
			request, err := http.NewRequest(input.Method, input.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header = input.Header.Clone()
			var wire bytes.Buffer
			if err := request.Write(&wire); err != nil {
				t.Fatal(err)
			}
			headers, _, _ := strings.Cut(wire.String(), "\r\n\r\n")
			if input.Policy.Purpose == "bilibili-page" {
				if !strings.Contains(headers, "\r\nUser-Agent: "+desktopUA+"\r\n") {
					t.Fatal("HTML fallback lost its PHP desktop User-Agent")
				}
			} else if strings.Contains(strings.ToLower(headers), "\r\nuser-agent:") {
				t.Fatalf("ordinary PHP-equivalent request acquired a User-Agent on the wire: %s", headers)
			}
		})
	}
}

func TestShareRedirectFragmentIsAllowedButNeverSent(t *testing.T) {
	target := "https://www.bilibili.com/video/av123?p=2&track=%23#discard"
	for _, policy := range []fetch.Policy{shareRequest("https://b23.tv/test").Policy, pageRequest(testURL, 10*time.Second).Policy} {
		if !policy.AllowedURL(target) {
			t.Fatal("legitimate redirect fragment rejected before PHP-equivalent cleanup")
		}
	}
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if err := request.Write(&wire); err != nil {
		t.Fatal(err)
	}
	requestLine, _, _ := strings.Cut(wire.String(), "\r\n")
	if requestLine != "GET /video/av123?p=2&track=%23 HTTP/1.1" {
		t.Fatalf("fragment altered request bytes: %s", requestLine)
	}
	if allowedAPIURL("https://api.bilibili.com/x/web-interface/view?bvid=BV1234567890#discard") {
		t.Fatal("metadata API unexpectedly accepts fragments")
	}
}

func TestPHPPartQueryNameAndAssignmentBoundaries(t *testing.T) {
	cases := []struct {
		query, value    string
		present, scalar bool
	}{
		{"p=2&p[=3", "2", true, true},
		{"p%00=2", "2", true, true},
		{"p=2&p[x]=3&p=1", "1", true, true},
		{"p[=3", "", false, false},
		{"p=2&p[]=3", "3", true, false},
		{"%20p=2", "2", true, true},
		{"p=2%00", "2\x00", true, true},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			value, present, scalar := queryPart(c.query)
			if value != c.value || present != c.present || scalar != c.scalar {
				t.Fatalf("p selection got (%q,%v,%v), want (%q,%v,%v)", value, present, scalar, c.value, c.present, c.scalar)
			}
		})
	}
}

func TestShortLinkPOverrideRetainsTrackingOrderAndArrayValues(t *testing.T) {
	got := redirectTarget("https://b23.tv/test?p=2", "https://www.bilibili.com/video/av123/?p=3&track=a%26b&foo[]=first&foo[]=second&tail=~#discard")
	want := "https://www.bilibili.com/video/av123/?p=2&track=a%26b&foo%5B0%5D=first&foo%5B1%5D=second&tail=%7E"
	if got != want {
		t.Fatalf("redirect reserialization lost query facts: got %s; want %s", got, want)
	}
	value, present, scalar := queryPart(strings.SplitN(got, "?", 2)[1])
	if value != "2" || !present || !scalar {
		t.Fatal("short-link p override changed target part")
	}
}

func TestRequestPolicyKeepsMetadataOnApprovedOrigins(t *testing.T) {
	for _, raw := range []string{testURL, "https://m.bilibili.com/video/av123", "http://bilibili.com/video/BV1234567890", "https://b23.tv/test", "https://www.bilibili.com/video/av123?p=2#discard"} {
		t.Run("allow/"+raw, func(t *testing.T) {
			if !allowedShareURL(raw) {
				t.Fatal("approved share host rejected")
			}
		})
	}
	for _, raw := range []string{"https://bilibili.com.evil.test/video/BV1234567890", "https://api.bilibili.com/x/web-interface/view", "https://127.0.0.1/a", "https://localhost/a", "https://user@b23.tv/a", "https://b23.tv:444/a", "https://b23.tv/a\\b", "https://b23.tv/a\nb", "https://other.bilibili.com/video/BV1234567890"} {
		t.Run("reject/"+raw, func(t *testing.T) {
			if allowedShareURL(raw) {
				t.Fatal("unapproved share target accepted")
			}
		})
	}
	for _, raw := range []string{"https://api.bilibili.com/x/web-interface/view?bvid=BV1234567890", "https://api.bilibili.com/x/player/playurl?cid=101"} {
		if !allowedAPIURL(raw) {
			t.Fatalf("approved API rejected: %s", raw)
		}
	}
	for _, raw := range []string{testURL, "http://api.bilibili.com/x/web-interface/view", "https://api.bilibili.com/x/other", "https://api.bilibili.com/x/player%2fplayurl", "https://api.bilibili.com.evil.test/x/player/playurl", "https://user@api.bilibili.com/x/player/playurl", "https://api.bilibili.com/x/player/playurl#fragment"} {
		if allowedAPIURL(raw) {
			t.Fatalf("unapproved API accepted: %s", raw)
		}
	}
	for _, request := range []fetch.Request{viewRequest("BV1234567890", 20*time.Second), playRequest("BV1234567890", "00102", 20*time.Second)} {
		if request.Policy.FollowRedirects || request.Method != http.MethodGet || request.Policy.MaxBytes > 8<<20 || request.Policy.Timeout > 20*time.Second {
			t.Fatalf("unbounded API request: %#v", request)
		}
		for _, name := range []string{"Cookie", "Authorization", "User-Agent", "Referer"} {
			if request.Header.Get(name) != "" {
				t.Fatalf("API introduces %s", name)
			}
		}
	}
}

func TestCancellationStopsEveryRequestBoundary(t *testing.T) {
	for _, boundary := range []string{"before", "share", "page", "view", "play"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			input := testURL
			if boundary == "share" {
				input = "https://b23.tv/test"
			}
			if boundary == "page" {
				input = "https://www.bilibili.com/video/av123"
			}
			calls := 0
			parser := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 35*time.Second {
					t.Fatal("parser lacks its shared deadline")
				}
				if request.Policy.Purpose == "bilibili-"+boundary {
					cancel()
					return fetch.Response{}, ctx.Err()
				}
				if request.Policy.Purpose != "bilibili-view" {
					t.Fatalf("unexpected request after cancellation: %s", request.Policy.Purpose)
				}
				return fetch.Response{StatusCode: 200, Body: []byte(testView), URL: request.URL}, nil
			}))
			if boundary == "before" {
				cancel()
			}
			payload, err := parser.Parse(ctx, input, 1793361600)
			if !errors.Is(err, context.Canceled) || payload != nil {
				t.Fatalf("cancellation hidden: payload=%#v err=%v", payload, err)
			}
			want := 1
			if boundary == "before" {
				want = 0
			}
			if boundary == "play" {
				want = 2
			}
			if calls != want {
				t.Fatalf("request count after cancellation: got %d want %d", calls, want)
			}
		})
	}
}

func TestExpiredBudgetDoesNotStartAnotherAPIRequest(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	parser := New(executorFunc(func(context.Context, fetch.Request) (fetch.Response, error) {
		t.Fatal("expired parser budget started a network request")
		return fetch.Response{}, nil
	}))
	response := parser.apiRequest(ctx, viewRequest("BV1234567890", remaining(ctx, 20*time.Second)))
	if failure(response, "视频信息")["code"] != int64(502) {
		t.Fatal("expired API budget did not produce the legacy transport failure")
	}
}

func TestConcurrentParseKeepsPartSelectionIsolated(t *testing.T) {
	parser := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
		body := testView
		if request.Policy.Purpose == "bilibili-play" {
			body = testPlay
			if strings.Contains(request.URL, "cid=102&") {
				body = strings.ReplaceAll(testPlay, "video.mp4", "part2.mp4")
			}
		}
		return fetch.Response{StatusCode: 200, Body: []byte(body), URL: request.URL}, nil
	}))
	var workers sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		part := i%2 + 1
		workers.Add(1)
		go func() {
			defer workers.Done()
			payload, err := parser.Parse(context.Background(), fmt.Sprintf("%s?p=%d", testURL, part), 1793361600)
			if err == nil {
				data := unified.Map(payload["data"])
				wantID := fmt.Sprintf("BV1234567890-p%d", part)
				items := unified.Slice(data["items"])
				if len(items) != 2 || data["default_item_id"] != wantID {
					err = fmt.Errorf("concurrent parse selected another part: %#v", data)
				} else {
					current := unified.Map(items[part-1])
					wantURL := "https://upos-test.bilivideo.com/video.mp4"
					if part == 2 {
						wantURL = "https://upos-test.bilivideo.com/part2.mp4"
					}
					if child(current, "default", "url") != wantURL || unified.Map(items[2-part])["state"] != "deferred" {
						err = fmt.Errorf("concurrent parser borrowed another part's source: %#v", items)
					}
				}
			}
			results <- err
		}()
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
}
