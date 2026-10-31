package fetch

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func guardedResponse(t *testing.T, wire string, limit int64) (*http.Response, error) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go func() { defer server.Close(); _, _ = io.WriteString(server, wire) }()
	reader := bufio.NewReader(newMediaHeaderConn(client, limit))
	for {
		response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
		if err != nil || response.StatusCode >= 200 || response.StatusCode == 101 {
			return response, err
		}
	}
}

func TestStrictMediaRejectsRawDuplicateContentLength(t *testing.T) {
	wire := "HTTP/1.1 200 OK\r\nContent-Length: 3\r\nContent-Length: 3\r\nContent-Type: video/mp4\r\n\r\nabc"
	ordinary, err := http.ReadResponse(bufio.NewReader(strings.NewReader(wire)), &http.Request{Method: http.MethodGet})
	if err != nil || len(ordinary.Header.Values("Content-Length")) != 1 {
		t.Fatalf("standard parser behavior changed: %v", err)
	}
	ordinary.Body.Close()
	if _, err := guardedResponse(t, wire, 16<<10); !errors.Is(err, ErrMediaHeaders) {
		t.Fatalf("raw duplicate escaped strict boundary: %v", err)
	}
}

func TestStrictMediaWhitespaceContinuationCannotEndInspection(t *testing.T) {
	for _, continuation := range []string{" ", "\t"} {
		wire := "HTTP/1.1 200 OK\r\nContent-Length: 3\r\n" + continuation + "\r\nContent-Length: 3\r\nContent-Type: video/mp4\r\n\r\nabc"
		if _, err := guardedResponse(t, wire, 16<<10); !errors.Is(err, ErrMediaHeaders) {
			t.Fatalf("whitespace continuation bypassed duplicate inspection: %v", err)
		}
	}
}

func TestStrictMediaCriticalFieldsAndOrdinaryRepeatedFields(t *testing.T) {
	for _, key := range []string{"Location", "Content-Range", "Content-Type", "Content-Encoding"} {
		t.Run(key, func(t *testing.T) {
			wire := "HTTP/1.1 206 Partial Content\r\n" + key + ": value\r\n" + strings.ToLower(key) + ": value\r\nContent-Length: 3\r\n\r\nabc"
			if _, err := guardedResponse(t, wire, 16<<10); !errors.Is(err, ErrMediaHeaders) {
				t.Fatalf("duplicate %s accepted: %v", key, err)
			}
		})
	}
	response, err := guardedResponse(t, "HTTP/1.1 200 OK\r\nServer-Timing: first\r\nServer-Timing: second\r\nContent-Length: 3\r\n\r\nabc", 16<<10)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "abc" || len(response.Header.Values("Server-Timing")) != 2 {
		t.Fatal("legal repeated headers/body altered")
	}
}

func TestStrictMediaHeaderBudgetInterimAndTruncation(t *testing.T) {
	for _, wire := range []string{"HTTP/1.1 200 OK\r\nX-Large: " + strings.Repeat("x", 17000), "HTTP/1.1 200 OK\r\nContent-Length: 3\r\n", "HTTP/1.1 103 Early Hints\r\nX-Hint: " + strings.Repeat("x", 9000) + "\r\n\r\nHTTP/1.1 200 OK\r\nX-Large: " + strings.Repeat("x", 9000) + "\r\nContent-Length: 0\r\n\r\n"} {
		if _, err := guardedResponse(t, wire, 16<<10); err == nil {
			t.Fatal("oversized/truncated raw headers accepted")
		}
	}
	response, err := guardedResponse(t, "HTTP/1.1 103 Early Hints\r\nContent-Type: text/plain\r\n\r\nHTTP/1.1 200 OK\r\nContent-Type: video/mp4\r\nContent-Length: 3\r\n\r\nabc", 16<<10)
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("valid interim response rejected: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "abc" {
		t.Fatal("interim response changed body")
	}
}

func TestStrictMediaTrailersShareCriticalFieldsAndRawBudget(t *testing.T) {
	cases := []struct {
		name, head, trailer string
		limit               int64
		reject              bool
	}{
		{"duplicate", "Content-Type: video/mp4\r\n", "Content-Type: video/mp4\r\n", 16 << 10, true},
		{"duplicate-trailer-only", "", "Content-Type: video/mp4\r\ncontent-type: video/mp4\r\n", 16 << 10, true},
		{"unique-critical", "", "Content-Type: video/mp4\r\n", 16 << 10, false},
		{"ordinary", "Content-Type: video/mp4\r\n", "Server-Timing: one\r\nServer-Timing: two\r\n", 16 << 10, false},
		{"combined-budget", "X-Initial: " + strings.Repeat("x", 80) + "\r\n", "X-Trailer: " + strings.Repeat("x", 80) + "\r\n", 180, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: Content-Type, Server-Timing, X-Trailer\r\n" + tc.head + "\r\n3;fixture=1\r\nabc\r\n0\r\n" + tc.trailer + "\r\n"
			client := fixtureClient(func(_ *http.Request, _ []netip.Addr, policy Policy) (*http.Response, error) {
				return guardedResponse(t, wire, policy.MaxHeaderBytes)
			})
			policy := testPolicy()
			policy.StrictMediaHeaders = true
			policy.MaxHeaderBytes = tc.limit
			response, err := client.Do(context.Background(), Request{URL: "https://api.example.test/media", Policy: policy})
			if tc.reject {
				if err == nil {
					t.Fatal("critical/oversized trailer accepted")
				}
				return
			}
			if err != nil || string(response.Body) != "abc" || response.Header.Get("Content-Type") != "video/mp4" {
				t.Fatalf("valid chunked body/metadata changed: %+v %v", response, err)
			}
		})
	}
}

func TestRequestCancellationClosesInFlightTLSConnection(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "strict"}[strict], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://media.example.test/asset", nil)
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			hello, closed := make(chan struct{}), make(chan struct{})
			go func() {
				buffer := make([]byte, 8192)
				if _, err := server.Read(buffer); err == nil {
					close(hello)
				}
				for {
					if _, err := server.Read(buffer); err != nil {
						close(closed)
						return
					}
				}
			}()
			done := make(chan error, 1)
			go func() {
				_, err := roundTripWithDial(req, []netip.Addr{netip.MustParseAddr("8.8.8.8")}, Policy{MaxHeaderBytes: 16 << 10, StrictMediaHeaders: strict}, func(dialCtx context.Context, _, _ string) (net.Conn, error) {
					if dialCtx != req.Context() {
						t.Error("dial escaped original request context")
					}
					return client, nil
				})
				done <- err
			}()
			select {
			case <-hello:
			case <-time.After(time.Second):
				t.Fatal("TLS handshake did not start")
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled TLS returned success")
				}
			case <-time.After(time.Second):
				t.Fatal("RoundTrip did not cancel")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("cancelled TLS socket stayed open after RoundTrip returned")
			}
		})
	}
}
