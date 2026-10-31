package douyin

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var videoFactOrder = []string{"width", "height", "fps", "hdr_type"}
var audioFactOrder = []string{"audio_channels", "audio_language", "audio_language_role", "has_audio"}

func videoMetadata(item object) *ordered {
	address := unified.Map(item["play_addr"])
	if address == nil && unified.Slice(item["play_addr"]) == nil {
		address = unified.Map(first(item["playAddr"]))
	}
	extra := jsonObject(item["video_extra"])
	sub := jsonObject(extra["sub_info"])
	metadata := newOrdered()
	for _, source := range []object{item, address, extra, sub} {
		metadata.unionKnown(unified.VideoFacts(source), videoFactOrder)
	}
	if pick(item["format"], extra["format"], "") != "dash" && !truthy(item["is_dash"]) {
		for _, source := range []object{item, extra, sub} {
			metadata.unionKnown(unified.AudioFacts(source), audioFactOrder)
		}
	}
	for _, field := range []struct {
		key     string
		aliases []string
	}{{"width", []string{"width"}}, {"height", []string{"height"}}, {"bit_rate", []string{"bit_rate", "bitRate"}}, {"size", []string{"data_size", "dataSize"}}} {
		for _, alias := range field.aliases {
			value := pick(item[alias], address[alias])
			if n, ok := unified.Numeric(value); ok && n > 0 {
				integer, _ := unified.PHPInt(value)
				metadata.set(field.key, integer)
				break
			}
		}
	}
	codec := strings.ToLower(stringValue(pick(item["codec"], item["codec_type"], "")))
	switch codec {
	case "h264", "avc", "avc1", "h.264":
		metadata.set("codec", "h264")
	case "hevc", "h265", "h.265":
		metadata.set("codec", "hevc")
	default:
		if value, ok := unified.Text(item["is_h265"]); ok && (value == "0" || value == "1") {
			codec = "h264"
			if value == "1" {
				codec = "hevc"
			}
			metadata.set("codec", codec)
		}
	}
	return metadata
}

func representationIdentity(item object) (string, error) {
	address := unified.Map(pick(item["play_addr"], first(item["playAddr"])))
	extra := jsonObject(item["video_extra"])
	hash, fileID := unified.Str(address["file_hash"]), unified.Str(extra["file_id"])
	if hash == "" && fileID == "" {
		return "", nil
	}
	encoded, err := unified.PHPJSON(array{"douyin", "file", phpValue(pick(address["uri"], "")), hash, fileID}, true)
	return string(encoded), err
}

func collectPlayURLs(item object) []string {
	var candidates []string
	for _, entry := range unified.Slice(item["playAddr"]) {
		if src := unified.Str(unified.Map(entry)["src"]); src != "" {
			candidates = append(candidates, src)
		}
	}
	if len(candidates) == 0 {
		for _, entry := range unified.Slice(child(item, "play_addr", "url_list")) {
			if src, ok := entry.(string); ok && src != "" {
				candidates = append(candidates, src)
			}
		}
	}
	return candidates
}

func pixelCount(metadata object) float64 {
	w, _ := unified.Numeric(metadata["width"])
	h, _ := unified.Numeric(metadata["height"])
	return w * h
}

func bitrate(metadata object) float64 {
	value, _ := unified.Numeric(metadata["bit_rate"])
	return value
}

func geometryLabel(metadata object) string {
	w, wok := unified.Numeric(metadata["width"])
	h, hok := unified.Numeric(metadata["height"])
	if !wok || !hok {
		return ""
	}
	return stringValue(math.Min(w, h)) + "p"
}

func withURL(metadata object, raw, quality, label string) object {
	result := unified.Clone(metadata)
	if result == nil {
		result = object{}
	}
	result["url"], result["quality"], result["label"] = raw, quality, label
	return result
}

type qualityVideo struct {
	url      string
	gearName string
	backup   array
	metadata object
}

func extractHighestQualityVideo(detail object) (qualityVideo, error) {
	video := unified.Map(detail["video"])
	rates := unified.Slice(video["bitRateList"])
	if len(rates) == 0 {
		rates = unified.Slice(video["bit_rate"])
	}
	rates = append(array(nil), rates...)
	result := qualityVideo{backup: array{}, metadata: object{}}
	// PHP 8's stable sort preserves first observations when geometry and bitrate
	// tie. It only orders private formatter candidates; the gateway chooses the
	// public default independently from complete eligible files.
	sort.SliceStable(rates, func(i, j int) bool {
		a, b := videoMetadata(unified.Map(rates[i])).values, videoMetadata(unified.Map(rates[j])).values
		if pixelCount(a) != pixelCount(b) {
			return pixelCount(a) > pixelCount(b)
		}
		return bitrate(a) > bitrate(b)
	})
	for _, entry := range rates {
		rate := unified.Map(entry)
		if rate == nil {
			continue
		}
		candidates := collectPlayURLs(rate)
		if len(candidates) == 0 {
			continue
		}
		v3, v26 := "", ""
		for _, candidate := range candidates {
			if strings.Contains(candidate, "v3-web") {
				v3 = candidate
				break
			}
			if v26 == "" && strings.Contains(candidate, "v26-web") {
				v26 = candidate
			}
		}
		best := candidates[0]
		if v3 != "" {
			best = v3
		} else if v26 != "" {
			best = replacePlayHost(v26)
		}
		metadata := videoMetadata(rate)
		identity, err := representationIdentity(rate)
		if err != nil {
			return qualityVideo{}, err
		}
		if identity != "" {
			metadata.set("private_media_identity", identity)
		}
		if expiry, ok := unified.Numeric(video["cdn_url_expired"]); ok && expiry > 0 {
			integer, _ := unified.PHPInt(video["cdn_url_expired"])
			metadata.set("expires_at", time.Unix(integer, 0).UTC().Format(time.RFC3339))
		}
		extra := jsonObject(rate["video_extra"])
		if pick(rate["format"], extra["format"], "") == "dash" || truthy(rate["is_dash"]) {
			metadata.set("private_delivery_kind", "video_track")
			metadata.set("private_audio_id", unified.Str(extra["audio_file_id"]))
		}
		identityURLs := append([]string(nil), candidates...)
		sort.Strings(identityURLs)
		representation, err := hashPHP(array{metadata.phpValue(), unified.Slice(identityURLs)})
		if err != nil {
			return qualityVideo{}, err
		}
		metadata.set("private_representation_id", representation)
		quality := geometryLabel(metadata.values)
		if quality == "" {
			quality = stringValue(pick(rate["gearName"], rate["gear_name"], "unknown"))
		}
		if result.url == "" || result.url == "0" {
			result.url, result.gearName, result.metadata = best, quality, metadata.values
		}
		for _, candidate := range candidates {
			if strings.Contains(candidate, "v26-web") {
				candidate = replacePlayHost(candidate)
			}
			// A shared video source may have several explicitly associated audio
			// tracks. Keep observations until identity and pairing are known.
			result.backup = append(result.backup, withURL(metadata.values, candidate, quality, quality))
		}
	}
	if !truthy(result.url) {
		uri := unified.Str(video["uri"])
		if uri == "" && child(video, "play_addr", "uri") != nil {
			uri = stringValue(child(video, "play_addr", "uri"))
		}
		playAPI := unified.Str(video["playApi"])
		if playAPI == "" {
			playAPI = unified.Str(first(child(video, "play_addr", "url_list")))
		}
		if playAPI != "" {
			result.url = strings.ReplaceAll(playAPI, "playwm", "play")
			metadata := videoMetadata(video)
			identity, err := representationIdentity(video)
			if err != nil {
				return qualityVideo{}, err
			}
			if identity != "" {
				metadata.set("private_media_identity", identity)
			}
			if expiry, ok := unified.Numeric(video["cdn_url_expired"]); ok && expiry > 0 {
				integer, _ := unified.PHPInt(video["cdn_url_expired"])
				metadata.set("expires_at", time.Unix(integer, 0).UTC().Format(time.RFC3339))
			}
			representation, err := hashPHP(phpValue(pick(child(video, "play_addr", "url_list"), array{playAPI})))
			if err != nil {
				return qualityVideo{}, err
			}
			metadata.set("private_representation_id", representation)
			result.metadata = metadata.values
		} else if uri != "" {
			result.url = buildOriginalPlayURL(uri)
		}
		urls := unified.Slice(child(video, "play_addr", "url_list"))
		for index, entry := range urls {
			link, ok := entry.(string)
			if index == 0 || !ok || link == "" {
				continue
			}
			converted := strings.ReplaceAll(link, "playwm", "play")
			if truthy(converted) && converted != result.url {
				result.backup = append(result.backup, withURL(result.metadata, converted, "unknown", "备用播放源"))
			}
		}
	}
	if result.gearName == "" {
		result.gearName = geometryLabel(result.metadata)
	}
	if !truthy(result.gearName) {
		result.gearName = "unknown"
	}
	return result, nil
}

func candidateObservationKey(raw string, metadata object) (string, error) {
	delivery, err := unified.PHPJSON(array{raw, phpValue(pick(metadata["private_media_identity"], metadata["private_representation_id"])),
		phpValue(pick(metadata["private_delivery_kind"], "file")), phpValue(metadata["private_audio_id"])}, true)
	if err != nil {
		return "", err
	}
	facts := unified.Clone(metadata)
	for _, field := range []string{"url", "quality", "label", "private_representation_id"} {
		delete(facts, field)
	}
	key, err := unified.PHPJSON(array{string(delivery), phpValue(facts)}, true)
	return string(key), err
}

func audioCandidates(entries array, expiresAt any) array {
	result := array{}
	for _, entry := range entries {
		audio := unified.Map(child(entry, "audio_meta"))
		id, isString := audio["file_id"].(string)
		if audio == nil || unified.Str(audio["media_type"]) != "audio" || !isString {
			continue
		}
		sources := array{}
		for _, value := range unified.Slice(audio["url_list"]) {
			if raw, ok := value.(string); ok && unified.MediaURL(raw) != "" {
				source := object{"url": raw}
				if n, ok := unified.Numeric(expiresAt); ok && n > 0 {
					integer, _ := unified.PHPInt(expiresAt)
					source["expires_at"] = time.Unix(integer, 0).UTC().Format(time.RFC3339)
				}
				sources = append(sources, source)
			}
		}
		if len(sources) == 0 {
			continue
		}
		candidate := object{"private_audio_id": id, "private_media_identity": "douyin:audio:" + id, "sources": sources}
		info := jsonObject(audio["sub_info"])
		for _, facts := range []object{unified.AudioFacts(audio), unified.AudioFacts(info)} {
			for key, value := range facts {
				if _, exists := candidate[key]; !exists {
					candidate[key] = value
				}
			}
		}
		if codec, ok := info["audio_profile"].(string); ok {
			candidate["audio_codec"] = codec
		}
		for _, field := range []struct {
			value any
			key   string
		}{{info["audio_sample_rate"], "sample_rate_hz"}, {info["audio_channels"], "audio_channels"}, {audio["bitrate"], "audio_bitrate_bps"}, {audio["size"], "size"}} {
			if n, ok := unified.Numeric(field.value); ok && n > 0 {
				candidate[field.key], _ = unified.PHPInt(field.value)
			}
		}
		result = append(result, candidate)
	}
	return result
}
