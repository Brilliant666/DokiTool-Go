package douyin

import (
	"context"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// formatData keeps the legacy shape private so the upstream extraction can be
// compared directly with the frozen PHP oracle. Only adapt emits public data.
func (p *Parser) formatData(ctx context.Context, detail object, fallbackVideoID string) (object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	title := stringValue(pick(detail["desc"], ""))
	author, music := unified.Map(detail["author"]), unified.Map(detail["music"])
	musicCover := unified.Map(music["cover_thumb"])
	if musicCover == nil && unified.Slice(music["cover_thumb"]) == nil {
		musicCover = unified.Map(music["cover_thumb_medium"])
	}
	var duration any
	if n, ok := unified.Numeric(child(detail, "video", "duration")); ok {
		duration = unified.PHPRound(n/1000, 2)
	}
	created := normalizeCreateTime(pick(detail["create_time"], detail["createTime"], 0))
	result := object{
		"type": "unknown", "title": title, "desc": title,
		"author": object{
			"name":   stringValue(pick(author["nickname"], "")),
			"id":     stringValue(pick(author["uid"], author["unique_id"], author["short_id"], "")),
			"avatar": stringValue(pick(first(child(author, "avatar_thumb", "url_list")), "")),
		},
		"cover": "", "url": nil, "quality": "", "duration": duration, "size": 0, "size_label": "",
		"create_time": created, "publish_time": formatPublishTime(created),
		"video_backup": array{}, "images": array{}, "live_photo": array{},
		"music": object{
			"title":  stringValue(pick(music["title"], music["music_name"], "")),
			"author": stringValue(pick(music["author"], music["owner_nickname"], "")),
			"url":    toHTTPS(stringValue(pick(first(child(music, "play_url", "url_list")), ""))),
			"cover":  toHTTPS(stringValue(pick(first(musicCover["url_list"]), ""))),
		},
		"extra": detailMetadata(detail),
	}
	images := unified.Slice(detail["images"])
	if len(images) == 0 {
		images = unified.Slice(detail["image_list"])
	}
	if len(images) > 0 {
		result["type"] = "image"
		type liveCandidate struct{ image, fallback, vid string }
		candidates := []liveCandidate{}
		vids := []string{}
		for _, value := range images {
			image := unified.Map(value)
			if image == nil {
				continue
			}
			imageURL := pickImageURL(image)
			if truthy(imageURL) {
				result["images"] = append(unified.Slice(result["images"]), imageURL)
			}
			video := unified.Map(image["video"])
			motion := extractLivePhotoURL(video)
			if truthy(motion) {
				motion = toHTTPS(strings.ReplaceAll(motion, "playwm", "play"))
			}
			if truthy(imageURL) && truthy(motion) {
				vid := extractVID(video)
				candidates = append(candidates, liveCandidate{image: imageURL, fallback: motion, vid: vid})
				vids = append(vids, vid)
			}
		}
		resolved, err := p.resolveVidBatch(ctx, vids)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			motion := resolved[candidate.vid]
			if !truthy(motion) {
				motion = candidate.fallback
			}
			result["live_photo"] = append(unified.Slice(result["live_photo"]), object{"image": candidate.image, "video": ensureVideoMIME(motion)})
		}
		if len(unified.Slice(result["live_photo"])) > 0 {
			result["type"] = "live"
		}
	} else {
		result["type"] = "video"
		videoInfo, err := extractHighestQualityVideo(detail)
		if err != nil {
			return nil, err
		}
		playURI := extractVID(unified.Map(detail["video"]))
		main := videoInfo.url
		if truthy(main) {
			main = toHTTPS(strings.ReplaceAll(main, "playwm", "play"))
		}
		if fallback := buildOriginalPlayURL(playURI); fallback != "" {
			var probed object
			if p.prober != nil {
				probed, err = p.prober.Probe(ctx, fallback)
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if err != nil {
					probed = nil // An optional enhancement cannot discard known media.
				}
			}
			originalURL := stringValue(pick(probed["url"], fallback))
			originalMetadata := unified.Clone(unified.Map(probed["metadata"]))
			if originalMetadata == nil {
				originalMetadata = object{}
			}
			originalQuality := geometryLabel(originalMetadata)
			if originalQuality == "" {
				originalQuality = "unknown"
			}
			if len(originalMetadata) > 0 {
				originalMetadata["is_original"] = true
			}
			originalMetadata["private_media_identity"] = "douyin:original:" + fallback
			originalMetadata["private_source_identity"] = "douyin:original-route:" + fallback
			currentPixels, originalPixels := pixelCount(videoInfo.metadata), pixelCount(originalMetadata)
			better := originalPixels > currentPixels || originalPixels > 0 && originalPixels == currentPixels && bitrate(originalMetadata) > bitrate(videoInfo.metadata)
			if !truthy(main) || better {
				if truthy(main) && main != originalURL {
					videoInfo.backup = append(array{withURL(videoInfo.metadata, main, videoInfo.gearName, videoInfo.gearName)}, videoInfo.backup...)
				}
				main, videoInfo.metadata, videoInfo.gearName = originalURL, originalMetadata, originalQuality
			} else if originalURL != main {
				videoInfo.backup = append(videoInfo.backup, withURL(originalMetadata, originalURL, originalQuality, originalQuality))
			}
		}
		result["quality"] = videoInfo.gearName
		for key, value := range videoInfo.metadata {
			result[key] = unified.CloneValue(value)
		}
		main = ensureVideoMIME(main)
		backups, seen := array{}, map[string]bool{}
		if truthy(main) {
			key, err := candidateObservationKey(main, videoInfo.metadata)
			if err != nil {
				return nil, err
			}
			seen[key] = true
		}
		for _, value := range videoInfo.backup {
			item := unified.Clone(unified.Map(value))
			if raw, ok := value.(string); ok {
				item = object{"url": raw}
			}
			converted := ensureVideoMIME(toHTTPS(strings.ReplaceAll(stringValue(pick(item["url"], "")), "playwm", "play")))
			if !truthy(converted) {
				continue
			}
			key, err := candidateObservationKey(converted, item)
			if err != nil {
				return nil, err
			}
			if seen[key] {
				continue
			}
			item["url"] = converted
			backups = append(backups, item)
			seen[key] = true
		}
		result["url"] = main
		result["size_label"] = formatSizeLabel(result["size"])
		result["video_backup"] = backups
		result["private_audio_candidates"] = audioCandidates(unified.Slice(child(detail, "video", "bit_rate_audio")), child(detail, "video", "cdn_url_expired"))
		result["video_id"] = fallbackVideoID
		if truthy(playURI) {
			result["video_id"] = playURI
		}
	}
	result["cover"] = toHTTPS(pickCover(detail))
	return legacyWireData(result)
}

func detailMetadata(detail object) object {
	result, statistics := object{}, object{}
	for key, value := range unified.Map(detail["statistics"]) {
		if n, ok := unified.Numeric(value); strings.HasSuffix(key, "_count") && ok && n >= 0 {
			statistics[key], _ = unified.PHPInt(value)
		}
	}
	if len(statistics) > 0 {
		result["statistics"] = statistics
	}
	hashtags, tags := array{}, array{}
	seenHashtag, seenTag := map[string]bool{}, map[string]bool{}
	for _, field := range [][2]string{{"text_extra", "hashtag_name"}, {"cha_list", "cha_name"}} {
		for _, value := range unified.Slice(detail[field[0]]) {
			if text, ok := unified.Map(value)[field[1]].(string); ok {
				text = strings.Trim(text, phpWhitespace)
				if text != "" && !seenHashtag[text] {
					hashtags = append(hashtags, text)
					seenHashtag[text] = true
				}
			}
		}
	}
	for _, value := range unified.Slice(detail["video_tag"]) {
		if text, ok := unified.Map(value)["tag_name"].(string); ok {
			text = strings.Trim(text, phpWhitespace)
			if text != "" && !seenTag[text] {
				tags = append(tags, text)
				seenTag[text] = true
			}
		}
	}
	if len(hashtags) > 0 {
		result["hashtags"] = hashtags
	}
	if len(tags) > 0 {
		result["video_tags"] = tags
	}
	return result
}
