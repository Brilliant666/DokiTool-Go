package fetch

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	browserhttp "github.com/bogdanfinn/fhttp"
	browserh2 "github.com/bogdanfinn/fhttp/http2"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

const xhsChrome131UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var xhsPageHosts = AllowHosts("xiaohongshu.com", "www.xiaohongshu.com", "rednote.com", "www.rednote.com", "xhslink.com", "www.xhslink.com", "xhs.com", "www.xhs.com")

// NewXiaohongshuPages changes ONLY the HTTPS xiaohongshu-page handshake.
// Client.Do still owns URL/DNS/public-IP checks and bounded decoding. The parser
// still owns its manual redirect chain. Media probes and every other purpose
// retain standardRoundTrip, including strict raw media-header verification.
// This opt-in constructor is not a generic browser/proxy transport factory.
func NewXiaohongshuPages() *Client {
	client := New()
	client.transport = xhsPageDispatch(standardRoundTrip, chrome131RoundTrip)
	return client
}

func xhsPageDispatch(standard, browser roundTrip) roundTrip {
	return func(req *http.Request, ips []netip.Addr, policy Policy) (*http.Response, error) {
		if policy.Purpose != "xiaohongshu-page" {
			return standard(req, ips, policy)
		}
		if req == nil || req.URL == nil || !xhsPageHosts(req.URL.String()) || req.URL.Fragment != "" ||
			req.Method != http.MethodGet || req.ContentLength != 0 ||
			policy.FollowRedirects || policy.StrictMediaHeaders || policy.Timeout <= 0 || policy.Timeout > 10*time.Second {
			return nil, ErrPolicy
		}
		// No cookies, authorization, media ranges or arbitrary browser-header
		// controls enter this anonymous page-only transport, even via casing.
		for key := range req.Header {
			switch strings.ToLower(key) {
			case "user-agent", "accept", "accept-language", "accept-encoding":
			default:
				return nil, ErrPolicy
			}
		}
		if req.URL.Scheme == "http" {
			// Preserve the existing cleartext short-share entry behavior. Any
			// following HTTPS hop is independently checked and uses Chrome131.
			return standard(req, ips, policy)
		}
		return browser(req, ips, policy)
	}
}

// Clone the upstream profile: its HTTP/2 setting otherwise allows 256 KiB,
// regardless of the HTTP/1 MaxResponseHeaderBytes option. Do not mutate a
// shared library profile. ClientHello remains Chrome_131; the bounded HTTP/2
// header setting is deliberately NOT an exact browser fingerprint.
func boundedChrome131(headerLimit int64) profiles.ClientProfile {
	base := profiles.Chrome_131
	settings := make(map[browserh2.SettingID]uint32, len(base.GetSettings()))
	for key, value := range base.GetSettings() {
		settings[key] = value
	}
	settings[browserh2.SettingMaxHeaderListSize] = uint32(headerLimit)
	return profiles.NewClientProfile(base.GetClientHelloId(), settings,
		append([]browserh2.SettingID(nil), base.GetSettingsOrder()...),
		append([]string(nil), base.GetPseudoHeaderOrder()...), base.GetConnectionFlow(),
		append([]browserh2.Priority(nil), base.GetPriorities()...), base.GetHeaderPriority(),
		base.GetStreamID(), base.GetAllowHTTP(), nil, nil, 0, nil, false)
}

func chrome131RoundTrip(req *http.Request, ips []netip.Addr, policy Policy) (*http.Response, error) {
	return chrome131WithDial(req, ips, policy, (&net.Dialer{Timeout: 5 * time.Second}).DialContext, nil)
}

// roots and dial are private test seams, never caller-controlled service inputs.
// The production path always uses verified system roots and the pinned TCP IPs.
func chrome131WithDial(req *http.Request, ips []netip.Addr, policy Policy,
	dial func(context.Context, string, string) (net.Conn, error), roots *x509.CertPool,
) (*http.Response, error) {
	if req == nil || req.URL == nil || req.URL.Scheme != "https" || !xhsPageHosts(req.URL.String()) || len(ips) == 0 ||
		policy.MaxHeaderBytes <= 0 || policy.MaxHeaderBytes > 64<<10 {
		return nil, ErrPolicy
	}
	for _, ip := range ips {
		if !PublicIP(ip) {
			return nil, ErrPolicy
		}
	}
	deadline, exists := req.Context().Deadline()
	if !exists || req.Context().Err() != nil {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		return nil, ErrPolicy
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, context.DeadlineExceeded
	}
	connections := &xhsConnections{}
	pinned := func(_ context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(host, req.URL.Hostname()) || port != "443" ||
			(network != "tcp" && network != "tcp4" && network != "tcp6") {
			return nil, ErrPolicy
		}
		var last error
		for _, ip := range ips {
			if err := req.Context().Err(); err != nil {
				return nil, err
			}
			// Use the original request context even when fhttp detaches its
			// HTTP/2 redial context. Never pass a hostname back to a resolver.
			conn, err := dial(req.Context(), "tcp", net.JoinHostPort(ip.String(), "443"))
			if err != nil {
				last = err
				continue
			}
			conn = bindRequestConnection(req.Context(), conn)
			if err := conn.SetDeadline(deadline); err != nil {
				_ = conn.Close()
				return nil, err
			}
			if !connections.add(conn) {
				return nil, context.Canceled
			}
			return conn, nil
		}
		return nil, last
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
		tlsclient.WithClientProfile(boundedChrome131(policy.MaxHeaderBytes)),
		tlsclient.WithNotFollowRedirects(), tlsclient.WithDisableHttp3(),
		tlsclient.WithDialContext(pinned),
		tlsclient.WithTimeoutMilliseconds(int((remaining+time.Millisecond-1)/time.Millisecond)),
		tlsclient.WithTransportOptions(&tlsclient.TransportOptions{
			RootCAs: roots, DisableCompression: true, DisableKeepAlives: true,
			MaxResponseHeaderBytes: policy.MaxHeaderBytes, ResponseHeaderTimeout: remaining,
		}),
	)
	if err != nil {
		return nil, err
	}
	cleanup := sync.OnceFunc(func() {
		// Close sockets first, so a background TLS dial cannot keep the
		// library transport's shutdown lock held after cancellation.
		connections.close()
		client.Close()
	})
	request, err := browserhttp.NewRequestWithContext(req.Context(), http.MethodGet, req.URL.String(), nil)
	if err != nil {
		cleanup()
		return nil, ErrPolicy
	}
	request.Header = make(browserhttp.Header)
	for key, values := range req.Header {
		request.Header[strings.ToLower(key)] = append([]string(nil), values...)
	}
	request.Header["user-agent"] = []string{xhsChrome131UA}
	request.Header["sec-ch-ua"] = []string{`"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`}
	request.Header["sec-ch-ua-mobile"] = []string{"?0"}
	request.Header["sec-ch-ua-platform"] = []string{`"Windows"`}
	request.Header["upgrade-insecure-requests"] = []string{"1"}
	request.Header["sec-fetch-site"] = []string{"none"}
	request.Header["sec-fetch-mode"] = []string{"navigate"}
	request.Header["sec-fetch-user"] = []string{"?1"}
	request.Header["sec-fetch-dest"] = []string{"document"}
	// Advertise only codecs supported by the shared bounded decoder.
	request.Header["accept-encoding"] = []string{"gzip,deflate"}
	request.Header[browserhttp.HeaderOrderKey] = []string{
		"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "upgrade-insecure-requests",
		"user-agent", "accept", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-user",
		"sec-fetch-dest", "accept-encoding", "accept-language",
	}
	response, err := client.Do(request)
	if err != nil {
		cleanup()
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		// The library's url.Error includes the signed request URL. Preserve
		// the underlying error without putting that query in diagnostics.
		var requestError *url.Error
		if errors.As(err, &requestError) {
			return nil, requestError.Err
		}
		return nil, err
	}
	if response == nil || response.Body == nil {
		cleanup()
		return nil, errors.New("missing browser page response")
	}
	return &http.Response{
		StatusCode: response.StatusCode, Status: response.Status, Proto: response.Proto,
		ProtoMajor: response.ProtoMajor, ProtoMinor: response.ProtoMinor,
		Header: http.Header(response.Header), Trailer: http.Header(response.Trailer),
		ContentLength: response.ContentLength, Request: req,
		Body: &xhsResponseBody{ReadCloser: response.Body, cleanup: cleanup},
	}, nil
}

type xhsConnections struct {
	sync.Mutex
	closed bool
	all    []net.Conn
}

func (s *xhsConnections) add(conn net.Conn) bool {
	s.Lock()
	defer s.Unlock()
	if s.closed {
		_ = conn.Close()
		return false
	}
	s.all = append(s.all, conn)
	return true
}

func (s *xhsConnections) close() {
	s.Lock()
	s.closed = true
	all := s.all
	s.all = nil
	s.Unlock()
	for _, conn := range all {
		_ = conn.Close()
	}
}

type xhsResponseBody struct {
	io.ReadCloser
	cleanup func()
}

func (b *xhsResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cleanup()
	return err
}
