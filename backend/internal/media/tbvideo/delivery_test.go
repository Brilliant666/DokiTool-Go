package tbvideo_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/media/tbvideo"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/pipigx"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/zuiyou"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func TestCandidate(t *testing.T) {
	const edge = "https://tbvideo.ixiaochuan.cn"
	for _, test := range []struct{ platform, raw, want string }{
		{"pipigx", "http://video01.szsttkj.com/zyvd/a%2Fb?auth_key=fake&x=2#clip", edge + "/zyvd/a%2Fb"},
		{"pipigx", "https://VIDEO03.szsttkj.com/new-folder/abc", edge + "/new-folder/abc"},
		{"pipigx", "http://xlal-ppvideo.szsttkj.com/zyvqwz/media", edge + "/zyvqwz/media"},
		{"zuiyou", "http://web-v01.izuiyou.com/obj?a=&auth_key=fake", edge + "/obj"},
		{"zuiyou", "http://web-v01.izuiyou.com?auth_key=fake", ""},
		{"zuiyou", "/relative", ""},
		{"zuiyou", "http://web-v01.izuiyou.com/%ZZ", ""},
		{"zuiyou", "ftp://web-v01.izuiyou.com/media", ""},
		{"zuiyou", "https://web-v01.izuiyou.com.other.test/media", ""},
		{"zuiyou", "https://user@web-v01.izuiyou.com/media", ""},
		{"zuiyou", "https://web-v01.izuiyou.com:88/media", ""},
		{"zuiyou", "https://web-v01.izuiyou.com/%2e%2e/a", ""},
		{"pipigx", "http://video01.szsttkj.com/zywmvd/watermarked", ""},
		{"other", "http://video01.szsttkj.com/zyvd/a", ""},
	} {
		if got := tbvideo.Candidate(test.platform, test.raw); got != test.want {
			t.Errorf("Candidate(%q,%q) = %q, want %q", test.platform, test.raw, got, test.want)
		}
	}
}

type details []byte

func (d details) Do(_ context.Context, r fetch.Request) (fetch.Response, error) {
	if r.Method != "POST" {
		return fetch.Response{}, fmt.Errorf("unexpected request: %s", r.Method)
	}
	return fetch.Response{StatusCode: 200, Body: d}, nil
}

func TestParsedDownloadCandidatesFollowTheUnwatermarkedVideo(t *testing.T) {
	for _, tc := range []struct{ platform, host, link string }{
		{"pipigx", "video01.szsttkj.com", "https://h5.pipigx.com/pp/post/123?pid=123&mid=1"},
		{"zuiyou", "web-v01.izuiyou.com", "https://share.izuiyou.com/hybrid/share/post?pid=123"},
	} {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/enabled=%t", tc.platform, enabled), func(t *testing.T) {
				// The malformed middle video must not shift the following mapping.
				body := fmt.Sprintf(`{"data":{"post":{"videos":{"11":{"url":"http://%[1]s/zyvd/one?auth_key=private","urlwm":"http://%[1]s/zywmvd/wm1"},"12":{"urlwm":"http://%[1]s/zywmvd/ignored"},"13":{"url":"http://%[1]s/zyvqwz/two?auth_key=other","urlwm":"http://%[1]s/zywmvd/wm2"}}}}}`, tc.host)
				var parser interface {
					Parse(context.Context, string, int64) (unified.Object, error)
				}
				if tc.platform == "pipigx" {
					parser = pipigx.NewWithTBVideo(details(body), enabled)
				} else {
					parser = zuiyou.NewWithTBVideo(details(body), enabled)
				}
				res, err := parser.Parse(context.Background(), tc.link, 1)
				if err != nil || res["code"] != "OK" {
					t.Fatalf("parse: %v %v", res, err)
				}
				for _, debug := range []bool{false, true} {
					pub := unified.PublicView(res, debug, 1)
					if err := unified.ValidateEnvelopeAt(pub, 1); err != nil {
						t.Fatal(err)
					}
					data := unified.Map(pub["data"])
					plan := unified.Map(unified.Map(data["extra"])["tbvideo"])
					items := unified.Slice(data["items"])
					lookup := unified.Map(plan["items"])
					if plan["pid"] != "123" || len(items) != 2 || len(lookup) != 2 {
						t.Fatalf("unexpected plan: %v", plan)
					}
					for i, entry := range items {
						item := unified.Map(entry)
						route := unified.Map(lookup[unified.Str(item["id"])])
						variant := unified.Map(unified.Slice(item["variants"])[0])
						source := unified.Map(unified.Slice(unified.Map(variant["delivery"])["sources"])[0])
						object := []string{"/zyvd/one", "/zyvqwz/two"}[i]
						wantPreview := "http://" + tc.host + object + []string{"?auth_key=private", "?auth_key=other"}[i]
						if source["url"] != wantPreview || unified.Map(item["default"])["url"] != wantPreview {
							t.Fatalf("preview changed for %v", item["id"])
						}
						urls := unified.Map(route["sources"])
						if route["vid"] != []string{"11", "13"}[i] || urls == nil || (enabled && (len(urls) != 1 || urls[unified.Str(source["id"])] != "https://tbvideo.ixiaochuan.cn"+object)) || (!enabled && len(urls) != 0) {
							t.Fatalf("wrong route: %v", route)
						}
					}
				}
			})
		}
	}
}
