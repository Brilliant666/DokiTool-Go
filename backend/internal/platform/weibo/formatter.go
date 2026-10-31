package weibo

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// These functions implement the private, frozen PHP response boundary before
// unified-v1 adaptation. Raw URL order must be recovered from the original
// bounded component JSON, never from Go map iteration.
func legacyWeiboFormatStatus(status unified.Object) unified.Object {
	if ok, exists := status["ok"]; exists && ok != nil && !phpGuestTruthy(ok) {
		return weiboFormatFailure(503, "微博未返回可用的正文数据，可能暂时限制访问或作品不可见")
	}
	if !weiboDefined(status, "id") && !weiboDefined(status, "idstr") && !weiboDefined(unified.Map(status["page_info"]), "media_info") {
		return weiboFormatFailure(502, "微博返回的正文数据不完整")
	}
	// PHP empty() also treats an empty array and the string "0" as missing.
	if !phpGuestTruthy(unified.Map(status["page_info"])["media_info"]) {
		return weiboFormatFailure(404, "该微博正文没有可用的视频")
	}
	return legacyWeiboVideoResult(nil, status, nil)
}

func legacyWeiboFormatComponent(component, status unified.Object, urlKeys []string) unified.Object {
	code, numeric := unified.Numeric(component["code"])
	if !numeric || code != 100000 {
		// In the original PHP this comparison uses loose numeric equality.
		if v, ok := component["code"].(bool); !ok || !v {
			return weiboFormatFailure(503, "微博未返回可用的作品数据，可能暂时限制访问或作品不可见")
		}
	}
	video := unified.Map(unified.Map(component["data"])["Component_Play_Playinfo"])
	if len(video) == 0 {
		return weiboFormatFailure(502, "微博返回的作品数据不完整")
	}
	return legacyWeiboVideoResult(video, status, urlKeys)
}

func weiboFormatFailure(code int, message string) unified.Object {
	return unified.Object{"code": code, "msg": message}
}

func weiboDefined(data unified.Object, key string) bool {
	value, exists := data[key]
	return exists && value != nil
}

func weiboFirstDefined(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func weiboPHPString(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string:
		return value
	case json.Number:
		return value.String()
	case bool:
		if value {
			return "1"
		}
		return ""
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(value, 10)
	case int:
		return strconv.Itoa(value)
	default:
		if unified.Map(value) != nil || unified.Slice(value) != nil {
			return "Array"
		}
		return fmt.Sprint(value)
	}
}

func weiboDuration(video, media unified.Object) float64 {
	if value, valid := unified.Numeric(video["duration_time"]); valid {
		return value
	}
	if value, valid := unified.Numeric(media["duration"]); valid {
		return value
	}
	return 0
}

func weiboCreatedAt(raw any) int64 {
	date, ok := raw.(string)
	if !ok || date == "" {
		return 0
	}
	for _, layout := range []string{
		"Mon Jan 02 15:04:05 -0700 2006",
		"Mon Jan 2 15:04:05 -0700 2006",
		"Mon, 02 Jan 2006 15:04:05 -0700",
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339,
		"2006-01-02 15:04:05",
	} {
		parsed, err := time.Parse(layout, date)
		if err == nil {
			return parsed.Unix()
		}
	}
	return 0
}

func weiboTags(video, status unified.Object) []any {
	tags := make([]any, 0)
	seen := make(map[string]bool)
	for _, entries := range []unified.Array{unified.Slice(video["topics"]), unified.Slice(status["topic_struct"])} {
		for _, entry := range entries {
			tag := unified.Map(entry)
			value := weiboFirstDefined(tag["content"], tag["topic_title"])
			text, ok := value.(string)
			if !ok {
				continue
			}
			name := strings.Trim(text, " \n\r\t\v\x00")
			if name != "" && !seen[name] {
				seen[name] = true
				tags = append(tags, name)
			}
		}
	}
	return tags
}

func legacyWeiboVideoResult(video, status unified.Object, urlKeys []string) unified.Object {
	candidates := legacyWeiboCandidates(video, status, urlKeys)
	if len(candidates) == 0 {
		return weiboFormatFailure(404, "作品没有可用的视频地址")
	}
	media := unified.Map(unified.Map(status["page_info"])["media_info"])
	user := unified.Map(status["user"])
	title := weiboFirstDefined(video["title"], media["video_title"], status["text_raw"], "")
	extra := unified.Object{
		"ip_info": weiboFirstDefined(video["ip_info_str"], ""),
		"date":    weiboFirstDefined(video["date"], ""),
	}
	stats := unified.Object{}
	for _, pair := range []struct {
		key   string
		value any
	}{
		{"play_count", weiboFirstDefined(media["online_users_number"], video["play_count"])},
		{"digg_count", weiboFirstDefined(status["attitudes_count"], video["attitudes_count"])},
		{"comment_count", weiboFirstDefined(status["comments_count"], video["comments_count"])},
		{"share_count", weiboFirstDefined(status["reposts_count"], video["reposts_count"])},
	} {
		if value, ok := legacyWeiboCount(pair.value); ok {
			stats[pair.key] = value
		}
	}
	if len(stats) > 0 {
		extra["statistics"] = stats
	}
	if tags := weiboTags(video, status); len(tags) > 0 {
		extra["hashtags"] = tags
	}
	data := unified.Object{
		"type":  "video",
		"title": title,
		"desc":  weiboFirstDefined(status["text_raw"], title),
		"author": unified.Object{
			"name":   weiboFirstDefined(video["author"], user["screen_name"], ""),
			"id":     weiboPHPString(weiboFirstDefined(video["author_id"], user["idstr"], user["id"], "")),
			"avatar": legacyMediaURL(weiboFirstDefined(video["avatar"], user["avatar_hd"], user["profile_image_url"])),
		},
		"cover":        legacyMediaURL(weiboFirstDefined(video["cover_image"], unified.Map(status["page_info"])["page_pic"])),
		"duration":     weiboDuration(video, media),
		"video_backup": candidates,
		"extra":        extra,
	}
	for _, key := range []string{"url", "quality", "width", "height", "bit_rate", "codec", "size", "fps"} {
		if value, present := candidates[0][key]; present && value != nil {
			data[key] = value
		}
	}
	timestamp := weiboCreatedAt(status["created_at"])
	if timestamp == 0 {
		if value, ok := legacyWeiboCount(weiboFirstDefined(video["real_date"], media["video_publish_time"])); ok {
			timestamp = value
		}
	}
	if timestamp > 0 {
		data["create_time"] = timestamp
		data["publish_time"] = time.Unix(timestamp, 0).In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02 15:04:05")
	}
	return unified.Object{"code": 200, "msg": "解析成功", "data": data}
}
