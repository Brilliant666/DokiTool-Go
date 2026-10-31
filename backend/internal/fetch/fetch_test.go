package fetch

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, name string) ([]netip.Addr, error) {
	return f(ctx, network, name)
}

func fixtureClient(send roundTrip) *Client {
	return &Client{resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}), transport: send}
}
func testPolicy() Policy {
	return Policy{Purpose: "test-metadata", AllowedURL: AllowHosts("api.example.test", "cdn.example.test"), Timeout: time.Second, MaxBytes: 100, FollowRedirects: true, MaxRedirects: 2}
}
func reply(status int, headers http.Header, value string) *http.Response {
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(value))}
}

func TestPublicAddressBoundary(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.1.2.3", "100.64.0.1", "169.254.1.1", "192.0.2.1", "198.18.0.1", "203.0.113.1", "224.0.0.1", "::1", "::ffff:8.8.8.8", "2001:db8::1", "2002:0808:0808::", "3fff::1", "fd00::1"} {
		if PublicIP(netip.MustParseAddr(raw)) {
			t.Errorf("accepted reserved %s", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !PublicIP(netip.MustParseAddr(raw)) {
			t.Errorf("rejected public %s", raw)
		}
	}
}

func TestRejectMixedDNSBeforeTransport(t *testing.T) {
	called := false
	c := fixtureClient(func(*http.Request, []netip.Addr, Policy) (*http.Response, error) {
		called = true
		return reply(200, nil, "{}"), nil
	})
	c.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	})
	_, err := c.Do(context.Background(), Request{URL: "https://api.example.test/detail", Policy: testPolicy()})
	if !errors.Is(err, ErrPolicy) || called {
		t.Fatalf("mixed DNS escaped policy: %v called=%v", err, called)
	}
}

func TestRedirectChecksAndQueryPreservation(t *testing.T) {
	var calls []string
	c := fixtureClient(func(req *http.Request, ips []netip.Addr, policy Policy) (*http.Response, error) {
		calls = append(calls, req.URL.String())
		if len(ips) != 1 || ips[0].String() != "8.8.8.8" {
			t.Fatal("lost pinned target")
		}
		if len(calls) == 1 {
			return reply(302, http.Header{"Location": {"https://cdn.example.test/item?a=1%2F2&a=3+4"}}, "ignored"), nil
		}
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Fatal("credentials followed cross-host redirect")
		}
		return reply(200, nil, "ok"), nil
	})
	result, err := c.Do(context.Background(), Request{URL: "https://api.example.test/start", Header: http.Header{"Cookie": {"test=1"}, "Authorization": {"test"}}, Policy: testPolicy()})
	if err != nil || result.URL != "https://cdn.example.test/item?a=1%2F2&a=3+4" || len(result.History) != 1 || string(result.Body) != "ok" {
		t.Fatalf("redirect changed transport data: %+v %v", result, err)
	}

	calls = nil
	c.transport = func(req *http.Request, _ []netip.Addr, _ Policy) (*http.Response, error) {
		calls = append(calls, req.URL.String())
		return reply(302, http.Header{"Location": {"https://evil.example/"}}, ""), nil
	}
	_, err = c.Do(context.Background(), Request{URL: "https://api.example.test/start", Policy: testPolicy()})
	if !errors.Is(err, ErrPolicy) || len(calls) != 1 {
		t.Fatalf("redirect bypassed target policy: %v %v", calls, err)
	}
}

func TestRedirectBudgetAndNoFollow(t *testing.T) {
	calls := 0
	c := fixtureClient(func(*http.Request, []netip.Addr, Policy) (*http.Response, error) {
		calls++
		return reply(302, http.Header{"Location": {"/loop"}}, ""), nil
	})
	_, err := c.Do(context.Background(), Request{URL: "https://api.example.test/start", Policy: testPolicy()})
	if !errors.Is(err, ErrRedirect) || calls != 3 {
		t.Fatalf("redirect budget %d %v", calls, err)
	}
	p := testPolicy()
	p.FollowRedirects = false
	out, err := c.Do(context.Background(), Request{URL: "https://api.example.test/start", Policy: p})
	if err != nil || out.StatusCode != 302 || len(out.Body) != 0 {
		t.Fatalf("single-hop response %+v %v", out, err)
	}
}

func TestDecodedAndWireLimits(t *testing.T) {
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, _ = gz.Write(bytes.Repeat([]byte("x"), 101))
	_ = gz.Close()
	if _, err := readBody(bytes.NewReader(compressed.Bytes()), "gzip", 100); !errors.Is(err, ErrLimit) {
		t.Fatalf("decoded limit %v", err)
	}
	if _, err := readBody(strings.NewReader(strings.Repeat("x", 101)), "identity", 100); !errors.Is(err, ErrLimit) {
		t.Fatalf("wire limit %v", err)
	}
	if value, err := readBody(strings.NewReader("complete"), "identity", 100); err != nil || string(value) != "complete" {
		t.Fatal("bounded complete body rejected")
	}
}

func TestRequestDeadlineCoversDNS(t *testing.T) {
	c := fixtureClient(func(*http.Request, []netip.Addr, Policy) (*http.Response, error) {
		t.Fatal("transport called after cancelled DNS")
		return nil, nil
	})
	c.resolver = resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) { <-ctx.Done(); return nil, ctx.Err() })
	p := testPolicy()
	p.Timeout = 10 * time.Millisecond
	_, err := c.Do(context.Background(), Request{URL: "https://api.example.test/detail", Policy: p})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
}
