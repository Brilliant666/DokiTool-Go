package weibo

import "testing"

func TestTargetSmokeWithoutNetwork(t *testing.T) {
	const fid = "1034:1234567890123456"
	cases := []struct {
		id, input, kind, mediaID string
	}{
		{"component", "https://video.weibo.com/show?fid=" + fid, "video", fid},
		{"component-path", "https://weibo.com/tv/show/" + fid, "video", fid},
		{"status", "https://weibo.com/12345678/AbC09", "status", "AbC09"},
		{"mobile", "https://m.weibo.cn/detail/12345", "status", "12345"},
		{"array-displaces", "https://video.weibo.com/show?fid=" + fid + "&fid[]=123", "", ""},
		{"last-scalar", "https://video.weibo.com/show?fid[]=bad&fid=" + fid, "video", fid},
		{"private-host", "https://video.weibo.com.evil.example/show?fid=" + fid, "", ""},
		{"credentials", "https://user@video.weibo.com/show?fid=" + fid, "", ""},
		{"fragment-not-input", "https://weibo.com/tv/show/" + fid + "#test", "video", fid},
		{"invalid-port", "https://video.weibo.com:444/show?fid=" + fid, "", ""},
		{"encoded-path", "https://weibo.com/tv%2Fshow%2F" + fid, "video", fid},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			value := ParseTarget(tc.input)
			if tc.kind == "" {
				if value != nil {
					t.Fatalf("unexpected target: %#v", value)
				}
				return
			}
			if value == nil || value.Kind != tc.kind || value.ID != tc.mediaID {
				t.Fatalf("target=%#v, want %s:%s", value, tc.kind, tc.mediaID)
			}
		})
	}
	if ParseTarget([]string{"https://weibo.com/123/ab"}) != nil {
		t.Fatal("PHP rejects non-scalar URL input")
	}
}
