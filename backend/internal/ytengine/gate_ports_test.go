package ytengine

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testGatePorts(t *testing.T, count int) []int {
	t.Helper()
	var listeners []net.Listener
	var ports []int
	for i := 0; i < count; i++ {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}
	for _, ln := range listeners {
		_ = ln.Close()
	}
	return ports
}

func inertGateBinary(t *testing.T) (string, map[string]string) {
	t.Helper()
	b := []byte("not executed: binary preflight fixture")
	p := filepath.Join(t.TempDir(), "inert")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return p, map[string]string{hex.EncodeToString(h[:]): "inert"}
}

func TestGatePortConfiguration(t *testing.T) {
	bin, hashes := inertGateBinary(t)
	for _, ports := range [][]int{{1023, 19002}, {19001, 65536}, {19001, 19001}, {19001}, {19001, 19002, 19003}} {
		e, err := New(Config{BinaryPath: bin, AllowedHashes: hashes, GatePorts: ports})
		if err == nil || e.Enabled() {
			t.Fatalf("invalid port set accepted: %v", ports)
		}
	}
	for _, raw := range []string{"127.0.0.1:19001,19002", "localhost:19001", "19001,", "+19001,19002", "19001-19002"} {
		_, err := NewFromEnv(func(key string) string {
			if key == "DOKITOOL_YTDLP_GATE_PORTS" {
				return raw
			}
			return ""
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "GATE_PORTS") {
			t.Fatalf("invalid env syntax accepted: %q", raw)
		}
	}
	ports, err := parseGatePorts("19001, 19002")
	if err != nil {
		t.Fatal(err)
	}
	eng, err := New(Config{BinaryPath: bin, AllowedHashes: hashes, GatePorts: ports})
	if err != nil {
		t.Fatal(err)
	}
	ports[0] = 19999
	if eng.gatePorts.ports[0] != 19001 {
		t.Fatal("configuration slice retained by reference")
	}
	clone := eng.WithTargetPolicy(func(string) bool { return true }, func(string) bool { return true })
	if clone.gatePorts != eng.gatePorts || clone.sem != eng.sem {
		t.Fatal("platform clones must share port and concurrency allocation")
	}
	_, err = New(Config{BinaryPath: bin, AllowedHashes: hashes, GatePorts: []int{19001, 19002}, SharedSemaphore: make(chan struct{}, 1)})
	if err == nil {
		t.Fatal("port pool must match actual shared semaphore capacity")
	}
}

func TestGateFixedPoolNoFallbackAndReclaims(t *testing.T) {
	ports := testGatePorts(t, 2)
	p, err := newGatePortPool(ports, 2)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{gatePorts: p}
	x := e.WithTargetPolicy(nil, func(s string) bool { return s == "x.example" })
	y := e.WithTargetPolicy(nil, func(s string) bool { return s == "y.example" })
	xURL, xClose, err := x.startOutboundGate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer xClose()
	yURL, yClose, err := y.startOutboundGate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer yClose()
	xu, _ := url.Parse(xURL)
	yu, _ := url.Parse(yURL)
	if xu.Host == yu.Host {
		t.Fatal("concurrent platform tasks share a listener")
	}
	if got := gateRawRequest(t, xu, "GET", "http://y.example/", gateAuth(xu)); got != 403 {
		t.Fatal("x task borrowed y target policy")
	}
	if got := gateRawRequest(t, yu, "CONNECT", "x.example:443", gateAuth(yu)); got != 403 {
		t.Fatal("y task borrowed x target policy")
	}
	if got := gateRawRequest(t, yu, "GET", "http://y.example/", gateAuth(xu)); got != 407 {
		t.Fatal("one task credential authorized another task")
	}
	if _, _, err := e.startOutboundGate(context.Background()); err == nil {
		t.Fatal("exhausted fixed pool must not fall back")
	}
	xClose()
	yClose()
	occupied, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", fmt.Sprint(ports[0])))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	u, closeGate, err := e.startOutboundGate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(u)
	if parsed.Port() != fmt.Sprint(ports[1]) {
		t.Fatal("must skip occupied port without contacting its service")
	}
	if _, _, err := e.startOutboundGate(context.Background()); err == nil {
		t.Fatal("occupied ports must not cause random fallback")
	}
	closeGate()
	ctx, cancel := context.WithCancel(context.Background())
	_, closeGate, err = e.startOutboundGate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	closeGate()
	_, closeGate, err = e.startOutboundGate(context.Background())
	if err != nil {
		t.Fatal("cancel leaked port:", err)
	}
	closeGate()
}

type countingGateResolver struct{ count atomic.Int64 }

func (r *countingGateResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.count.Add(1)
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

func gateAuth(u *url.URL) string {
	pass, _ := u.User.Password()
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pass))
}

func gateRawRequest(t *testing.T, u *url.URL, method, target, auth string) int {
	t.Helper()
	c, err := net.DialTimeout("tcp4", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = fmt.Fprintf(c, "%s %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\nConnection: close\r\n\r\n", method, target, "public.example", auth)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestGateAuthenticationBeforePolicyAndExpiry(t *testing.T) {
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("credential forwarded to upstream")
		}
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	}))
	defer up.Close()
	upURL, _ := url.Parse(up.URL)
	resolver := &countingGateResolver{}
	pool, _ := newGatePortPool(testGatePorts(t, 1), 1)
	e := &Engine{maxStdoutBytes: defaultMaxStdoutBytes, gatePorts: pool, resolver: resolver, allowedOutboundHost: func(h string) bool { return h == "public.example" }, dialAddr: func(netip.Addr, string) string { return upURL.Host }}
	u, closeGate, err := e.startOutboundGate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer closeGate()
	parsed, _ := url.Parse(u)
	for _, method := range []string{"GET", "CONNECT"} {
		target := "http://public.example/"
		if method == "CONNECT" {
			target = "public.example:443"
		}
		for _, auth := range []string{"", "Basic d3Jvbmc6d3Jvbmc="} {
			if got := gateRawRequest(t, parsed, method, target, auth); got != 407 {
				t.Fatalf("unauthorized %s status %d", method, got)
			}
		}
	}
	if resolver.count.Load() != 0 || hits.Load() != 0 {
		t.Fatal("unauthenticated request reached DNS or upstream")
	}
	tr := &http.Transport{Proxy: http.ProxyURL(parsed)}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	resp, err := client.Post("http://public.example/post", "text/plain", strings.NewReader("exact request body"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "exact request body" {
		t.Fatalf("authenticated HTTP body not preserved: status=%d body=%q", resp.StatusCode, string(b))
	}
	for _, target := range []string{"http://other.example/", "http://127.0.0.1/", "http://169.254.169.254/"} {
		if got := gateRawRequest(t, parsed, "GET", target, gateAuth(parsed)); got != 403 {
			t.Fatalf("policy bypass %s: %d", target, got)
		}
	}
	oldAuth := gateAuth(parsed)
	closeGate()
	u, nextClose, err := e.startOutboundGate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer nextClose()
	next, _ := url.Parse(u)
	if parsed.Host != next.Host {
		t.Fatal("test must actually reuse the fixed port")
	}
	if oldAuth == gateAuth(next) {
		t.Fatal("new task reused credential")
	}
	before := resolver.count.Load()
	if got := gateRawRequest(t, next, "GET", "http://public.example/", oldAuth); got != 407 {
		t.Fatal("old credential accepted")
	}
	if got := gateRawRequest(t, next, "CONNECT", "public.example:443", oldAuth); got != 407 {
		t.Fatal("old CONNECT credential accepted")
	}
	if resolver.count.Load() != before {
		t.Fatal("expired credential reached DNS")
	}
}

func TestGateCancelActiveTunnelBeforePortReuse(t *testing.T) {
	up, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	upDone := make(chan struct{})
	go func() {
		defer close(upDone)
		c, err := up.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()
	pool, _ := newGatePortPool(testGatePorts(t, 1), 1)
	e := &Engine{gatePorts: pool, resolver: &countingGateResolver{}, allowedOutboundHost: func(h string) bool { return h == "public.example" }, dialAddr: func(netip.Addr, string) string { return up.Addr().String() }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u, closeGate, err := e.startOutboundGate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGate()
	parsed, _ := url.Parse(u)
	c, err := net.DialTimeout("tcp4", parsed.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = fmt.Fprintf(c, "CONNECT public.example:443 HTTP/1.1\r\nHost: public.example:443\r\nProxy-Authorization: %s\r\n\r\n", gateAuth(parsed))
	reader := bufio.NewReader(c)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal("authenticated CONNECT not established")
	}
	_, _ = c.Write([]byte("x"))
	b, err := reader.ReadByte()
	if err != nil || b != 'x' {
		t.Fatal("CONNECT tunnel did not carry payload")
	}
	cancel()
	closeGate()
	_, err = reader.ReadByte()
	if err == nil {
		t.Fatal("cancel left tunnel open")
	}
	if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
		t.Fatal("tunnel expired only by client timeout")
	}
	select {
	case <-upDone:
	case <-time.After(time.Second):
		t.Fatal("upstream tunnel remained open")
	}
	u, nextClose, err := e.startOutboundGate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer nextClose()
	next, _ := url.Parse(u)
	if next.Host != parsed.Host {
		t.Fatal("fixed tunnel port not reused")
	}
	if got := gateRawRequest(t, next, "CONNECT", "public.example:443", gateAuth(parsed)); got != 407 {
		t.Fatal("cancelled tunnel credential remained valid")
	}
}
