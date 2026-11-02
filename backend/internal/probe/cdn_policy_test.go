package probe

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func TestPublicCDNIsNotAnInitialPlatformEntry(t *testing.T) {
	calls := 0
	executor := executorFunc(func(context.Context, fetch.Request) (fetch.Response, error) {
		calls++
		return fetch.Response{}, nil
	})
	result, err := New(executor, testAllowedURL).Probe(context.Background(), "https://edge-a.example.net/original.mov")
	if err != nil || result != nil || calls != 0 {
		t.Fatalf("unapproved entry reached transport: result=%v err=%v calls=%d", result, err, calls)
	}
}

func TestConfirmedPlatformCanChoosePublicCDNOnLaterRange(t *testing.T) {
	media := tailMedia(makeMovie(2160, 3840, "hvc1", 500000, 0, false))
	media.parts[0].data = makeBox("ftyp", append([]byte("qt  "), append(words(0), []byte("qt  ")...)...))
	first := "https://edge-a.example.net/original.mov?sig=a%2Fb+z&sig=second"
	last := "https://edge-b.example.org/original.mov?sig=next%252F"
	var confirmedMetadata unified.Object
	for _, mime := range []string{"video/mp4", "video/quicktime", "Video/QuickTime; charset=binary"} {
		t.Run(mime, func(t *testing.T) {
			calls := 0
			executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				offset, _ := requestedRange(t, request)
				if !request.Policy.AllowedURL(request.URL) || request.Policy.AllowedURL("https://127.0.0.1/private") {
					t.Fatal("executor did not receive the safe transport policy")
				}
				if request.URL == testOriginal {
					return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {first}}}, nil
				}
				if request.URL == first && offset > 0 {
					return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {last}}}, nil
				}
				if request.URL != first && request.URL != last {
					t.Fatalf("source bytes changed: %q", request.URL)
				}
				response := media.response(t, request)
				response.Header.Set("Content-Type", mime)
				return response, nil
			})
			result, err := New(executor, testAllowedURL).Probe(context.Background(), testOriginal)
			if err != nil || result == nil || result["url"] != last || calls != 5 {
				t.Fatalf("CDN reassignment failed: result=%v err=%v calls=%d", result, err, calls)
			}
			metadata := unified.Map(result["metadata"])
			if metadata["width"] != int64(2160) || metadata["height"] != int64(3840) || metadata["codec"] != "hevc" || metadata["size"] != media.size {
				t.Fatalf("verified original facts lost: %v", metadata)
			}
			if confirmedMetadata == nil {
				confirmedMetadata = metadata
			} else if !reflect.DeepEqual(confirmedMetadata, metadata) {
				t.Fatalf("MIME changed measured facts: %v != %v", metadata, confirmedMetadata)
			}
		})
	}
}

func TestUnsafeLaterCDNRetainsLastConfirmedSource(t *testing.T) {
	media := tailMedia(makeMovie(2160, 3840, "hvc1", 500000, 0, false))
	cdn := "https://edge-a.example.net/original.mov"
	calls := 0
	executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
		calls++
		offset, _ := requestedRange(t, request)
		if request.URL == testOriginal {
			return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {cdn}}}, nil
		}
		if request.URL != cdn {
			t.Fatalf("unsafe CDN was requested: %s", request.URL)
		}
		if offset > 0 {
			return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {"https://10.0.0.1/private"}}}, nil
		}
		return media.response(t, request), nil
	})
	result, err := New(executor, testAllowedURL).Probe(context.Background(), testOriginal)
	expectUnknown(t, result, err, cdn)
	if calls != 3 {
		t.Fatalf("unsafe redirect reached transport or triggered a retry: %d", calls)
	}
}

func TestRootRelativeCDNRedirectPreservesIPv6Authority(t *testing.T) {
	media := tailMedia(makeMovie(2160, 3840, "hvc1", 500000, 0, false))
	for _, host := range []string{"[2606:4700:4700::1111]", "[2606:4700:4700::1111]:443", "edge-a.example.net:443"} {
		t.Run(host, func(t *testing.T) {
			first := "https://" + host + "/a?old=discarded"
			// PHP parse_url(..., HOST) keeps IPv6 brackets but omits the
			// explicit default port when constructing a root-relative target.
			finalHost := host
			if host == "[2606:4700:4700::1111]:443" {
				finalHost = "[2606:4700:4700::1111]"
			} else if host == "edge-a.example.net:443" {
				finalHost = "edge-a.example.net"
			}
			last := "https://" + finalHost + "/b?sig=a%2Fb+z&sig=second"
			calls := 0
			executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				requestedRange(t, request)
				if !request.Policy.AllowedURL(request.URL) {
					t.Fatalf("legal authority was rejected: %s", request.URL)
				}
				switch request.URL {
				case testOriginal:
					return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {first}}}, nil
				case first:
					return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {"/b?sig=a%2Fb+z&sig=second"}}}, nil
				case last:
					return media.response(t, request), nil
				default:
					t.Fatalf("redirect changed authority/path/query bytes: %s", request.URL)
					return fetch.Response{}, nil
				}
			})
			result, err := New(executor, testAllowedURL).Probe(context.Background(), testOriginal)
			if err != nil || result == nil || result["url"] != last || unified.Map(result["metadata"])["width"] != int64(2160) || calls != 5 {
				t.Fatalf("root-relative CDN failed: result=%v err=%v calls=%d", result, err, calls)
			}
		})
	}
}

func TestMediaRedirectTransportBoundary(t *testing.T) {
	for _, target := range []string{
		"https://localhost/a", "https://sub.localhost./a", "https://127.0.0.1/a", "https://10.0.0.1/a",
		"https://169.254.169.254/a", "https://[::1]/a", "https://[::ffff:127.0.0.1]/a", "https://[fe80::1%25eth0]/a",
		"https://user:password@edge-a.example.net/a", "https://edge-a.example.net:444/a",
		"https://edge-a.example.net/a#fragment", "https://edge-a.example.net/a#", "file:///tmp/media.mp4",
		"ftp://edge-a.example.net/a", "https://edge-a.example.net/\na", "https://edge-a.example.net/\\a",
	} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			executor := executorFunc(func(_ context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				return fetch.Response{StatusCode: 302, URL: request.URL, Header: http.Header{"Location": {target}}}, nil
			})
			result, err := New(executor, testAllowedURL).Probe(context.Background(), testOriginal)
			if err != nil || result != nil || calls != 1 {
				t.Fatalf("unsafe redirect reached transport: result=%v err=%v calls=%d", result, err, calls)
			}
		})
	}
}
