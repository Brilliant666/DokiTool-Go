package unified

import (
	"path"
	"sort"
	"strings"
)

func phpTruthy(value any) bool {
	if value == nil {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v != "" && v != "0"
	case Object:
		return len(v) > 0
	}
	if array := Slice(value); array != nil {
		return len(array) > 0
	}
	if n, ok := Numeric(value); ok {
		return n != 0
	}
	return true
}

// DeferAudioContradiction isolates one identity after its observations have
// merged; it never invents has_audio=true or removes another valid pairing.
func DeferAudioContradiction(variant Object) Object {
	if !readyState(variant) {
		return Clone(variant)
	}
	layers := []Object{Map(variant["metadata"])}
	for _, value := range Slice(Map(variant["delivery"])["resources"]) {
		layers = append(layers, Map(Map(value)["metadata"]))
	}
	for _, metadata := range layers {
		if value, ok := metadata["has_audio"].(bool); !ok || value {
			continue
		}
		for _, field := range []string{"audio_codec", "audio_language", "audio_language_role", "audio_channels", "sample_rate_hz", "audio_bitrate_bps"} {
			_, scalar := metadata[field]
			_, conflict := Map(metadata["field_conflicts"])[field]
			if !scalar && !conflict {
				continue
			}
			deferred := Object{"id": variant["id"], "state": "deferred", "reason": Object{"code": "CONFLICTING_MEDIA_FACTS", "message": "同一媒体的音轨信息互相矛盾，无法确认可交付版本，暂不可下载"}}
			for _, key := range []string{"label", "provenance"} {
				if value, exists := variant[key]; exists {
					deferred[key] = CloneValue(value)
				}
			}
			return deferred
		}
	}
	return Clone(variant)
}

// FileVariants preserves the PHP adapter's two-stage identity aggregation and
// only the audio candidates already explicitly associated by a platform parser.
func FileVariants(candidates Array, now int64) Array {
	variants := make([]Object, 0)
	keys := map[string]int{}
	stableIdentities := map[int]string{}
	trackAudio := map[int]Array{}
	for _, value := range candidates {
		if text, ok := value.(string); ok {
			value = Object{"url": text}
		}
		candidate := Map(value)
		if candidate == nil {
			continue
		}
		sourceInputs := Slice(candidate["sources"])
		if sourceInputs == nil {
			sourceInputs = Array{candidate}
		}
		sources := Sources(sourceInputs, now)
		if len(sources) == 0 {
			continue
		}
		if size, ok := Numeric(coalesce(candidate["size_bytes"], candidate["size"])); ok && size > 0 {
			for _, sourceValue := range sources {
				source := Map(sourceValue)
				if !nonNullField(source, "size_bytes") {
					source["size_bytes"] = int64(size)
				}
			}
		}
		metadata := CandidateMetadata(candidate)
		parts, _ := splitRawURL(Str(Map(sources[0])["url"]))
		extension := strings.ToLower(strings.TrimPrefix(path.Ext(strings.TrimRight(parts.path, "/")), "."))
		kind := Str(candidate["protocol"])
		if candidate["protocol"] == nil {
			kind = "file"
			if extension == "m3u8" {
				kind = "hls"
			} else if extension == "mpd" {
				kind = "dash"
			}
		}
		if !hasString([]string{"file", "hls", "dash"}, kind) {
			continue
		}
		mediaIdentity, mediaIdentified := Text(candidate["private_media_identity"])
		representation, represented := mediaIdentity, mediaIdentified
		if !represented {
			representation, represented = Text(candidate["private_representation_id"])
		}
		track := Str(candidate["private_delivery_kind"]) == "video_track"
		signature := ""
		if represented {
			signature = PHPIdentityJSON(Array{kind, track, representation}, false)
		}
		if index, exists := keys[signature]; represented && exists {
			variant := variants[index]
			delivery := Map(variant["delivery"])
			delivery["sources"] = MergeSources(Slice(delivery["sources"]), sources, now)
			merged := MergeMetadata(Map(variant["metadata"]), metadata)
			if len(merged) > 0 {
				variant["metadata"] = merged
			}
			if track {
				trackAudio[index] = append(trackAudio[index], Slice(CloneValue(candidate["private_audio_candidates"]))...)
			}
			continue
		}
		delivery := Object{"kind": kind, "sources": sources}
		variant := Object{"delivery": delivery}
		if kind != "file" {
			delivery["scope"] = coalesce(candidate["scope"], "single")
		}
		if phpTruthy(candidate["is_original"]) {
			variant["provenance"] = "original_endpoint"
		}
		if len(metadata) > 0 {
			variant["metadata"] = metadata
		}
		if label, ok := Text(coalesce(candidate["label"], candidate["quality"])); ok && label != "0" {
			variant["label"] = label
		}
		index := len(variants)
		if represented {
			keys[signature] = index
		}
		if mediaIdentified {
			stableIdentities[index] = mediaIdentity
		}
		if track {
			trackAudio[index] = Slice(CloneValue(candidate["private_audio_candidates"]))
		}
		variants = append(variants, variant)
	}
	identified := make([]Object, 0)
	byID := map[string]int{}
	audioByVariant := map[string]Array{}
	for index, variant := range variants {
		delivery := Map(variant["delivery"])
		ids := make([]string, 0)
		for _, source := range Slice(delivery["sources"]) {
			ids = append(ids, Str(Map(source)["id"]))
		}
		sort.Strings(ids)
		identityValue := any(Slice(ids))
		if identity, exists := stableIdentities[index]; exists {
			identityValue = identity
		}
		audio, track := trackAudio[index]
		id := "v-" + identityDigest(PHPIdentityJSON(Array{delivery["kind"], track, identityValue}, false))
		variant["id"] = id
		if position, exists := byID[id]; exists {
			previous := identified[position]
			previousDelivery := Map(previous["delivery"])
			previousDelivery["sources"] = MergeSources(Slice(previousDelivery["sources"]), Slice(delivery["sources"]), now)
			merged := MergeMetadata(Map(previous["metadata"]), Map(variant["metadata"]))
			if len(merged) > 0 {
				previous["metadata"] = merged
			}
		} else {
			byID[id] = len(identified)
			identified = append(identified, variant)
		}
		if track {
			audioByVariant[id] = append(audioByVariant[id], audio...)
		}
	}
	complete := make(Array, 0)
	for _, variant := range identified {
		id := Str(variant["id"])
		audioCandidates, track := audioByVariant[id]
		if !track {
			complete = append(complete, variant)
			continue
		}
		video := Object{"id": "video", "role": "video", "delivery": CloneValue(variant["delivery"])}
		if nonNullField(variant, "metadata") {
			video["metadata"] = CloneValue(variant["metadata"])
		}
		audioOptions := FileVariants(audioCandidates, now)
		if len(audioOptions) == 0 {
			complete = append(complete, Object{"id": id, "state": "deferred", "reason": Object{"code": "MISSING_AUDIO_TRACK", "message": "未取得可确认的完整音视频组合，暂不可下载"}})
			continue
		}
		for _, value := range audioOptions {
			audio := Map(value)
			combinedID := id + "-" + Str(audio["id"])
			if Str(audio["state"]) == "deferred" {
				complete = append(complete, Object{"id": combinedID, "state": "deferred", "reason": CloneValue(audio["reason"])})
				continue
			}
			resource := Object{"id": "audio", "role": "audio", "delivery": CloneValue(audio["delivery"])}
			if nonNullField(audio, "metadata") {
				resource["metadata"] = CloneValue(audio["metadata"])
			}
			combined := Clone(variant)
			combined["id"] = combinedID
			combined["delivery"] = Object{"kind": "mux", "resources": Array{Clone(video), resource}, "output": Object{"container": "mp4", "extension": "mp4", "mime_type": "video/mp4"}}
			complete = append(complete, combined)
		}
	}
	for index, value := range complete {
		complete[index] = DeferAudioContradiction(Map(value))
	}
	return complete
}
