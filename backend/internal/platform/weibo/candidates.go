package weibo

import (
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// These helpers mirror the frozen PHP candidate extraction and deliberately
// preserve the original candidate order before the stable quality sort.
// They do not select unified-v1 defaults or publish unvalidated sources.
var (
	weiboCommaCount         = regexp.MustCompile("^[0-9]{1,3}(?:,[0-9]{3})+$")
	weiboTemplateDimensions = regexp.MustCompile("^([0-9]{2,5})x([0-9]{2,5})(?:\\.|$)")
	weiboLabelP             = regexp.MustCompile("(?i)([0-9]{3,4})p")
	weiboLabelK             = regexp.MustCompile("(?i)\\b([248])k\\b")
)

// PHP weiboCount truncates valid, nonnegative finite numbers to an integer.
// The numeric input is checked, rather than treating bool or an empty string
// as zero; absence and an explicit zero are different for metadata emission.
func legacyWeiboCount(input any) (int64, bool) {
	if value, ok := input.(string); ok && weiboCommaCount.MatchString(value) {
		input = strings.ReplaceAll(value, ",", "")
	}
	number, valid := unified.Numeric(input)
	if !valid || number < 0 || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	if number >= math.MaxInt64 {
		return math.MaxInt64, true
	}
	return int64(number), true
}

func legacyMediaURL(input any) string {
	value, ok := input.(string)
	if !ok {
		return ""
	}
	value = strings.Trim(value, " \n\r\t\v\x00")
	if strings.HasPrefix(value, "//") {
		value = "https:" + value
	}
	if value == "" || strings.ContainsAny(value, "\\ \r\n\t\x00") {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Opaque != "" || parsed.Hostname() == "" {
		return ""
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return ""
	}
	return value
}

// PHP parse_str keeps the final scalar value for repeated query parameters.
// Array-valued template parameters are not valid resolution hints.
func weiboTemplate(rawQuery string) (string, bool) {
	query, _ := url.ParseQuery(rawQuery)
	values := query["template"]
	if len(values) == 0 {
		return "", false
	}
	return values[len(values)-1], true
}

// legacyWeiboCandidate returns raw, ordered PHP-media facts. Source eligibility,
// signed relay URLs and default selection belong to the common adapter layers.
func legacyWeiboCandidate(rawURL any, label string, info unified.Object) unified.Object {
	mediaURL := legacyMediaURL(rawURL)
	if mediaURL == "" {
		return nil
	}
	result := unified.Object{"url": mediaURL, "label": label, "quality": "unknown"}
	for source, destination := range map[string]string{
		"width": "width", "height": "height", "bitrate": "bit_rate",
		"size": "size", "fps": "fps",
	} {
		if count, ok := legacyWeiboCount(info[source]); ok && count > 0 {
			result[destination] = count
		}
	}
	_, hasWidth := result["width"]
	_, hasHeight := result["height"]
	if !hasWidth || !hasHeight {
		if parsed, err := url.Parse(mediaURL); err == nil {
			if tmpl, ok := weiboTemplate(parsed.RawQuery); ok {
				if match := weiboTemplateDimensions.FindStringSubmatch(tmpl); match != nil {
					width, _ := strconv.ParseInt(match[1], 10, 64)
					height, _ := strconv.ParseInt(match[2], 10, 64)
					result["width"] = width
					result["height"] = height
				}
			}
		}
	}
	if w, ok := result["width"].(int64); ok {
		if h, ok := result["height"].(int64); ok {
			if w > h {
				w = h
			}
			result["quality"] = strconv.FormatInt(w, 10) + "p"
		}
	}
	if result["quality"] == "unknown" {
		if match := weiboLabelP.FindStringSubmatch(label); match != nil {
			result["quality"] = match[1] + "p"
		} else if match := weiboLabelK.FindStringSubmatch(label); match != nil {
			result["quality"] = map[string]string{"2": "1440p", "4": "2160p", "8": "4320p"}[match[1]]
		}
	}
	if codec, ok := info["video_codecs"].(string); ok && codec != "" {
		result["codec"] = codec
	}
	return result
}

// The original PHP array preserves insertion order of the urls map. Go's map
// does not; callers must pass urlKeys in observed upstream JSON key order.
// This explicit parameter prevents an accidental, unstable iteration order.
func legacyWeiboCandidates(videoInfo, status unified.Object, urlKeys []string) []unified.Object {
	media := unified.Map(unified.Map(status["page_info"])["media_info"])
	candidates := make([]unified.Object, 0)
	byURL := map[string]int{}
	add := func(candidate unified.Object, overwrite bool) {
		if candidate == nil {
			return
		}
		key := unified.Str(candidate["url"])
		if at, exists := byURL[key]; exists {
			if overwrite {
				candidates[at] = candidate
			}
			return
		}
		byURL[key] = len(candidates)
		candidates = append(candidates, candidate)
	}
	for _, item := range unified.Slice(media["playback_list"]) {
		info := unified.Map(unified.Map(item)["play_info"])
		mime, ok := info["mime"].(string)
		if !ok || !strings.HasPrefix(mime, "video/") {
			continue
		}
		label := info["label"]
		if label == nil {
			label = info["quality_desc"]
		}
		add(legacyWeiboCandidate(info["url"], unified.Str(label), info), true)
	}
	urls := unified.Map(videoInfo["urls"])
	for _, key := range urlKeys {
		add(legacyWeiboCandidate(urls[key], key, nil), false)
	}
	if len(candidates) == 0 {
		for _, key := range []string{
			"mp4_1080p_mp4", "mp4_720p_mp4", "mp4_hd_url", "mp4_sd_url",
			"stream_url_hd", "stream_url",
		} {
			add(legacyWeiboCandidate(media[key], key, nil), true)
		}
	}
	qualityRank := func(value unified.Object) (int64, int64, int64) {
		text := unified.Str(value["quality"])
		raw, _, _ := strings.Cut(text, "p")
		q, _ := strconv.ParseInt(raw, 10, 64)
		width, _ := value["width"].(int64)
		height, _ := value["height"].(int64)
		bitrate, _ := value["bit_rate"].(int64)
		return q, width * height, bitrate
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		aq, ap, ab := qualityRank(candidates[i])
		bq, bp, bb := qualityRank(candidates[j])
		if aq != bq {
			return aq > bq
		}
		if ap != bp {
			return ap > bp
		}
		return ab > bb
	})
	return candidates
}
