// Shared yt-dlp -J to unified-v1 conversion pipeline and fail-soft size prober.
package ytengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const (
	maxSafeInteger        int64 = 9007199254740991
	sizeProbeTotalTimeout       = 1500 * time.Millisecond
	sizeProbeConcurrency        = 4
	maxProbedURLs               = 12
)

// AdaptOptions configures how yt-dlp -J Info is converted into a unified-v1 envelope.
type AdaptOptions struct {
	PlatformID                   string
	ContentID                    string
	SourceURL                    string
	SelectedIndex                int
	MaxEntries                   int
	AllowedMediaURL              func(string) bool
	EnableDASHMux                bool
	EnableHLS                    bool
	AllowCodecLessProgressiveMP4 bool
}

// SizeProber resolves the exact byte length of a progressive MP4 URL.
// Returning (0, false) leaves the format's Filesize unset (fail-soft).
type SizeProber func(ctx context.Context, rawURL string) (int64, bool)

// NewHTTPSizeProber creates a bounded, no-redirect HTTP Range size prober
// restricted to URLs permitted by allowedMediaURL.
func NewHTTPSizeProber(proxyURL string, allowedMediaURL func(string) bool) SizeProber {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 1200 * time.Millisecond,
	}
	if trimmed := strings.TrimSpace(proxyURL); trimmed != "" {
		if parsed, err := url.Parse(trimmed); err == nil &&
			(parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "socks5" || parsed.Scheme == "socks5h") {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   sizeProbeTotalTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return func(ctx context.Context, rawURL string) (int64, bool) {
		cleanURL := SanitizeMediaURL(rawURL, allowedMediaURL)
		if cleanURL == "" {
			return 0, false
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cleanURL, nil)
		if err != nil {
			return 0, false
		}
		req.Header.Set("Range", "bytes=0-0")
		req.Header.Set("Accept", "*/*")

		resp, err := client.Do(req)
		if err != nil {
			return 0, false
		}
		defer func() {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64))
			_ = resp.Body.Close()
		}()

		switch resp.StatusCode {
		case http.StatusPartialContent:
			if size, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
				return size, true
			}
		case http.StatusOK:
			if size, ok := parsePositiveContentLength(resp.Header.Get("Content-Length")); ok {
				return size, true
			}
		}
		return 0, false
	}
}

func parseContentRangeTotal(raw string) (int64, bool) {
	slash := strings.LastIndexByte(strings.TrimSpace(raw), '/')
	if slash < 0 {
		return 0, false
	}
	totalStr := strings.TrimSpace(raw[slash+1:])
	if totalStr == "" || totalStr == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(totalStr, 10, 64)
	if err != nil || n <= 0 || n > maxSafeInteger {
		return 0, false
	}
	return n, true
}

func parsePositiveContentLength(raw string) (int64, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || n <= 0 || n > maxSafeInteger {
		return 0, false
	}
	return n, true
}

// PopulateMissingMP4Sizes concurrently probes exact byte lengths for progressive MP4
// formats that lack an exact Filesize from yt-dlp. Any probe failure is ignored (fail-soft).
func PopulateMissingMP4Sizes(ctx context.Context, info *Info, prober SizeProber, opts AdaptOptions) {
	if info == nil || prober == nil {
		return
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = 4
	}
	var entries []*Info
	if len(info.Entries) > 0 {
		limit := len(info.Entries)
		if limit > maxEntries {
			limit = maxEntries
		}
		for i := 0; i < limit; i++ {
			entries = append(entries, &info.Entries[i])
		}
	} else {
		entries = []*Info{info}
	}

	var uniqueURLs []string
	seen := make(map[string]bool)
	for _, entry := range entries {
		for _, f := range entry.Formats {
			if f.Filesize != nil && *f.Filesize > 0 {
				continue
			}
			cleanURL := SanitizeMediaURL(f.URL, opts.AllowedMediaURL)
			if cleanURL == "" {
				continue
			}
			fCopy := f
			fCopy.URL = cleanURL
			if !isProgressiveMP4Format(fCopy, opts.AllowCodecLessProgressiveMP4) {
				continue
			}
			if !seen[cleanURL] {
				seen[cleanURL] = true
				uniqueURLs = append(uniqueURLs, cleanURL)
				if len(uniqueURLs) >= maxProbedURLs {
					break
				}
			}
		}
		if len(uniqueURLs) >= maxProbedURLs {
			break
		}
	}

	if len(uniqueURLs) == 0 {
		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, sizeProbeTotalTimeout)
	defer cancel()

	var (
		mu      sync.Mutex
		results = make(map[string]int64, len(uniqueURLs))
		sem     = make(chan struct{}, sizeProbeConcurrency)
		wg      sync.WaitGroup
	)

	for _, targetURL := range uniqueURLs {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-probeCtx.Done():
				return
			}
			if size, ok := prober(probeCtx, u); ok && size > 0 && size <= maxSafeInteger {
				mu.Lock()
				results[u] = size
				mu.Unlock()
			}
		}(targetURL)
	}
	wg.Wait()

	if len(results) == 0 {
		return
	}

	for _, entry := range entries {
		for i := range entry.Formats {
			f := &entry.Formats[i]
			if f.Filesize != nil && *f.Filesize > 0 {
				continue
			}
			cleanURL := SanitizeMediaURL(f.URL, opts.AllowedMediaURL)
			if size, ok := results[cleanURL]; ok {
				sz := size
				f.Filesize = &sz
			}
		}
	}
}

// SanitizeMediaURL strips URL fragments and validates the URL against allowedMediaURL.
func SanitizeMediaURL(raw string, allowedMediaURL func(string) bool) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if before, _, found := strings.Cut(trimmed, "#"); found {
		trimmed = before
	}
	if unified.MediaURL(trimmed) == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.User != nil || u.Opaque != "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	if allowedMediaURL != nil && !allowedMediaURL(trimmed) {
		return ""
	}
	return trimmed
}

// AdaptInfo converts yt-dlp -J metadata into a unified-v1 envelope.
func AdaptInfo(info *Info, opts AdaptOptions, now int64) unified.Object {
	if info == nil {
		return unified.ContractError("UPSTREAM_INVALID_RESPONSE", "解析器返回数据为空", false)
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = 4
	}

	var entries []Info
	if len(info.Entries) > 0 {
		limit := len(info.Entries)
		if limit > maxEntries {
			limit = maxEntries
		}
		entries = info.Entries[:limit]
	} else {
		entries = []Info{*info}
	}

	items := make(unified.Array, 0, len(entries))
	seenItemIDs := make(map[string]bool, len(entries))
	selectedItemID := ""

	for idx, entry := range entries {
		position := idx + 1
		item := buildEntryVideoItem(&entry, info, opts, position, len(entries), now)
		if item == nil {
			continue
		}
		itemID := unified.Str(item["id"])
		if itemID == "" || seenItemIDs[itemID] {
			continue
		}
		seenItemIDs[itemID] = true
		items = append(items, item)
		if opts.SelectedIndex > 0 && position == opts.SelectedIndex {
			selectedItemID = itemID
		}
	}

	if len(items) == 0 {
		return unified.ContractError("NO_MEDIA", "该内容未包含可解析的视频媒体", false)
	}

	if selectedItemID == "" {
		selectedItemID = unified.Str(unified.Map(items[0])["id"])
	}

	data := unified.Object{
		"platform":        opts.PlatformID,
		"content_id":      opts.ContentID,
		"source_url":      opts.SourceURL,
		"default_item_id": selectedItemID,
		"items":           items,
		"generated_at":    time.Unix(now, 0).UTC().Format("2006-01-02T15:04:05Z"),
	}

	if title := cleanDisplayTitle(info, opts.ContentID); title != "" {
		data["title"] = title
	}
	if desc := strings.TrimSpace(info.Description); desc != "" {
		data["description"] = desc
	}
	if cover := pickCoverObject(info, entries, opts.AllowedMediaURL, now); cover != nil {
		data["cover"] = cover
	}
	if author := buildAuthorObject(info); author != nil {
		data["author"] = author
	}
	if publishedAt := formatTimestamp(info.Timestamp, entries); publishedAt != "" {
		data["published_at"] = publishedAt
	}
	if stats := buildStatisticsObject(info, entries); len(stats) > 0 {
		data["statistics"] = stats
	}

	return unified.FinalizeData(data, now)
}

func buildEntryVideoItem(entry, parent *Info, opts AdaptOptions, position, total int, now int64) unified.Object {
	if entry == nil {
		return nil
	}
	entryKey := resolveEntryKey(entry, opts.ContentID, position)
	variants := buildEntryVariants(entry, entryKey, opts, now)
	if len(variants) == 0 {
		return nil
	}

	itemID := "video-" + shortDigest(opts.PlatformID+":item:"+opts.ContentID+":"+entryKey)
	item := unified.Object{
		"id":          itemID,
		"type":        "video",
		"index":       int64(position),
		"default":     nil,
		"variants":    variants,
		"resolve_url": unified.ResolveURL(opts.SourceURL, itemID),
	}

	if itemTitle := resolveItemTitle(entry, parent, opts.ContentID, position, total); itemTitle != "" {
		item["title"] = itemTitle
	}
	if durationMs := resolveDurationMs(entry.Duration); durationMs > 0 {
		item["duration_ms"] = durationMs
	} else if total == 1 {
		if parentDuration := resolveDurationMs(parent.Duration); parentDuration > 0 {
			item["duration_ms"] = parentDuration
		}
	}
	if cover := pickSingleCover(entry, opts.AllowedMediaURL, now); cover != nil {
		item["cover"] = cover
	} else if cover := pickSingleCover(parent, opts.AllowedMediaURL, now); cover != nil {
		item["cover"] = cover
	}
	return item
}

func buildEntryVariants(entry *Info, entryKey string, opts AdaptOptions, now int64) unified.Array {
	var mp4Formats []Format
	var dashVideoFormats []Format
	var dashAudioFormats []Format
	var hlsVideoFormats []Format
	var hlsAudioFormats []Format

	// First pass: check if any audio-bearing format exists in the entry
	hasAnyAudioInEntry := false
	for _, f := range entry.Formats {
		if !isNoCodec(f.ACodec) && strings.TrimSpace(f.ACodec) != "" {
			hasAnyAudioInEntry = true
			break
		}
	}

	for _, f := range entry.Formats {
		cleanURL := SanitizeMediaURL(f.URL, opts.AllowedMediaURL)
		if cleanURL == "" {
			continue
		}
		f.URL = cleanURL

		if isHLSAudioOnly(f) {
			hlsAudioFormats = append(hlsAudioFormats, f)
			continue
		}
		if isProgressiveMP4Format(f, opts.AllowCodecLessProgressiveMP4) {
			if (f.Width == nil || f.Height == nil) && entry.Width != nil && entry.Height != nil && *entry.Width > 0 && *entry.Height > 0 {
				w, h := *entry.Width, *entry.Height
				f.Width = &w
				f.Height = &h
			}
			mp4Formats = append(mp4Formats, f)
			continue
		}
		// Silent progressive MP4 post (e.g. vintage Instagram post with no audio streams at all)
		if opts.AllowCodecLessProgressiveMP4 && !hasAnyAudioInEntry && isSilentCodecLessMP4(f) {
			if (f.Width == nil || f.Height == nil) && entry.Width != nil && entry.Height != nil && *entry.Width > 0 && *entry.Height > 0 {
				w, h := *entry.Width, *entry.Height
				f.Width = &w
				f.Height = &h
			}
			mp4Formats = append(mp4Formats, f)
			continue
		}
		if isHTTPDASHVideoOnly(f) {
			dashVideoFormats = append(dashVideoFormats, f)
			continue
		}
		if isHTTPDASHAudioOnly(f) {
			dashAudioFormats = append(dashAudioFormats, f)
			continue
		}
		if isHLSVideo(f) {
			hlsVideoFormats = append(hlsVideoFormats, f)
			continue
		}
	}

	sortFormats(mp4Formats)
	sortDASHVideoFormats(dashVideoFormats)
	sortAudioFormats(dashAudioFormats)
	sortFormats(hlsVideoFormats)
	sortAudioFormats(hlsAudioFormats)

	variants := make(unified.Array, 0, len(mp4Formats)+len(dashVideoFormats)+len(hlsVideoFormats))
	seenVariantIDs := make(map[string]bool)

	// 1. Deliverable progressive MP4s (kind == "file")
	dedupedMP4 := deduplicateProgressiveFormats(mp4Formats)
	for i, f := range dedupedMP4 {
		sources := buildFormatSources(f, now)
		if len(sources) == 0 {
			continue
		}
		hasAudio := !isNoCodec(f.ACodec)
		metadata := buildFormatMetadata(f, hasAudio, true)
		formatKey := nonEmptyOr(f.FormatID, fmt.Sprintf("mp4-%d", i+1))
		variantID := "v-" + shortDigest(opts.PlatformID+":file:"+entryKey+":"+formatKey)
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
		if label := qualityLabel(f, ""); label != "" {
			variant["label"] = label
		}
		variants = append(variants, variant)
	}

	// 2. High-Resolution DASH Video+Audio Mux Variants (kind == "mux")
	if opts.EnableDASHMux && len(dashVideoFormats) > 0 && len(dashAudioFormats) > 0 {
		var maxMP4Edge int64
		var maxMP4FPS float64
		if len(dedupedMP4) > 0 {
			maxMP4Edge = shortEdge(dedupedMP4[0])
			maxMP4FPS = derefFloat(dedupedMP4[0].FPS)
		}
		bestAudio := dashAudioFormats[0]
		audioResource := buildMuxTrackResource(bestAudio, "audio", now)
		if audioResource != nil {
			audioKey := nonEmptyOr(bestAudio.FormatID, "audio")
			dedupedDASH := deduplicateDASHVideoFormats(dashVideoFormats)
			for i, vf := range dedupedDASH {
				if maxMP4Edge > 0 {
					edge := shortEdge(vf)
					fps := derefFloat(vf.FPS)
					higherFPS := edge == maxMP4Edge && fps > maxMP4FPS+5 && fps >= 50
					if edge <= maxMP4Edge && !higherFPS {
						continue
					}
				}
				videoResource := buildMuxTrackResource(vf, "video", now)
				if videoResource == nil {
					continue
				}
				videoKey := nonEmptyOr(vf.FormatID, fmt.Sprintf("dashv-%d", i+1))
				variantID := "v-" + shortDigest(opts.PlatformID+":mux:"+entryKey+":"+videoKey+":"+audioKey)
				if seenVariantIDs[variantID] {
					continue
				}
				seenVariantIDs[variantID] = true

				muxSuffix := formatMuxSuffix(vf, bestAudio)
				variant := unified.Object{
					"id":       variantID,
					"label":    qualityLabel(vf, muxSuffix),
					"metadata": buildMuxParentMetadata(vf),
					"delivery": unified.Object{
						"kind":      "mux",
						"resources": unified.Array{unified.Clone(videoResource), unified.Clone(audioResource)},
						"output": unified.Object{
							"container": "mp4",
							"extension": "mp4",
							"mime_type": "video/mp4",
						},
					},
				}
				variants = append(variants, variant)
			}
		}
	}

	// 3. Non-single-file HLS variants (kind == "hls", scope == "adaptive")
	if opts.EnableHLS && len(hlsVideoFormats) > 0 {
		var bestHLSAudio *Format
		if len(hlsAudioFormats) > 0 {
			bestHLSAudio = &hlsAudioFormats[0]
		}
		dedupedHLS := deduplicateProgressiveFormats(hlsVideoFormats)
		for i, f := range dedupedHLS {
			sources := buildFormatSources(f, now)
			if len(sources) == 0 {
				continue
			}
			hasEmbeddedAudio := !isNoCodec(f.ACodec) && strings.TrimSpace(f.ACodec) != ""
			metadata := buildFormatMetadata(f, hasEmbeddedAudio, false)
			formatKey := nonEmptyOr(f.FormatID, fmt.Sprintf("hls-%d", i+1))
			variantID := "v-" + shortDigest(opts.PlatformID+":hls:"+entryKey+":"+formatKey)
			if seenVariantIDs[variantID] {
				continue
			}
			seenVariantIDs[variantID] = true

			selection := unified.Object{
				"video_representation_id": formatKey,
			}
			if !hasEmbeddedAudio && bestHLSAudio != nil && strings.TrimSpace(bestHLSAudio.FormatID) != "" {
				selection["audio_representation_id"] = strings.TrimSpace(bestHLSAudio.FormatID)
			}
			variant := unified.Object{
				"id":       variantID,
				"label":    qualityLabel(f, "HLS 需合流"),
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
	}

	return variants
}

func buildMuxTrackResource(f Format, role string, now int64) unified.Object {
	sources := buildFormatSources(f, now)
	if len(sources) == 0 {
		return nil
	}
	metadata := unified.Object{
		"evidence": "upstream",
	}
	if role == "video" {
		metadata["has_audio"] = false
		if f.Width != nil && *f.Width > 0 && f.Height != nil && *f.Height > 0 {
			metadata["width"] = *f.Width
			metadata["height"] = *f.Height
		}
		if f.FPS != nil && *f.FPS > 0 && !math.IsNaN(*f.FPS) && !math.IsInf(*f.FPS, 0) {
			metadata["fps"] = normalizeFPS(*f.FPS)
		}
		if hdr := mapDynamicRange(f.DynamicRange); hdr != "" {
			metadata["hdr_type"] = hdr
		}
		if vc := cleanCodec(f.VCodec); vc != "" {
			metadata["video_codec"] = vc
		}
		if vbr := kbpsToBps(pickBitrateKbps(f.VBR, f.TBR)); vbr > 0 {
			metadata["video_bitrate_bps"] = vbr
		}
	} else {
		metadata["has_audio"] = true
		if ac := cleanCodec(f.ACodec); ac != "" {
			metadata["audio_codec"] = ac
		}
		if abr := kbpsToBps(pickBitrateKbps(f.ABR, f.TBR)); abr > 0 {
			metadata["audio_bitrate_bps"] = abr
		}
		if f.ASR != nil && *f.ASR > 0 {
			metadata["sample_rate_hz"] = *f.ASR
		}
		if f.AudioChannels != nil && *f.AudioChannels > 0 {
			metadata["audio_channels"] = *f.AudioChannels
		}
	}
	return unified.Object{
		"id":       role,
		"role":     role,
		"metadata": metadata,
		"delivery": unified.Object{
			"kind":    "file",
			"sources": sources,
		},
	}
}

func buildMuxParentMetadata(vf Format) unified.Object {
	metadata := unified.Object{
		"evidence": "upstream",
	}
	if vf.Width != nil && *vf.Width > 0 && vf.Height != nil && *vf.Height > 0 {
		metadata["width"] = *vf.Width
		metadata["height"] = *vf.Height
	}
	if vf.FPS != nil && *vf.FPS > 0 && !math.IsNaN(*vf.FPS) && !math.IsInf(*vf.FPS, 0) {
		metadata["fps"] = normalizeFPS(*vf.FPS)
	}
	if hdr := mapDynamicRange(vf.DynamicRange); hdr != "" {
		metadata["hdr_type"] = hdr
	}
	if vc := cleanCodec(vf.VCodec); vc != "" {
		metadata["video_codec"] = vc
	}
	if vbr := kbpsToBps(pickBitrateKbps(vf.VBR, vf.TBR)); vbr > 0 {
		metadata["video_bitrate_bps"] = vbr
	}
	return metadata
}

func formatMuxSuffix(vf, af Format) string {
	var parts []string
	if shortEdge(vf) >= 2160 || longEdge(vf) >= 3840 {
		parts = append(parts, "4K")
	} else if shortEdge(vf) >= 1440 || longEdge(vf) >= 2560 {
		parts = append(parts, "2K")
	}
	if vf.Filesize != nil && *vf.Filesize > 0 && af.Filesize != nil && *af.Filesize > 0 {
		totalBytes := *vf.Filesize + *af.Filesize
		if totalBytes > 0 && totalBytes <= maxSafeInteger {
			parts = append(parts, fmt.Sprintf("%.1f MB", float64(totalBytes)/1e6))
		}
	}
	parts = append(parts, "需合流")
	return strings.Join(parts, " · ")
}

func buildFormatSources(f Format, now int64) unified.Array {
	rawSource := unified.Object{
		"url": f.URL,
	}
	if f.Filesize != nil && *f.Filesize > 0 && *f.Filesize <= maxSafeInteger {
		rawSource["size_bytes"] = *f.Filesize
	}
	return unified.Sources(unified.Array{rawSource}, now)
}

func buildFormatMetadata(f Format, hasAudio, isFile bool) unified.Object {
	meta := unified.Object{
		"has_audio": hasAudio,
		"evidence":  "upstream",
	}
	if isFile {
		ext := strings.ToLower(strings.TrimSpace(f.Ext))
		if ext == "" || ext == "mp4" {
			meta["container"] = "mp4"
			meta["extension"] = "mp4"
			meta["mime_type"] = "video/mp4"
		}
	}
	if f.Width != nil && *f.Width > 0 && f.Height != nil && *f.Height > 0 {
		meta["width"] = *f.Width
		meta["height"] = *f.Height
	}
	if f.FPS != nil && *f.FPS > 0 && !math.IsNaN(*f.FPS) && !math.IsInf(*f.FPS, 0) {
		meta["fps"] = normalizeFPS(*f.FPS)
	}
	if hdr := mapDynamicRange(f.DynamicRange); hdr != "" {
		meta["hdr_type"] = hdr
	}
	if vcodec := cleanCodec(f.VCodec); vcodec != "" {
		meta["video_codec"] = vcodec
	}
	if vbr := kbpsToBps(pickBitrateKbps(f.VBR, f.TBR)); vbr > 0 {
		meta["video_bitrate_bps"] = vbr
	}
	if hasAudio {
		if acodec := cleanCodec(f.ACodec); acodec != "" {
			meta["audio_codec"] = acodec
		}
		if abr := kbpsToBps(f.ABR); abr > 0 {
			meta["audio_bitrate_bps"] = abr
		}
		if f.ASR != nil && *f.ASR > 0 {
			meta["sample_rate_hz"] = *f.ASR
		}
		if f.AudioChannels != nil && *f.AudioChannels > 0 {
			meta["audio_channels"] = *f.AudioChannels
		}
	}
	return meta
}

func isProgressiveMP4Format(f Format, allowCodecLess bool) bool {
	proto := strings.ToLower(strings.TrimSpace(f.Protocol))
	if proto != "https" && proto != "http" {
		return false
	}
	ext := strings.ToLower(strings.TrimSpace(f.Ext))
	if ext != "mp4" {
		return false
	}
	u, err := url.Parse(f.URL)
	if err != nil {
		return false
	}
	urlPath := strings.ToLower(u.Path)
	if strings.HasSuffix(urlPath, ".m3u8") || strings.HasSuffix(urlPath, ".mpd") {
		return false
	}
	if isNoCodec(f.VCodec) || isNoCodec(f.ACodec) {
		return false
	}
	vc := strings.TrimSpace(f.VCodec)
	ac := strings.TrimSpace(f.ACodec)
	if vc == "" && ac == "" {
		if !allowCodecLess {
			return false
		}
		fid := strings.ToLower(strings.TrimSpace(f.FormatID))
		note := strings.ToLower(strings.TrimSpace(f.FormatNote))
		if strings.HasPrefix(fid, "dash") || strings.Contains(note, "dash") {
			return false
		}
	}
	return true
}

func isSilentCodecLessMP4(f Format) bool {
	proto := strings.ToLower(strings.TrimSpace(f.Protocol))
	if proto != "https" && proto != "http" {
		return false
	}
	if strings.ToLower(strings.TrimSpace(f.Ext)) != "mp4" {
		return false
	}
	fid := strings.ToLower(strings.TrimSpace(f.FormatID))
	note := strings.ToLower(strings.TrimSpace(f.FormatNote))
	if strings.HasPrefix(fid, "dash") || strings.Contains(note, "dash") {
		return false
	}
	return strings.TrimSpace(f.VCodec) == "" && isNoCodec(f.ACodec)
}

func isHTTPDASHVideoOnly(f Format) bool {
	proto := strings.ToLower(strings.TrimSpace(f.Protocol))
	if proto != "https" && proto != "http" {
		return false
	}
	u, err := url.Parse(f.URL)
	if err != nil {
		return false
	}
	urlPath := strings.ToLower(u.Path)
	if strings.HasSuffix(urlPath, ".m3u8") || strings.HasSuffix(urlPath, ".mpd") {
		return false
	}
	return !isNoCodec(f.VCodec) && strings.TrimSpace(f.VCodec) != "" && isNoCodec(f.ACodec)
}

func isHTTPDASHAudioOnly(f Format) bool {
	proto := strings.ToLower(strings.TrimSpace(f.Protocol))
	if proto != "https" && proto != "http" {
		return false
	}
	u, err := url.Parse(f.URL)
	if err != nil {
		return false
	}
	urlPath := strings.ToLower(u.Path)
	if strings.HasSuffix(urlPath, ".m3u8") || strings.HasSuffix(urlPath, ".mpd") {
		return false
	}
	return isNoCodec(f.VCodec) && !isNoCodec(f.ACodec) && strings.TrimSpace(f.ACodec) != ""
}

func isHLSVideo(f Format) bool {
	if isNoCodec(f.VCodec) {
		return false
	}
	proto := strings.ToLower(strings.TrimSpace(f.Protocol))
	u, err := url.Parse(f.URL)
	urlPath := ""
	if err == nil {
		urlPath = strings.ToLower(u.Path)
	}
	return strings.Contains(proto, "m3u8") || strings.HasSuffix(urlPath, ".m3u8")
}

func isHLSAudioOnly(f Format) bool {
	if !isNoCodec(f.VCodec) {
		return false
	}
	if isNoCodec(f.ACodec) || strings.TrimSpace(f.ACodec) == "" {
		return false
	}
	proto := strings.ToLower(strings.TrimSpace(f.Protocol))
	u, err := url.Parse(f.URL)
	urlPath := ""
	if err == nil {
		urlPath = strings.ToLower(u.Path)
	}
	return strings.Contains(proto, "m3u8") || strings.HasSuffix(urlPath, ".m3u8")
}

func isNoCodec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	return c == "none"
}

func cleanCodec(codec string) string {
	c := strings.TrimSpace(codec)
	if c == "" || strings.EqualFold(c, "none") || strings.EqualFold(c, "unknown") {
		return ""
	}
	return c
}

func sortFormats(formats []Format) {
	sort.SliceStable(formats, func(i, j int) bool {
		a, b := formats[i], formats[j]
		sa, sb := shortEdge(a), shortEdge(b)
		if sa != sb {
			return sa > sb
		}
		la, lb := longEdge(a), longEdge(b)
		if la != lb {
			return la > lb
		}
		fa, fb := derefFloat(a.FPS), derefFloat(b.FPS)
		if math.Abs(fa-fb) > 0.5 {
			return fa > fb
		}
		ta, tb := derefFloat(a.TBR), derefFloat(b.TBR)
		if ta != tb {
			return ta > tb
		}
		return a.FormatID > b.FormatID
	})
}

func sortDASHVideoFormats(formats []Format) {
	sort.SliceStable(formats, func(i, j int) bool {
		a, b := formats[i], formats[j]
		sa, sb := shortEdge(a), shortEdge(b)
		if sa != sb {
			return sa > sb
		}
		la, lb := longEdge(a), longEdge(b)
		if la != lb {
			return la > lb
		}
		fa, fb := math.Round(derefFloat(a.FPS)), math.Round(derefFloat(b.FPS))
		if fa != fb {
			return fa > fb
		}
		// Prefer MP4 container over WebM at identical resolution and framerate
		aMP4 := strings.EqualFold(a.Ext, "mp4")
		bMP4 := strings.EqualFold(b.Ext, "mp4")
		if aMP4 != bMP4 {
			return aMP4
		}
		// Prefer H.264 (avc1) over AV1 over VP9 for broadest playback compatibility
		ca, cb := dashCodecRank(a.VCodec), dashCodecRank(b.VCodec)
		if ca != cb {
			return ca > cb
		}
		ta, tb := derefFloat(pickBitrateKbps(a.VBR, a.TBR)), derefFloat(pickBitrateKbps(b.VBR, b.TBR))
		if ta != tb {
			return ta > tb
		}
		return a.FormatID > b.FormatID
	})
}

func dashCodecRank(vcodec string) int {
	vc := strings.ToLower(strings.TrimSpace(vcodec))
	switch {
	case strings.HasPrefix(vc, "avc1") || strings.HasPrefix(vc, "h264"):
		return 3
	case strings.HasPrefix(vc, "av01") || strings.HasPrefix(vc, "av1"):
		return 2
	case strings.HasPrefix(vc, "vp9") || strings.HasPrefix(vc, "vp09"):
		return 1
	default:
		return 0
	}
}

func sortAudioFormats(formats []Format) {
	sort.SliceStable(formats, func(i, j int) bool {
		a, b := formats[i], formats[j]
		// Prefer MP4/M4A AAC audio for MP4 container mux compatibility
		aM4A := strings.EqualFold(a.Ext, "m4a") || strings.EqualFold(a.Ext, "mp4")
		bM4A := strings.EqualFold(b.Ext, "m4a") || strings.EqualFold(b.Ext, "mp4")
		if aM4A != bM4A {
			return aM4A
		}
		// Avoid DRC variants if standard track exists
		aDRC := strings.Contains(strings.ToLower(a.FormatID), "drc")
		bDRC := strings.Contains(strings.ToLower(b.FormatID), "drc")
		if aDRC != bDRC {
			return !aDRC
		}
		aa, ab := derefFloat(pickBitrateKbps(a.ABR, a.TBR)), derefFloat(pickBitrateKbps(b.ABR, b.TBR))
		if aa != ab {
			return aa > ab
		}
		return a.FormatID > b.FormatID
	})
}

func deduplicateProgressiveFormats(formats []Format) []Format {
	out := make([]Format, 0, len(formats))
	seenRes := make(map[string]bool)
	seenURL := make(map[string]bool)
	for _, f := range formats {
		if seenURL[f.URL] {
			continue
		}
		seenURL[f.URL] = true
		w, h := derefInt(f.Width), derefInt(f.Height)
		if w > 0 && h > 0 {
			key := strconv.FormatInt(w, 10) + "x" + strconv.FormatInt(h, 10)
			if seenRes[key] {
				continue
			}
			seenRes[key] = true
		}
		out = append(out, f)
	}
	return out
}

func deduplicateDASHVideoFormats(formats []Format) []Format {
	out := make([]Format, 0, len(formats))
	seenTier := make(map[string]bool)
	seenURL := make(map[string]bool)
	for _, f := range formats {
		if seenURL[f.URL] {
			continue
		}
		seenURL[f.URL] = true
		w, h := derefInt(f.Width), derefInt(f.Height)
		fps := int64(math.Round(derefFloat(f.FPS)))
		if fps <= 30 {
			fps = 30
		}
		hdr := mapDynamicRange(f.DynamicRange)
		if w > 0 && h > 0 {
			key := fmt.Sprintf("%dx%d@%d:%s", w, h, fps, hdr)
			if seenTier[key] {
				continue
			}
			seenTier[key] = true
		}
		out = append(out, f)
	}
	return out
}

func shortEdge(f Format) int64 {
	w, h := derefInt(f.Width), derefInt(f.Height)
	if w > 0 && h > 0 {
		if w < h {
			return w
		}
		return h
	}
	if h > 0 {
		return h
	}
	return w
}

func longEdge(f Format) int64 {
	w, h := derefInt(f.Width), derefInt(f.Height)
	if w > h {
		return w
	}
	return h
}

func qualityLabel(f Format, suffix string) string {
	edge := shortEdge(f)
	base := ""
	if edge > 0 {
		base = strconv.FormatInt(edge, 10) + "p"
		if fps := math.Round(derefFloat(f.FPS)); fps >= 50 {
			base += strconv.FormatInt(int64(fps), 10)
		}
	} else if u, err := url.Parse(f.URL); err == nil {
		for _, seg := range strings.Split(u.Path, "/") {
			if parts := strings.Split(seg, "x"); len(parts) == 2 {
				w, err1 := strconv.ParseInt(parts[0], 10, 64)
				h, err2 := strconv.ParseInt(parts[1], 10, 64)
				if err1 == nil && err2 == nil && w > 0 && h > 0 {
					minEdge := w
					if h < w {
						minEdge = h
					}
					base = strconv.FormatInt(minEdge, 10) + "p"
					break
				}
			}
		}
	}
	if base == "" {
		if suffix != "" {
			return "自适应 (" + suffix + ")"
		}
		return "默认画质"
	}
	if suffix != "" {
		return base + " (" + suffix + ")"
	}
	return base
}

func mapDynamicRange(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "SDR":
		return "sdr"
	case "HDR10":
		return "hdr10"
	case "HDR10+":
		return "hdr10_plus"
	case "HLG":
		return "hlg"
	case "DV", "DOLBY VISION", "DOLBY_VISION":
		return "dolby_vision"
	default:
		return "hdr_unknown"
	}
}

func pickBitrateKbps(primary, fallback *float64) *float64 {
	if primary != nil && *primary > 0 {
		return primary
	}
	return fallback
}

func kbpsToBps(kbps *float64) int64 {
	if kbps == nil || *kbps <= 0 || math.IsNaN(*kbps) || math.IsInf(*kbps, 0) {
		return 0
	}
	bps := math.Round(*kbps * 1000)
	if bps < 1 || bps > float64(maxSafeInteger) {
		return 0
	}
	return int64(bps)
}

func normalizeFPS(v float64) any {
	rounded := math.Round(v*1000) / 1000
	if rounded == math.Trunc(rounded) {
		return int64(rounded)
	}
	return rounded
}

func resolveDurationMs(dur *float64) int64 {
	if dur == nil || *dur <= 0 || math.IsNaN(*dur) || math.IsInf(*dur, 0) {
		return 0
	}
	ms := math.Round(*dur * 1000)
	if ms < 1 || ms > float64(maxSafeInteger) {
		return 0
	}
	return int64(ms)
}

func cleanDisplayTitle(info *Info, contentID string) string {
	for _, candidate := range []string{info.Title, info.Fulltitle, info.Description} {
		trimmed := strings.TrimSpace(candidate)
		if trimmed != "" {
			return trimmed
		}
	}
	return contentID
}

func resolveItemTitle(entry, parent *Info, contentID string, position, total int) string {
	if t := strings.TrimSpace(entry.Title); t != "" {
		return t
	}
	base := cleanDisplayTitle(parent, contentID)
	if total > 1 {
		return fmt.Sprintf("%s #%d", base, position)
	}
	return base
}

func resolveEntryKey(entry *Info, contentID string, position int) string {
	if id := strings.TrimSpace(entry.ID); id != "" && id != contentID {
		return id
	}
	for _, f := range entry.Formats {
		if u, err := url.Parse(f.URL); err == nil && u.Path != "" {
			return path.Base(u.Path)
		}
	}
	return "video-" + strconv.Itoa(position)
}

func pickCoverObject(info *Info, entries []Info, allowedMediaURL func(string) bool, now int64) unified.Object {
	if c := pickSingleCover(info, allowedMediaURL, now); c != nil {
		return c
	}
	for i := range entries {
		if c := pickSingleCover(&entries[i], allowedMediaURL, now); c != nil {
			return c
		}
	}
	return nil
}

func pickSingleCover(info *Info, allowedMediaURL func(string) bool, now int64) unified.Object {
	if info == nil {
		return nil
	}
	if clean := SanitizeMediaURL(info.Thumbnail, allowedMediaURL); clean != "" {
		sources := unified.Sources(unified.Array{clean}, now)
		if len(sources) > 0 {
			return unified.Map(sources[0])
		}
	}
	for i := len(info.Thumbnails) - 1; i >= 0; i-- {
		if clean := SanitizeMediaURL(info.Thumbnails[i].URL, allowedMediaURL); clean != "" {
			sources := unified.Sources(unified.Array{clean}, now)
			if len(sources) > 0 {
				return unified.Map(sources[0])
			}
		}
	}
	return nil
}

func buildAuthorObject(info *Info) unified.Object {
	if info == nil {
		return nil
	}
	author := unified.Object{}
	name := strings.TrimSpace(info.Uploader)
	if name == "" {
		name = strings.TrimSpace(info.Channel)
	}
	if name != "" {
		author["name"] = name
	}
	id := strings.TrimSpace(info.UploaderID)
	if id == "" {
		id = strings.TrimSpace(info.ChannelID)
	}
	if id != "" {
		author["id"] = id
	}
	if len(author) == 0 {
		return nil
	}
	return author
}

func formatTimestamp(primary *float64, entries []Info) string {
	ts := derefFloat(primary)
	if ts <= 0 {
		for _, e := range entries {
			if v := derefFloat(e.Timestamp); v > 0 {
				ts = v
				break
			}
		}
	}
	if ts <= 0 || math.IsNaN(ts) || math.IsInf(ts, 0) {
		return ""
	}
	sec := int64(math.Round(ts))
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02T15:04:05Z")
}

func buildStatisticsObject(info *Info, entries []Info) unified.Object {
	stats := unified.Object{}
	addStat := func(key string, ptr *int64) {
		if ptr != nil && *ptr >= 0 && *ptr <= maxSafeInteger {
			stats[key] = unified.Object{"value": *ptr}
		}
	}
	if info != nil {
		addStat("views", info.ViewCount)
		addStat("likes", info.LikeCount)
		addStat("comments", info.CommentCount)
		addStat("shares", info.RepostCount)
	}
	if len(stats) == 0 && len(entries) > 0 {
		first := entries[0]
		addStat("views", first.ViewCount)
		addStat("likes", first.LikeCount)
		addStat("comments", first.CommentCount)
		addStat("shares", first.RepostCount)
	}
	return stats
}

func derefInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func nonEmptyOr(val, fallback string) string {
	trimmed := strings.TrimSpace(val)
	if trimmed != "" {
		return trimmed
	}
	return fallback
}

func shortDigest(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:24]
}
