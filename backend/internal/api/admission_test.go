package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testEdgeKey = "synthetic-test-key-32-bytes-not-a-deployment-key"

func protected(t *testing.T, mode string, next http.Handler) *admission {
	t.Helper()
	key := ""
	if mode == "caddy" {
		key = testEdgeKey
	}
	entry, err := WithAdmission(next, mode, key)
	if err != nil {
		t.Fatal(err)
	}
	return entry.(*admission)
}

func edgeRequest(path, ip string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set(edgeHeader, testEdgeKey)
	req.Header.Set(clientHeader, ip)
	return req
}
func call(entry http.Handler, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	entry.ServeHTTP(w, req)
	return w
}

func TestAdmissionRateAndContracts(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(edgeHeader) != "" || r.Header.Get(clientHeader) != "" {
			t.Error("hop-private header leaked downstream")
		}
		w.WriteHeader(200)
	})
	now := time.Unix(1000, 0)
	entry := protected(t, "caddy", next)
	entry.now = func() time.Time { return now }
	paths := []string{"/api/parse", "/api/resolve", "/api/platforms", "/api/bilibili"}
	for _, path := range paths {
		if got := call(entry, edgeRequest(path, "::ffff:198.51.100.11")); got.Code != 200 {
			t.Fatalf("%s: %d", path, got.Code)
		}
	}
	check := func(path string, want int, message string) {
		t.Helper()
		w := call(entry, edgeRequest(path, "198.51.100.11"))
		if w.Code != 429 || w.Header().Get("Retry-After") != strconv.Itoa(want) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %v", path, w.Code, w.Header())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if path == "/api/short_videos" {
			if body["code"] != float64(429) || body["data"] != nil || body["msg"] != message {
				t.Fatal(body)
			}
		} else {
			err, ok := body["error"].(map[string]any)
			if !ok || body["code"] != "RATE_LIMITED" || body["schema_version"] != "1.0" || err["retry_after_seconds"] != float64(want) || err["retryable"] != true || err["message"] != message {
				t.Fatal(body)
			}
		}
	}
	check("/api/short_videos", 5, "解析请求过于频繁，请稍后重试")
	now = now.Add(1500 * time.Millisecond)
	check("/api/resolve", 4, "解析请求过于频繁，请稍后重试")
	if w := call(entry, edgeRequest("/api/media/weibo", "198.51.100.11")); w.Code != 200 {
		t.Fatalf("public exhausted relay: %d", w.Code)
	}
	now = now.Add(3500 * time.Millisecond)
	if w := call(entry, edgeRequest("/api/platforms", "198.51.100.11")); w.Code != 200 {
		t.Fatal(w.Code)
	}
	// Encoded or unknown paths are dispatched as-is, not charged to either pool.
	if w := call(entry, edgeRequest("/api/%70arse", "bad IP")); w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestAdmissionGlobalRateAndIPGroups(t *testing.T) {
	for _, tc := range []struct {
		name     string
		capacity int
		step     time.Duration
		path     string
	}{
		{"public", 8, time.Second, "/api/platforms"}, {"relay", 24, time.Second / 2, "/api/media/weibo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			entry := protected(t, "caddy", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
			entry.now = func() time.Time { return now }
			for i := 0; i < tc.capacity; i++ {
				if w := call(entry, edgeRequest(tc.path, fmt.Sprintf("192.0.2.%d", i+1))); w.Code != 204 {
					t.Fatal(w.Code)
				}
			}
			if w := call(entry, edgeRequest(tc.path, "192.0.2.99")); w.Code != 429 || w.Header().Get("Retry-After") != "1" {
				t.Fatal(w.Code, w.Header())
			}
			now = now.Add(tc.step)
			if w := call(entry, edgeRequest(tc.path, "192.0.2.99")); w.Code != 204 {
				t.Fatal(w.Code)
			}
		})
	}
	entry := protected(t, "caddy", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	entry.now = func() time.Time { return time.Unix(1000, 0) }
	for i := 1; i <= 12; i++ {
		if w := call(entry, edgeRequest("/api/media/weibo", fmt.Sprintf("2001:db8:1:2::%x", i))); w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
	req := edgeRequest("/api/media/weibo", "2001:db8:1:2::aa")
	if w := call(entry, req.Clone(req.Context())); w.Code != 429 || !strings.Contains(w.Body.String(), "媒体请求过于频繁") {
		t.Fatal(w.Body.String())
	}
	req.Method = http.MethodHead
	if w := call(entry, req); w.Code != 429 || w.Body.Len() != 0 || w.Header().Get("Retry-After") != "1" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(entry, edgeRequest("/api/media/weibo", "2001:db8:1:3::1")); w.Code != 204 {
		t.Fatal(w.Code)
	}
}

func TestAdmissionClientTrust(t *testing.T) {
	for _, tc := range []struct{ mode, key string }{{"caddy", ""}, {"caddy", "too-short"}, {"caddy", testEdgeKey + "\n"}, {"auto", testEdgeKey}, {"direct", testEdgeKey}} {
		if _, err := WithAdmission(nil, tc.mode, tc.key); err == nil {
			t.Fatal("invalid identity configuration accepted")
		}
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	entry := protected(t, "caddy", next)
	for _, change := range []func(*http.Request){
		func(r *http.Request) { r.RemoteAddr = "192.0.2.1:10" },
		func(r *http.Request) { r.Header.Del(edgeHeader) },
		func(r *http.Request) { r.Header.Set(edgeHeader, "incorrect") },
		func(r *http.Request) { r.Header.Add(edgeHeader, testEdgeKey) },
		func(r *http.Request) { r.Header.Add(clientHeader, "192.0.2.2") },
		func(r *http.Request) { r.Header.Del(clientHeader) },
		func(r *http.Request) { r.Header.Set(clientHeader, "192.0.2.1, 192.0.2.2") },
		func(r *http.Request) { r.Header.Set(clientHeader, "fe80::1%eth0") },
	} {
		req := edgeRequest("/api/parse", "198.51.100.2")
		change(req)
		req.Header.Set("X-Forwarded-For", "198.51.100.2")
		w := call(entry, req)
		if w.Code != 503 || w.Header().Get("Retry-After") != "30" || !strings.Contains(w.Body.String(), "SERVICE_NOT_CONFIGURED") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, mode := range []string{"direct", "caddy"} {
		entry := protected(t, mode, next)
		entry.now = func() time.Time { return time.Unix(1000, 0) }
		for i := 0; i < 5; i++ {
			req := edgeRequest("/api/parse", "203.0.113.6")
			if mode == "direct" {
				req.Header.Del(edgeHeader)
				req.Header.Del(clientHeader)
			}
			req.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d", i))
			req.Header.Set("X-Real-IP", fmt.Sprintf("198.51.100.%d", i))
			want := 204
			if i == 4 {
				want = 429
			}
			if got := call(entry, req); got.Code != want {
				t.Fatalf("%s: %d", mode, got.Code)
			}
		}
	}
	if w := call(protected(t, "direct", next), edgeRequest("/api/parse", "192.0.2.1")); w.Code != 503 {
		t.Fatal("misconfigured Caddy silently collapsed identities")
	}
}

func TestAdmissionFullStreamLeaseAndRelease(t *testing.T) {
	finished := make(chan struct{}, 4)
	entry := protected(t, "caddy", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("slow") {
			w.Write([]byte("first"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		} else if r.URL.Query().Has("error") {
			w.WriteHeader(http.StatusBadGateway)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry.ServeHTTP(w, r)
		if r.URL.Query().Has("slow") {
			finished <- struct{}{}
		}
	}))
	defer s.Close()
	send := func(path, ip string, want int) *http.Response {
		t.Helper()
		req, err := http.NewRequest("GET", s.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(edgeHeader, testEdgeKey)
		req.Header.Set(clientHeader, ip)
		resp, err := s.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			resp.Body.Close()
			t.Fatalf("%s: got %d want %d", path, resp.StatusCode, want)
		}
		return resp
	}
	check := func(path, ip string, want int) {
		resp := send(path, ip, want)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	stop := func(resp *http.Response) {
		resp.Body.Close()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("client disconnect did not release lease")
		}
	}
	hold := func(path, ip string) *http.Response {
		r := send(path+"?slow", ip, 200)
		t.Cleanup(func() { r.Body.Close() })
		return r
	}
	p1 := hold("/api/parse", "192.0.2.1")
	defer p1.Body.Close()
	check("/api/resolve", "192.0.2.1", 429)
	p2 := hold("/api/platforms", "192.0.2.2")
	defer p2.Body.Close()
	check("/api/resolve", "192.0.2.3", 429)
	m1 := hold("/api/media/weibo", "192.0.2.1")
	defer m1.Body.Close()
	m2 := hold("/api/media/weibo", "192.0.2.1")
	defer m2.Body.Close()
	check("/api/media/weibo", "192.0.2.2", 429)
	stop(m1)
	stop(p1)
	check("/api/resolve?error", "192.0.2.1", 502)
	check("/api/parse", "192.0.2.1", 204)
	check("/api/media/weibo", "192.0.2.1", 204)
	stop(m2)
	stop(p2)
}

func TestAdmissionBoundedClientsAndIdleExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	p := newPool(12, 1, 24, 2, 2)
	for i := 0; i < 1024; i++ {
		release, retry := p.acquire(netip.AddrFrom4([4]byte{192, 0, byte(i >> 8), byte(i)}), now)
		if retry != 0 {
			t.Fatal(retry)
		}
		release()
		now = now.Add(time.Second / 2)
	}
	ip := netip.MustParseAddr("198.51.100.5")
	if release, retry := p.acquire(ip, now); release != nil || retry != 60 {
		t.Fatal(retry)
	}
	if release, retry := p.acquire(ip, now.Add(10*time.Minute)); release == nil || retry != 0 {
		t.Fatal(retry)
	} else {
		release()
	}
}
