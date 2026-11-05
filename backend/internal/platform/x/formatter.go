// Adapted format-cleaning pipeline inspired by videdown and tubesync (MIT License).
package x

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

// AdaptInfo converts yt-dlp -J metadata for an X status into a unified-v1 envelope.
func AdaptInfo(info *ytengine.Info, tweetID, sourceURL string, selectedIndex int, now int64) unified.Object {
	if info == nil {
		return unified.ContractError("UPSTREAM_INVALID_RESPONSE", "解析器返回数据为空", false)
	}

	var entries []ytengine.Info
	if len(info.Entries) > 0 {
		limit := len(info.Entries)
		if limit > maxTweetVideos {
			limit = maxTweetVideos
		}
		entries = info.Entries[:limit]
	} else if len(info.Formats) > 0 {
		entries = []ytengine.Info{*info}
	}
	if len(entries) == 0 {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false)
	}

	contentID := strings.TrimSpace(tweetID)
	if contentID == "" {
		contentID = firstNonEmpty(info.DisplayID, info.ID, entries[0].DisplayID, entries[0].ID)
	}
	if contentID == "" {
		return unified.ContractError("UPSTREAM_INVALID_RESPONSE", "缺少有效的作品标识", false)
	}

	data := unified.Object{
		"platform":    "x",
		"content_id":  contentID,
		"source_url":  sourceURL,
		"refresh_url": "/api/parse?schema_version=1.0&url=" + unified.RFC3986Query(sourceURL),
		"items":       unified.Array{},
	}

	if title := firstNonEmpty(info.Title, entries[0].Title); title != "" {
		data["title"] = title
	}
	if desc := firstNonEmpty(info.Description, entries[0].Description); desc != "" {
		data["description"] = desc
	}
	if author := buildAuthor(info, &entries[0]); len(author) > 0 {
		data["author"] = author
	}
	if cover := pickBestCover(info, &entries[0]); cover != nil {
		data["cover"] = cover
	}
	if published := pickPublishedAt(info, &entries[0]); published != "" {
		data["published_at"] = published
	}
	if stats := buildStatistics(info, &entries[0]); len(stats) > 0 {
		data["statistics"] = stats
	}
	if tags := buildTags(info, &entries[0]); len(tags) > 0 {
		data["tags"] = tags
	}

	items := make(unified.Array, 0, len(entries)+1)
	usedItemIDs := make(map[string]bool, len(entries)+1)
	hasDeliverableItem := false
	selectedItemID := ""

	for idx := range entries {
		position := idx + 1
		entry := &entries[idx]
		entryKey := strings.TrimSpace(entry.ID)
		if entryKey == "" {
			entryKey = strconv.Itoa(position)
		}
		variants := buildEntryVariants(entry, contentID, entryKey, now)
		if len(variants) == 0 {
			if selectedIndex == position {
				itemID := buildItemID(entry.ID, position, usedItemIDs)
				usedItemIDs[itemID] = true
				unavail := buildUnavailableVideoItem(entry, data, itemID, position, len(entries) > 1)
				items = append(items, unavail)
				selectedItemID = itemID
			}
			continue
		}
		hasDeliverableItem = true
		itemID := buildItemID(entry.ID, position, usedItemIDs)
		usedItemIDs[itemID] = true

		item := unified.Object{
			"id":          itemID,
			"type":        "video",
			"index":       position,
			"default":     nil,
			"variants":    variants,
			"resolve_url": unified.ResolveURL(sourceURL, itemID),
		}
		if len(entries) > 1 {
			if title := strings.TrimSpace(entry.Title); title != "" {
				item["title"] = title
			}
		}
		if cover := pickEntryCover(entry); cover != nil {
			item["cover"] = cover
		} else if data["cover"] != nil {
			item["cover"] = unified.CloneValue(data["cover"])
		}
		if entry.Duration != nil && *entry.Duration > 0 {
			durMS := int64(unified.PHPRound(*entry.Duration*1000, 0))
			if durMS > 0 {
				item["duration_ms"] = durMS
			}
		}
		items = append(items, item)
		if selectedIndex == position {
			selectedItemID = itemID
		}
	}

	if !hasDeliverableItem {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false)
	}
	if selectedIndex > len(entries) {
		itemID := buildItemID("", selectedIndex, usedItemIDs)
		usedItemIDs[itemID] = true
		items = append(items, buildUnavailableVideoItem(nil, data, itemID, selectedIndex, true))
		selectedItemID = itemID
	}
	if selectedItemID == "" {
		selectedItemID = unified.Str(unified.Map(items[0])["id"])
	}
	data["items"] = items
	data["default_item_id"] = selectedItemID
	return unified.FinalizeData(data, now)
}

func buildUnavailableVideoItem(entry *ytengine.Info, data unified.Object, itemID string, position int, multiEntry bool) unified.Object {
	item := unified.Object{
		"id":                         itemID,
		"type":                       "video",
		"index":                      position,
		"default":                    nil,
		"state":                      "unavailable",
		"default_unavailable_reason": "unavailable",
		"error": unified.Object{
			"code":    "CONTENT_UNAVAILABLE",
			"message": fmt.Sprintf("所选第 %d 个视频不存在或不可用", position),
		},
	}
	if entry != nil {
		if multiEntry {
			if title := strings.TrimSpace(entry.Title); title != "" {
				item["title"] = title
			}
		}
		if cover := pickEntryCover(entry); cover != nil {
			item["cover"] = cover
		} else if data["cover"] != nil {
			item["cover"] = unified.CloneValue(data["cover"])
		}
		if entry.Duration != nil && *entry.Duration > 0 {
			durMS := int64(unified.PHPRound(*entry.Duration*1000, 0))
			if durMS > 0 {
				item["duration_ms"] = durMS
			}
		}
	}
	return item
}

func buildItemID(upstreamID string, position int, used map[string]bool) string {
	trimmed := strings.TrimSpace(upstreamID)
	if trimmed != "" {
		sum := sha256.Sum256([]byte(trimmed))
		candidate := "video-" + hex.EncodeToString(sum[:])[:24]
		if !used[candidate] {
			return candidate
		}
	}
	return "video-" + strconv.Itoa(position)
}

func buildEntryVariants(entry *ytengine.Info, contentID, entryKey string, now int64) unified.Array {
	var mp4Formats []ytengine.Format
	var hlsVideoFormats []ytengine.Format
	var hlsAudioFormats []ytengine.Format

	for _, f := range entry.Formats {
		cleanURL := sanitizeXMediaURL(f.URL)
		if cleanURL == "" {
			continue
		}
		f.URL = cleanURL
		if isHLSAudioOnly(f) {
			hlsAudioFormats = append(hlsAudioFormats, f)
			continue
		}
		if isProgressiveMP4(f) {
			mp4Formats = append(mp4Formats, f)
			continue
		}
		if isHLSVideo(f) {
			hlsVideoFormats = append(hlsVideoFormats, f)
			continue
		}
	}

	sortFormats(mp4Formats)
	sortFormats(hlsVideoFormats)
	sortAudioFormats(hlsAudioFormats)

	var bestAudio *ytengine.Format
	if len(hlsAudioFormats) > 0 {
		bestAudio = &hlsAudioFormats[0]
	}

	variants := make(unified.Array, 0, len(mp4Formats)+len(hlsVideoFormats))
	seenVariantIDs := make(map[string]bool)

	// 1. Deliverable progressive MP4s (kind == "file")
	dedupedMP4 := deduplicateVideoFormats(mp4Formats)
	for i, f := range dedupedMP4 {
		formatKey := nonEmptyOr(f.FormatID, fmt.Sprintf("mp4-%d", i+1))
		sources := buildFormatSources(f, contentID, entryKey, formatKey, now)
		if len(sources) == 0 {
			continue
		}
		hasAudio := !isNoCodec(f.ACodec)
		metadata := buildFormatMetadata(f, hasAudio, true)
		variantID := "v-" + shortDigest("x:file:"+entryKey+":"+formatKey)
		if seenVariantIDs[variantID] {
			continue
		}
		seenVariantIDs[variantID] = true

		variant := unified.Object{
			"id":       variantID,
			"metadata": metadata,
			"delivery": unified.Object{
				"kind":    "file",
				"sources": sources,
			},
		}
		if label := qualityLabel(f, false); label != "" {
			variant["label"] = label
		}
		variants = append(variants, variant)
	}

	// 2. Non-single-file HLS variants (kind == "hls", scope == "adaptive", marked as requiring hls/mux processing)
	dedupedHLS := deduplicateVideoFormats(hlsVideoFormats)
	for i, f := range dedupedHLS {
		formatKey := nonEmptyOr(f.FormatID, fmt.Sprintf("hls-%d", i+1))
		sources := buildFormatSources(f, contentID, entryKey, formatKey, now)
		if len(sources) == 0 {
			continue
		}
		hasEmbeddedAudio := !isNoCodec(f.ACodec) && strings.TrimSpace(f.ACodec) != ""
		metadata := buildFormatMetadata(f, hasEmbeddedAudio, false)
		variantID := "v-" + shortDigest("x:hls:"+entryKey+":"+formatKey)
		if seenVariantIDs[variantID] {
			continue
		}
		seenVariantIDs[variantID] = true

		selection := unified.Object{
			"video_representation_id": formatKey,
		}
		if !hasEmbeddedAudio && bestAudio != nil && strings.TrimSpace(bestAudio.FormatID) != "" {
			selection["audio_representation_id"] = strings.TrimSpace(bestAudio.FormatID)
		}
		variant := unified.Object{
			"id":       variantID,
			"label":    qualityLabel(f, true),
			"metadata": metadata,
			"delivery": unified.Object{
				"kind":      "hls",
				"scope":     "adaptive",
				"selection": selection,
				"sources":   sources,
				"output": unified.Object{
					"container": "mp4",
					"extension": "mp4",
					"mime_type": "video/mp4",
				},
			},
		}
		variants = append(variants, variant)
	}

	return variants
}

func buildFormatSources(f ytengine.Format, contentID, entryKey, formatKey string, now int64) unified.Array {
	rawSource := unified.Object{
		"url": f.URL,
	}
	var sizeVal int64
	// Strictly require exact Filesize; never populate size_bytes from FilesizeApprox.
	if f.Filesize != nil && *f.Filesize > 0 {
		rawSource["size_bytes"] = *f.Filesize
		sizeVal = *f.Filesize
	}
	if id := buildXSourceIdentity(f, contentID, entryKey, formatKey, sizeVal); id != "" {
		rawSource["private_source_identity"] = id
	}
	return unified.Sources(unified.Array{rawSource}, now)
}

func buildXSourceIdentity(f ytengine.Format, contentID, entryKey, formatKey string, sizeBytes int64) string {
	if contentID == "" || entryKey == "" || f.URL == "" {
		return ""
	}
	var w, h int64
	if f.Width != nil && *f.Width > 0 {
		w = *f.Width
	}
	if f.Height != nil && *f.Height > 0 {
		h = *f.Height
	}
	return unified.PHPIdentityJSON(unified.Array{
		"x-source",
		contentID,
		entryKey,
		formatKey,
		strings.ToLower(strings.TrimSpace(f.Ext)),
		strings.TrimSpace(f.VCodec),
		strings.TrimSpace(f.ACodec),
		w,
		h,
		sizeBytes,
		unified.SourceIdentity(f.URL),
	}, true)
}

func buildFormatMetadata(f ytengine.Format, hasAudio bool, isMP4File bool) unified.Object {
	metadata := unified.Object{
		"has_audio": hasAudio,
		"evidence":  "upstream",
	}
	if f.Width != nil && *f.Width > 0 && f.Height != nil && *f.Height > 0 {
		metadata["width"] = *f.Width
		metadata["height"] = *f.Height
	}
	if f.FPS != nil && *f.FPS > 0 {
		metadata["fps"] = *f.FPS
	}
	if hdr, ok := unified.HDRType(f.DynamicRange); ok {
		metadata["hdr_type"] = hdr
	}
	if f.TBR != nil && *f.TBR > 0 {
		bps := int64(unified.PHPRound(*f.TBR*1000, 0))
		if bps > 0 {
			metadata["bitrate_bps"] = bps
		}
	}
	if f.VBR != nil && *f.VBR > 0 {
		bps := int64(unified.PHPRound(*f.VBR*1000, 0))
		if bps > 0 {
			metadata["video_bitrate_bps"] = bps
		}
	}
	if hasAudio && f.ABR != nil && *f.ABR > 0 {
		bps := int64(unified.PHPRound(*f.ABR*1000, 0))
		if bps > 0 {
			metadata["audio_bitrate_bps"] = bps
		}
	}
	if vcodec := strings.TrimSpace(f.VCodec); vcodec != "" && !isNoCodec(vcodec) {
		metadata["video_codec"] = vcodec
	}
	if hasAudio {
		if acodec := strings.TrimSpace(f.ACodec); acodec != "" && !isNoCodec(acodec) {
			metadata["audio_codec"] = acodec
		}
		if f.AudioChannels != nil && *f.AudioChannels > 0 {
			metadata["audio_channels"] = *f.AudioChannels
		}
		if f.ASR != nil && *f.ASR > 0 {
			metadata["sample_rate_hz"] = *f.ASR
		}
	}
	if isMP4File {
		metadata["container"] = "mp4"
		metadata["extension"] = "mp4"
		metadata["mime_type"] = "video/mp4"
	}
	return metadata
}

func isNoCodec(codec string) bool {
	return strings.EqualFold(strings.TrimSpace(codec), "none")
}

func isHLSAudioOnly(f ytengine.Format) bool {
	if !isNoCodec(f.VCodec) {
		return false
	}
	if isNoCodec(f.ACodec) || strings.TrimSpace(f.ACodec) == "" {
		return false
	}
	return isHLSProtocolOrExt(f)
}

func isProgressiveMP4(f ytengine.Format) bool {
	if isNoCodec(f.VCodec) || isHLSProtocolOrExt(f) {
		return false
	}
	proto := strings.ToLower(strings.TrimSpace(f.Protocol))
	if proto != "" && proto != "https" && proto != "http" {
		return false
	}
	ext := strings.ToLower(strings.TrimSpace(firstNonEmpty(f.Ext, f.VideoExt)))
	if ext != "mp4" {
		return false
	}
	u, err := url.Parse(f.URL)
	if err != nil {
		return false
	}
	urlExt := strings.ToLower(path.Ext(u.Path))
	return urlExt != ".m3u8" && urlExt != ".mpd"
}

func isHLSVideo(f ytengine.Format) bool {
	if isNoCodec(f.VCodec) {
		return false
	}
	return isHLSProtocolOrExt(f)
}

func isHLSProtocolOrExt(f ytengine.Format) bool {
	proto := strings.ToLower(strings.TrimSpace(f.Protocol))
	if strings.Contains(proto, "m3u8") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(f.Ext), "m3u8") {
		return true
	}
	u, err := url.Parse(f.URL)
	if err != nil {
		return false
	}
	return strings.EqualFold(path.Ext(u.Path), ".m3u8")
}

func sortFormats(formats []ytengine.Format) {
	sort.SliceStable(formats, func(i, j int) bool {
		a, b := formats[i], formats[j]
		aShort, aArea := formatDimensions(a)
		bShort, bArea := formatDimensions(b)
		if aShort != bShort {
			return aShort > bShort
		}
		if aArea != bArea {
			return aArea > bArea
		}
		aAudio := !isNoCodec(a.ACodec)
		bAudio := !isNoCodec(b.ACodec)
		if aAudio != bAudio {
			return aAudio
		}
		aRate := effectiveBitrateKbps(a)
		bRate := effectiveBitrateKbps(b)
		if aRate != bRate {
			return aRate > bRate
		}
		return a.FormatID < b.FormatID
	})
}

func sortAudioFormats(formats []ytengine.Format) {
	sort.SliceStable(formats, func(i, j int) bool {
		aRate := effectiveAudioBitrateKbps(formats[i])
		bRate := effectiveAudioBitrateKbps(formats[j])
		if aRate != bRate {
			return aRate > bRate
		}
		return formats[i].FormatID < formats[j].FormatID
	})
}

func deduplicateVideoFormats(sorted []ytengine.Format) []ytengine.Format {
	out := make([]ytengine.Format, 0, len(sorted))
	seen := make(map[string]bool, len(sorted))
	for _, f := range sorted {
		var key string
		if f.Width != nil && *f.Width > 0 && f.Height != nil && *f.Height > 0 {
			key = fmt.Sprintf("%dx%d:%t", *f.Width, *f.Height, !isNoCodec(f.ACodec))
		} else {
			key = "id:" + nonEmptyOr(f.FormatID, f.URL)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

func formatDimensions(f ytengine.Format) (int64, int64) {
	if f.Width == nil || f.Height == nil || *f.Width <= 0 || *f.Height <= 0 {
		return 0, 0
	}
	w, h := *f.Width, *f.Height
	shortSide := w
	if h < shortSide {
		shortSide = h
	}
	return shortSide, w * h
}

func effectiveBitrateKbps(f ytengine.Format) float64 {
	if f.TBR != nil && *f.TBR > 0 {
		return *f.TBR
	}
	if f.VBR != nil && *f.VBR > 0 {
		return *f.VBR
	}
	return 0
}

func effectiveAudioBitrateKbps(f ytengine.Format) float64 {
	if f.ABR != nil && *f.ABR > 0 {
		return *f.ABR
	}
	if f.TBR != nil && *f.TBR > 0 {
		return *f.TBR
	}
	return 0
}

func qualityLabel(f ytengine.Format, isHLS bool) string {
	shortSide, _ := formatDimensions(f)
	base := ""
	if shortSide > 0 {
		base = strconv.FormatInt(shortSide, 10) + "p"
	}
	if isHLS {
		if base != "" {
			return base + " (HLS 需合流)"
		}
		return "HLS 需合流"
	}
	return base
}

func sanitizeXMediaURL(raw string) string {
	cleaned := unified.MediaURL(raw)
	if cleaned == "" {
		return ""
	}
	u, err := url.Parse(cleaned)
	if err != nil || u.User != nil || u.Opaque != "" {
		return ""
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "twimg.com" && !strings.HasSuffix(host, ".twimg.com") {
		return ""
	}
	return cleaned
}

func buildAuthor(primary, fallback *ytengine.Info) unified.Object {
	author := unified.Object{}
	if id := firstNonEmpty(primary.UploaderID, fallback.UploaderID); id != "" {
		author["id"] = id
	}
	if name := firstNonEmpty(primary.Uploader, fallback.Uploader); name != "" {
		author["name"] = name
	}
	rawURL := firstNonEmpty(primary.UploaderURL, fallback.UploaderURL)
	if profile := sanitizeAuthorURL(rawURL); profile != "" {
		author["url"] = profile
	} else if id, ok := author["id"].(string); ok && screenNamePattern.MatchString(id) {
		author["url"] = "https://x.com/" + id
	}
	return author
}

func sanitizeAuthorURL(raw string) string {
	cleaned := unified.MediaURL(raw)
	if cleaned == "" {
		return ""
	}
	u, err := url.Parse(cleaned)
	if err != nil || u.User != nil || !strings.EqualFold(u.Scheme, "https") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "x.com", "www.x.com", "twitter.com", "www.twitter.com":
		return cleaned
	default:
		return ""
	}
}

func pickBestCover(primary, fallback *ytengine.Info) unified.Object {
	if cover := pickEntryCover(primary); cover != nil {
		return cover
	}
	return pickEntryCover(fallback)
}

func pickEntryCover(info *ytengine.Info) unified.Object {
	if info == nil {
		return nil
	}
	if urlStr := sanitizeXMediaURL(info.Thumbnail); urlStr != "" {
		if src := unified.Source(urlStr); src != nil {
			return src
		}
	}
	var bestURL string
	var bestArea int64
	for _, t := range info.Thumbnails {
		urlStr := sanitizeXMediaURL(t.URL)
		if urlStr == "" {
			continue
		}
		area := t.Width * t.Height
		if bestURL == "" || area >= bestArea {
			bestURL = urlStr
			bestArea = area
		}
	}
	if bestURL != "" {
		return unified.Source(bestURL)
	}
	return nil
}

func pickPublishedAt(primary, fallback *ytengine.Info) string {
	for _, candidate := range []*ytengine.Info{primary, fallback} {
		if candidate != nil && candidate.Timestamp != nil && *candidate.Timestamp > 0 {
			sec := int64(*candidate.Timestamp)
			if sec > 100000000000 {
				sec /= 1000
			}
			if sec > 0 {
				return time.Unix(sec, 0).UTC().Format(time.RFC3339)
			}
		}
	}
	return ""
}

func buildStatistics(primary, fallback *ytengine.Info) unified.Object {
	stats := unified.Object{}
	setStat := func(key string, a, b *int64) {
		val := a
		if val == nil {
			val = b
		}
		if val != nil && *val >= 0 {
			stats[key] = unified.Object{"value": *val}
		}
	}
	setStat("views", primary.ViewCount, fallback.ViewCount)
	setStat("likes", primary.LikeCount, fallback.LikeCount)
	setStat("shares", primary.RepostCount, fallback.RepostCount)
	setStat("comments", primary.CommentCount, fallback.CommentCount)
	return stats
}

func buildTags(primary, fallback *ytengine.Info) unified.Array {
	tags := unified.Array{}
	seen := make(map[string]bool)
	for _, list := range [][]string{primary.Tags, fallback.Tags} {
		for _, raw := range list {
			trimmed := strings.TrimSpace(raw)
			if trimmed != "" && !seen[trimmed] {
				seen[trimmed] = true
				tags = append(tags, trimmed)
			}
		}
	}
	return tags
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func nonEmptyOr(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}

func shortDigest(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:24]
}
