package unified

import (
	"regexp"
	"sort"
	"strings"
)

var frameFractionPattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)/(\d+(?:\.\d+)?)$`)
var audioLanguagePattern = regexp.MustCompile(`^[a-z]{2,3}(?:-[a-z0-9]{2,8})*$`)

func FrameRate(raw any) (float64, bool) {
	if text, ok := raw.(string); ok {
		if parts := frameFractionPattern.FindStringSubmatch(text); parts != nil {
			numerator, _ := Numeric(parts[1])
			denominator, _ := Numeric(parts[2])
			if denominator <= 0 {
				return 0, false
			}
			raw = numerator / denominator
		}
	}
	n, ok := Numeric(raw)
	return n, ok && n > 0
}

func HDRType(raw any) (string, bool) {
	text, ok := raw.(string)
	if !ok {
		return "", false
	}
	switch strings.ToLower(strings.Trim(text, phpTrimCharacters)) {
	case "sdr":
		return "sdr", true
	case "hdr10":
		return "hdr10", true
	case "hdr10+", "hdr10_plus":
		return "hdr10_plus", true
	case "hlg":
		return "hlg", true
	case "dolby vision", "dolby_vision", "dovi":
		return "dolby_vision", true
	case "hdr", "hdr_unknown":
		return "hdr_unknown", true
	default:
		return "", false
	}
}

func coalesce(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func VideoFacts(raw Object) Object {
	facts := Object{}
	for _, field := range []string{"width", "height"} {
		if n, ok := Numeric(raw[field]); ok && n > 0 {
			integer, _ := PHPInt(raw[field])
			facts[field] = integer
		}
	}
	for _, field := range []string{"fps", "frameRate", "frame_rate", "video_fps"} {
		if fps, ok := FrameRate(raw[field]); ok {
			facts["fps"] = fps
			break
		}
	}
	if hdr, ok := HDRType(coalesce(raw["hdr_type"], raw["dynamic_range"])); ok {
		facts["hdr_type"] = hdr
	} else if raw["is_hdr"] == true || phpIntegerOne(raw["is_hdr"]) {
		facts["hdr_type"] = "hdr_unknown"
	}
	return facts
}

func phpIntegerOne(value any) bool {
	text, ok := Text(value)
	_, stringValue := value.(string)
	return ok && !stringValue && text == "1"
}

func AudioFacts(raw Object) Object {
	facts := Object{}
	for _, field := range []string{"audio_channels", "channels", "channel_count"} {
		if n, ok := Numeric(raw[field]); ok && n > 0 {
			integer, _ := PHPInt(raw[field])
			facts["audio_channels"] = integer
			break
		}
	}
	if language, ok := coalesce(raw["audio_language"], raw["language"], raw["lang"]).(string); ok {
		language = strings.ToLower(strings.ReplaceAll(strings.Trim(language, phpTrimCharacters), "_", "-"))
		if audioLanguagePattern.MatchString(language) && !hasString([]string{"und", "mul", "zxx"}, language) {
			facts["audio_language"] = language
			role := coalesce(raw["audio_language_role"], raw["language_role"])
			if text, ok := role.(string); ok && hasString([]string{"main", "alternate"}, text) {
				facts["audio_language_role"] = text
			} else if raw["is_alternate_language"] == true {
				facts["audio_language_role"] = "alternate"
			}
		}
	}
	if value, ok := raw["has_audio"].(bool); ok {
		facts["has_audio"] = value
	}
	return facts
}

func CandidateMetadata(raw Object) Object {
	metadata := VideoFacts(raw)
	for field, value := range AudioFacts(raw) {
		if _, exists := metadata[field]; !exists {
			metadata[field] = value
		}
	}
	for _, field := range []string{"width", "height", "bitrate_bps", "video_bitrate_bps", "audio_bitrate_bps", "sample_rate_hz", "audio_channels"} {
		if n, ok := Numeric(raw[field]); ok && n > 0 {
			metadata[field] = int64(PHPRound(n, 0))
		}
	}
	if n, ok := Numeric(raw["bit_rate"]); ok && n > 0 {
		field := "bitrate_bps"
		if hasString([]string{"mp4", "probe"}, Str(raw["metadata_source"])) || Str(raw["private_delivery_kind"]) == "video_track" {
			field = "video_bitrate_bps"
		}
		metadata[field] = int64(PHPRound(n, 0))
	}
	if n, ok := Numeric(raw["fps"]); ok && n > 0 {
		metadata["fps"] = n
	}
	if n, ok := Numeric(raw["duration_ms"]); ok && n > 0 {
		metadata["duration_ms"] = int64(PHPRound(n, 0))
	} else if n, ok := Numeric(raw["duration"]); ok && n > 0 {
		metadata["duration_ms"] = int64(PHPRound(n*1000, 0))
	}
	for _, pair := range [][2]string{{"codec", "video_codec"}, {"video_codec", "video_codec"}, {"audio_codec", "audio_codec"}, {"container", "container"}, {"extension", "extension"}, {"mime_type", "mime_type"}} {
		if value, ok := Text(raw[pair[0]]); ok && value != "0" {
			metadata[pair[1]] = value
		}
	}
	if value, ok := raw["has_audio"].(bool); ok {
		metadata["has_audio"] = value
	}
	if !nonNullField(metadata, "extension") && hasString([]string{"mp4", "mov", "webm", "mkv", "mp3", "m4a", "aac", "flac"}, Str(metadata["container"])) {
		metadata["extension"] = metadata["container"]
	}
	if len(metadata) > 0 {
		metadata["evidence"] = "upstream"
		if hasString([]string{"mp4", "probe"}, Str(raw["metadata_source"])) {
			metadata["evidence"] = "probe"
		}
	}
	return metadata
}

// MergeMetadata retains every confirmed observation, promotes agreement with a
// probe, and keeps conflicting observations instead of picking by input order.
func MergeMetadata(existing, additional Object) Object {
	observations := map[string]Array{}
	for _, metadata := range []Object{existing, additional} {
		for field, value := range metadata {
			if hasString([]string{"evidence", "field_evidence", "field_conflicts"}, field) {
				continue
			}
			evidence := coalesce(Map(metadata["field_evidence"])[field], metadata["evidence"], "upstream")
			observations[field] = append(observations[field], Object{"value": CloneValue(value), "evidence": CloneValue(evidence)})
		}
		for field, facts := range Map(metadata["field_conflicts"]) {
			observations[field] = append(observations[field], Slice(CloneValue(facts))...)
		}
	}
	fields := make([]string, 0, len(observations))
	for field := range observations {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	merged, conflicts, fieldEvidence := Object{}, Object{}, Object{}
	for _, field := range fields {
		unique := map[string]Object{}
		for _, value := range observations[field] {
			fact := Map(value)
			if fact == nil {
				continue
			}
			key := phpJSON(fact["value"], true, true)
			if _, exists := unique[key]; !exists || fact["evidence"] == "probe" {
				unique[key] = Clone(fact)
			}
		}
		keys := make([]string, 0, len(unique))
		for key := range unique {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 1 {
			facts := make(Array, 0, len(keys))
			for _, key := range keys {
				facts = append(facts, unique[key])
			}
			conflicts[field] = facts
			continue
		}
		if len(keys) == 0 {
			continue
		}
		fact := unique[keys[0]]
		merged[field] = CloneValue(fact["value"])
		fieldEvidence[field] = CloneValue(fact["evidence"])
	}
	if len(conflicts) > 0 {
		merged["field_conflicts"] = conflicts
	}
	if len(fieldEvidence) > 0 {
		var evidence any
		initialized, mixed := false, false
		for _, field := range fields {
			value, exists := fieldEvidence[field]
			if !exists {
				continue
			}
			if !initialized {
				evidence, initialized = value, true
			} else if !Equal(evidence, value) {
				mixed = true
			}
		}
		if mixed {
			merged["evidence"] = "mixed"
			merged["field_evidence"] = fieldEvidence
		} else {
			merged["evidence"] = evidence
		}
	}
	return merged
}
