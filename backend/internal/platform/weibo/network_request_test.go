package weibo

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type stagedWeiboHTTP struct {
	replies []fetch.Response
	calls   []fetch.Request
}

func (s *stagedWeiboHTTP) Do(_ context.Context, r fetch.Request) (fetch.Response, error) {
	s.calls = append(s.calls, r)
	if len(s.replies) == 0 {
		return fetch.Response{}, errors.New("unexpected request")
	}
	answer := s.replies[0]
	s.replies = s.replies[1:]
	return answer, nil
}

func TestWeiboOfficialHTTPGuestCookieFlow(t *testing.T) {
	client := &stagedWeiboHTTP{replies: []fetch.Response{
		{StatusCode: 200, Header: http.Header{}, Body: []byte("<html>Sina Visitor System</html>")},
		{StatusCode: 200, Header: http.Header{}, Body: []byte(`gen_callback({"retcode":20000000,"data":{"tid":"tid123","confidence":85}});`)},
		{StatusCode: 200, Header: http.Header{"Set-Cookie": {"session=legitimate; Domain=.weibo.com; Path=/; Secure", "poison=bad; Domain=com; Path=/; Secure"}}, Body: []byte(`cross_domain({"retcode":20000000});`)},
		{StatusCode: 200, Header: http.Header{}, Body: []byte(`{"ok":1,"idstr":"9007199254740993"}`)},
	}}
	session := newNetworkGuestSession(client)
	session.nonce = func() string { return "0.2" }
	address := "https://weibo.com/ajax/statuses/show?id=9007199254740993"
	result := session.jsonRequest(context.Background(), address, nil, "https://weibo.com/")
	if unified.Map(result["json"]) == nil || !session.attempted {
		t.Fatalf("guest success not retained: %#v", result)
	}
	if len(client.calls) != 4 {
		t.Fatalf("expected 4 real exchanges, got %d", len(client.calls))
	}
	if client.calls[1].URL != "https://passport.weibo.com/visitor/genvisitor" || !strings.HasPrefix(client.calls[2].URL, "https://passport.weibo.com/visitor/visitor?") {
		t.Fatalf("guest endpoints changed: %q %q", client.calls[1].URL, client.calls[2].URL)
	}
	if got := client.calls[3].Header.Get("Cookie"); !strings.Contains(got, "session=legitimate") || strings.Contains(got, "poison") {
		t.Fatalf("guest cookie scope incorrect: %q", got)
	}
	for _, call := range client.calls {
		if !call.Policy.AllowedURL(call.URL) || call.Policy.FollowRedirects || call.Policy.MaxBytes != 2<<20 || call.Policy.Timeout <= 0 || call.Policy.Timeout > 20*time.Second {
			t.Fatalf("unsafe request policy: %#v", call)
		}
		if call.Header.Get("Accept") != "application/json, text/plain, */*" || call.Header.Get("User-Agent") != weiboUserAgent {
			t.Fatalf("official headers changed: %#v", call.Header)
		}
	}
	// Session isolation: a new parse must not inherit a previous visitor token.
	another := &stagedWeiboHTTP{replies: []fetch.Response{{StatusCode: 200, Header: http.Header{}, Body: []byte(`{"ok":1}`)}}}
	_ = newNetworkGuestSession(another).jsonRequest(context.Background(), address, nil, "https://weibo.com/")
	if another.calls[0].Header.Get("Cookie") != "" {
		t.Fatal("cookies leaked to new parse")
	}
}

func TestWeiboOfficialHTTPRedirectScope(t *testing.T) {
	for _, address := range []string{"http://weibo.com/ajax/statuses/show?id=1", "https://weibo.com:444/ajax/statuses/show?id=1", "https://weibo.com/evil", "https://weibo.com@evil.example/ajax/statuses/show?id=1", "https://passport.weibo.com/visitor/genvisitor"} {
		fake := &stagedWeiboHTTP{}
		_, err := newNetworkGuestSession(fake).exchange(context.Background(), address, nil, "https://weibo.com/")
		if !errors.Is(err, fetch.ErrPolicy) || len(fake.calls) != 0 {
			t.Fatalf("unsafe initial URL accepted %q: %v", address, err)
		}
	}
	fake := &stagedWeiboHTTP{replies: []fetch.Response{{StatusCode: 302, Header: http.Header{"Location": {"https://evil.example/download"}}}}}
	_, err := newNetworkGuestSession(fake).exchange(context.Background(), "https://weibo.com/ajax/statuses/show?id=1", nil, "https://weibo.com/")
	if !errors.Is(err, fetch.ErrPolicy) || len(fake.calls) != 1 {
		t.Fatalf("unsafe redirect followed: %v %d", err, len(fake.calls))
	}
	same := &stagedWeiboHTTP{replies: []fetch.Response{
		{StatusCode: 302, Header: http.Header{"Location": {"https://passport.weibo.com/visitor/visitor"}, "Set-Cookie": {"hop=ok; Domain=.weibo.com; Path=/; Secure"}}},
		{StatusCode: 200, Header: http.Header{}, Body: []byte("Sina Visitor System")},
	}}
	reply, err := newNetworkGuestSession(same).exchange(context.Background(), "https://weibo.com/ajax/statuses/show?id=1", nil, "https://weibo.com/")
	if err != nil || reply["url"] != "https://passport.weibo.com/visitor/visitor" || len(same.calls) != 2 || !strings.Contains(same.calls[1].Header.Get("Cookie"), "hop=ok") {
		t.Fatalf("approved redirect/cookie failed: %v, %#v, %#v", err, reply, same.calls)
	}
}

func TestWeiboOfficialHTTPCancelAndBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &stagedWeiboHTTP{}
	_, err := newNetworkGuestSession(fake).exchange(ctx, "https://weibo.com/ajax/statuses/show?id=1", nil, "https://weibo.com/")
	if !errors.Is(err, context.Canceled) || len(fake.calls) != 0 {
		t.Fatalf("cancellation ignored: %v", err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer stop()
	fake = &stagedWeiboHTTP{replies: []fetch.Response{{StatusCode: 200, Header: http.Header{}, Body: []byte("{}")}}}
	_, err = newNetworkGuestSession(fake).exchange(ctx, "https://weibo.com/ajax/statuses/show?id=1", nil, "https://weibo.com/")
	if err != nil || len(fake.calls) != 1 || fake.calls[0].Policy.Timeout > 150*time.Millisecond {
		t.Fatalf("budget was reset: %v %#v", err, fake.calls)
	}
}

// The expected strings are frozen PHP getRequestHeaders outputs. The CI oracle
// step regenerates the file with PHP 8.3.6 and compares exact bytes.
func TestPHPWeiboRequestHeaders(t *testing.T) {
	raw, err := os.ReadFile("testdata/php-network-headers.json")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := unified.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !unified.Equal(payload["schema"], 1) || payload["php_source_commit"] != "25beaee107f8c1a208ced9deb3c637068445e316" {
		t.Fatal("untrusted PHP oracle provenance")
	}
	cases := unified.Slice(payload["cases"])
	if len(cases) != 4 {
		t.Fatalf("unexpected PHP header oracle size: %d", len(cases))
	}
	for _, item := range cases {
		input := unified.Map(item)
		if !unified.Equal(officialWeiboHeaders(unified.Str(input["referer"])), input["expected"]) {
			t.Fatalf("PHP request header mismatch for %s", unified.Str(input["id"]))
		}
	}
}
