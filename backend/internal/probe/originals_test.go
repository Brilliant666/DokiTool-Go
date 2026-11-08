package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"net/http"
	"strings"
	"testing"
	"time"
)

const shCDN = "https://v26-cold.douyinvod.com/00000000000000000000000000000000/abcdef01/video/tos/cn/tos-cn-v/test-object/"

func originalMovie(audio bool) sparseMedia {
	m := makeMovie(3840, 2160, "hvc1", 500000, 0, false)
	if audio {
		handler := makeBox("hdlr", append(make([]byte, 8), []byte("soun")...))
		minf := makeBox("minf", makeBox("stbl", makeBox("stsz", words(0, 5, 10))))
		track := makeBox("trak", makeBox("mdia", append(handler, minf...)))
		m = makeBox("moov", append(m[8:], track...))
	}
	return tailMedia(m)
}

func TestOriginalsDispatchBudgetAndIdentity(t *testing.T) {
	for _, mode := range []string{"paired", "silent", "changed-object", "redispatch", "cancel", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			media := originalMovie(mode != "silent")
			calls, starts, read := 0, 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var prefix []byte
			var deadline time.Time
			ex := executorFunc(func(c context.Context, q fetch.Request) (fetch.Response, error) {
				calls++
				dl, _ := c.Deadline()
				if deadline.IsZero() {
					deadline = dl
				} else if !dl.Equal(deadline) {
					t.Fatal("per-route deadline reset")
				}
				if !q.Policy.PartialMediaOnly {
					t.Fatal("complete upstream body permitted")
				}
				if q.URL == testOriginal {
					starts++
					if mode == "cancel" {
						cancel()
						return fetch.Response{}, context.Canceled
					}
					loc := shCDN
					if mode == "redispatch" {
						loc = testOriginal
					}
					return fetch.Response{StatusCode: 302, Header: http.Header{"Location": {loc}}}, nil
				}
				if q.Header.Get("Range") != "bytes=0-65535" && mode == "changed-object" && q.URL == shCDN {
					return fetch.Response{StatusCode: 302, Header: http.Header{"Location": {strings.Replace(shCDN, "test-object", "different", 1)}}}, nil
				}
				out := media.response(t, q)
				if mode == "oversized" {
					copy(out.Body, append(testFTYP(), makeHeader("moov", MaxBytes)...))
				}
				if q.Header.Get("Range") == "bytes=0-65535" {
					prefix = out.Body
				}
				read += len(out.Body)
				return out, nil
			})
			got, err := New(ex, testAllowedURL).Originals(ctx, testOriginal)
			if mode == "cancel" {
				if !errors.Is(err, context.Canceled) || calls != 1 {
					t.Fatalf("cancellation %v calls=%d", err, calls)
				}
				return
			}
			if err != nil || starts != 2 || calls > MaxRequests || int64(read) > MaxBytes {
				t.Fatalf("starts=%d calls=%d bytes=%d err=%v", starts, calls, read, err)
			}
			if mode != "paired" {
				if len(got) > 0 {
					t.Fatal("unproved original accepted")
				}
				return
			}
			digest := sha256.Sum256(prefix)
			if len(got) != 2 {
				t.Fatalf("observations %d", len(got))
			}
			for _, v := range got {
				if unified.Map(v["metadata"])["has_audio"] != true || v["prefix_sha256"] != hex.EncodeToString(digest[:]) {
					t.Fatal("missing bounded evidence")
				}
			}
			if read >= 2*int(media.size) || bytes.Equal(prefix, nil) {
				t.Fatal("unexpected full media read")
			}
		})
	}
}

// A large, valid moov may consume almost the whole budget on the first route.
// Keep that result without refreshing the ledger for a second dispatch.
func TestOriginalsPreserveFirstWhenRemainingBudgetCannotReadAnotherPrefix(t *testing.T) {
	movie := originalMovie(true).parts[2].data
	wantSize := MaxBytes - prefixBytes - 17
	pad := makeBox("free", make([]byte, int(wantSize)-len(movie)-8))
	media := tailMedia(makeBox("moov", append(movie[8:], pad...)))
	var read int64
	var starts int
	ex := executorFunc(func(_ context.Context, q fetch.Request) (fetch.Response, error) {
		if q.URL == testOriginal {
			starts++
			return fetch.Response{StatusCode: 302, Header: http.Header{"Location": {shCDN}}}, nil
		}
		out := media.response(t, q)
		read += int64(len(out.Body))
		return out, nil
	})
	got, err := New(ex, testAllowedURL).Originals(context.Background(), testOriginal)
	if err != nil || starts != 1 || read != MaxBytes-1 || len(got) != 1 {
		t.Fatalf("got %d candidates / %d dispatches / %d bytes / %v", len(got), starts, read, err)
	}
}
