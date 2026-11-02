package weibo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

type failedFirstByte struct {
	err    error
	closed bool
}

func (b *failedFirstByte) Read([]byte) (int, error) { return 0, b.err }
func (b *failedFirstByte) Close() error {
	b.closed = true
	return nil
}

func TestMediaRelayRangeFailureBeforeFirstByteUsesPHPError(t *testing.T) {
	for _, tc := range []struct {
		name, requested, contentRange string
	}{
		{"interior", "bytes=4-7", "bytes 4-7/12"},
		{"short-start", "bytes=0-3", "bytes 0-3/12"},
		{"suffix", "bytes=-4", "bytes 8-11/12"},
	} {
		for _, failure := range []struct {
			name string
			err  error
		}{{"eof", io.EOF}, {"network", errors.New("synthetic first-byte failure")}} {
			t.Run(tc.name+"/"+failure.name, func(t *testing.T) {
				relay, path := relayFixture(t)
				body := &failedFirstByte{err: failure.err}
				relay.open = func(_ context.Context, _ string, _ netip.Addr, byteRange string) (*http.Response, error) {
					if byteRange != tc.requested {
						t.Fatal("range changed", byteRange)
					}
					response := fakeMedia(206, "4", tc.contentRange, "")
					response.Body = body
					return response, nil
				}
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Range", tc.requested)
				w := httptest.NewRecorder()
				relay.ServeHTTP(w, req)
				var got struct {
					Code int             `json:"code"`
					Msg  string          `json:"msg"`
					Data json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatalf("media headers were committed before any byte: status=%d body=%q err=%v", w.Code, w.Body.String(), err)
				}
				if w.Code != 502 || got.Code != 502 || got.Msg != "微博视频传输未完成，请重试或重新解析" || string(got.Data) != "null" {
					t.Fatalf("PHP first-byte failure changed: status=%d body=%q", w.Code, w.Body.String())
				}
				if w.Header().Get("Content-Type") != "application/json; charset=utf-8" || w.Header().Get("Content-Range") != "" || w.Header().Get("Content-Length") != "" {
					t.Fatalf("failed transfer leaked successful media headers: %v", w.Header())
				}
				if !body.closed {
					t.Fatal("failed upstream was not closed")
				}
			})
		}
	}
}

func TestMediaRelayRangeKeepsFirstByteAndNeverAppendsError(t *testing.T) {
	for _, tc := range []struct {
		name, requested, length, contentRange, body string
	}{
		{"one-byte", "bytes=8-8", "1", "bytes 8-8/12", "D"},
		{"short-after-first-byte", "bytes=8-11", "4", "bytes 8-11/12", "D"},
		{"complete-range", "bytes=8-11", "4", "bytes 8-11/12", "DATA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay, path := relayFixture(t)
			relay.open = func(context.Context, string, netip.Addr, string) (*http.Response, error) {
				return fakeMedia(206, tc.length, tc.contentRange, tc.body), nil
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Range", tc.requested)
			w := httptest.NewRecorder()
			relay.ServeHTTP(w, req)
			if w.Code != 206 || w.Body.String() != tc.body || strings.Contains(w.Body.String(), "code") {
				t.Fatalf("range first byte lost/duplicated or JSON appended: status=%d body=%q", w.Code, w.Body.String())
			}
			if w.Header().Get("Content-Length") != tc.length || w.Header().Get("Content-Range") != tc.contentRange {
				t.Fatalf("range headers changed: %v", w.Header())
			}
		})
	}
}
