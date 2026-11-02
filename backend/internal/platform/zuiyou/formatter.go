package zuiyou

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func formatResponse(root *jsonNode) unified.Object {
	post := root.child("data", "post")
	if !post.isArray() || len(post.keys) == 0 {
		return failure(404, "作品不存在或暂时不可用")
	}
	member, images, videos := post.child("member"), post.child("imgs"), post.child("videos")
	ordered, seen := []string{}, map[string]bool{}
	for _, image := range images.values() {
		id := image.child("id").valueOf()
		key, valid := id.(string)
		if number, ok := id.(int64); ok {
			key, valid = strconv.FormatInt(number, 10), true
		}
		if valid && videos.has(key) && !seen[key] {
			ordered, seen[key] = append(ordered, key), true
		}
	}
	for _, key := range videos.keysOf() {
		if !seen[key] {
			ordered, seen[key] = append(ordered, key), true
		}
	}
	private := unified.Array{}
	primary := ""
	for _, key := range ordered {
		video := videos.child(key)
		value := unified.CloneValue(video.valueOf())
		if video.isArray() {
			fields := arrayObject(value)
			fields["private_video_id"] = key
			value = fields
			if primary == "" {
				primary = unified.MediaURL(fields["url"])
			}
		}
		private = append(private, value)
	}
	pictures := unified.Array{}
	for _, image := range images.values() {
		urls := image.child("urls")
		if !urls.isArray() {
			continue
		}
		best := urls.child("origin")
		if best.valueOf() == nil {
			best = urls.child("large")
		}
		if best.valueOf() == nil && len(urls.keys) > 0 {
			best = urls.child(urls.keys[0])
		}
		if picture := unified.MediaURL(best.child("urls", "0").valueOf()); picture != "" {
			pictures = append(pictures, picture)
		}
	}
	if primary == "" && (len(private) > 0 || len(pictures) == 0) {
		return failure(404, "作品没有可用的媒体地址")
	}
	cover := images.child("0", "urls", "540_webp", "urls", "0").valueOf()
	if cover == nil && len(pictures) > 0 {
		cover = pictures[0]
	}
	data := unified.Object{
		"type": "video", "author": defined(member.child("name").valueOf(), ""),
		"avatar": unified.MediaURL(member.child("avatar_urls", "origin", "urls", "0").valueOf()),
		"title":  defined(post.child("content").valueOf(), ""), "cover": unified.MediaURL(cover),
		"url": primary, "private_videos": private,
	}
	if primary == "" {
		data["type"], data["url"], data["images"] = "image", nil, pictures
	}
	return unified.Object{"code": 200, "msg": "解析成功", "data": data}
}

// The frozen public adapter hashes the upstream mapping key after the PHP
// entry wire conversion. Display index and source URL never replace that ID.
func adapt(raw unified.Object, source string, now int64) unified.Object {
	data := unified.SimpleData("zuiyou", raw, source, now)
	private := unified.Slice(raw["private_videos"])
	if len(private) == 0 {
		return data
	}
	items := unified.Array{}
	for index, value := range private {
		variants := unified.FileVariants(unified.Array{value}, now)
		if len(variants) == 0 {
			continue
		}
		id := "video-" + strconv.Itoa(index+1)
		if key, valid := unified.Text(unified.Map(value)["private_video_id"]); valid {
			digest := sha256.Sum256([]byte(key))
			id = "video-" + hex.EncodeToString(digest[:])[:24]
		}
		items = append(items, unified.Object{"id": id, "type": "video", "default": nil, "variants": variants, "resolve_url": unified.ResolveURL(source, id)})
	}
	if len(items) == 0 {
		return data
	}
	data["default_item_id"] = unified.Map(items[0])["id"]
	for _, value := range unified.Slice(data["items"]) {
		if unified.Map(value)["type"] != "video" {
			items = append(items, value)
		}
	}
	data["items"] = items
	return data
}
