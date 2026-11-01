package fetch

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func redirectBodyPolicy() Policy {
	policy := testPolicy()
	policy.FollowRedirects = false
	policy.ReadRedirectBody = true
	return policy
}

func TestReadRedirectBodyThroughActualTransportWithoutFollowingLocation(t *testing.T) {
	const body = `{"data":{"post":{"videos":[]}}}`
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			var calls atomic.Int32
			client := reviewRealTransportClient(t, func(conn net.Conn) {
				calls.Add(1)
				_, _ = fmt.Fprintf(conn, "HTTP/1.1 302 Found\r\nLocation: http://127.0.0.1/private\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
			})
			result, err := client.Do(context.Background(), Request{URL: "http://api.example.test/detail", Method: method,
				Body: []byte(`{}`), Policy: redirectBodyPolicy()})
			if err != nil || result.StatusCode != 302 || result.URL != "http://api.example.test/detail" || string(result.Body) != body || len(result.History) != 0 || calls.Load() != 1 {
				t.Fatalf("non-followed response lost body or visited Location: calls=%d response=%+v err=%v", calls.Load(), result, err)
			}
		})
	}
}

type unreadRedirectBody struct {
	t      *testing.T
	closed bool
}

func (body *unreadRedirectBody) Read([]byte) (int, error) {
	body.t.Error("default/HEAD redirect unexpectedly read response bytes")
	return 0, errors.New("unexpected redirect body read")
}

func (body *unreadRedirectBody) Close() error { body.closed = true; return nil }

func TestRedirectBodyDefaultAndHEADDoNotRead(t *testing.T) {
	for _, test := range []struct {
		name, method string
		read         bool
	}{
		{"default-get", http.MethodGet, false},
		{"default-post", http.MethodPost, false},
		{"default-head", http.MethodHead, false},
		{"opt-in-head", http.MethodHead, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &unreadRedirectBody{t: t}
			calls := 0
			client := fixtureClient(func(*http.Request, []netip.Addr, Policy) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://example.com/private"}}, Body: body}, nil
			})
			policy := testPolicy()
			policy.FollowRedirects = false
			policy.ReadRedirectBody = test.read
			result, err := client.Do(context.Background(), Request{URL: "https://api.example.test/start", Method: test.method, Policy: policy})
			if err != nil || result.StatusCode != 302 || len(result.Body) != 0 || !body.closed || calls != 1 {
				t.Fatalf("default/HEAD behavior changed: response=%+v err=%v closed=%v calls=%d", result, err, body.closed, calls)
			}
		})
	}
}

func TestReadRedirectBodyKeepsWireAndDecodedByteLimits(t *testing.T) {
	var compressed bytes.Buffer
	zip := gzip.NewWriter(&compressed)
	_, _ = zip.Write(bytes.Repeat([]byte("x"), 101))
	_ = zip.Close()
	if compressed.Len() >= 100 {
		t.Fatal("compressed test must isolate the decoded byte limit")
	}
	for _, test := range []struct {
		name, encoding string
		body           []byte
	}{
		{"wire", "identity", bytes.Repeat([]byte("x"), 101)},
		{"compressed-wire", "gzip", bytes.Repeat([]byte("x"), 101)},
		{"decoded", "gzip", compressed.Bytes()},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := reviewRealTransportClient(t, func(conn net.Conn) {
				_, _ = fmt.Fprintf(conn, "HTTP/1.1 302 Found\r\nLocation: /unused\r\nContent-Encoding: %s\r\nContent-Length: %d\r\n\r\n", test.encoding, len(test.body))
				_, _ = conn.Write(test.body)
			})
			result, err := client.Do(context.Background(), Request{URL: "http://api.example.test/detail", Policy: redirectBodyPolicy()})
			if !errors.Is(err, ErrLimit) || result.StatusCode != 0 || len(result.Body) != 0 {
				t.Fatalf("redirect body bypassed byte limit: response=%+v err=%v", result, err)
			}
		})
	}
}

func TestReadRedirectBodyUsesExistingStrictTrailerValidation(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		name := "merged"
		if duplicate {
			name = "duplicate-rejected"
		}
		t.Run(name, func(t *testing.T) {
			client := reviewRealTransportClient(t, func(conn net.Conn) {
				head := "HTTP/1.1 302 Found\r\nLocation: /unused\r\nTransfer-Encoding: chunked\r\nTrailer: Content-Type\r\n"
				if duplicate {
					head += "Content-Type: application/json\r\n"
				}
				_, _ = io.WriteString(conn, head+"\r\n2\r\n{}\r\n0\r\nContent-Type: application/json\r\n\r\n")
			})
			policy := redirectBodyPolicy()
			policy.StrictMediaHeaders = true
			policy.MaxHeaderBytes = 16 << 10
			result, err := client.Do(context.Background(), Request{URL: "http://api.example.test/detail", Policy: policy})
			if duplicate {
				if err == nil || result.StatusCode != 0 || len(result.Body) != 0 {
					t.Fatalf("duplicate critical trailer accepted: response=%+v err=%v", result, err)
				}
			} else if err != nil || string(result.Body) != "{}" || result.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("valid redirect trailer lost: response=%+v err=%v", result, err)
			}
		})
	}
}

func TestReadRedirectBodyStopsOnExternalCancellation(t *testing.T) {
	ready, closed := make(chan struct{}), make(chan struct{})
	client := reviewRealTransportClient(t, func(conn net.Conn) {
		if _, err := io.WriteString(conn, "HTTP/1.1 302 Found\r\nLocation: /unused\r\nContent-Length: 100\r\n\r\n{"); err != nil {
			return
		}
		close(ready)
		var one [1]byte
		if _, err := conn.Read(one[:]); err != nil {
			close(closed)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	policy := redirectBodyPolicy()
	policy.Timeout = 5 * time.Second
	done := make(chan error, 1)
	go func() {
		result, err := client.Do(ctx, Request{URL: "http://api.example.test/detail", Policy: policy})
		if result.StatusCode != 0 || len(result.Body) != 0 {
			done <- fmt.Errorf("cancelled redirect returned partial success: %+v", result)
			return
		}
		done <- err
	}()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("controlled redirect did not reach body reading")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || strings.Contains(err.Error(), "partial success") {
			t.Fatalf("external cancellation did not reject incomplete body: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("redirect body kept reading after cancellation")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancelled redirect left its connection open")
	}
}
