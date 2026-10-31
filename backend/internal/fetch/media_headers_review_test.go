package fetch

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func reviewChunkedWire(head, trailer string) (string, []byte) {
	// Apparent framing/header text inside a chunk must remain ordinary data.
	first := []byte("A\x00\xff\r\nContent-Length: 1\r\n")
	second := []byte("0\r\nContent-Type: text/html\r\n")
	body := append(bytes.Clone(first), second...)
	wire := "HTTP/1.1 206 Partial Content\r\nTransfer-Encoding: chunked\r\n" + head + "\r\n" +
		fmt.Sprintf("%x;fixture=one\r\n%s\r\n%x\r\n%s\r\n0\r\n", len(first), first, len(second), second) + trailer + "\r\n"
	return wire, body
}

func TestStrictMediaObserverPreservesChunkWireBytes(t *testing.T) {
	wire, _ := reviewChunkedWire("Trailer: Content-Type, Server-Timing\r\n", "Content-Type: video/mp4\r\nServer-Timing: one\r\nServer-Timing: two\r\n")
	wire = "HTTP/1.1 103 Early Hints\r\nX-Hint: harmless\r\n\r\n" + wire
	for _, fragmentSize := range []int{1, 4096} {
		t.Run(fmt.Sprintf("wire-fragments-%d", fragmentSize), func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			go func() {
				defer server.Close()
				for remaining := []byte(wire); len(remaining) > 0; {
					n := min(fragmentSize, len(remaining))
					if _, err := server.Write(remaining[:n]); err != nil {
						return
					}
					remaining = remaining[n:]
				}
			}()
			guard := newMediaHeaderConn(client, 16<<10)
			var actual bytes.Buffer
			buffer := make([]byte, 7)
			if _, err := io.CopyBuffer(struct{ io.Writer }{&actual}, guard, buffer); err != nil {
				t.Fatal(err)
			}
			if actual.String() != wire {
				t.Fatal("chunk observer changed response bytes, delimiters or trailers")
			}
		})
	}
}

func reviewRealTransportClient(t *testing.T, serve func(net.Conn)) *Client {
	t.Helper()
	return fixtureClient(func(req *http.Request, ips []netip.Addr, policy Policy) (*http.Response, error) {
		return roundTripWithDial(req, ips, policy, func(ctx context.Context, network, address string) (net.Conn, error) {
			if ctx != req.Context() || network != "tcp" || address != "8.8.8.8:80" {
				t.Errorf("controlled transport escaped original context or pinned target: %s %s", network, address)
			}
			client, server := net.Pipe()
			t.Cleanup(func() { client.Close(); server.Close() })
			go func() {
				defer server.Close()
				request, err := http.ReadRequest(bufio.NewReader(server))
				if err != nil {
					return
				}
				request.Body.Close()
				serve(server)
			}()
			return client, nil
		})
	})
}

func TestStrictMediaTrailersThroughActualStandardTransport(t *testing.T) {
	cases := []struct {
		name, head, trailer string
		limit               int64
		reject              bool
	}{
		{"critical-trailer-merged", "Trailer: Content-Type\r\n", "Content-Type: video/mp4\r\n", 16 << 10, false},
		{"duplicate-in-trailer", "Content-Type: video/mp4\r\nTrailer: Content-Type\r\n", "Content-Type: video/mp4\r\n", 16 << 10, true},
		{"unannounced-duplicate", "Content-Type: video/mp4\r\n", "Content-Type: video/mp4\r\n", 16 << 10, true},
		{"shared-raw-header-budget", "X-Initial: " + strings.Repeat("x", 160) + "\r\n", "X-Trailer: " + strings.Repeat("x", 160) + "\r\n", 320, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wire, body := reviewChunkedWire(c.head, c.trailer)
			client := reviewRealTransportClient(t, func(conn net.Conn) { _, _ = io.WriteString(conn, wire) })
			policy := testPolicy()
			policy.FollowRedirects = false
			policy.StrictMediaHeaders = true
			policy.MaxHeaderBytes = c.limit
			response, err := client.Do(context.Background(), Request{URL: "http://api.example.test/media", Policy: policy})
			if c.reject {
				// net/http may translate a guard read error into its own trailer
				// EOF error. The contract is rejection without a partial response.
				if err == nil || response.StatusCode != 0 || len(response.Body) != 0 {
					t.Fatalf("expected strict header rejection without payload, got %+v %v", response, err)
				}
				return
			}
			if err != nil || !bytes.Equal(response.Body, body) || response.Header.Get("Content-Type") != "video/mp4" || response.URL != "http://api.example.test/media" {
				t.Fatalf("actual transport changed payload, trailer or URL: %+v %v", response, err)
			}
		})
	}
}

func TestStrictMediaCancellationDuringChunkDataOrTrailers(t *testing.T) {
	for _, stage := range []string{"chunk-data", "trailer-line"} {
		t.Run(stage, func(t *testing.T) {
			ready, closed := make(chan struct{}), make(chan struct{})
			client := reviewRealTransportClient(t, func(conn net.Conn) {
				prefix := "HTTP/1.1 206 Partial Content\r\nTransfer-Encoding: chunked\r\nContent-Type: video/mp4\r\n\r\n"
				if stage == "chunk-data" {
					prefix += "10\r\nabc"
				} else {
					prefix += "3\r\nabc\r\n0\r\nX-Pending: "
				}
				if _, err := io.WriteString(conn, prefix); err != nil {
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
			policy := testPolicy()
			policy.StrictMediaHeaders = true
			policy.MaxHeaderBytes = 16 << 10
			policy.Timeout = 5 * time.Second
			done := make(chan error, 1)
			go func() {
				_, err := client.Do(ctx, Request{URL: "http://api.example.test/media", Policy: policy})
				done <- err
			}()
			select {
			case <-ready:
			case <-time.After(time.Second):
				t.Fatal("controlled chunk response did not reach target stage")
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled partial response became success")
				}
			case <-time.After(time.Second):
				t.Fatal("chunk observer kept reading after caller cancellation")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("cancelled chunk/trailer left its peer connection open")
			}
		})
	}
}
