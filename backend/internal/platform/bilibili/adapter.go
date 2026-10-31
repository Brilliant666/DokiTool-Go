package bilibili

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func adapt(raw object, sourceURL string, now int64) object {
	raw = unified.Clone(raw)
	raw["author"] = pick(raw["user"], object{})
	data := unified.WorkMetadata("bilibili", raw, sourceURL)
	bvid := unified.Str(child(raw, "extra", "bvid"))
	current := intValue(pick(child(raw, "extra", "current_page"), 1))
	data["content_id"] = bvid
	items, partIDs := array{}, array{}
	known := map[string]bool{}
	for i, value := range unified.Slice(child(raw, "extra", "pages")) {
		page := unified.Map(value)
		number := intValue(pick(page["page"], i+1))
		id := bvid + "-p" + strconv.FormatInt(number, 10)
		pageURL := "https://www.bilibili.com/video/" + unified.RFC3986Query(bvid) + "/?p=" + strconv.FormatInt(number, 10)
		item := object{"id": id, "type": "video", "index": number, "default": nil, "source_url": pageURL, "resolve_url": unified.ResolveURL(pageURL, id)}
		if title, ok := unified.Text(page["part"]); ok && title != "0" {
			item["title"] = title
		}
		if duration, ok := unified.Numeric(page["duration"]); ok && duration > 0 {
			item["duration_ms"] = int64(unified.PHPRound(duration*1000, 0))
		}
		if number == current {
			variants := variants(raw, now)
			if len(variants) == 0 {
				delete(item, "resolve_url")
				item["state"] = "unavailable"
				item["error"] = object{"code": "CONTENT_UNAVAILABLE", "message": "当前分P未提供完整媒体描述"}
			} else {
				item["variants"] = variants
			}
		} else {
			item["state"] = "deferred"
		}
		items = append(items, item)
		partIDs = append(partIDs, id)
		known[id] = true
	}
	data["default_item_id"] = bvid + "-p" + strconv.FormatInt(current, 10)
	groups := array{object{"id": "parts", "kind": "multipart", "item_ids": partIDs, "complete": true, "total_count": len(items)}}
	for sectionIndex, value := range unified.Slice(child(raw, "private_collection", "sections")) {
		section := unified.Map(value)
		members, seen := array{}, map[string]bool{}
		for _, value := range unified.Slice(section["episodes"]) {
			episode := unified.Map(value)
			episodeID := unified.Str(pick(episode["bvid"], child(episode, "arc", "bvid")))
			if !videoID.MatchString(episodeID) {
				continue
			}
			id := episodeID + "-p1"
			if !seen[id] {
				members = append(members, id)
				seen[id] = true
			}
			if known[id] {
				continue
			}
			known[id] = true
			pageURL := "https://www.bilibili.com/video/" + episodeID + "/?p=1"
			item := object{"id": id, "type": "video", "state": "deferred", "default": nil, "source_url": pageURL, "resolve_url": unified.ResolveURL(pageURL, id)}
			if title, ok := unified.Text(pick(episode["title"], child(episode, "arc", "title"))); ok && title != "0" {
				item["title"] = title
			}
			if cover := unified.Source(child(episode, "arc", "pic")); cover != nil {
				item["cover"] = cover
			}
			items = append(items, item)
		}
		if len(members) > 0 {
			group := object{"id": "collection-" + strconv.Itoa(sectionIndex), "kind": "collection", "item_ids": members, "complete": false}
			if title, ok := unified.Text(section["title"]); ok && title != "0" {
				group["title"] = title
			}
			groups = append(groups, group)
		}
	}
	data["items"], data["groups"] = items, groups
	return data
}

func variants(raw object, now int64) array {
	if raw["private_error"] != nil {
		return array{}
	}
	play := unified.Map(raw["private_play"])
	if len(play) == 0 {
		sources := append(array{object{"url": raw["url"]}}, unified.Slice(raw["video_backup"])...)
		return unified.FileVariants(array{object{"url": raw["url"], "sources": sources, "label": pick(raw["quality"], "规格未知")}}, now)
	}
	result := array{}
	durl := unified.Slice(play["durl"])
	if len(durl) == 1 {
		part := unified.Map(durl[0])
		candidate := object{"sources": partSources(part), "label": pick(raw["quality"], "规格未知")}
		// PHP array union keeps the first fact: part before play, video before audio.
		union(candidate, unified.VideoFacts(part), unified.VideoFacts(play), unified.AudioFacts(part), unified.AudioFacts(play))
		result = unified.FileVariants(array{candidate}, now)
	} else if len(durl) > 1 {
		pieces := array{}
		for i, value := range durl {
			part := unified.Map(value)
			piece := object{"index": i + 1, "sources": unified.Sources(partSources(part), now)}
			if length, ok := unified.Numeric(part["length"]); ok && length > 0 {
				piece["duration_ms"] = intValue(part["length"])
			}
			pieces = append(pieces, piece)
		}
		result = append(result, object{"id": "segments", "delivery": object{"kind": "segments", "pieces": pieces, "join": "remux", "output": output()}})
	}
	audioOptions := array{}
	for _, value := range unified.Slice(child(play, "dash", "audio")) {
		if resource := track(unified.Map(value), "audio", now); resource != nil {
			audioOptions = append(audioOptions, resource)
		}
	}
	for _, value := range unified.Slice(child(play, "dash", "video")) {
		video := track(unified.Map(value), "video", now)
		if video == nil {
			continue
		}
		variantID := "mux-" + digest(unified.PHPIdentityJSON(unified.ResourceIdentity(video), false))
		if len(audioOptions) == 0 {
			result = append(result, object{"id": variantID, "state": "deferred", "reason": object{"code": "MISSING_AUDIO_TRACK", "message": "未取得可确认的完整音视频组合，暂不可下载"}})
			continue
		}
		for _, value := range audioOptions {
			audio := unified.Map(value)
			id := variantID + "-" + digest(unified.PHPIdentityJSON(unified.ResourceIdentity(audio), false))
			variant := object{"id": id, "delivery": object{"kind": "mux", "resources": array{unified.Clone(video), unified.Clone(audio)}, "output": output()}}
			if video["metadata"] != nil {
				variant["metadata"] = unified.CloneValue(video["metadata"])
			}
			result = append(result, variant)
		}
	}
	return result
}

func partSources(part object) array {
	return append(array{object{"url": part["url"], "size": part["size"]}}, unified.Slice(part["backup_url"])...)
}

func track(raw object, role string, now int64) object {
	sourceInputs := append(array{pick(raw["baseUrl"], raw["base_url"], "")}, unified.Slice(pick(raw["backupUrl"], raw["backup_url"]))...)
	sources := unified.Sources(sourceInputs, now)
	if len(sources) == 0 {
		return nil
	}
	resource := object{"id": role, "role": role, "delivery": object{"kind": "file", "sources": sources}}
	metadata := object{"evidence": "upstream"}
	if role == "video" {
		union(metadata, unified.VideoFacts(raw))
		if metadata["hdr_type"] == nil && (intValue(raw["id"]) == 125 || intValue(raw["id"]) == 126) {
			metadata["hdr_type"] = "hdr_unknown"
			if intValue(raw["id"]) == 126 {
				metadata["hdr_type"] = "dolby_vision"
			}
		}
		for _, field := range []string{"width", "height"} {
			if n, ok := unified.Numeric(raw[field]); ok && n > 0 {
				metadata[field] = intValue(raw[field])
			}
		}
		if codec, ok := unified.Text(raw["codecs"]); ok && codec != "0" {
			metadata["video_codec"] = codec
		}
		if bandwidth, ok := unified.Numeric(raw["bandwidth"]); ok && bandwidth > 0 {
			metadata["video_bitrate_bps"] = intValue(raw["bandwidth"])
		}
	} else {
		union(metadata, unified.AudioFacts(raw))
		if codec, ok := unified.Text(raw["codecs"]); ok && codec != "0" {
			metadata["audio_codec"] = codec
		}
		if bandwidth, ok := unified.Numeric(raw["bandwidth"]); ok && bandwidth > 0 {
			metadata["audio_bitrate_bps"] = intValue(raw["bandwidth"])
		}
	}
	if len(metadata) > 1 {
		resource["metadata"] = metadata
	}
	return resource
}

func output() object { return object{"container": "mp4", "extension": "mp4", "mime_type": "video/mp4"} }
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:24]
}
func union(target object, values ...object) {
	for _, value := range values {
		for field, fact := range value {
			if _, exists := target[field]; !exists {
				target[field] = unified.CloneValue(fact)
			}
		}
	}
}
