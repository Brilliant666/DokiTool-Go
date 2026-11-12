package douyin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const (
	ShanghaiDetailTimeout       = 4 * time.Second
	MaxShanghaiRenditionObjects = 64
	MaxShanghaiRenditionURLs    = 8
)

var (
	// ErrProtocolV1 reports that the configured Shanghai endpoint/worker speaks
	// only the legacy video_id protocol and should be queried via ResolveOriginal.
	ErrProtocolV1 = errors.New("shanghai resolver uses v1 video_id protocol")

	domesticMediaHost = regexp.MustCompile(`^v[0-9][a-z0-9-]*\.douyinvod\.com$`)
)

// ShanghaiWork carries validated Shanghai observations for an aweme_id:
// domestic rendition URLs keyed by DouyinStoragePath plus 0..2 probed 4K
// original candidates.
type ShanghaiWork struct {
	VideoID    string
	Candidates []unified.Object
	Renditions map[string]unified.Array
}

// WorkResolver extends OriginalResolver with parallel aweme_id resolution.
type WorkResolver interface {
	OriginalResolver
	ResolveWork(context.Context, string) (*ShanghaiWork, error)
}

// ShanghaiDetail holds the minimal metadata Shanghai extracts from its own
// Douyin detail lookup without retaining or forwarding raw upstream JSON.
type ShanghaiDetail struct {
	VideoID    string
	IsAlbum    bool
	Renditions map[string][]string
}

type shanghaiOutcome struct {
	work *ShanghaiWork
	err  error
}

type shanghaiState struct {
	resolved      bool
	useV1Fallback bool
	work          *ShanghaiWork
}

func shanghaiStateFromOutcome(outcome shanghaiOutcome) shanghaiState {
	if errors.Is(outcome.err, ErrProtocolV1) {
		return shanghaiState{resolved: true, useV1Fallback: true}
	}
	if outcome.err != nil || outcome.work == nil {
		return shanghaiState{resolved: true}
	}
	return shanghaiState{resolved: true, work: outcome.work}
}

// ValidAwemeID reports whether id matches Douyin's numeric aweme_id grammar.
func ValidAwemeID(id string) bool {
	return awemeID.MatchString(id)
}

func isImageOrLiveDetail(detail object) bool {
	return len(unified.Slice(detail["images"])) > 0 || len(unified.Slice(detail["image_list"])) > 0
}

func isOriginalDispatcher(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Hostname(), "aweme.snssdk.com") &&
		(strings.TrimRight(u.Path, "/") == "/aweme/v1/play" || strings.TrimRight(u.Path, "/") == "/aweme/v1/playwm")
}

// NormalizeShanghaiRenditionURL validates a Shanghai rendition URL against
// AllowedMediaURL and the douyinvod.com-only host rule, rewrites v26-web to
// v26-luna (matching video.go), and confirms a non-empty DouyinStoragePath.
func NormalizeShanghaiRenditionURL(raw string) (string, bool) {
	if !AllowedMediaURL(raw) || isOriginalDispatcher(raw) {
		return "", false
	}
	host := urlHost(raw)
	if !domesticMediaHost.MatchString(host) {
		return "", false
	}
	if strings.Contains(host, "v26-web") {
		raw = replacePlayHost(raw)
		if !AllowedMediaURL(raw) || isOriginalDispatcher(raw) || !domesticMediaHost.MatchString(urlHost(raw)) {
			return "", false
		}
	}
	if unified.DouyinStoragePath(raw) == "" {
		return "", false
	}
	return raw, true
}

// FetchShanghaiDetail fetches Douyin's detail endpoint from the Shanghai exit
// and extracts only {object_key -> domestic douyinvod.com URLs} and video_id.
func FetchShanghaiDetail(ctx context.Context, executor fetch.Executor, id string) (ShanghaiDetail, error) {
	if err := ctx.Err(); err != nil {
		return ShanghaiDetail{}, err
	}
	if !ValidAwemeID(id) || executor == nil {
		return ShanghaiDetail{}, errors.New("invalid Shanghai detail request")
	}
	detailCtx, cancel := context.WithTimeout(ctx, ShanghaiDetailTimeout)
	defer cancel()
	req := detailRequest(id)
	if deadline, ok := detailCtx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < req.Policy.Timeout {
			req.Policy.Timeout = remaining
		}
	}
	response, err := executor.Do(detailCtx, req)
	if ctx.Err() != nil {
		return ShanghaiDetail{}, ctx.Err()
	}
	if err != nil {
		return ShanghaiDetail{}, err
	}
	if !utf8.Valid(response.Body) {
		return ShanghaiDetail{}, errors.New("invalid UTF-8 in Douyin detail")
	}
	payload, err := unified.Decode(response.Body)
	if err != nil {
		return ShanghaiDetail{}, err
	}
	if _, ok := unified.Numeric(payload["status_code"]); !ok {
		return ShanghaiDetail{}, errors.New("missing status_code in Douyin detail")
	}
	status, _ := unified.PHPInt(payload["status_code"])
	if status != 0 {
		return ShanghaiDetail{}, fmt.Errorf("douyin detail status_code=%d", status)
	}
	detail := unified.Map(payload["aweme_detail"])
	if len(detail) == 0 || stringValue(detail["aweme_id"]) != id {
		return ShanghaiDetail{}, errors.New("mismatched or empty aweme_detail")
	}
	if isImageOrLiveDetail(detail) {
		return ShanghaiDetail{IsAlbum: true}, nil
	}
	video := unified.Map(detail["video"])
	return ShanghaiDetail{
		VideoID:    extractShanghaiVID(video),
		Renditions: extractShanghaiRenditions(video),
	}, nil
}

func extractShanghaiVID(video object) string {
	if vid := extractVID(video); vid != "" && vid != "0" {
		return vid
	}
	for _, field := range []string{"play_addr_h264", "play_addr_265", "play_addr_lowbr", "download_addr"} {
		if value := child(video, field, "uri"); value != nil && scalar(value) {
			if vid := stringValue(value); vid != "" && vid != "0" {
				return vid
			}
		}
	}
	rates := unified.Slice(video["bitRateList"])
	if len(rates) == 0 {
		rates = unified.Slice(video["bit_rate"])
	}
	for _, entry := range rates {
		rate := unified.Map(entry)
		if value := child(rate, "play_addr", "uri"); value != nil && scalar(value) {
			if vid := stringValue(value); vid != "" && vid != "0" {
				return vid
			}
		}
		if value := unified.Map(first(rate["playAddr"]))["uri"]; value != nil && scalar(value) {
			if vid := stringValue(value); vid != "" && vid != "0" {
				return vid
			}
		}
	}
	return ""
}

func extractShanghaiRenditions(video object) map[string][]string {
	renditions := map[string][]string{}
	add := func(raw string) {
		if raw == "" {
			return
		}
		candidate := toHTTPS(strings.ReplaceAll(raw, "playwm", "play"))
		normalized, ok := NormalizeShanghaiRenditionURL(candidate)
		if !ok {
			return
		}
		key := unified.DouyinStoragePath(normalized)
		if key == "" {
			return
		}
		existing := renditions[key]
		if len(existing) >= MaxShanghaiRenditionURLs {
			return
		}
		for _, prev := range existing {
			if prev == normalized {
				return
			}
		}
		if len(existing) == 0 && len(renditions) >= MaxShanghaiRenditionObjects {
			return
		}
		renditions[key] = append(existing, normalized)
	}
	rates := unified.Slice(video["bitRateList"])
	if len(rates) == 0 {
		rates = unified.Slice(video["bit_rate"])
	}
	for _, entry := range rates {
		rate := unified.Map(entry)
		if rate == nil {
			continue
		}
		extra := jsonObject(rate["video_extra"])
		if pick(rate["format"], extra["format"], "") == "dash" || truthy(rate["is_dash"]) {
			continue
		}
		for _, u := range collectPlayURLs(rate) {
			add(u)
		}
	}
	for _, field := range []string{"play_addr", "play_addr_h264", "play_addr_265", "play_addr_lowbr"} {
		for _, entry := range unified.Slice(child(video, field, "url_list")) {
			if s, ok := entry.(string); ok {
				add(s)
			}
		}
	}
	if playAPI := unified.Str(video["playApi"]); playAPI != "" {
		add(playAPI)
	}
	if len(renditions) == 0 {
		return nil
	}
	return renditions
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

func (p *Parser) probeOriginal(ctx context.Context, vid, entry string, detail object, known qualityVideo) (object, *shanghaiSameObject, error) {
	return p.probeOriginalWithShanghai(ctx, vid, entry, detail, known, shanghaiState{})
}

// The official dispatcher and validated service response establish provenance.
// Compare against this work's own duration and declared renditions before
// allowing an optional resolver to replace or supplement the Japan route.
func (p *Parser) probeOriginalWithShanghai(ctx context.Context, vid, entry string, detail object, known qualityVideo, shState shanghaiState) (object, *shanghaiSameObject, error) {
	video := unified.Map(detail["video"])
	if shState.work != nil {
		if len(shState.work.Candidates) > 0 {
			if shState.work.VideoID == vid {
				if validated := validateShanghai(vid, shState.work.Candidates, video); validated != nil {
					if higherThanDeclared(validated.metadata, known, video, validated.objectKey) {
						return object{"url": validated.url, "metadata": validated.metadata}, nil, nil
					}
					return nil, &shanghaiSameObject{objectKey: validated.objectKey, sources: validated.sources}, nil
				}
			}
		} else if shState.work.VideoID != "" && shState.work.VideoID == vid {
			// Shanghai completed original probing for this video_id and confirmed no higher-quality original exists.
			return nil, nil, nil
		}
	} else if p.resolver != nil && (!shState.resolved || shState.useV1Fallback) {
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
	var leadURL any
	for i, c := range candidates {
		raw := unified.Str(c["url"])
		if !AllowedMediaURL(raw) || isOriginalDispatcher(raw) {
			return nil
		}
		sourceKey := unified.Str(c["source_key"])
		if strings.Contains(urlHost(raw), "v26-web") {
			raw = replacePlayHost(raw)
			if !AllowedMediaURL(raw) || isOriginalDispatcher(raw) {
				return nil
			}
			sourceKey = urlHost(raw)
		}
		if c["object_key"] != objectKey || unified.DouyinStoragePath(raw) != objectKey || c["prefix_sha256"] != prefix || !unified.Equal(c["metadata"], observation["metadata"]) {
			return nil
		}
		if i == 0 {
			leadURL = raw
		}
		source := object{"url": raw, "size": metadata["size"], "private_source_identity": identity + "@" + sourceKey}
		if c["expires_at"] != nil {
			source["expires_at"] = c["expires_at"]
		}
		sources = append(sources, source)
	}
	metadata["sources"] = sources
	metadata["private_media_identity"] = identity
	return &shanghaiValidated{url: leadURL, objectKey: objectKey, sources: sources, metadata: metadata}
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
	prependShanghaiSources(mainCandidate, backups, same, nil)
}

func prependShanghaiSources(mainCandidate object, backups array, same *shanghaiSameObject, renditions map[string]array) {
	hasSame := same != nil && same.objectKey != "" && len(same.sources) > 0
	if !hasSame && len(renditions) == 0 {
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
	groupObjectKey := map[string]string{}
	for _, candidate := range all {
		if truthy(candidate["is_original"]) {
			continue
		}
		groupKey := renditionIdentityKey(candidate)
		if groupKey == "" || groupObjectKey[groupKey] != "" {
			continue
		}
		if objKey := unified.DouyinStoragePath(unified.Str(candidate["url"])); objKey != "" {
			groupObjectKey[groupKey] = objKey
		}
	}
	if len(groupObjectKey) == 0 {
		return
	}
	prepended := map[string]bool{}
	for _, candidate := range all {
		if truthy(candidate["is_original"]) {
			continue
		}
		groupKey := renditionIdentityKey(candidate)
		objKey := groupObjectKey[groupKey]
		if objKey == "" || prepended[groupKey] {
			continue
		}
		extra := array{}
		seenURL := map[string]bool{}
		if hasSame && same.objectKey == objKey {
			for _, src := range same.sources {
				cloned := unified.CloneValue(src)
				if u := unified.Str(unified.Map(cloned)["url"]); u != "" {
					seenURL[u] = true
				}
				extra = append(extra, cloned)
			}
		}
		for _, entry := range renditions[objKey] {
			raw := ""
			var expiresAt any
			if m := unified.Map(entry); m != nil {
				raw = unified.Str(m["url"])
				expiresAt = m["expires_at"]
			} else if s, ok := entry.(string); ok {
				raw = s
			}
			normalized, ok := NormalizeShanghaiRenditionURL(raw)
			if !ok || unified.DouyinStoragePath(normalized) != objKey || seenURL[normalized] {
				continue
			}
			seenURL[normalized] = true
			src := object{
				"url":                     normalized,
				"private_source_identity": "douyin:shanghai-rendition:" + unified.SourceIdentity(normalized),
			}
			if expiresAt != nil {
				src["expires_at"] = unified.CloneValue(expiresAt)
			}
			extra = append(extra, src)
		}
		if len(extra) == 0 {
			continue
		}
		existing := unified.Slice(candidate["sources"])
		if existing == nil {
			existing = array{declaredCandidateSource(candidate)}
		}
		merged := make(array, 0, len(extra)+len(existing))
		merged = append(merged, extra...)
		merged = append(merged, existing...)
		candidate["sources"] = merged
		prepended[groupKey] = true
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
