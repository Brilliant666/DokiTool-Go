package douyin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type executorFunc func(context.Context, fetch.Request) (fetch.Response, error)

func (f executorFunc) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	return f(ctx, request)
}

type proberFunc func(context.Context, string) (object, error)

func (f proberFunc) Probe(ctx context.Context, raw string) (object, error) { return f(ctx, raw) }

func findFixture(t *testing.T, id string) object {
	t.Helper()
	for _, value := range phpDouyinFixtures(t) {
		if c := unified.Map(value); unified.Str(c["id"]) == id {
			return c
		}
	}
	t.Fatalf("missing fixture %s", id)
	return nil
}

func TestNilProbePreservesKnownFileAndUnknownOriginal(t *testing.T) {
	c := findFixture(t, "original/unavailable")
	input, expected := unified.Map(c["input"]), unified.Map(c["expected"])
	now, _ := unified.Numeric(input["now"])
	parser := New(&fixtureExecutor{t: t, responses: unified.Slice(input["http_responses"])}, nil)
	full, err := parser.Parse(context.Background(), unified.Str(input["url"]), int64(now))
	if err != nil {
		t.Fatal(err)
	}
	requireSameWire(t, unified.PublicView(full, false, int64(now)), expected["normal"])
}

func TestNamedAudioURLsPreserveExplicitMuxAndWireOrder(t *testing.T) {
	input := unified.Map(findFixture(t, "audio-capability/alternate-language")["input"])
	now, _ := unified.Numeric(input["now"])
	responses := unified.Slice(input["http_responses"])
	parse := func(urls json.RawMessage) object {
		t.Helper()
		response := unified.Clone(unified.Map(responses[0]))
		payload, err := unified.Decode([]byte(unified.Str(response["body"])))
		if err != nil {
			t.Fatal(err)
		}
		tracks := unified.Slice(child(payload, "aweme_detail", "video", "bit_rate_audio"))
		unified.Map(child(tracks[0], "audio_meta"))["url_list"] = urls
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		response["body"] = string(encoded)
		parser := New(&fixtureExecutor{t: t, responses: array{response}}, nil)
		full, err := parser.Parse(context.Background(), unified.Str(input["url"]), int64(now))
		if err != nil {
			t.Fatal(err)
		}
		return unified.PublicView(full, true, int64(now))
	}
	want := parse(json.RawMessage(`["https://audio.example/z.m4a","https://audio.example/a.m4a"]`))
	got := parse(json.RawMessage(`{"z":"https://audio.example/discard.m4a","a":"https://audio.example/a.m4a","z":"https://audio.example/z.m4a"}`))
	requireSameWire(t, got, want)
	items := unified.Slice(child(got, "data", "items"))
	if len(items) != 1 {
		t.Fatal("missing video item")
	}
	for _, variant := range unified.Slice(child(items[0], "variants")) {
		if child(variant, "delivery", "kind") != "mux" {
			continue
		}
		for _, resource := range unified.Slice(child(variant, "delivery", "resources")) {
			if child(resource, "role") != "audio" {
				continue
			}
			sources := unified.Slice(child(resource, "delivery", "sources"))
			if len(sources) == 2 && child(sources[0], "url") == "https://audio.example/z.m4a" && child(sources[1], "url") == "https://audio.example/a.m4a" {
				return
			}
		}
	}
	t.Fatal("explicit mux lost audio sources or their original order")
}

func TestParseCancellationStopsEachBoundary(t *testing.T) {
	for _, boundary := range []string{"before", "short-link", "detail", "probe"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			url := "https://www.douyin.com/video/7628438784602389247"
			if boundary == "short-link" {
				url = "https://v.douyin.com/example/"
			}
			calls, probes := 0, 0
			executor := executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				if boundary == "before" {
					t.Fatal("canceled request reached transport")
				}
				if boundary == "short-link" || boundary == "detail" {
					cancel()
					return fetch.Response{}, ctx.Err()
				}
				return fetch.Response{StatusCode: 200, Body: []byte(`{"status_code":0,"aweme_detail":{"aweme_id":"7628438784602389247","video":{"play_addr":{"uri":"fixture","url_list":["https://v5-test.douyinvod.com/known.mp4"]}}}}`)}, nil
			})
			prober := proberFunc(func(ctx context.Context, raw string) (object, error) {
				probes++
				cancel()
				return nil, ctx.Err()
			})
			if boundary == "before" {
				cancel()
			}
			payload, err := New(executor, prober).Parse(ctx, url, 1793361600)
			if !errors.Is(err, context.Canceled) || payload != nil {
				t.Fatalf("cancellation was hidden: payload=%#v err=%v", payload, err)
			}
			if boundary == "before" && calls != 0 || boundary != "before" && calls != 1 {
				t.Fatalf("unexpected requests after cancel: %d", calls)
			}
			if boundary == "probe" && probes != 1 || boundary != "probe" && probes != 0 {
				t.Fatalf("unexpected probes after cancel: %d", probes)
			}
		})
	}
}

func TestParserIsReusableAcrossConcurrentCalls(t *testing.T) {
	c := findFixture(t, "video/multi-quality")
	input, expected := unified.Map(c["input"]), unified.Map(c["expected"])
	response := unified.Map(unified.Slice(input["http_responses"])[0])
	now, _ := unified.Numeric(input["now"])
	body := []byte(unified.Str(response["body"]))
	parser := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
		return fetch.Response{StatusCode: 200, Body: body, URL: request.URL}, nil
	}), nil)
	results := make(chan error, 12)
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			payload, err := parser.Parse(context.Background(), unified.Str(input["url"]), int64(now))
			if err == nil {
				if difference := firstDifference(unified.PublicView(payload, false, int64(now)), expected["normal"], "$"); difference != "" {
					err = errors.New(difference)
				}
			}
			results <- err
		}()
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestLiveVIDLimitAndImagePairing(t *testing.T) {
	images := array{}
	for i := 0; i < 32; i++ {
		images = append(images, object{"url_list": array{fmt.Sprintf("https://images.example/%d.jpg", i)},
			"video": object{"play_addr": object{"uri": fmt.Sprintf("vid-%d", i), "url_list": array{fmt.Sprintf("https://v5-test.douyinvod.com/known-%d.mp4", i)}}}})
	}
	// Repeated videos use the same original HEAD result without changing still
	// associations; entries beyond the cap retain their own known motion URL.
	images = append(images, object{"url_list": array{"https://images.example/repeated.jpg"},
		"video": object{"play_addr": object{"uri": "vid-0", "url_list": array{"https://v5-test.douyinvod.com/repeated.mp4"}}}})
	requests := []string{}
	parser := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
		if request.Method != http.MethodHead || request.Policy.FollowRedirects || !request.Policy.AllowedURL(request.URL) {
			t.Fatalf("unbounded live request: %#v", request)
		}
		requests = append(requests, request.URL)
		return fetch.Response{StatusCode: 200, URL: request.URL}, nil
	}), nil)
	data, err := parser.formatData(context.Background(), object{"images": images}, "7628438784602389247")
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 30 {
		t.Fatalf("made %d original HEADs, want exactly 30 unique video lookups", len(requests))
	}
	live := unified.Slice(data["live_photo"])
	if len(live) != 33 {
		t.Fatalf("lost live-photo entries: %d", len(live))
	}
	for i, value := range live {
		entry := unified.Map(value)
		if i < 32 && unified.Str(entry["image"]) != fmt.Sprintf("https://images.example/%d.jpg", i) {
			t.Fatalf("image %d was paired with another motion", i)
		}
		if i >= 30 && i < 32 {
			if unified.Str(entry["video"]) != fmt.Sprintf("https://v5-test.douyinvod.com/known-%d.mp4?mime_type=video_mp4", i) {
				t.Fatalf("entry beyond HEAD cap lost its fallback: %#v", entry)
			}
		}
	}
	if unified.Str(unified.Map(live[32])["video"]) != unified.Str(unified.Map(live[0])["video"]) {
		t.Fatal("duplicate VID did not reuse per-parse original resolution")
	}
}

func TestLiveRedirectBoundaries(t *testing.T) {
	for _, target := range []string{"https://127.0.0.1/private", "https://v5-test.douyinvod.com.evil.test/a", "https://aweme.snssdk.com/other", "https://v5-test.douyinvod.com/a#fragment", "http://v5-test.douyinvod.com/a"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			parser := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
				calls++
				return fetch.Response{StatusCode: 302, Header: http.Header{"Location": {target}}}, nil
			}), nil)
			value, err := parser.followLocation(context.Background(), buildOriginalPlayURL("fixture"))
			if err != nil || value != "" || calls != 1 {
				t.Fatalf("unsafe redirect followed or exposed: value=%q calls=%d error=%v", value, calls, err)
			}
		})
	}
	calls := 0
	parser := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
		calls++
		return fetch.Response{StatusCode: 302, Header: http.Header{"Location": {"https://v5-test.douyinvod.com/loop"}}}, nil
	}), nil)
	if _, err := parser.followLocation(context.Background(), buildOriginalPlayURL("fixture")); err != nil || calls != 3 {
		t.Fatalf("live redirect budget failed: calls=%d err=%v", calls, err)
	}
}

func TestPlatformRequestPurposeAllowLists(t *testing.T) {
	for _, raw := range []string{originalEndpoint + "?video_id=x", "https://v5-test.douyinvod.com/a", "https://v11-o.douyinvod.com/a", "https://v5-hl-mly-ov.zjcdn.com/a", "https://v5-test.douyinvod.com:443/a"} {
		if !AllowedMediaURL(raw) {
			t.Errorf("official media rejected: %s", raw)
		}
	}
	for _, raw := range []string{"http://v5-test.douyinvod.com/a", "HTTPS://v5-test.douyinvod.com/a", "https://V5-test.douyinvod.com/a", "https://v5-test.douyinvod.com:444/a", "https://v5-test.douyinvod.com.evil.test/a", "https://evil.douyinvod.com/a", "https://127.0.0.1/a", "https://user@v5-test.douyinvod.com/a", "https://v5-test.douyinvod.com/a#", "https://aweme.snssdk.com/other", "https://v5-test.douyinvod.com/\na"} {
		if AllowedMediaURL(raw) {
			t.Errorf("unsafe media allowed: %s", raw)
		}
	}
	if allowedDetailURL("https://api.amemv.com/other") || allowedDetailURL("https://api.amemv.com.evil.test/aweme/v1/aweme/detail/") || allowedShareURL("https://douyin.com.evil.test/video/1") {
		t.Fatal("request-purpose hostname or path boundary widened")
	}
	if AllowedMediaURL(detailEndpoint) || allowedDetailURL(originalEndpoint) || allowedShareURL(originalEndpoint) {
		t.Fatal("metadata, share and media purposes were conflated")
	}
}

func TestInvalidIdentityDoesNotRequestDetail(t *testing.T) {
	parser := New(executorFunc(func(ctx context.Context, request fetch.Request) (fetch.Response, error) {
		t.Fatal("invalid identity caused an upstream request")
		return fetch.Response{}, nil
	}), nil)
	for _, raw := range []string{"", "hello", "https://douyin.com.evil.test/video/7628438784602389247", "https://www.douyin.com/video/123", "https://www.douyin.com/video/0123456789012345", "https://www.douyin.com/video/7628438784602389247?vid=other"} {
		t.Run(raw, func(t *testing.T) {
			payload, err := parser.Parse(context.Background(), raw, 1793361600)
			if err != nil || unified.Str(payload["code"]) == "OK" {
				t.Fatalf("invalid identity accepted: payload=%#v error=%v", payload, err)
			}
		})
	}
	for _, tc := range []struct{ raw, id string }{
		{"https://www.douyin.com/video/7628438784602389247.html", "7628438784602389247"},
		{"https://www.douyin.com/?vid=bad&vid=7628438784602389247&id=other", "7628438784602389247"},
		{"https://www.douyin.com/?vid=bad&vid[]=7628438784602389247&id=1234567890123456", "1234567890123456"},
		{"https://www.douyin.com/?modal.id=7628438784602389247", "7628438784602389247"},
	} {
		if got := extractAwemeID(tc.raw); got != tc.id {
			t.Errorf("query/path identity: got %q want %q", got, tc.id)
		}
	}
}

func TestSourceQueryBytesAndLegacyNumericBoundary(t *testing.T) {
	for _, raw := range []string{
		"https://v5-test.douyinvod.com/a?token=a%2Bb+z&line=1&line=2#fragment",
		"https://v5-test.douyinvod.com/a?mime.type=custom&sign=a%2Fz",
		"https://v5-test.douyinvod.com/a?mime_type[]=custom&sign=a%2Fz",
	} {
		converted := ensureVideoMIME(raw)
		if strings.Contains(raw, "mime") && converted != raw {
			t.Fatalf("existing PHP query alias was rewritten: %s", converted)
		}
		if !strings.Contains(raw, "mime") && converted != "https://v5-test.douyinvod.com/a?token=a%2Bb+z&line=1&line=2&mime_type=video_mp4#fragment" {
			t.Fatalf("source query bytes changed: %s", converted)
		}
	}
	wire, err := legacyWireData(object{"author": object{"id": "9223372036854775800"}, "scientific": "1e3", "negative_zero": "-0.0", "unbounded": "1e999", "empty": object{}, "items": array{}})
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := unified.Decode([]byte(`{"author":{"id":9223372036854775800},"scientific":1000,"negative_zero":0,"unbounded":"1e999","empty":{},"items":[]}`))
	requireSameWire(t, wire, expected)
}

func TestFormatterUsesPHPRounding(t *testing.T) {
	// Values were executed with PHP 8.3 round($duration / 1000, 2). These
	// decimal half boundaries do not match math.Round(value*100)/100.
	for _, tc := range []struct{ milliseconds, seconds float64 }{
		{1004.9999999999999, 1.01}, {1074.9999999999998, 1.08}, {2674.9999999999995, 2.68},
	} {
		got, err := New(nil, nil).formatData(context.Background(), object{"video": object{"duration": tc.milliseconds}}, "7628438784602389247")
		if err != nil || !unified.Equal(got["duration"], tc.seconds) {
			t.Fatalf("PHP duration rounding mismatch: duration=%v got=%v err=%v", tc.milliseconds, got["duration"], err)
		}
	}
	if label := formatSizeLabel(500.49999999999994); label != "501B" {
		t.Fatalf("PHP byte-label rounding mismatch: %s", label)
	}
}
