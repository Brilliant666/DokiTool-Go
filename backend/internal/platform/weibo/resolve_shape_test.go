package weibo

import (
	"context"
	"strings"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func resolveOneTVResponse(t *testing.T, body string) (unified.Object, error) {
	t.Helper()
	calls := 0
	session := newGuestSession(func(_ context.Context, address string, post *string, _ string) (unified.Object, error) {
		calls++
		if calls != 1 || post == nil || !strings.HasPrefix(address, "https://weibo.com/tv/api/component?") {
			t.Fatalf("unexpected request after component-only response: calls=%d address=%s", calls, address)
		}
		return unified.Object{"error": false, "status": 200, "url": address, "body": body}, nil
	})
	got, err := session.resolveLegacy(context.Background(), &Target{Kind: "video", ID: "1034:1234567890123456"})
	if calls != 1 {
		t.Fatalf("component request count: %d", calls)
	}
	return got, err
}

func TestWeiboTVBusinessErrorPrecedesSourceExtraction(t *testing.T) {
	tooManyURLs := "[" + strings.Repeat(`"https://f.video.weibocdn.com/a.mp4",`, 4096) + `"https://f.video.weibocdn.com/z.mp4"]`
	for _, tc := range []struct {
		name, body, message string
		code                int
	}{
		{"error-scalar-data", `{"code":100001,"data":""}`, "微博未返回可用的作品数据，可能暂时限制访问或作品不可见", 503},
		{"error-list-data", `{"code":100001,"data":["not a component"]}`, "微博未返回可用的作品数据，可能暂时限制访问或作品不可见", 503},
		{"error-excessive-urls", `{"code":100001,"data":{"Component_Play_Playinfo":{"mid":"123","urls":` + tooManyURLs + `}}}`, "微博未返回可用的作品数据，可能暂时限制访问或作品不可见", 503},
		{"success-missing-component", `{"code":100000,"data":""}`, "微博返回的作品数据不完整", 502},
		{"scalar-urls-no-media", `{"code":100000,"data":{"Component_Play_Playinfo":{"mid":"123","urls":true}}}`, "作品没有可用的视频地址", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveOneTVResponse(t, tc.body)
			want := unified.Object{"code": tc.code, "msg": tc.message}
			if err != nil || !unified.Equal(got, want) {
				t.Fatalf("platform failure was replaced: got=%#v want=%#v err=%v", got, want, err)
			}
		})
	}
}

func TestWeiboTVURLListPreservesPHPIndexesAndCandidates(t *testing.T) {
	const body = `{"code":100000,"data":{"Component_Play_Playinfo":{"title":"array source","urls":["https://f.video.weibocdn.com/first.mp4?template=1280x720",null,"https://f.video.weibocdn.com/best.mp4?template=1920x1080","https://f.video.weibocdn.com/first.mp4?template=1280x720","https://f.video.weibocdn.com/second.mp4?template=1280x720"]}}}`
	keys, err := weiboURLKeysFromRaw([]byte(body), "data", "Component_Play_Playinfo", "urls")
	if err != nil || !unified.Equal(keys, []string{"0", "1", "2", "3", "4"}) {
		t.Fatalf("PHP array indexes lost: keys=%#v err=%v", keys, err)
	}
	got, err := resolveOneTVResponse(t, body)
	if err != nil || !unified.Equal(got["code"], 200) {
		t.Fatalf("PHP-compatible URL list was rejected: %#v, %v", got, err)
	}
	data := unified.Map(got["data"])
	candidates := unified.Slice(data["video_backup"])
	if len(candidates) != 3 {
		t.Fatalf("URL list lost candidates or retained invalid/duplicate entries: %#v", candidates)
	}
	for index, label := range []string{"2", "0", "4"} {
		if unified.Map(candidates[index])["label"] != label {
			t.Fatalf("PHP labels or stable quality order changed: %#v", candidates)
		}
	}
	const best = "https://f.video.weibocdn.com/best.mp4?template=1920x1080"
	if data["url"] != best || data["quality"] != "1080p" {
		t.Fatalf("array default facts changed: %#v", data)
	}
	adapted := adaptLegacy(got, "https://weibo.com/tv/show/1034:1234567890123456", 1730000000)
	if adapted["code"] != "OK" {
		t.Fatalf("array source failed product adaptation: %#v", adapted)
	}
	items := unified.Slice(unified.Map(adapted["data"])["items"])
	if len(items) != 1 || unified.Map(unified.Map(items[0])["default"])["url"] != best {
		t.Fatalf("array source lost default URL: %#v", items)
	}
}

func TestWeiboOrderedURLFieldsRespectPHPShapesAndBudget(t *testing.T) {
	for _, body := range []string{
		`{"urls":true}`, `{"urls":"not a list"}`, `{"urls":null}`, `{"urls":[]}`, `{"urls":1e309}`,
	} {
		keys, err := weiboURLKeysFromRaw([]byte(body), "urls")
		if err != nil || len(keys) != 0 {
			t.Fatalf("PHP-empty source field became an extraction error: body=%s keys=%#v err=%v", body, keys, err)
		}
	}
	tooMany := `{"urls":[` + strings.Repeat(`"https://f.video.weibocdn.com/a.mp4",`, 4096) + `"https://f.video.weibocdn.com/z.mp4"]}`
	if _, err := weiboURLKeysFromRaw([]byte(tooMany), "urls"); err == nil {
		t.Fatal("URL array bypassed the 4096-source budget")
	}
}
