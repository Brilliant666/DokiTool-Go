package douyin

import (
	"context"
	"math"
	"net/url"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func isOriginalDispatcher(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Hostname(), "aweme.snssdk.com") &&
		(strings.TrimRight(u.Path, "/") == "/aweme/v1/play" || strings.TrimRight(u.Path, "/") == "/aweme/v1/playwm")
}

// A detail response can also list the dispatcher among its playback URLs.
// It is a request for a route, never an independently verified file source.
func removeDispatchEntries(q *qualityVideo) {
	kept := q.backup[:0]
	for _, v := range q.backup {
		if !isOriginalDispatcher(unified.Str(unified.Map(v)["url"])) {
			kept = append(kept, v)
		}
	}
	q.backup = kept
	if !isOriginalDispatcher(q.url) {
		return
	}
	q.url, q.gearName, q.metadata = "", "", object{}
	if len(kept) > 0 {
		c := unified.Map(kept[0])
		q.url, q.gearName, q.metadata = unified.Str(c["url"]), unified.Str(c["quality"]), unified.Clone(c)
		delete(q.metadata, "url")
		delete(q.metadata, "quality")
		delete(q.metadata, "label")
	}
}

type shanghaiSameObject struct {
	objectKey string
	sources   array
}

type shanghaiValidated struct {
	url       any
	objectKey string
	sources   array
	metadata  object
}

// The official dispatcher and validated service response establish provenance.
// Compare against this work's own duration and declared renditions before
// allowing an optional resolver to replace or supplement the Japan route.
func (p *Parser) probeOriginal(ctx context.Context, vid, entry string, detail object, known qualityVideo) (object, *shanghaiSameObject, error) {
	video := unified.Map(detail["video"])
	if p.resolver != nil {
		candidates, err := p.resolver.ResolveOriginal(ctx, vid)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if err == nil {
			if validated := validateShanghai(vid, candidates, video); validated != nil {
				if higherThanDeclared(validated.metadata, known, video, validated.objectKey) {
					return object{"url": validated.url, "metadata": validated.metadata}, nil, nil
				}
				return nil, &shanghaiSameObject{objectKey: validated.objectKey, sources: validated.sources}, nil
			}
		}
	}
	if p.prober == nil {
		return nil, nil, nil
	}
	probed, err := p.prober.Probe(ctx, entry)
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if err != nil {
		return nil, nil, nil
	}
	// With the feature enabled the same confirmed CDN object keeps its version
	// identity even when its credentials came from the Japan fallback.
	if p.resolver != nil && probed != nil {
		raw := unified.Str(probed["url"])
		key := unified.DouyinStoragePath(raw)
		meta := unified.Map(probed["metadata"])
		if len(meta) > 0 && !higherThanDeclared(meta, known, video, key) {
			return nil, nil, nil
		}
		if key != "" && len(meta) > 0 {
			// DouyinStoragePath has already parsed and validated this URL.
			u, _ := url.Parse(raw)
			id := originalFileID(vid, key, unified.Str(probed["prefix_sha256"]), meta)
			meta = unified.Clone(meta)
			src := object{"url": raw, "size": meta["size"], "private_source_identity": id + "@" + strings.ToLower(u.Hostname())}
			meta["sources"] = array{src}
			meta["private_media_identity"] = id
			return object{"url": raw, "metadata": meta}, nil, nil
		}
	}
	return probed, nil, nil
}

func validateShanghai(vid string, candidates []object, video object) *shanghaiValidated {
	if len(candidates) == 0 || len(candidates) > 2 {
		return nil
	}
	observation := candidates[0]
	metadata := unified.Clone(unified.Map(observation["metadata"]))
	size, _ := unified.Numeric(metadata["size"])
	duration, _ := unified.Numeric(metadata["duration"])
	declaredMS, _ := unified.Numeric(video["duration"])
	objectKey := unified.Str(observation["object_key"])
	prefix := unified.Str(observation["prefix_sha256"])
	if objectKey == "" || prefix == "" || size <= 0 || declaredMS <= 0 || duration <= 0 || math.Abs(duration*1000-declaredMS) > 1000 {
		return nil
	}
	identity := originalFileID(vid, objectKey, prefix, metadata)
	sources := array{}
	for _, c := range candidates {
		raw := unified.Str(c["url"])
		if !AllowedMediaURL(raw) || isOriginalDispatcher(raw) {
			return nil
		}
		if c["object_key"] != objectKey || c["prefix_sha256"] != prefix || !unified.Equal(c["metadata"], observation["metadata"]) {
			return nil
		}
		source := object{"url": raw, "size": metadata["size"], "private_source_identity": identity + "@" + unified.Str(c["source_key"])}
		if c["expires_at"] != nil {
			source["expires_at"] = c["expires_at"]
		}
		sources = append(sources, source)
	}
	metadata["sources"] = sources
	metadata["private_media_identity"] = identity
	return &shanghaiValidated{url: observation["url"], objectKey: objectKey, sources: sources, metadata: metadata}
}

func renditionIdentityKey(candidate object) string {
	if unified.Str(candidate["private_delivery_kind"]) == "video_track" {
		return ""
	}
	if id, ok := unified.Text(candidate["private_media_identity"]); ok {
		return "m:" + id
	}
	if rep, ok := unified.Text(candidate["private_representation_id"]); ok {
		return "r:" + rep
	}
	return "u:" + unified.Str(candidate["url"])
}

func declaredCandidateSource(candidate object) object {
	src := object{"url": candidate["url"]}
	for _, field := range []string{"size", "size_bytes", "expires_at", "availability", "browser_fetch", "fallback_urls", "private_source_identity"} {
		if value := candidate[field]; value != nil {
			src[field] = unified.CloneValue(value)
		}
	}
	return src
}

func prependSameObjectSources(mainCandidate object, backups array, same *shanghaiSameObject) {
	if same == nil || same.objectKey == "" || len(same.sources) == 0 {
		return
	}
	all := make([]object, 0, 1+len(backups))
	if truthy(mainCandidate["url"]) {
		all = append(all, mainCandidate)
	}
	for _, value := range backups {
		if entry := unified.Map(value); entry != nil && truthy(entry["url"]) {
			all = append(all, entry)
		}
	}
	matchedGroups := map[string]bool{}
	for _, candidate := range all {
		key := renditionIdentityKey(candidate)
		if key == "" {
			continue
		}
		if unified.DouyinStoragePath(unified.Str(candidate["url"])) == same.objectKey {
			matchedGroups[key] = true
		}
	}
	if len(matchedGroups) == 0 {
		return
	}
	prepended := map[string]bool{}
	for _, candidate := range all {
		key := renditionIdentityKey(candidate)
		if !matchedGroups[key] || prepended[key] {
			continue
		}
		existing := unified.Slice(candidate["sources"])
		if existing == nil {
			existing = array{declaredCandidateSource(candidate)}
		}
		merged := make(array, 0, len(same.sources)+len(existing))
		for _, src := range same.sources {
			merged = append(merged, unified.CloneValue(src))
		}
		merged = append(merged, existing...)
		candidate["sources"] = merged
		prepended[key] = true
	}
}

// Both routes identify the storage object and fingerprint the same initial bytes.
func originalFileID(vid, key, prefix string, m object) string {
	return unified.PHPIdentityJSON(array{"douyin:original", vid, key, prefix, m["size"], m["width"], m["height"], m["codec"], m["duration"]}, false)
}

func higherThanDeclared(m object, known qualityVideo, video object, objectKey string) bool {
	pixels, current := pixelCount(m), pixelCount(known.metadata)
	if pixels < current || (current > 0 && pixels == current && bitrate(m) <= bitrate(known.metadata)) {
		return false
	}
	size, _ := unified.Numeric(m["size"])
	matches := func(raw any) bool {
		key := unified.DouyinStoragePath(unified.Str(raw))
		return key != "" && key == objectKey
	}
	for _, field := range []string{"play_addr", "play_addr_h264", "play_addr_265", "play_addr_lowbr", "download_addr", "download_suffix_logo_addr"} {
		addr := unified.Map(video[field])
		if n, _ := unified.Numeric(addr["data_size"]); n == size {
			return false
		}
		for _, raw := range unified.Slice(addr["url_list"]) {
			if matches(raw) {
				return false
			}
		}
	}
	for _, candidate := range known.backup {
		c := unified.Map(candidate)
		if n, _ := unified.Numeric(c["size"]); n == size || matches(c["url"]) {
			return false
		}
	}
	return true
}
