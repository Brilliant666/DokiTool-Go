package unified

import (
	"math"
	"net/url"
	"strings"
)

func SameFrameRate(a, b float64) bool {
	return a > 0 && b > 0 && math.Abs(a-b)/math.Max(a, b) <= 0.01
}

func policyMetadata(parent, resource Object, fields []string) (Object, bool) {
	merged := overlayMetadata(resource, parent)
	conflict := false
	for _, field := range fields {
		if nonNullField(Map(parent["field_conflicts"]), field) || nonNullField(Map(resource["field_conflicts"]), field) {
			conflict = true
		}
		if !nonNullField(parent, field) || !nonNullField(resource, field) {
			continue
		}
		equal := Equal(parent[field], resource[field])
		if field == "fps" {
			a, aOK := Numeric(parent[field])
			b, bOK := Numeric(resource[field])
			equal = aOK && bOK && SameFrameRate(a, b)
		}
		conflict = conflict || !equal
	}
	return merged, conflict
}

func policyFactValues(metadata Object, field string) []string {
	values := make([]string, 0)
	seen := map[string]bool{}
	add := func(value string) {
		if !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	for _, value := range Slice(Map(metadata["field_conflicts"])[field]) {
		add(Str(Map(value)["value"]))
	}
	if nonNullField(metadata, field) {
		add(Str(metadata[field]))
	}
	return values
}

type policyAudioTrack struct {
	metadata         Object
	languages, roles []string
}

// Both main-language evidence and replacement coverage use this association.
func policyAudioMetadata(variant Object) ([]policyAudioTrack, bool) {
	resources := make([]Object, 0)
	for _, value := range Slice(Map(variant["delivery"])["resources"]) {
		resource := Map(value)
		if Str(resource["role"]) == "audio" {
			resources = append(resources, resource)
		}
	}
	parent := Map(variant["metadata"])
	fields := []string{"audio_language", "audio_language_role", "audio_channels"}
	ambiguous := false
	if len(resources) > 1 {
		for _, field := range fields {
			if _, exists := parent[field]; exists || nonNullField(Map(parent["field_conflicts"]), field) {
				ambiguous = true
			}
		}
	}
	if ambiguous {
		parent = nil
	}
	tracks := make([]policyAudioTrack, 0, len(resources))
	conflict := ambiguous
	for _, resource := range resources {
		facts := Map(resource["metadata"])
		metadata, inconsistent := policyMetadata(parent, facts, fields)
		tracks = append(tracks, policyAudioTrack{
			metadata:  metadata,
			languages: append(policyFactValues(parent, "audio_language"), policyFactValues(facts, "audio_language")...),
			roles:     append(policyFactValues(parent, "audio_language_role"), policyFactValues(facts, "audio_language_role")...),
		})
		conflict = conflict || inconsistent
	}
	return tracks, conflict
}

func hasString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// FilterItemVariants requires one usable file to cover every triggered protected
// capability. It never combines the advantages of different files.
func FilterItemVariants(input Object, now int64) Object {
	item := objectCopy(input)
	if !readyState(item) || Str(item["type"]) != "video" {
		return item
	}
	variants := Slice(item["variants"])
	completeFiles, files := make([]Object, 0), make([]Object, 0)
	for _, value := range variants {
		v := Map(value)
		if readyState(v) && Str(Map(v["delivery"])["kind"]) == "file" && Map(v["metadata"])["has_audio"] != false {
			completeFiles = append(completeFiles, v)
			if DeliveryUsable(Map(v["delivery"]), now) {
				files = append(files, v)
			}
		}
	}
	if len(files) == 0 {
		return item
	}
	mainLanguages := make([]string, 0)
	for _, file := range completeFiles {
		for _, language := range policyFactValues(Map(file["metadata"]), "audio_language") {
			mainLanguages = append(mainLanguages, strings.ToLower(language))
		}
	}
	for _, value := range variants {
		tracks, _ := policyAudioMetadata(Map(value))
		for _, track := range tracks {
			if hasString(track.roles, "main") {
				for _, language := range track.languages {
					mainLanguages = append(mainLanguages, strings.ToLower(language))
				}
			}
		}
	}
	remaining := make(Array, 0, len(variants))
	for _, value := range variants {
		mux := Map(value)
		keep := true
		if readyState(mux) && Str(Map(mux["delivery"])["kind"]) == "mux" {
			videos := make([]Object, 0)
			for _, value := range Slice(Map(mux["delivery"])["resources"]) {
				resource := Map(value)
				if Str(resource["role"]) == "video" {
					videos = append(videos, resource)
				}
			}
			audio, audioConflict := policyAudioMetadata(mux)
			if len(videos) == 1 && len(audio) > 0 {
				video, conflict := policyMetadata(Map(mux["metadata"]), Map(videos[0]["metadata"]), []string{"width", "height", "fps", "hdr_type"})
				if !conflict && !audioConflict {
					for _, file := range files {
						complete := Map(file["metadata"])
						conflicting := false
						for _, field := range []string{"width", "height", "fps", "hdr_type", "audio_language", "audio_language_role", "audio_channels", "has_audio"} {
							if nonNullField(Map(complete["field_conflicts"]), field) {
								conflicting = true
							}
						}
						cmp, comparable := QualityCompare(file, Object{"metadata": video})
						if conflicting || !comparable || cmp < 0 {
							continue
						}
						fps, fpsKnown := Numeric(video["fps"])
						fileFPS, fileFPSKnown := Numeric(complete["fps"])
						if fpsKnown && fps > 30 && !SameFrameRate(fps, 30) && (!fileFPSKnown || (fileFPS < fps && !SameFrameRate(fps, fileFPS))) {
							continue
						}
						hdr := Str(video["hdr_type"])
						if hdr != "" && hdr != "sdr" && (hdr == "hdr_unknown" || Str(complete["hdr_type"]) != hdr) {
							continue
						}
						covered := true
						for _, entry := range audio {
							track := entry.metadata
							channels, _ := Numeric(track["audio_channels"])
							fileChannels, _ := Numeric(complete["audio_channels"])
							if channels > 2 && fileChannels < channels {
								covered = false
								break
							}
							language := strings.ToLower(Str(track["audio_language"]))
							different := language != "" && Str(track["audio_language_role"]) == "alternate"
							if language != "" {
								for _, main := range mainLanguages {
									different = different || main != language
								}
							}
							if different && strings.ToLower(Str(complete["audio_language"])) != language {
								covered = false
								break
							}
						}
						if covered {
							keep = false
							break
						}
					}
				}
			}
		}
		if keep {
			remaining = append(remaining, value)
		}
	}
	item["variants"] = remaining
	return item
}

// ViewURL only updates API navigation generated by the application, never media URLs.
func ViewURL(raw string, debug bool) string {
	path, query, _ := strings.Cut(raw, "?")
	parts := make([]string, 0)
	for _, part := range strings.Split(query, "&") {
		if part == "" {
			continue
		}
		key, _, _ := strings.Cut(part, "=")
		decoded, err := url.PathUnescape(key) // PHP rawurldecode keeps '+' intact.
		if err == nil && decoded == "debug" {
			continue
		}
		parts = append(parts, part)
	}
	if debug {
		parts = append(parts, "debug=1")
	}
	if len(parts) == 0 {
		return path
	}
	return path + "?" + strings.Join(parts, "&")
}

// PublicView follows public-source qualification/signing in the gateway.
func PublicView(payload Object, debug bool, now int64) Object {
	if Str(payload["code"]) != "OK" {
		return Clone(payload)
	}
	data := objectCopy(Map(payload["data"]))
	if nonNullField(data, "refresh_url") {
		data["refresh_url"] = ViewURL(Str(data["refresh_url"]), debug)
	}
	items := make(Array, 0, len(Slice(data["items"])))
	for _, value := range Slice(data["items"]) {
		item := objectCopy(Map(value))
		if !debug {
			item = FilterItemVariants(item, now)
		}
		if nonNullField(item, "resolve_url") {
			item["resolve_url"] = ViewURL(Str(item["resolve_url"]), debug)
		}
		items = append(items, item)
	}
	data["items"] = items
	return FinalizeData(data, now)
}
