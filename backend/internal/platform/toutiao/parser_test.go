package toutiao

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type executorFunc func(context.Context, fetch.Request) (fetch.Response, error)

func (f executorFunc) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	return f(ctx, request)
}

func testPage(id, title string) string {
	return `<script id="RENDER_DATA">{"data":{"itemId":"` + id + `","initialVideo":{"title":"` + title + `","userInfo":{"name":"author","description":"profile"},"diggCount":0,"videoPlayInfo":{"video_list":{"only":{"main_url":"https://media.example/video.mp4","video_meta":{"vwidth":1920,"vheight":1080}}}}}}}</script>`
}

func TestConcurrentParsesDoNotShareAnonymousCookie(t *testing.T) {
	type sessionKey struct{}
	barrier := make(chan struct{})
	registrations := make(chan struct{}, 2)
	executor := executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
		id := ctx.Value(sessionKey{}).(string)
		if request.URL == registrationURL {
			if request.Header.Get("Cookie") != "" || request.Method != http.MethodPost || string(request.Body) != registrationBody {
				return fetch.Response{}, fmt.Errorf("anonymous registration reused a cookie or changed its request")
			}
			registrations <- struct{}{}
			select {
			case <-barrier:
			case <-ctx.Done():
				return fetch.Response{}, ctx.Err()
			}
			return fetch.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {"ttwid=for-" + id + "; Path=/"}}, URL: request.URL}, nil
		}
		if request.URL != "https://www.toutiao.com/video/"+id+"/" || request.Header.Get("Cookie") != "ttwid=for-"+id {
			return fetch.Response{}, fmt.Errorf("parse %s received another parse's identity or cookie", id)
		}
		return fetch.Response{StatusCode: 200, Body: []byte(testPage(id, id)), URL: request.URL}, nil
	})
	p := New(executor)
	result := make(chan error, 2)
	for _, id := range []string{"7501991960077500979", "7501991960077500980"} {
		go func(id string) {
			ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), sessionKey{}, id), 5*time.Second)
			defer cancel()
			payload, err := p.Parse(ctx, "https://www.toutiao.com/video/"+id+"/", 1793361600)
			if err == nil && (payload["code"] != "OK" || unified.Map(payload["data"])["content_id"] != id) {
				err = fmt.Errorf("parse identity lost: %v", payload)
			}
			if err == nil {
				encoded, _ := unified.Encode(payload)
				if strings.Contains(string(encoded), "for-"+id) {
					err = fmt.Errorf("anonymous cookie leaked into public data")
				}
			}
			result <- err
		}(id)
	}
	for range 2 {
		select {
		case <-registrations:
		case <-time.After(5 * time.Second):
			close(barrier)
			t.Fatal("concurrent parsers did not both reach isolated registration")
		}
	}
	close(barrier)
	for range 2 {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCancelStopsPageAfterRegistration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	p := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
		calls++
		cancel()
		return fetch.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {"ttwid=anonymous"}}, URL: request.URL}, nil
	}))
	result, err := p.Parse(ctx, "https://www.toutiao.com/video/123456789/", 1793361600)
	if !errors.Is(err, context.Canceled) || result != nil || calls != 1 {
		t.Fatalf("cancel did not stop the parse: calls=%d, result=%v, err=%v", calls, result, err)
	}
}

func TestRealRequestBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"canonical", "https://www.toutiao.com/video/123456789/"},
		{"share", "https://m.toutiao.com/is/synthetic/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 35*time.Second || request.Policy.Timeout <= 0 || request.Policy.Timeout > 20*time.Second || request.Policy.FollowRedirects || request.Policy.MaxRedirects != 0 {
					t.Error("request did not preserve bounded manual redirect flow")
				}
				if request.Policy.AllowedURL == nil || !request.Policy.AllowedURL(request.URL) || request.Policy.AllowedURL("https://example.com/private") {
					t.Error("request lacks a platform-specific destination policy")
				}
				if request.URL == registrationURL {
					if request.Policy.MaxBytes != 65536 || request.Policy.AllowedURL(registrationURL+"?extra=1") || request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
						t.Error("registration policy changed")
					}
					return fetch.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {"ttwid=anonymous"}}, URL: request.URL}, nil
				}
				if request.Policy.MaxBytes != 2<<20 || request.Method != http.MethodGet {
					t.Error("metadata GET byte limit changed")
				}
				if strings.Contains(request.URL, "/is/") {
					return fetch.Response{StatusCode: 302, Header: http.Header{"Location": {"/video/123456789/"}}, URL: request.URL}, nil
				}
				if request.Policy.AllowedURL("https://m.toutiao.com/is/other/") {
					t.Error("cookie-bearing canonical request accepts another destination")
				}
				return fetch.Response{StatusCode: 200, Body: []byte(testPage("123456789", "test")), URL: request.URL}, nil
			}))
			payload, err := p.Parse(context.Background(), tc.url, 1793361600)
			wantCalls := 2
			if tc.name == "share" {
				wantCalls = 3
			}
			if err != nil || payload["code"] != "OK" || calls != wantCalls {
				t.Fatalf("failed bounded parse: %v %v; calls=%d", payload, err, calls)
			}
		})
	}
}

func TestInvalidInputDoesNotStartTransport(t *testing.T) {
	for _, input := range []string{
		"https://toutiao.com.example/video/123456789/",
		"https://user@www.toutiao.com/video/123456789/",
		"https://www.toutiao.com:8080/video/123456789/",
		"https://www.toutiao.com/video/123456789/\n",
	} {
		p := New(executorFunc(func(context.Context, fetch.Request) (fetch.Response, error) {
			t.Fatal("invalid input reached network")
			return fetch.Response{}, nil
		}))
		result, err := p.Parse(context.Background(), input, 1793361600)
		if err != nil || result["code"] != "INVALID_INPUT" {
			t.Fatalf("invalid input result: %v %v", result, err)
		}
	}
}

func TestMediaURLStrictBase64Boundaries(t *testing.T) {
	const source = "https://media.example/video.mp4"
	const encoded = "aHR0cHM6Ly9tZWRpYS5leGFtcGxlL3ZpZGVvLm1wNA=="
	const onePad = "aHR0cHM6Ly9tZWRpYS5leGFtcGxlL2E="
	const noPad = "aHR0cHM6Ly9tZWRpYS5leGFtcGxlL2Fi"
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"padded", encoded, source},
		{"legal-whitespace", " \t\r\n" + encoded[:8] + " \t\r\n" + encoded[8:len(encoded)-1] + " \t\r\n= \t\r\n", source},
		{"unpadded", strings.TrimRight(encoded, "="), source},
		{"one-pad", onePad, "https://media.example/a"},
		{"one-pad-omitted", strings.TrimRight(onePad, "="), "https://media.example/a"},
		{"padding-unneeded", noPad, "https://media.example/ab"},
		// PHP strict mode rejects malformed alphabet/padding, but does not
		// require unused trailing bits to be zero. Do not use Encoding.Strict.
		{"nonzero-unused-bits", encoded[:len(encoded)-3] + "B==", source},
		{"vertical-tab", encoded[:8] + "\v" + encoded[8:], ""},
		{"form-feed", encoded[:8] + "\f" + encoded[8:], ""},
		{"partial-padding", encoded[:len(encoded)-1], ""},
		{"excess-padding", encoded + "=", ""},
		{"interior-padding", encoded[:8] + "=" + encoded[8:], ""},
		{"data-after-padding", encoded + "A", ""},
		{"one-character-last-group", noPad + "A", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mediaURL(tc.raw); got != tc.want {
				t.Fatalf("media URL: got %q, want %q", got, tc.want)
			}
		})
	}
}
