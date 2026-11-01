package fetch

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestWeiboPinnedTargetValidation(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://f.video.weibocdn.com/movie.mp4":                true,
		"https://f.video.weibocdn.com:0443/movie.mp4?token=abc": true,
		"https://F.VIDEO.WEIBOCDN.COM/movie.mp4":                false,
		"https://f.video.weibocdn.com/movie.mp4#":               false,
		"http://f.video.weibocdn.com/movie.mp4":                 false,
		"https://f.video.weibocdn.com:444/movie.mp4":            false,
		"https://f.video.weibocdn.com.evil.test/movie.mp4":      false,
		"https://user@f.video.weibocdn.com/movie.mp4":           false,
		"https://f.video.weibocdn.com/movie%ZZ.mp4":             false,
	} {
		if got := validWeiboTarget(raw); got != want {
			t.Errorf("stream target %q allowed=%v want=%v", raw, got, want)
		}
	}
}

func TestWeiboStreamingRejectsUnsafeInputsBeforeDial(t *testing.T) {
	good := "https://f.video.weibocdn.com/a.mp4"
	for _, tc := range []struct {
		name, raw, ip, byteRange string
	}{
		{"foreign-host", "https://evil.test/a.mp4", "8.8.8.8", ""},
		{"http", "http://f.video.weibocdn.com/a.mp4", "8.8.8.8", ""},
		{"loopback", good, "127.0.0.1", ""},
		{"documentation-ip", good, "203.0.113.10", ""},
		{"ipv6-mapped", good, "::ffff:8.8.8.8", ""},
		{"multiple-ranges", good, "8.8.8.8", "bytes=0-1,3-4"},
		{"malformed-range", good, "8.8.8.8", "bytes=abc"},
		{"newline-range", good, "8.8.8.8", "bytes=0-7\r\nCookie: secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address := netip.MustParseAddr(tc.ip)
			response, err := OpenWeiboMedia(context.Background(), tc.raw, address, tc.byteRange)
			if !errors.Is(err, ErrPolicy) || response != nil {
				t.Fatalf("unsafe media request not rejected: response=%v err=%v", response, err)
			}
		})
	}
}
