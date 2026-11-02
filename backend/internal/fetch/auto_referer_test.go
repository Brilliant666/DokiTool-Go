package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"testing"
)

func TestAutoRefererFollowsEachApprovedGETHop(t *testing.T) {
	targets := []string{
		"https://api.example.test/start?sig=a%2Bb+c&x=1&x=2#private",
		"https://api.example.test/second?sig=z%2Fv+q#another",
		"https://cdn.example.test/third?bytes=%2f%2F",
	}
	wanted := []string{"", "https://api.example.test/start?sig=a%2Bb+c&x=1&x=2", "https://api.example.test/second?sig=z%2Fv+q"}
	calls := 0
	client := fixtureClient(func(request *http.Request, _ []netip.Addr, _ Policy) (*http.Response, error) {
		index := calls
		calls++
		if index >= len(targets) || request.Method != http.MethodGet || request.URL.String() != targets[index] || request.Header.Get("Referer") != wanted[index] {
			t.Fatalf("hop %d: %s %s Referer=%q", index, request.Method, request.URL, request.Header.Get("Referer"))
		}
		if index == 2 && (request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "") {
			t.Fatal("cross-origin redirect retained authentication")
		}
		if index+1 < len(targets) {
			return reply(302, http.Header{"Location": {targets[index+1]}}, ""), nil
		}
		return reply(200, nil, "ok"), nil
	})
	policy := testPolicy()
	policy.AutoReferer = true
	_, err := client.Do(context.Background(), Request{URL: targets[0], Method: http.MethodGet,
		Header: http.Header{"Cookie": {"synthetic=1"}, "Authorization": {"synthetic"}}, Policy: policy})
	if err != nil || calls != 3 {
		t.Fatalf("approved redirect chain: calls=%d err=%v", calls, err)
	}
}

func TestAutoRefererDisabledByDefaultIncludingHEAD(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			client := fixtureClient(func(request *http.Request, _ []netip.Addr, _ Policy) (*http.Response, error) {
				calls++
				if request.Method != method || request.Header.Get("Referer") != "" {
					t.Fatal("default redirect behavior acquired a Referer or changed HEAD")
				}
				if calls == 1 {
					return reply(302, http.Header{"Location": {"/final"}}, ""), nil
				}
				return reply(200, nil, "ok"), nil
			})
			_, err := client.Do(context.Background(), Request{URL: "https://api.example.test/start", Method: method, Policy: testPolicy()})
			if err != nil || calls != 2 {
				t.Fatalf("default redirect: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestAutoRefererPreservesLibcurlDowngradeSemantics(t *testing.T) {
	calls := 0
	client := fixtureClient(func(request *http.Request, _ []netip.Addr, _ Policy) (*http.Response, error) {
		calls++
		if calls == 1 {
			return reply(302, http.Header{"Location": {"http://api.example.test/final"}}, ""), nil
		}
		if request.Header.Get("Referer") != "https://api.example.test/start?sig=%2B+value" || request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" {
			t.Fatal("automatic Referer changed or authentication followed an insecure downgrade")
		}
		return reply(200, nil, "ok"), nil
	})
	policy := testPolicy()
	policy.AutoReferer = true
	_, err := client.Do(context.Background(), Request{URL: "https://api.example.test/start?sig=%2B+value#secret",
		Header: http.Header{"Cookie": {"synthetic=1"}, "Authorization": {"synthetic"}}, Policy: policy})
	if err != nil || calls != 2 {
		t.Fatalf("libcurl-compatible downgrade: calls=%d err=%v", calls, err)
	}
}

func TestAutoRefererDoesNotAuthorizeRejectedNextHop(t *testing.T) {
	for _, target := range []string{"https://evil.example/private", "https://user:pass@api.example.test/private", "https://127.0.0.1/private"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			client := fixtureClient(func(request *http.Request, _ []netip.Addr, _ Policy) (*http.Response, error) {
				calls++
				return reply(302, http.Header{"Location": {target}}, ""), nil
			})
			policy := testPolicy()
			policy.AutoReferer = true
			_, err := client.Do(context.Background(), Request{URL: "https://api.example.test/start", Policy: policy})
			if !errors.Is(err, ErrPolicy) || calls != 1 {
				t.Fatalf("rejected redirect reached transport: calls=%d err=%v", calls, err)
			}
		})
	}
}
