package shresolver

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type resolveExecutor func(context.Context, fetch.Request) (fetch.Response, error)

func (f resolveExecutor) Do(ctx context.Context, r fetch.Request) (fetch.Response, error) {
	return f(ctx, r)
}

const testEndpoint = "https://resolver.example.org/v1/douyin/original"
const wireVideo = "v019abcdef012345"
const wireNow int64 = 1800000000
const mediaPath = "/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/6ae980c3/video/tos/cn/tos-cn-v-0015/objectA/"
const wireToken = "0123456789abcdef0123456789abcdef"

func goodResponse() Response {
	return Response{VideoID: wireVideo, Candidates: []Candidate{{URL: "https://v3-dy-o.zjcdn.com" + mediaPath, ObtainedAt: wireNow, PrefixSHA256: strings.Repeat("a", 64), Metadata: unified.Object{"width": 2160, "height": 3840, "size": 93733891, "bit_rate": 54730256, "duration": 13.667, "codec": "hevc", "metadata_source": "mp4", "has_audio": true}}}}
}
func newWireClient(t *testing.T, r Response, privateDNS bool) *Client {
	t.Helper()
	c, err := NewClient(testEndpoint, wireToken, resolveExecutor(func(ctx context.Context, req fetch.Request) (fetch.Response, error) {
		if req.Method != "POST" || req.URL != testEndpoint || req.Header.Get("Authorization") != "Bearer "+wireToken || req.Policy.FollowRedirects || req.Policy.AllowedURL(testEndpoint+"oops") || req.Policy.MaxBytes != maxResponseBytes || strings.Contains(string(req.Body), wireToken) || string(req.Body) != "{\"video_id\":\""+wireVideo+"\"}" {
			t.Fatal("incorrect service request/credential scope")
		}
		body, err := json.Marshal(r)
		return fetch.Response{StatusCode: 200, Body: body}, err
	}))
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Unix(wireNow, 0) }
	c.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		ips := []netip.Addr{netip.MustParseAddr("1.1.1.1")}
		if privateDNS {
			ips = append(ips, netip.MustParseAddr("127.0.0.1"))
		}
		return ips, nil
	}
	return c
}
func TestClientChecksTheWholeOriginalSet(t *testing.T) {
	r := goodResponse()
	next := r.Candidates[0]
	next.URL = "https://v26-cold.douyinvod.com" + mediaPath + "?expires=1800001234"
	next.ExpiresAt = 1800001234
	r.Candidates = append(r.Candidates, next)
	got, err := newWireClient(t, r, false).ResolveOriginal(t.Context(), wireVideo)
	if err != nil || len(got) != 2 || got[0]["source_key"] == got[1]["source_key"] || got[0]["object_key"] != "/video/tos/cn/tos-cn-v-0015/objectA" || got[1]["expires_at"] != time.Unix(next.ExpiresAt, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("usable sources missing: %v, %v", got, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Response)
	}{
		{"wrong video", func(r *Response) { r.VideoID = "v019abcdef012346" }},
		{"different object", func(r *Response) { r.Candidates[1].URL = strings.Replace(r.Candidates[1].URL, "objectA", "objectB", 1) }},
		{"changed bytes", func(r *Response) { r.Candidates[1].PrefixSHA256 = strings.Repeat("b", 64) }},
		{"bad expiry", func(r *Response) { r.Candidates[1].ExpiresAt++ }},
		{"ordinary 1080p", func(r *Response) { r.Candidates[0].Metadata["width"] = 1080 }},
		{"no audio proof", func(r *Response) { delete(r.Candidates[0].Metadata, "has_audio") }},
		{"private identity injection", func(r *Response) { r.Candidates[0].Metadata["private_source_identity"] = "inject" }},
		{"forged host", func(r *Response) { r.Candidates[0].URL = "https://zjcdn.com.evil.example" + mediaPath }},
		{"local IP", func(r *Response) { r.Candidates[0].URL = "https://127.0.0.1" + mediaPath }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(r)
			var bad Response
			json.Unmarshal(body, &bad)
			tc.change(&bad)
			got, err := newWireClient(t, bad, false).ResolveOriginal(t.Context(), wireVideo)
			if err == nil || len(got) > 0 {
				t.Fatal("invalid set accepted")
			}
		})
	}
	if _, err := newWireClient(t, r, true).ResolveOriginal(t.Context(), wireVideo); err == nil {
		t.Fatal("mixed public/private DNS accepted")
	}
}
func TestClientRejectsUnavailableControlPlane(t *testing.T) {
	for _, check := range []struct {
		name   string
		status int
		body   string
		err    error
	}{
		{"authentication", 401, "", nil}, {"redirect", 302, "", nil}, {"disconnect", 0, "", errors.New("EOF")}, {"deadline", 0, "", context.DeadlineExceeded}, {"bad JSON", 200, "{", nil}, {"trailing JSON", 200, "{}{}", nil}, {"over limit", 200, strings.Repeat(" ", maxResponseBytes+1), nil},
	} {
		t.Run(check.name, func(t *testing.T) {
			c, err := NewClient(testEndpoint, wireToken, resolveExecutor(func(context.Context, fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: check.status, Body: []byte(check.body)}, check.err
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.ResolveOriginal(t.Context(), wireVideo); err == nil {
				t.Fatal("invalid control plane accepted")
			}
		})
	}
}
func TestLoopbackTLSAndNoRedirect(t *testing.T) {
	var received atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.URL.Path != Path {
			t.Error("followed a redirect with service token")
		}
		http.Redirect(w, r, "/wrong", 302)
	}))
	defer service.Close()
	c, err := NewClient(service.URL+Path, wireToken, fetch.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ResolveOriginal(t.Context(), wireVideo); err == nil || received.Load() != 0 {
		t.Fatal("untrusted TLS certificate accepted")
	}
	c, err = NewClient(service.URL+Path, wireToken, fetch.New())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(service.Certificate())
	c.local.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool
	if _, err = c.ResolveOriginal(t.Context(), wireVideo); err == nil || received.Load() != 1 {
		t.Fatalf("TLS checked restricted endpoint: %v, calls=%d", err, received.Load())
	}
}

func TestClientCancellationStopsBeforeAnyServiceRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c, err := NewClient(testEndpoint, wireToken, resolveExecutor(func(context.Context, fetch.Request) (fetch.Response, error) {
		t.Fatal("cancelled resolution reached transport")
		return fetch.Response{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ResolveOriginal(ctx, wireVideo); !errors.Is(err, context.Canceled) {
		t.Fatalf("want original cancellation, got %v", err)
	}
}
