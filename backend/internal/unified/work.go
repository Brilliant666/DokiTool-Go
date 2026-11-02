package unified

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RFC3986Query escapes a single query value without changing the original
// media URL passed as that value. PHP's RFC3986 http_build_query uses %20.
func RFC3986Query(value string) string { return strings.ReplaceAll(url.QueryEscape(value), "+", "%20") }

func ResolveURL(source, itemID string) string {
	return "/api/resolve?schema_version=1.0&url=" + RFC3986Query(source) + "&item_id=" + RFC3986Query(itemID)
}

func nonzeroText(value any) (string, bool) {
	text, ok := Text(value)
	return text, ok && text != "0"
}

// WorkMetadata maps existing shared work facts. It never chooses a media
// variant or infers that a missing numeric fact is a known zero.
func WorkMetadata(platform string, raw Object, sourceURL string) Object {
	data := Object{"platform": platform, "source_url": sourceURL, "items": Array{}, "refresh_url": "/api/parse?schema_version=1.0&url=" + RFC3986Query(sourceURL)}
	for _, pair := range [][2]string{{"id", "content_id"}, {"title", "title"}, {"desc", "description"}, {"description", "description"}} {
		if text, ok := nonzeroText(raw[pair[0]]); ok {
			data[pair[1]] = text
		}
	}
	if cover := Source(raw["cover"]); cover != nil {
		data["cover"] = cover
	}
	author := Map(raw["author"])
	if author == nil && Slice(raw["author"]) == nil {
		author = Object{"name": raw["author"], "avatar": raw["avatar"]}
	}
	publicAuthor := Object{}
	for _, field := range []string{"id", "name", "description"} {
		if text, ok := Text(author[field]); ok {
			publicAuthor[field] = text
		}
	}
	if avatar := Source(author["avatar"]); avatar != nil {
		publicAuthor["avatar"] = avatar
	}
	if profile := MediaURL(author["url"]); profile != "" {
		publicAuthor["url"] = profile
	}
	if len(publicAuthor) > 0 {
		data["author"] = publicAuthor
	}
	timestamp := raw["create_time"]
	if timestamp == nil {
		timestamp = raw["time"]
	}
	if number, ok := Numeric(timestamp); ok && number > 0 {
		if number > 100000000000 {
			number /= 1000
		}
		data["published_at"] = time.Unix(int64(number), 0).UTC().Format(time.RFC3339)
	}
	extra := Map(raw["extra"])
	stats := Clone(Map(extra["statistics"]))
	if stats == nil {
		stats = Object{}
	}
	if value, exists := raw["like"]; exists {
		stats["digg_count"] = value
	}
	publicStats := Object{}
	for _, pair := range [][2]string{{"play_count", "plays"}, {"view_count", "views"}, {"digg_count", "likes"}, {"comment_count", "comments"}, {"share_count", "shares"}, {"collect_count", "saves"}} {
		if number, ok := Numeric(stats[pair[0]]); ok && number >= 0 {
			integer, _ := PHPInt(stats[pair[0]])
			fact := Object{"value": integer}
			if platform == "douyin" && pair[1] == "plays" && integer == 0 {
				fact["state"] = "unverified"
			}
			publicStats[pair[1]] = fact
		}
	}
	if len(publicStats) > 0 {
		data["statistics"] = publicStats
	}
	tags, seen := Array{}, map[string]bool{}
	for _, list := range []Array{Slice(extra["hashtags"]), Slice(extra["video_tags"])} {
		for _, value := range list {
			if text, ok := Text(value); ok && !seen[text] {
				seen[text] = true
				tags = append(tags, text)
			}
		}
	}
	if len(tags) > 0 {
		data["tags"] = tags
	}
	music := Map(raw["music"])
	if text, ok := raw["music"].(string); ok {
		music = Object{"url": text}
	}
	if source := Source(music["url"]); source != nil {
		attachment := Object{"id": "background-audio", "role": "background_audio", "resource": Object{"id": "audio", "role": "audio", "delivery": Object{"kind": "file", "sources": Array{source}}}}
		title := music["title"]
		if title == nil {
			title = music["name"]
		}
		if text, ok := nonzeroText(title); ok {
			attachment["title"] = text
		}
		name := music["author"]
		if name == nil {
			name = music["artist"]
		}
		if text, ok := nonzeroText(name); ok {
			attachment["author"] = Object{"name": text}
		}
		if cover := Source(music["cover"]); cover != nil {
			attachment["cover"] = cover
		}
		data["attachments"] = Array{attachment}
	}
	return data
}

// SimpleData preserves the existing shared video/image/live-photo mapping.
// Platform extraction and explicit audio pairing happen before this function;
// FinalizeData and the public gateway own default selection and filtering.
func SimpleData(platform string, raw Object, sourceURL string, now int64) Object {
	data := WorkMetadata(platform, raw, sourceURL)
	type livePair struct {
		still   Object
		motions Array
	}
	pairs, pairOrder := map[string]livePair{}, []string{}
	for _, value := range Slice(raw["live_photo"]) {
		live := Map(value)
		if live == nil {
			continue
		}
		image := Source(live["image"])
		candidates := Slice(live["video_candidates"])
		if live["video_candidates"] == nil {
			candidates = Array{Object{"url": live["video"]}}
		}
		motions := FileVariants(candidates, now)
		if image != nil && len(motions) > 0 {
			key := Str(image["url"])
			if _, exists := pairs[key]; !exists {
				pairOrder = append(pairOrder, key)
			}
			pairs[key] = livePair{image, motions}
		}
	}
	images := append(Array{}, Slice(raw["images"])...)
	if len(images) == 0 && raw["type"] == "image" && MediaURL(raw["url"]) != "" {
		images = Array{raw["url"]}
	}
	for _, key := range pairOrder {
		images = append(images, key)
	}
	items, imageURLs := Array{}, map[string]bool{}
	for _, picture := range images {
		source := Source(picture)
		if source == nil || imageURLs[Str(source["url"])] {
			continue
		}
		imageURLs[Str(source["url"])] = true
		index := len(imageURLs)
		item := Object{"id": "image-" + strconv.Itoa(index), "type": "image", "index": index, "default": nil, "variants": Array{Object{"id": "main", "delivery": Object{"kind": "file", "sources": Array{source}}}}}
		if pair, exists := pairs[Str(source["url"])]; exists {
			item["id"], item["type"] = "live-"+strconv.Itoa(index), "live_photo"
			variants := Array{}
			for _, value := range pair.motions {
				motion := Map(value)
				resource := Object{"id": "motion", "role": "motion", "delivery": CloneValue(motion["delivery"])}
				if motion["metadata"] != nil {
					resource["metadata"] = CloneValue(motion["metadata"])
				}
				variant := Object{"id": "pair-" + Str(motion["id"]), "delivery": Object{"kind": "pair", "resources": Array{Object{"id": "image", "role": "image", "delivery": Object{"kind": "file", "sources": Array{Clone(pair.still)}}}, resource}}}
				for _, key := range []string{"metadata", "provenance", "label"} {
					if motion[key] != nil {
						variant[key] = CloneValue(motion[key])
					}
				}
				variants = append(variants, variant)
			}
			item["variants"], item["cover"] = variants, Clone(pair.still)
		}
		item["resolve_url"] = ResolveURL(sourceURL, Str(item["id"]))
		items = append(items, item)
	}
	if raw["type"] != "image" && (MediaURL(raw["url"]) != "" || len(Slice(raw["video_candidates"])) > 0) {
		candidates := Slice(raw["video_candidates"])
		if candidates == nil {
			candidates = append(Array{raw}, Slice(raw["video_backup"])...)
		}
		variants := FileVariants(candidates, now)
		if len(variants) > 0 {
			item := Object{"id": "video", "type": "video", "default": nil, "variants": variants, "resolve_url": ResolveURL(sourceURL, "video")}
			if data["cover"] != nil {
				item["cover"] = CloneValue(data["cover"])
			}
			if duration, ok := Numeric(raw["duration"]); ok && duration > 0 {
				item["duration_ms"] = int64(PHPRound(duration*1000, 0))
			}
			items = append(Array{item}, items...)
		}
	}
	data["items"] = items
	if len(items) > 1 {
		data["default_item_id"] = Map(items[0])["id"]
		album := Array{}
		for _, value := range items {
			item := Map(value)
			if item["type"] != "video" {
				album = append(album, item["id"])
			}
		}
		if len(album) > 0 {
			data["groups"] = Array{Object{"id": "album", "kind": "album", "item_ids": album, "complete": true}}
		}
	}
	return data
}
