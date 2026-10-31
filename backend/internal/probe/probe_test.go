package probe

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type executorFunc func(context.Context, fetch.Request) (fetch.Response, error)

func (f executorFunc) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	return f(ctx, request)
}

const testOriginal = "https://original.example.test/play/?video_id=fixture&ratio=default&line=0"
const testCDN = "https://media.example.test/file.mp4?sig=a%2Fb+z&sig=second&x=%252F"

func testAllowedURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" {
		return false
	}
	switch u.Hostname() {
	case "original.example.test", "media.example.test", "two.example.test", "three.example.test", "four.example.test":
		return true
	}
	return false
}

func makeBox(kind string, payload []byte) []byte {
	out := make([]byte, 8, len(payload)+8)
	binary.BigEndian.PutUint32(out, uint32(len(payload)+8))
	copy(out[4:], kind)
	return append(out, payload...)
}

func makeHeader(kind string, size int64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint32(out, uint32(size))
	copy(out[4:], kind)
	return out
}

func words(values ...uint32) []byte {
	out := make([]byte, len(values)*4)
	for i, value := range values {
		binary.BigEndian.PutUint32(out[i*4:], value)
	}
	return out
}

func makeMovie(width, height uint16, codec string, sampleSize uint32, version byte, variable bool) []byte {
	entry := make([]byte, 78)
	binary.BigEndian.PutUint16(entry[24:], width)
	binary.BigEndian.PutUint16(entry[26:], height)
	stsd := makeBox("stsd", append(words(0, 1), makeBox(codec, entry)...))
	stszData := words(0, sampleSize, 10)
	if variable {
		stszData = words(0, 0, 10)
		for i := 0; i < 10; i++ {
			stszData = append(stszData, words(sampleSize)...)
		}
	}
	mdhd := words(0, 0, 0, 1000, 10000)
	if version == 1 {
		mdhd = words(0x01000000, 0, 0, 0, 0, 1000, 0, 10000)
	}
	mdia := makeBox("mdia", bytes.Join([][]byte{
		makeBox("mdhd", mdhd),
		makeBox("hdlr", append(make([]byte, 8), []byte("vide")...)),
		makeBox("minf", makeBox("stbl", append(stsd, makeBox("stsz", stszData)...))),
	}, nil))
	return makeBox("moov", makeBox("trak", mdia))
}

func testFTYP() []byte {
	return makeBox("ftyp", append([]byte("isom"), append(words(0), []byte("isom")...)...))
}

type fragment struct {
	offset int64
	data   []byte
}

// Only the requested slices exist: a large mdat is never allocated or cached.
type sparseMedia struct {
	size  int64
	parts []fragment
}

func tailMedia(moov []byte) sparseMedia {
	ftyp := testFTYP()
	tail := int64(len(ftyp)) + 6000008
	return sparseMedia{size: tail + int64(len(moov)), parts: []fragment{
		{0, ftyp}, {int64(len(ftyp)), makeHeader("mdat", 6000008)}, {tail, moov},
	}}
}

func requestedRange(t *testing.T, request fetch.Request) (int64, int64) {
	t.Helper()
	var start, end int64
	if count, err := fmt.Sscanf(request.Header.Get("Range"), "bytes=%d-%d", &start, &end); count != 2 || err != nil || start < 0 || end < start {
		t.Fatalf("invalid finite Range: %q", request.Header.Get("Range"))
	}
	length := end - start + 1
	if request.Method != http.MethodGet || request.Header.Get("Accept-Encoding") != "identity" || request.Header.Get("User-Agent") != userAgent || request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" {
		t.Fatal("probe request lost isolated media headers")
	}
	p := request.Policy
	if p.Purpose != "media-probe" || p.AllowedURL == nil || p.Timeout <= 0 || p.Timeout > Timeout || p.MaxBytes != length || p.MaxHeaderBytes != maxHeaderBytes || !p.StrictMediaHeaders || p.FollowRedirects || p.MaxRedirects != 0 {
		t.Fatalf("unbounded/mismatched fetch policy: %+v range length=%d", p, length)
	}
	return start, length
}

func (m sparseMedia) response(t *testing.T, request fetch.Request) fetch.Response {
	t.Helper()
	offset, length := requestedRange(t, request)
	if offset >= m.size {
		return fetch.Response{StatusCode: http.StatusRequestedRangeNotSatisfiable, URL: request.URL}
	}
	actual := min(length, m.size-offset)
	body := bytes.Repeat([]byte{'x'}, int(actual))
	for _, part := range m.parts {
		start := max(offset, part.offset)
		end := min(offset+actual, part.offset+int64(len(part.data)))
		if start < end {
			copy(body[start-offset:end-offset], part.data[start-part.offset:end-part.offset])
		}
	}
	return fetch.Response{
		StatusCode: http.StatusPartialContent, URL: request.URL, Body: body,
		Header: http.Header{
			"Content-Type":  {"video/mp4"},
			"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", offset, offset+actual-1, m.size)},
		},
	}
}

func expectUnknown(t *testing.T, result unified.Object, err error, wantURL string) {
	t.Helper()
	if err != nil || result == nil || result["url"] != wantURL || len(unified.Map(result["metadata"])) != 0 {
		t.Fatalf("wanted confirmed URL with unknown metadata, got %#v err=%v", result, err)
	}
	if _, ok := result["metadata"].(map[string]any); !ok {
		t.Fatal("unknown metadata must be an empty object at the Go adapter boundary")
	}
}

func TestTailMovieUsesFiniteRangesAndPreservesSignedURL(t *testing.T) {
	media := tailMedia(makeMovie(2160, 3840, "hvc1", 500000, 0, false))
	var requests []fetch.Request
	var received int
	executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
		requests = append(requests, request)
		requestedRange(t, request)
		if request.URL == testOriginal {
			return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {testCDN}}}, nil
		}
		out := media.response(t, request)
		received += len(out.Body)
		return out, nil
	})
	result, err := New(executor, testAllowedURL).Probe(context.Background(), testOriginal)
	want := unified.Object{"url": testCDN, "metadata": unified.Object{
		"width": int64(2160), "height": int64(3840), "codec": "hevc", "duration": 10,
		"bit_rate": int64(4000000), "size": media.size, "metadata_source": "mp4",
	}}
	if err != nil || !unified.Equal(result, want) {
		t.Fatalf("tail metadata mismatch: %#v err=%v", result, err)
	}
	if len(requests) != 4 || received >= 70000 {
		t.Fatalf("read large mdat instead of skipping it: calls=%d received=%d", len(requests), received)
	}
	for _, request := range requests[1:] {
		if request.URL != testCDN {
			t.Fatalf("changed signed URL bytes: %q", request.URL)
		}
	}
	lastOffset, _ := requestedRange(t, requests[len(requests)-1])
	if lastOffset != media.parts[2].offset {
		t.Fatal("did not range-read trailing moov")
	}
}

func TestSmallCompleteMediaAndMIMECompatibility(t *testing.T) {
	data := bytes.Join([][]byte{testFTYP(), makeBox("mdat", bytes.Repeat([]byte{'x'}, 100)), makeMovie(720, 1280, "avc1", 1, 0, false)}, nil)
	for _, mime := range []string{"video/mp4", " VIDEO/MP4 ; charset=binary", "video/quicktime", "Video/QuickTime; charset=binary", "application/octet-stream", "video/x-m4v"} {
		t.Run(mime, func(t *testing.T) {
			calls := 0
			executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				requestedRange(t, request)
				return fetch.Response{StatusCode: 200, URL: request.URL, Body: data, Header: http.Header{
					"Content-Type": {mime}, "Content-Length": {strconv.Itoa(len(data))},
					"Server-Timing": {"edge;dur=1", "origin;dur=2"}, "Set-Cookie": {"a=1", "b=2"},
				}}, nil
			})
			result, err := New(executor, testAllowedURL).Probe(context.Background(), testCDN)
			if err != nil || result == nil || unified.Map(result["metadata"])["width"] != int64(720) || calls != 1 {
				t.Fatalf("small complete response rejected: %#v err=%v calls=%d", result, err, calls)
			}
		})
	}
}

func TestMalformedResponsesNeverConfirmMedia(t *testing.T) {
	media := tailMedia(makeMovie(2160, 3840, "hvc1", 500000, 0, false))
	cases := map[string]func(*fetch.Response){
		"bad status":                     func(r *fetch.Response) { r.StatusCode = 403 },
		"html MIME":                      func(r *fetch.Response) { r.Header.Set("Content-Type", "text/html") },
		"missing MIME":                   func(r *fetch.Response) { r.Header.Del("Content-Type") },
		"gzip decoded body":              func(r *fetch.Response) { r.Header.Set("Content-Encoding", "gzip") },
		"empty encoding":                 func(r *fetch.Response) { r.Header.Set("Content-Encoding", "") },
		"nonliteral identity":            func(r *fetch.Response) { r.Header.Set("Content-Encoding", "Identity") },
		"wrong offset":                   func(r *fetch.Response) { r.Header.Set("Content-Range", fmt.Sprintf("bytes 1-65536/%d", media.size)) },
		"wrong end":                      func(r *fetch.Response) { r.Header.Set("Content-Range", fmt.Sprintf("bytes 0-65534/%d", media.size)) },
		"wrong total":                    func(r *fetch.Response) { r.Header.Set("Content-Range", "bytes 0-65535/65535") },
		"file over four GiB":             func(r *fetch.Response) { r.Header.Set("Content-Range", "bytes 0-65535/4294967297") },
		"unbounded range digits":         func(r *fetch.Response) { r.Header.Set("Content-Range", "bytes 00000000000-65535/6000258") },
		"missing range":                  func(r *fetch.Response) { r.Header.Del("Content-Range") },
		"range with extra data":          func(r *fetch.Response) { r.Header.Set("Content-Range", r.Header.Get("Content-Range")+"\nx") },
		"truncated range body":           func(r *fetch.Response) { r.Body = r.Body[:len(r.Body)-1] },
		"oversized body":                 func(r *fetch.Response) { r.Body = append(r.Body, 0) },
		"unconfirmed transport redirect": func(r *fetch.Response) { r.URL = "https://two.example.test/file" },
		"hidden redirect history":        func(r *fetch.Response) { r.History = []string{testCDN} },
		"not MP4":                        func(r *fetch.Response) { copy(r.Body[4:8], []byte("junk")) },
		"oversized headers":              func(r *fetch.Response) { r.Header.Set("Server-Timing", strings.Repeat("x", maxHeaderBytes)) },
	}
	for _, field := range []string{"Location", "Content-Range", "Content-Length", "Content-Type", "Content-Encoding"} {
		cases["duplicate "+field] = func(r *fetch.Response) { r.Header[field] = []string{"x", "x"} }
		cases["mixed-case duplicate "+field] = func(r *fetch.Response) {
			r.Header[field] = []string{"x"}
			r.Header[strings.ToLower(field)] = []string{"x"}
		}
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			calls := 0
			executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				response := media.response(t, request)
				mutate(&response)
				return response, nil
			})
			result, err := New(executor, testAllowedURL).Probe(context.Background(), testCDN)
			if err != nil || result != nil || calls != 1 {
				t.Fatalf("unsafe response accepted: %#v err=%v calls=%d", result, err, calls)
			}
		})
	}
}

func TestIgnoredRangeRequiresExactSmallContentLength(t *testing.T) {
	data := bytes.Join([][]byte{testFTYP(), makeMovie(720, 1280, "avc1", 1, 0, false)}, nil)
	for _, value := range []string{"", "-1", "+254", "1e3", "99999999999999999999999999", strconv.Itoa(len(data) + 1)} {
		t.Run(value, func(t *testing.T) {
			executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
				return fetch.Response{StatusCode: 200, URL: request.URL, Body: data, Header: http.Header{
					"Content-Type": {"video/mp4"}, "Content-Length": {value},
				}}, nil
			})
			result, err := New(executor, testAllowedURL).Probe(context.Background(), testCDN)
			if err != nil || result != nil {
				t.Fatalf("unproven complete file accepted: %#v %v", result, err)
			}
		})
	}
}

func TestLaterReadFailurePreservesOnlyLastConfirmedURL(t *testing.T) {
	media := tailMedia(makeMovie(2160, 3840, "hvc1", 500000, 0, false))
	for _, cause := range []string{"network", "changed total", "ignored range", "redirect failure", "oversized header"} {
		t.Run(cause, func(t *testing.T) {
			calls := 0
			executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				offset, _ := requestedRange(t, request)
				if offset == 0 {
					return media.response(t, request), nil
				}
				if cause == "network" || request.URL == "https://two.example.test/unavailable.mp4" {
					return fetch.Response{}, errors.New("fixture network unavailable")
				}
				response := media.response(t, request)
				switch cause {
				case "changed total":
					response.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+int64(len(response.Body))-1, media.size+1))
				case "ignored range":
					response.StatusCode = 200
					response.Header.Set("Content-Length", strconv.Itoa(len(response.Body)))
				case "redirect failure":
					return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {"https://two.example.test/unavailable.mp4"}}}, nil
				case "oversized header":
					response.Header.Set("Server-Timing", strings.Repeat("x", maxHeaderBytes))
				}
				return response, nil
			})
			result, err := New(executor, testAllowedURL).Probe(context.Background(), testCDN)
			expectUnknown(t, result, err, testCDN)
			wantCalls := 2
			if cause == "redirect failure" {
				wantCalls = 3
			}
			if calls != wantCalls {
				t.Fatalf("unexpected retries: %d", calls)
			}
		})
	}
}

func TestRedirectTransformationAndGlobalBudget(t *testing.T) {
	media := tailMedia(makeMovie(2160, 3840, "hvc1", 500000, 0, false))
	for _, target := range []string{"//media.example.test/file.mp4?sig=a%2Fb", "http://media.example.test/file.mp4?sig=a%2Fb", "/file.mp4?sig=a%2Fb"} {
		t.Run(target, func(t *testing.T) {
			var urls []string
			executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
				urls = append(urls, request.URL)
				if request.URL == testOriginal {
					return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {target}}}, nil
				}
				return media.response(t, request), nil
			})
			result, err := New(executor, testAllowedURL).Probe(context.Background(), testOriginal)
			if err != nil || len(unified.Map(result["metadata"])) != 7 || !strings.Contains(unified.Str(result["url"]), "sig=a%2Fb") {
				t.Fatalf("legacy redirect lost: %#v %v", result, err)
			}
			for _, actual := range urls {
				if !strings.HasPrefix(actual, "https://") {
					t.Fatalf("made plaintext media request: %q", actual)
				}
			}
		})
	}
	for _, target := range []string{"https://127.0.0.1/private", "https://cdn.example.test/file#fragment", "file.mp4", "?query=only", ""} {
		t.Run("reject "+target, func(t *testing.T) {
			calls := 0
			executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {target}}}, nil
			})
			result, err := New(executor, testAllowedURL).Probe(context.Background(), testOriginal)
			if err != nil || result != nil || calls != 1 {
				t.Fatalf("unsafe redirect followed: %#v err=%v calls=%d", result, err, calls)
			}
		})
	}
	t.Run("redirect loop", func(t *testing.T) {
		calls := 0
		executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
			calls++
			return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {testCDN}}}, nil
		})
		result, err := New(executor, testAllowedURL).Probe(context.Background(), testOriginal)
		if err != nil || result != nil || calls != MaxRedirects+1 {
			t.Fatalf("redirect loop exceeded budget: %#v err=%v calls=%d", result, err, calls)
		}
	})
	t.Run("redirect budget spans all reads", func(t *testing.T) {
		calls := 0
		executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
			calls++
			var target string
			switch calls {
			case 1:
				target = testCDN
			case 3:
				target = "https://two.example.test/tail.mp4"
			case 5:
				target = "https://three.example.test/tail.mp4"
			case 6:
				target = "https://four.example.test/tail.mp4"
			}
			if target != "" {
				return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {target}}}, nil
			}
			return media.response(t, request), nil
		})
		result, err := New(executor, testAllowedURL).Probe(context.Background(), testOriginal)
		expectUnknown(t, result, err, "https://two.example.test/tail.mp4")
		if calls != 6 {
			t.Fatalf("redirect allowance reset between ranges: %d", calls)
		}
	})
}

func TestProbeByteRequestAndAtomBudgets(t *testing.T) {
	t.Run("exact byte ceiling and one extra byte", func(t *testing.T) {
		for _, extra := range []int64{0, 1} {
			base := makeMovie(720, 1280, "avc1", 1, 0, false)
			wanted := MaxBytes - prefixBytes - 16 + extra
			padding := makeBox("free", make([]byte, int(wanted)-len(base)-8))
			moov := makeBox("moov", append(base[8:], padding...))
			media := tailMedia(moov)
			var received int64
			calls := 0
			executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				response := media.response(t, request)
				received += int64(len(response.Body))
				return response, nil
			})
			result, err := New(executor, testAllowedURL).Probe(context.Background(), testCDN)
			if extra == 0 {
				if err != nil || len(unified.Map(result["metadata"])) != 7 || received != MaxBytes || calls != 3 {
					t.Fatalf("exact bounded metadata rejected: bytes=%d calls=%d result=%#v err=%v", received, calls, result, err)
				}
			} else {
				expectUnknown(t, result, err, testCDN)
				if received != prefixBytes+16 || calls != 2 {
					t.Fatal("fetched moov beyond remaining byte budget")
				}
			}
		}
	})
	t.Run("request ceiling", func(t *testing.T) {
		ftyp := testFTYP()
		media := sparseMedia{parts: []fragment{{0, ftyp}, {int64(len(ftyp)), makeHeader("mdat", prefixBytes-int64(len(ftyp)))}}}
		for i := int64(0); i < 20; i++ {
			media.parts = append(media.parts, fragment{prefixBytes + i*1024, makeHeader("free", 1024)})
		}
		moov := makeMovie(720, 1280, "avc1", 1, 0, false)
		media.parts = append(media.parts, fragment{prefixBytes + 20*1024, moov})
		media.size = prefixBytes + 20*1024 + int64(len(moov))
		calls := 0
		executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
			calls++
			return media.response(t, request), nil
		})
		result, err := New(executor, testAllowedURL).Probe(context.Background(), testCDN)
		expectUnknown(t, result, err, testCDN)
		if calls != MaxRequests {
			t.Fatalf("request budget not enforced: %d", calls)
		}
	})
	t.Run("top atom ceiling", func(t *testing.T) {
		for _, freeCount := range []int{22, 23} {
			data := testFTYP()
			for i := 0; i < freeCount; i++ {
				data = append(data, makeBox("free", nil)...)
			}
			data = append(data, makeMovie(720, 1280, "avc1", 1, 0, false)...)
			media := sparseMedia{size: int64(len(data)), parts: []fragment{{0, data}}}
			calls := 0
			executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				return media.response(t, request), nil
			})
			result, err := New(executor, testAllowedURL).Probe(context.Background(), testCDN)
			if freeCount == 23 {
				expectUnknown(t, result, err, testCDN)
			} else if err != nil || len(unified.Map(result["metadata"])) != 7 {
				t.Fatalf("24th atom not inspected: %#v %v", result, err)
			}
			if calls != 1 {
				t.Fatal("made unnecessary requests for cached prefix atoms")
			}
		}
	})
}

func TestCancellationAndConcurrentCalls(t *testing.T) {
	t.Run("cancelled before work", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		executor := executorFunc(func(context.Context, fetch.Request) (fetch.Response, error) {
			t.Fatal("called transport after cancellation")
			return fetch.Response{}, nil
		})
		result, err := New(executor, testAllowedURL).Probe(ctx, testCDN)
		if result != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation not propagated: %#v %v", result, err)
		}
	})
	t.Run("caller deadline applies during fetch", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
		defer cancel()
		executor := executorFunc(func(call context.Context, request fetch.Request) (fetch.Response, error) {
			requestedRange(t, request)
			if request.Policy.Timeout > 15*time.Millisecond {
				t.Fatal("probe lengthened caller deadline")
			}
			<-call.Done()
			return fetch.Response{}, call.Err()
		})
		result, err := New(executor, testAllowedURL).Probe(ctx, testCDN)
		if result != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline not propagated: %#v %v", result, err)
		}
	})
	t.Run("same instance receives independent budgets", func(t *testing.T) {
		media := tailMedia(makeMovie(2160, 3840, "hvc1", 500000, 0, false))
		var calls atomic.Int32
		executor := executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
			calls.Add(1)
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > Timeout {
				t.Error("missing global six-second deadline")
			}
			return media.response(t, request), nil
		})
		p := New(executor, testAllowedURL)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				result, err := p.Probe(context.Background(), testCDN)
				if err != nil || len(unified.Map(result["metadata"])) != 7 {
					t.Errorf("concurrent probe lost its budget/state: %#v %v", result, err)
				}
			}()
		}
		wg.Wait()
		if calls.Load() != 8*3 {
			t.Fatalf("state leaked across calls: %d", calls.Load())
		}
	})
}
