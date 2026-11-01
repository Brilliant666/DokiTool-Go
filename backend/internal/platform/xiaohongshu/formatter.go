package xiaohongshu

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var stateScript = regexp.MustCompile(`(?is)window\.__INITIAL_STATE__\s*=\s*(\{.*?\})\s*;?\s*</script>`)
var inertValue = regexp.MustCompile(`"(?:\\.|[^"\\])*"|\bundefined\b|\bnew (?:Set|Map)\(\[\]\)`)
var originalKey = regexp.MustCompile(`^[A-Za-z0-9_/-]+$`)

func extractNote(html, id string) *jsonNode {
	match := stateScript.FindStringSubmatch(html)
	if match == nil {
		return nil
	}
	clean := inertValue.ReplaceAllStringFunc(match[1], func(value string) string {
		switch value {
		case "undefined":
			return "null"
		case "new Set([])":
			return "[]"
		case "new Map([])":
			return "{}"
		}
		return value
	})
	root, err := decodeNode(clean)
	if err != nil {
		return nil
	}
	note := root.field("note", "noteDetailMap", id, "note")
	if note.val() == nil {
		note = root.field("noteData", "data", "noteData")
	}
	if !note.array() || len(note.values()) == 0 {
		return nil
	}
	if declared := note.field("noteId").val(); declared != nil && text(declared) != id {
		return nil
	}
	return note
}

func streamCandidate(stream *jsonNode, codec string) unified.Object {
	address := unified.MediaURL(first(stream.field("masterUrl").val(), stream.field("master_url").val(), stream.field("url").val(), ""))
	if address == "" {
		return nil
	}
	candidate := unified.Object{"url": address, "metadata_source": "upstream", "sources": unified.Array{unified.Object{"url": address}}}
	for _, group := range [][]string{{"width", "width"}, {"height", "height"}, {"bit_rate", "avgBitrate", "videoBitrate", "video_bitrate", "avg_bitrate", "bitrate"}, {"size", "size", "sizeBytes", "size_bytes"}} {
		for _, field := range group[1:] {
			value := stream.field(field).val()
			if number, valid := unified.Numeric(value); valid && number > 0 {
				candidate[group[0]], _ = unified.PHPInt(value)
				break
			}
		}
	}
	if duration, valid := unified.Numeric(stream.field("duration").val()); valid && duration > 0 {
		candidate["duration"] = duration / 1000
	}
	codec = strings.ToLower(text(first(stream.field("videoCodec").val(), stream.field("video_codec").val(), codec)))
	switch codec {
	case "h265", "hevc":
		candidate["codec"] = "hevc"
	case "h264", "avc", "avc1":
		candidate["codec"] = "h264"
	case "av1", "h266", "vvc":
		candidate["codec"] = codec
	}
	backups := stream.field("backupUrls")
	if backups.val() == nil {
		backups = stream.field("backup_urls")
	}
	seen := map[string]bool{address: true}
	for _, backup := range backups.values() {
		address := unified.MediaURL(backup.val())
		if address != "" && !seen[address] {
			seen[address] = true
			candidate["sources"] = append(unified.Slice(candidate["sources"]), unified.Object{"url": address})
		}
	}
	return candidate
}

func decodedObject(value *jsonNode) *jsonNode {
	if raw, ok := value.val().(string); ok {
		decoded, _ := decodeNode(raw)
		return decoded
	}
	return value
}

func streamCandidates(streams *jsonNode) unified.Array {
	candidates := unified.Array{}
	for index, group := range streams.values() {
		codec := strconv.Itoa(index)
		if streams.fields != nil {
			codec = streams.keys[index]
		}
		if !group.array() {
			continue
		}
		for _, stream := range group.values() {
			if stream.array() {
				if candidate := streamCandidate(stream, codec); candidate != nil {
					candidates = append(candidates, candidate)
				}
			}
		}
	}
	return candidates
}

func (p *Parser) videoCandidates(parent context.Context, video *jsonNode) (unified.Array, error) {
	candidates := streamCandidates(video.field("media", "stream"))
	opaque := decodedObject(decodedObject(video.field("mediaV2")).field("opaque1"))
	for _, key := range []string{"hd_screencast_stream", "default_screencast_stream"} {
		stream := decodedObject(opaque.field(key))
		if stream.array() {
			if candidate := streamCandidate(stream, ""); candidate != nil {
				candidates = append(candidates, candidate)
			}
		}
	}
	key, validKey := video.field("consumer", "originVideoKey").val().(string)
	if validKey && originalKey.MatchString(key) && !strings.Contains(key, "..") && !strings.HasPrefix(key, "/") {
		original := unified.Object{"is_original": true, "label": "原始入口", "sources": unified.Array{}}
		ctx, cancel := context.WithTimeout(parent, 10*time.Second)
		defer cancel()
		deadline := p.clock().Add(10 * time.Second)
		measured := false
		for _, cdn := range []string{"qn", "al", "qc", "bd"} {
			source := unified.Object{"url": "https://sns-video-" + cdn + ".xhscdn.com/" + key}
			if !measured && ctx.Err() == nil && p.clock().Before(deadline) {
				observed, _ := p.inspect(ctx, unified.Str(source["url"]))
				if err := parent.Err(); err != nil {
					return nil, err
				}
				metadata := unified.Map(unified.Map(observed["result"])["metadata"])
				if metadata["width"] != nil && metadata["height"] != nil {
					for field, value := range metadata {
						original[field] = value
					}
					source["availability"] = "available"
					original["url"] = source["url"]
					measured = true
				} else if status, _ := unified.PHPInt(observed["http_status"]); status == 403 || status == 404 || status == 410 {
					source["availability"] = "unavailable"
				}
			}
			original["sources"] = append(unified.Slice(original["sources"]), source)
		}
		if original["url"] == nil {
			original["url"] = unified.Map(unified.Slice(original["sources"])[0])["url"]
		}
		candidates = append(candidates, original)
	}
	return mergeCandidates(candidates), nil
}

func mergeCandidates(candidates unified.Array) unified.Array {
	result := unified.Array{}
	positions := map[string]int{}
	for _, raw := range candidates {
		candidate := unified.Map(raw)
		key := unified.Str(candidate["url"])
		position, exists := positions[key]
		if !exists {
			positions[key] = len(result)
			result = append(result, candidate)
			continue
		}
		previous := unified.Map(result[position])
		merged := unified.Clone(previous)
		for field, value := range candidate {
			if _, present := merged[field]; !present || candidate["metadata_source"] == "mp4" {
				merged[field] = value
			}
		}
		sources, seen := unified.Array{}, map[string]int{}
		for _, entry := range append(append(unified.Array{}, unified.Slice(previous["sources"])...), unified.Slice(candidate["sources"])...) {
			source := unified.Map(entry)
			url := unified.Str(source["url"])
			if index, exists := seen[url]; exists {
				prior := unified.Map(sources[index])
				for field, value := range source {
					if _, present := prior[field]; !present {
						prior[field] = value
					}
				}
			} else {
				seen[url] = len(sources)
				sources = append(sources, unified.Clone(source))
			}
		}
		merged["sources"] = sources
		result[position] = merged
	}
	return result
}

func (p *Parser) formatNote(ctx context.Context, note *jsonNode) (unified.Object, error) {
	kind := "image"
	if note.field("type").val() == "video" {
		kind = "video"
	}
	imageList := note.field("imageList")
	images := imageList.values()
	var initial *jsonNode
	if imageList != nil && imageList.fields != nil {
		initial = imageList.field("0")
	} else if len(images) != 0 {
		initial = images[0]
	}
	cover := first(initial.field("urlPre").val(), initial.field("urlDefault").val(), initial.field("url").val(), note.field("cover", "url").val(), "")
	if cover == "" {
		if value := text(note.field("video", "image", "thumbnailFileid").val()); value != "" && value != "0" {
			cover = "https://sns-img-hw.xhscdn.com/" + value
		}
	}
	if cover == "" {
		if value := text(note.field("cover", "fileId").val()); value != "" && value != "0" {
			cover = "https://sns-img-hw.xhscdn.com/" + value
		}
	}
	candidates, err := p.videoCandidates(ctx, note.field("video"))
	if err != nil {
		return nil, err
	}
	result := unified.Object{"type": kind, "title": first(note.field("title").val(), ""), "desc": first(note.field("desc").val(), ""),
		"author": unified.Object{"name": first(note.field("user", "nickname").val(), note.field("user", "nickName").val(), ""), "id": first(note.field("user", "userId").val(), ""), "avatar": first(note.field("user", "avatar").val(), "")},
		"cover":  processImageURL(cover), "images": unified.Array{}, "live_photo": unified.Array{}, "video_candidates": candidates}
	if id := note.field("noteId").val(); id != nil {
		result["id"] = text(id)
	}
	if created := note.field("time").val(); created != nil {
		if _, valid := unified.Numeric(created); valid {
			result["create_time"] = created
		}
	}
	statistics := unified.Object{}
	for _, pair := range [][2]string{{"likedCount", "like_count"}, {"commentCount", "comment_count"}, {"shareCount", "share_count"}, {"collectedCount", "collect_count"}} {
		value := note.field("interactInfo", pair[0]).val()
		if _, valid := unified.Numeric(value); valid {
			statistics[pair[1]], _ = unified.PHPInt(value)
		}
	}
	if len(statistics) != 0 {
		result["statistics"] = statistics
	}
	if kind != "video" {
		for _, image := range images {
			address := processImageURL(first(image.field("url").val(), image.field("urlDefault").val(), image.field("urlPre").val(), ""))
			if address == "" {
				continue
			}
			result["images"] = append(unified.Slice(result["images"]), address)
			live := mergeCandidates(streamCandidates(image.field("stream")))
			if len(live) != 0 {
				result["live_photo"] = append(unified.Slice(result["live_photo"]), unified.Object{"image": address, "video": unified.Map(live[0])["url"], "video_candidates": live})
			}
		}
	}
	if len(unified.Slice(result["live_photo"])) != 0 {
		result["type"] = "live"
	}
	return result, nil
}

var imageOSSSG = regexp.MustCompile(`/oss-sg/([a-zA-Z0-9_]+)/([a-zA-Z0-9]+)!`)
var imageDirectory = regexp.MustCompile(`/([a-zA-Z0-9_]+)/([a-zA-Z0-9]+)!`)
var imageNamed = regexp.MustCompile(`(notes_pre_post|spectrum|notes_uhdr)/([a-zA-Z0-9]+)`)
var imageOther = regexp.MustCompile(`/([a-zA-Z0-9]+)!`)
var hashDirectory = regexp.MustCompile(`^[a-f0-9]{32}$`)

func processImageURL(value any) string {
	address := text(value)
	if value == nil || value == false || value == int64(0) || address == "" || address == "0" {
		return ""
	}
	if fields := unified.Map(value); fields != nil && len(fields) == 0 {
		return ""
	}
	if values := unified.Slice(value); values != nil && len(values) == 0 {
		return ""
	}
	// The old concatenated-URL branch has no terminating transformation for
	// a leading "3http". Reject that malformed value rather than recurse.
	if strings.Contains(address, "3http") && !strings.HasPrefix(address, "http") {
		return ""
	}
	for index, pattern := range []*regexp.Regexp{imageOSSSG, imageDirectory} {
		if match := pattern.FindStringSubmatch(address); match != nil {
			_, numeric := unified.Numeric(match[1])
			if !hashDirectory.MatchString(match[1]) && !numeric {
				prefix := ""
				if index == 0 {
					prefix = "oss-sg/"
				}
				return "https://sns-img-hw.xhscdn.com/" + prefix + match[1] + "/" + match[2] + "?imageView2/2/w/0/format/jpg"
			}
		}
	}
	if match := imageNamed.FindStringSubmatch(address); match != nil {
		return "https://sns-img-hw.xhscdn.com/" + match[1] + "/" + match[2] + "?imageView2/2/w/0/format/jpg"
	}
	if match := imageOther.FindStringSubmatch(address); match != nil {
		return "https://ci.xiaohongshu.com/" + match[1] + "?imageView2/2/w/0/format/jpg"
	}
	return address
}
