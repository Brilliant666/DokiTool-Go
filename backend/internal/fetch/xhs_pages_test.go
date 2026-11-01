package fetch

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	browserh2 "github.com/bogdanfinn/fhttp/http2"
	"github.com/bogdanfinn/tls-client/profiles"
)

// These roots and the loopback dial mapping exist only in the test binary.
// The product constructor exposes neither, and production DNS still rejects
// loopback. Both real fhttp HTTP implementations and real TLS execute here.
func xhsTestServer(t *testing.T, h2 bool, handler http.Handler, names ...string) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	if len(names) == 0 {
		names = []string{"www.rednote.com", "www.xiaohongshu.com", "xhslink.com"}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: serial,
		Subject:   pkix.Name{CommonName: "DokiTool synthetic transport test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: names, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = h2
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, roots
}

func xhsLoopbackDial(t *testing.T, server *httptest.Server) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		// Failure here would expose DNS rebinding, a proxy or a QUIC escape.
		if network != "tcp" || address != "8.8.8.8:443" {
			return nil, fmt.Errorf("unapproved test dial destination")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
}

// XHSLoopbackClientForTest lets the external integration test import the actual
// platform and HTTP API without a package cycle. It is not compiled in service.
func XHSLoopbackClientForTest(t *testing.T, h2 bool, handler http.Handler) *Client {
	t.Helper()
	server, roots := xhsTestServer(t, h2, handler)
	client := NewXiaohongshuPages()
	client.resolver = resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	})
	client.transport = xhsPageDispatch(func(*http.Request, []netip.Addr, Policy) (*http.Response, error) {
		return nil, errors.New("unexpected non-page network operation in page fixture")
	}, func(req *http.Request, ips []netip.Addr, p Policy) (*http.Response, error) {
		return chrome131WithDial(req, ips, p, xhsLoopbackDial(t, server), roots)
	})
	return client
}

func xhsPageRequest() Request {
	return Request{URL: "https://www.rednote.com/explore/synthetic?xsec_token=test%2Bvalue%2F%3D&x=one+two&x=three",
		Header: http.Header{"User-Agent": {xhsChrome131UA}, "Accept": {"text/html"}, "Accept-Language": {"zh-CN"}},
		Policy: Policy{Purpose: "xiaohongshu-page", AllowedURL: xhsPageHosts, Timeout: 2 * time.Second, MaxBytes: 1 << 20}}
}

func TestXHSChrome131RealTLSAndHTTP(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("http2=%v", h2), func(t *testing.T) {
			for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
				t.Setenv(env, "http://127.0.0.1:1")
			}
			request := xhsPageRequest()
			calls := atomic.Int32{}
			client := XHSLoopbackClientForTest(t, h2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.TLS == nil || r.TLS.ServerName != "www.rednote.com" {
					t.Error("lost TLS/SNI identity")
				}
				if (r.ProtoMajor == 2) != h2 {
					t.Errorf("unexpected protocol %s", r.Proto)
				}
				if r.Host != "www.rednote.com" || r.RequestURI != strings.TrimPrefix(request.URL, "https://www.rednote.com") {
					t.Error("authority or signed query bytes changed")
				}
				for key, want := range map[string]string{"User-Agent": xhsChrome131UA, "Accept-Encoding": "gzip,deflate", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Sec-Ch-Ua-Platform": `"Windows"`} {
					if r.Header.Get(key) != want {
						t.Errorf("unexpected %s", key)
					}
				}
				if !strings.Contains(r.Header.Get("Sec-Ch-Ua"), `v="131"`) || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Error("profile or anonymous boundary changed")
				}
				var body bytes.Buffer
				gz := gzip.NewWriter(&body)
				_, _ = gz.Write([]byte("synthetic html"))
				_ = gz.Close()
				w.Header().Set("Content-Encoding", "gzip")
				_, _ = w.Write(body.Bytes())
			}))
			response, err := client.Do(context.Background(), request)
			if err != nil || response.StatusCode != 200 || string(response.Body) != "synthetic html" || calls.Load() != 1 {
				t.Fatalf("real Chrome131 request failed: status=%d calls=%d error=%v", response.StatusCode, calls.Load(), err)
			}
			// A new hop cannot inherit server cookies or a previous connection.
			if _, err := client.Do(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 {
				t.Fatal("unexpected request replay")
			}
		})
	}
}

func TestXHSPageTransportDispatchAndAuthorization(t *testing.T) {
	for _, name := range []string{"page", "http-share", "media", "other-platform", "foreign", "cdn-as-page", "suffix", "fragment", "post", "range", "cookie", "authorization", "header-control", "auto-follow", "strict", "long-budget"} {
		t.Run(name, func(t *testing.T) {
			request := xhsPageRequest()
			standard, browser := 0, 0
			client := fixtureClient(xhsPageDispatch(
				func(*http.Request, []netip.Addr, Policy) (*http.Response, error) {
					standard++
					return reply(200, nil, "standard"), nil
				},
				func(*http.Request, []netip.Addr, Policy) (*http.Response, error) {
					browser++
					return reply(200, nil, "browser"), nil
				}))
			accepted, useStandard := true, false
			switch name {
			case "http-share":
				request.URL = "http://xhslink.com/synthetic"
				useStandard = true
			case "media":
				request.Policy.Purpose = "media-probe"
				request.Policy.StrictMediaHeaders = true
				useStandard = true
			case "other-platform":
				request.Policy.Purpose = "weibo-json"
				useStandard = true
			case "foreign":
				request.URL = "https://example.org/"
				request.Policy.AllowedURL = AllowHosts("example.org")
				accepted = false
			case "cdn-as-page":
				request.URL = "https://sns-video-qn.xhscdn.com/key"
				request.Policy.AllowedURL = AllowHosts("sns-video-qn.xhscdn.com")
				accepted = false
			case "suffix":
				request.URL = "https://www.rednote.com.example.org/"
				accepted = false
			case "fragment":
				request.URL += "#fragment"
				accepted = false
			case "post":
				request.Method = http.MethodPost
				accepted = false
			case "range":
				request.Header["rAnGe"] = []string{"bytes=0-7"}
				accepted = false
			case "cookie":
				request.Header["cOoKiE"] = []string{"synthetic=1"}
				accepted = false
			case "authorization":
				request.Header.Set("Authorization", "synthetic")
				accepted = false
			case "header-control":
				request.Header["Header-Order:"] = []string{"cookie"}
				accepted = false
			case "auto-follow":
				request.Policy.FollowRedirects = true
				accepted = false
			case "strict":
				request.Policy.StrictMediaHeaders = true
				accepted = false
			case "long-budget":
				request.Policy.Timeout = 11 * time.Second
				accepted = false
			}
			_, err := client.Do(context.Background(), request)
			if accepted {
				if err != nil || standard+browser != 1 || (standard == 1) != useStandard {
					t.Fatalf("dispatch standard=%d browser=%d error=%v", standard, browser, err)
				}
			} else if !errors.Is(err, ErrPolicy) || standard+browser != 0 {
				t.Fatalf("unsafe page dispatched: %v", err)
			}
		})
	}
}

func TestXHSPagePublicDNSBeforeSocket(t *testing.T) {
	for _, bad := range []string{"127.0.0.1", "10.1.1.1", "::1", "::ffff:8.8.8.8", "2001:db8::1"} {
		t.Run(bad, func(t *testing.T) {
			called := false
			client := fixtureClient(xhsPageDispatch(standardRoundTrip, func(*http.Request, []netip.Addr, Policy) (*http.Response, error) { called = true; return nil, nil }))
			client.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr(bad)}, nil
			})
			_, err := client.Do(context.Background(), xhsPageRequest())
			if !errors.Is(err, ErrPolicy) || called {
				t.Fatalf("mixed DNS escaped: %v", err)
			}
		})
	}
}

func TestXHSChrome131CertificateVerification(t *testing.T) {
	for _, name := range []string{"untrusted-root", "wrong-name"} {
		t.Run(name, func(t *testing.T) {
			names := []string{"www.rednote.com"}
			if name == "wrong-name" {
				names = []string{"other.example.test"}
			}
			server, roots := xhsTestServer(t, false, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unverified request reached HTTP") }), names...)
			if name == "untrusted-root" {
				roots = x509.NewCertPool()
			}
			client := fixtureClient(xhsPageDispatch(standardRoundTrip, func(req *http.Request, ips []netip.Addr, p Policy) (*http.Response, error) {
				return chrome131WithDial(req, ips, p, xhsLoopbackDial(t, server), roots)
			}))
			request := xhsPageRequest()
			_, err := client.Do(context.Background(), request)
			if err == nil {
				t.Fatal("invalid certificate accepted")
			}
			if strings.Contains(err.Error(), "xsec_token") || strings.Contains(err.Error(), "test%2B") {
				t.Fatal("signed query leaked into transport error")
			}
		})
	}
}

func TestXHSChrome131NoImplicitRedirectsOrCookies(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprint(h2), func(t *testing.T) {
			calls := atomic.Int32{}
			client := XHSLoopbackClientForTest(t, h2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Cookie") != "" {
					t.Error("cookie jar leaked across requests")
				}
				w.Header().Set("Location", "https://127.0.0.1/private")
				w.Header().Set("Set-Cookie", "anonymous=synthetic; Path=/; Secure")
				w.WriteHeader(302)
				_, _ = io.WriteString(w, "not read")
			}))
			for i := 0; i < 2; i++ {
				result, err := client.Do(context.Background(), xhsPageRequest())
				if err != nil || result.StatusCode != 302 || len(result.Body) != 0 || len(result.History) != 0 {
					t.Fatalf("redirect not returned intact: %+v %v", result, err)
				}
			}
			if calls.Load() != 2 {
				t.Fatal("library followed or retried redirect")
			}
		})
	}
}

func TestXHSChrome131BodyAndHeaderLimits(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, mode := range []string{"wire", "gzip", "unsupported-encoding", "headers"} {
			t.Run(fmt.Sprintf("h2=%v/%s", h2, mode), func(t *testing.T) {
				request := xhsPageRequest()
				request.Policy.MaxBytes = 64
				request.Policy.MaxHeaderBytes = 1024
				client := XHSLoopbackClientForTest(t, h2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch mode {
					case "wire":
						_, _ = io.WriteString(w, strings.Repeat("x", 65))
					case "gzip":
						var body bytes.Buffer
						gz := gzip.NewWriter(&body)
						_, _ = gz.Write(bytes.Repeat([]byte("x"), 1000))
						_ = gz.Close()
						w.Header().Set("Content-Encoding", "gzip")
						_, _ = w.Write(body.Bytes())
					case "unsupported-encoding":
						w.Header().Set("Content-Encoding", "br")
						_, _ = io.WriteString(w, "not decoded")
					case "headers":
						w.Header().Set("X-Oversized", strings.Repeat("x", 4096))
						_, _ = io.WriteString(w, "small")
					}
				}))
				_, err := client.Do(context.Background(), request)
				if err == nil {
					t.Fatal("response escaped bound")
				}
				if (mode == "wire" || mode == "gzip") && !errors.Is(err, ErrLimit) {
					t.Fatalf("wrong size failure: %v", err)
				}
			})
		}
	}
}

func TestXHSChrome131CancellationClosesConnections(t *testing.T) {
	for _, phase := range []string{"tls", "headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			started, closed := make(chan struct{}), make(chan struct{})
			request := xhsPageRequest()
			request.Policy.Timeout = time.Second
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var client *Client
			if phase == "tls" {
				client = fixtureClient(xhsPageDispatch(standardRoundTrip, func(req *http.Request, ips []netip.Addr, p Policy) (*http.Response, error) {
					return chrome131WithDial(req, ips, p, func(ctx context.Context, network, address string) (net.Conn, error) {
						local, remote := net.Pipe()
						go func() { defer close(closed); defer remote.Close(); _, _ = io.Copy(io.Discard, remote) }()
						close(started)
						return local, nil
					}, nil)
				}))
			} else {
				client = XHSLoopbackClientForTest(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					close(started)
					if phase == "body" {
						_, _ = io.WriteString(w, "partial")
						w.(http.Flusher).Flush()
					}
					<-r.Context().Done()
					close(closed)
				}))
			}
			done := make(chan error, 1)
			go func() { _, err := client.Do(ctx, request); done <- err }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("phase did not start")
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Error("cancelled response succeeded")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("request did not terminate")
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("request socket or stream not closed")
			}
		})
	}
}

func TestXHSChrome131HeaderDeadline(t *testing.T) {
	client := XHSLoopbackClientForTest(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	request := xhsPageRequest()
	request.Policy.Timeout = 50 * time.Millisecond
	_, err := client.Do(context.Background(), request)
	if err == nil {
		t.Fatal("header wait exceeded budget without an error")
	}
}

func TestXHSChrome131ProfileIsolated(t *testing.T) {
	initial := profiles.Chrome_131.GetSettings()[browserh2.SettingMaxHeaderListSize]
	var group sync.WaitGroup
	for _, limit := range []int64{1024, 4096, 64 << 10} {
		group.Add(1)
		go func(limit int64) {
			defer group.Done()
			profile := boundedChrome131(limit)
			if profile.GetSettings()[browserh2.SettingMaxHeaderListSize] != uint32(limit) {
				t.Error("header profile not bounded")
			}
			profile.GetSettings()[browserh2.SettingHeaderTableSize] = 1
			if profile.GetClientHelloStr() != profiles.Chrome_131.GetClientHelloStr() {
				t.Error("ClientHello identity changed")
			}
		}(limit)
	}
	group.Wait()
	if profiles.Chrome_131.GetSettings()[browserh2.SettingMaxHeaderListSize] != initial || profiles.Chrome_131.GetSettings()[browserh2.SettingHeaderTableSize] == 1 {
		t.Fatal("global Chrome profile mutated")
	}
}

func TestXHSConnectionLateDialCannotOutliveCleanup(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	connections := &xhsConnections{}
	connections.close()
	if connections.add(local) {
		t.Fatal("late connection admitted")
	}
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := remote.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("late socket not closed: %v", err)
	}
}
