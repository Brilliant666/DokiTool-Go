package pipixia

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var scripts = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script>`)

func pageItem(html string) any {
	for _, match := range scripts.FindAllStringSubmatch(html, -1) {
		if !strings.Contains(match[1], "ppxItemDetail") {
			continue
		}
		raw := strings.Trim(match[1], " \t\n\r\x00\v")
		state, err := decodePHP([]byte(raw), true, 64)
		if err != nil || !isArray(state) {
			state, err = decodePHP([]byte(percentDecode(raw)), true, 64)
		}
		if err == nil && isArray(state) {
			item := field(field(state, "ppxItemDetail"), "item")
			if isArray(item) {
				return item
			}
		}
	}
	return nil
}
func mediaURLs(media any) []string {
	urls := []string{}
	seen := map[string]bool{}
	if !isArray(media) {
		return urls
	}
	for _, key := range []string{"url_list", "download_list"} {
		for _, entry := range values(field(media, key)) {
			if isArray(entry) {
				entry = field(entry, "url")
			}
			url := unified.MediaURL(entry)
			if url != "" && !seen[url] {
				seen[url] = true
				urls = append(urls, url)
			}
		}
	}
	return urls
}
func firstURL(media any) string {
	urls := mediaURLs(media)
	if len(urls) > 0 {
		return urls[0]
	}
	return ""
}
func itemResponse(item any, expected string) unified.Object {
	actual := first(field(item, "item_id_str"), field(item, "item_id"))
	id := ""
	switch value := actual.(type) {
	case string:
		id = value
	case int64:
		id = strconv.FormatInt(value, 10)
	}
	if id != expected {
		return failure(502, "皮皮虾返回的作品与分享链接不一致", "UPSTREAM_ITEM_MISMATCH")
	}
	if truthy(field(item, "status")) {
		return failure(404, "该皮皮虾作品当前不可访问", "ITEM_UNAVAILABLE")
	}
	video := field(item, "video")
	candidates := unified.Array{}
	seen := map[string]bool{}
	representations := []any{field(item, "origin_video_download")}
	for _, name := range []string{"video_download", "video_high", "video_mid", "video_low", "video_fallback"} {
		representations = append(representations, field(video, name))
	}
	for _, source := range representations {
		urls := mediaURLs(source)
		mediaIdentity := representationIdentity(urls)
		identity := append([]string{}, urls...)
		sort.Strings(identity)
		encoded, err := unified.PHPJSON(identity, false)
		if err != nil {
			return failure(502, "皮皮虾解析暂时失败，请稍后重试", "UPSTREAM_PARSE_FAILED")
		}
		digest := sha256.Sum256(encoded)
		for _, url := range urls {
			if seen[url] {
				continue
			}
			seen[url] = true
			candidate := unified.Object{"url": url, "quality": "unknown", "private_representation_id": hex.EncodeToString(digest[:])}
			if mediaIdentity != "" {
				candidate["private_media_identity"] = mediaIdentity
				if sourceIdentity := ppxRenewedSourceIdentity(url, mediaIdentity); sourceIdentity != "" {
					candidate["private_source_identity"] = sourceIdentity
				}
			}
			for _, key := range []string{"width", "height"} {
				value := field(source, key)
				if number, ok := unified.Numeric(value); ok && number > 0 {
					candidate[key], _ = unified.PHPInt(value)
				}
			}
			width, wok := candidate["width"].(int64)
			height, hok := candidate["height"].(int64)
			if wok && hok {
				if height < width {
					width = height
				}
				candidate["quality"] = strconv.FormatInt(width, 10) + "p"
			}
			candidate["label"] = candidate["quality"]
			if candidate["quality"] == "unknown" {
				candidate["label"] = "视频源"
			}
			candidates = append(candidates, candidate)
		}
	}
	area := func(value any) float64 {
		v := unified.Map(value)
		w, _ := unified.Numeric(v["width"])
		h, _ := unified.Numeric(v["height"])
		return w * h
	}
	sort.SliceStable(candidates, func(i, j int) bool { return area(candidates[i]) > area(candidates[j]) })
	images := unified.Array{}
	imageSeen := map[string]bool{}
	for _, image := range values(field(field(item, "note"), "multi_image")) {
		url := firstURL(image)
		if url != "" && !imageSeen[url] {
			images = append(images, url)
			imageSeen[url] = true
		}
	}
	if len(candidates) == 0 && len(images) == 0 {
		return failure(404, "该皮皮虾作品没有可用的视频或图片", "MEDIA_UNAVAILABLE")
	}
	selected := unified.Object{}
	backup := unified.Array{}
	kind := "image"
	if len(candidates) > 0 {
		selected = unified.Map(candidates[0])
		backup = append(backup, candidates[1:]...)
		kind = "video"
	}
	title, _ := field(item, "content").(string)
	author := field(item, "author")
	cover := firstURL(field(item, "cover"))
	if cover == "" {
		cover = firstURL(field(video, "cover_image"))
	}
	if cover == "" && len(images) > 0 {
		cover = unified.Str(images[0])
	}
	data := unified.Object{"id": expected, "type": kind, "title": title, "author": plain(first(field(author, "name"), "")), "avatar": firstURL(field(author, "avatar")), "cover": cover, "url": unified.Str(selected["url"]), "images": images, "imgurl": unified.CloneValue(images), "video_backup": backup}
	for _, key := range []string{"width", "height", "quality", "private_representation_id", "private_media_identity", "private_source_identity"} {
		if value, ok := selected[key]; ok {
			data[key] = value
		}
	}
	if duration, ok := unified.Numeric(first(field(video, "duration"), field(item, "duration"))); ok && duration >= 0 {
		data["duration"] = duration
	}
	if value := field(item, "create_time"); value != nil {
		if number, ok := unified.Numeric(value); ok && number > 0 {
			data["create_time"], _ = unified.PHPInt(value)
		}
	}
	statistics := unified.Object{}
	for _, pair := range [][2]string{{"like_count", "digg_count"}, {"comment_count", "comment_count"}, {"share_count", "share_count"}, {"play_count", "play_count"}, {"view_count", "view_count"}} {
		value := field(field(item, "stats"), pair[0])
		if count, ok := unified.Numeric(value); ok && count >= 0 {
			statistics[pair[1]], _ = unified.PHPInt(value)
		}
	}
	if len(statistics) > 0 {
		data["extra"] = unified.Object{"statistics": statistics}
	}
	return unified.Object{"code": 200, "msg": "解析成功", "data": data}
}
