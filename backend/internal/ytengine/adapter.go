// Shared yt-dlp -J to unified-v1 conversion pipeline and fail-soft size prober.
package ytengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const (
	maxSafeInteger        int64 = 9007199254740991
	sizeProbeTotalTimeout       = 1500 * time.Millisecond
	sizeProbeConcurrency        = 4
	maxProbedURLs               = 12
	maxProbeBodyBytes           = 4096
)

var (
	strictContentRangePattern = regexp.MustCompile(`^bytes 0-([0-9]{1,4})/([1-9][0-9]{0,15})$`)
	strictPositiveIntPattern  = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)
	googlevideoEdgePattern    = regexp.MustCompile(`^rr[0-9]+---sn-[a-z0-9-]+\.googlevideo\.com$`)
	instagramEdgePattern      = regexp.MustCompile(`^(?:scontent|instagram)[a-z0-9.-]*\.(?:cdninstagram\.com|fbcdn\.net)$`)
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

// NewSizeProber creates a bounded, no-redirect HTTP Range size prober backed by
// fetch.Executor (enforcing public-IP DNS verification, pinned IP dialing, and
// no environment proxy inheritance) and strict media header/magic validation.
func NewSizeProber(executor fetch.Executor, allowedMediaURL func(string) bool) SizeProber {
	if executor == nil {
		executor = fetch.New()
	}
	return func(ctx context.Context, rawURL string) (int64, bool) {
		cleanURL := SanitizeMediaURL(rawURL, allowedMediaURL)
		if cleanURL == "" {
			return 0, false
		}
		resp, err := executor.Do(ctx, fetch.Request{
			Method: http.MethodGet,
			URL:    cleanURL,
			Header: http.Header{
				"Range":           {"bytes=0-0"},
				"Accept":          {"*/*"},
				"Accept-Encoding": {"identity"},
			},
			Policy: fetch.Policy{
				Purpose: "ytengine_size_probe",
				AllowedURL: func(candidate string) bool {
					return SanitizeMediaURL(candidate, allowedMediaURL) != ""
				},
				Timeout:         sizeProbeTotalTimeout,
				MaxBytes:        maxProbeBodyBytes,
				FollowRedirects: false,
			},
		})
		if err != nil {
			return 0, false
		}
		return ValidateProbedMediaResponse(resp.StatusCode, resp.Header, resp.Body)
	}
}

// NewHTTPSizeProber creates a bounded, no-redirect HTTP Range size prober
// restricted to URLs permitted by allowedMediaURL. When proxyURL is empty, it
// delegates directly to NewSizeProber(fetch.New(), allowedMediaURL) and never
// inherits environment proxies. When proxyURL is non-empty, it fails closed
// (returning a no-op prober) rather than bypassing fetch.Executor's hop-by-hop
// DNS and public-IP boundary.
func NewHTTPSizeProber(proxyURL string, allowedMediaURL func(string) bool) SizeProber {
	if strings.TrimSpace(proxyURL) != "" {
		return func(context.Context, string) (int64, bool) { return 0, false }
	}
	return NewSizeProber(fetch.New(), allowedMediaURL)
}

// ValidateProbedMediaResponse validates an HTTP Range probe response and extracts
// an exact media byte size only when headers and body prove a genuine media response.
func ValidateProbedMediaResponse(status int, headers http.Header, body []byte) (int64, bool) {
	if !validCriticalProbeHeaders(headers) {
		return 0, false
	}
	encoding := strings.ToLower(strings.TrimSpace(headers.Get("Content-Encoding")))
	if encoding != "" && encoding != "identity" {
		return 0, false
	}
	if !isAllowedMediaContentType(headers.Get("Content-Type")) {
		return 0, false
	}

	switch status {
	case http.StatusPartialContent:
		cr := strings.TrimSpace(headers.Get("Content-Range"))
		m := strictContentRangePattern.FindStringSubmatch(cr)
		if m == nil {
			return 0, false
		}
		end, err1 := strconv.ParseInt(m[1], 10, 64)
		total, err2 := strconv.ParseInt(m[2], 10, 64)
		if err1 != nil || err2 != nil || end < 0 || total <= end || total > maxSafeInteger {
			return 0, false
		}
		expectedWindow := end + 1
		if cl := strings.TrimSpace(headers.Get("Content-Length")); cl != "" {
			clVal, ok := parsePositiveContentLength(cl)
			if !ok || clVal != expectedWindow {
				return 0, false
			}
		}
		if int64(len(body)) != expectedWindow {
			return 0, false
		}
		return total, true

	case http.StatusOK:
		if strings.TrimSpace(headers.Get("Content-Range")) != "" ||
			strings.TrimSpace(headers.Get("Transfer-Encoding")) != "" {
			return 0, false
		}
		size, ok := parsePositiveContentLength(headers.Get("Content-Length"))
		if !ok || size < 12 || size > maxProbeBodyBytes || int64(len(body)) != size {
			return 0, false
		}
		if !hasValidMediaMagic(body) {
			return 0, false
		}
		return size, true
	}
	return 0, false
}

func validCriticalProbeHeaders(headers http.Header) bool {
	for _, name := range []string{"Content-Length", "Content-Range", "Content-Encoding", "Content-Type", "Transfer-Encoding"} {
		values := headers.Values(name)
		if len(values) > 1 {
			return false
		}
		if len(values) == 1 && name != "Content-Type" && strings.Contains(values[0], ",") {
			return false
		}
	}
	return true
}

func isAllowedMediaContentType(raw string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(raw, ";")[0]))
	if ct == "" {
		return false
	}
	return strings.HasPrefix(ct, "video/") ||
		strings.HasPrefix(ct, "audio/") ||
		ct == "application/mp4" ||
		ct == "application/octet-stream"
}

func hasValidMediaMagic(body []byte) bool {
	if len(body) >= 12 && bytes.Equal(body[4:8], []byte("ftyp")) {
		return true
	}
	if len(body) >= 4 && bytes.Equal(body[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return true
	}
	return false
}

func parsePositiveContentLength(raw string) (int64, bool) {
	trimmed := strings.TrimSpace(raw)
	if !strictPositiveIntPattern.MatchString(trimmed) {
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

	items := make(unified.Array, 0, len(entries)+1)
	attachments := make(unified.Array, 0, 1)
	seenItemIDs := make(map[string]bool, len(entries)+1)
	seenAttachmentIDs := make(map[string]bool, 1)
	selectedItemID := ""
	hasDeliverableItem := false

	for idx, entry := range entries {
		position := idx + 1
		item, att := buildEntryVideoItem(&entry, info, opts, position, len(entries), now)
		if item == nil {
			if opts.SelectedIndex > 0 && position == opts.SelectedIndex {
				unavail := buildUnavailableEntryVideoItem(&entry, info, opts, position, len(entries), now)
				if unavail != nil {
					unavailID := unified.Str(unavail["id"])
					if unavailID != "" && !seenItemIDs[unavailID] {
						seenItemIDs[unavailID] = true
						items = append(items, unavail)
						selectedItemID = unavailID
					}
				}
			}
			continue
		}
		itemID := unified.Str(item["id"])
		if itemID == "" || seenItemIDs[itemID] {
			continue
		}
		seenItemIDs[itemID] = true
		hasDeliverableItem = true
		items = append(items, item)
		if att != nil {
			attID := unified.Str(att["id"])
			if attID != "" && !seenAttachmentIDs[attID] {
				seenAttachmentIDs[attID] = true
				attachments = append(attachments, att)
			}
		}
		if opts.SelectedIndex > 0 && position == opts.SelectedIndex {
			selectedItemID = itemID
		}
	}

	if !hasDeliverableItem {
		return unified.ContractError("NO_MEDIA", "该内容未包含可解析的视频媒体", false)
	}

	if opts.SelectedIndex > len(entries) {
		unavailID := "video-" + shortDigest(fmt.Sprintf("%s:item:%s:unavailable-%d", opts.PlatformID, opts.ContentID, opts.SelectedIndex))
		if !seenItemIDs[unavailID] {
			seenItemIDs[unavailID] = true
			items = append(items, unified.Object{
				"id":                         unavailID,
				"type":                       "video",
				"index":                      int64(opts.SelectedIndex),
				"default":                    nil,
				"state":                      "unavailable",
				"default_unavailable_reason": "unavailable",
				"error": unified.Object{
					"code":    "CONTENT_UNAVAILABLE",
					"message": fmt.Sprintf("所选第 %d 个视频不存在或不可用", opts.SelectedIndex),
				},
			})
			selectedItemID = unavailID
		}
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
	if len(attachments) > 0 {
		data["attachments"] = attachments
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

func buildUnavailableEntryVideoItem(entry, parent *Info, opts AdaptOptions, position, total int, now int64) unified.Object {
	entryKey := "video-" + strconv.Itoa(position)
	if entry != nil {
		entryKey = resolveEntryKey(entry, opts.ContentID, position)
	}
	itemID := "video-" + shortDigest(opts.PlatformID+":item:"+opts.ContentID+":"+entryKey)
	item := unified.Object{
		"id":                         itemID,
		"type":                       "video",
		"index":                      int64(position),
		"default":                    nil,
		"state":                      "unavailable",
		"default_unavailable_reason": "unavailable",
		"error": unified.Object{
			"code":    "CONTENT_UNAVAILABLE",
			"message": fmt.Sprintf("所选第 %d 个视频不存在或不可用", position),
		},
	}
	if entry != nil {
		if itemTitle := resolveItemTitle(entry, parent, opts.ContentID, position, total); itemTitle != "" {
			item["title"] = itemTitle
		}
		if durationMs := resolveDurationMs(entry.Duration); durationMs > 0 {
			item["duration_ms"] = durationMs
		}
		if cover := pickSingleCover(entry, opts.AllowedMediaURL, now); cover != nil {
			item["cover"] = cover
		} else if cover := pickSingleCover(parent, opts.AllowedMediaURL, now); cover != nil {
			item["cover"] = cover
		}
	}
	return item
}

func buildEntryVideoItem(entry, parent *Info, opts AdaptOptions, position, total int, now int64) (unified.Object, unified.Object) {
	if entry == nil {
		return nil, nil
	}
	entryKey := resolveEntryKey(entry, opts.ContentID, position)
	itemTitle := resolveItemTitle(entry, parent, opts.ContentID, position, total)
	variants, audioAttachment := buildEntryVariants(entry, parent, entryKey, itemTitle, opts, now)
	if len(variants) == 0 {
		return nil, nil
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

	if itemTitle != "" {
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
	return item, audioAttachment
}

func boolPtr(v bool) *bool { return &v }

func resolveFormatHasAudio(acodec string) *bool {
	if isNoCodec(acodec) {
		return boolPtr(false)
	}
	if cleanCodec(acodec) != "" {
		return boolPtr(true)
	}
	return nil
}

func buildEntryVariants(entry, parent *Info, entryKey, itemTitle string, opts AdaptOptions, now int64) (unified.Array, unified.Object) {
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

	variants := make(unified.Array, 0, len(mp4Formats)+len(dashVideoFormats)+len(hlsVideoFormats)+1)
	seenVariantIDs := make(map[string]bool)
	var audioAttachment unified.Object

	dedupedMP4 := deduplicateProgressiveFormats(mp4Formats)
	var filteredDASHVideo []Format
	if opts.EnableDASHMux && len(dashVideoFormats) > 0 {
		var maxMP4Edge int64
		var maxMP4FPS float64
		if len(dedupedMP4) > 0 {
			maxMP4Edge = shortEdge(dedupedMP4[0])
			maxMP4FPS = derefFloat(dedupedMP4[0].FPS)
		}
		dedupedDASH := deduplicateDASHVideoFormats(dashVideoFormats)
		for _, vf := range dedupedDASH {
			if maxMP4Edge > 0 {
				edge := shortEdge(vf)
				fps := derefFloat(vf.FPS)
				higherFPS := edge == maxMP4Edge && fps > maxMP4FPS+5 && fps >= 50
				if edge <= maxMP4Edge && !higherFPS {
					continue
				}
			}
			filteredDASHVideo = append(filteredDASHVideo, vf)
		}
	}

	hasSplitSilentTracks := len(filteredDASHVideo) > 0

	// 1. High-Resolution DASH Video-Only Tracks as Directly Downloadable Files (kind == "file", has_audio == false).
	// True width and height are preserved in metadata; unified.SelectDefault explicitly
	// prefers complete audiovisual files over split silent video-only tracks for item.default.
	for i, vf := range filteredDASHVideo {
		videoKey := nonEmptyOr(vf.FormatID, fmt.Sprintf("dashv-%d", i+1))
		sources := buildFormatSourcesWithContext(vf, opts, entryKey, videoKey, "", now)
		if len(sources) == 0 {
			continue
		}
		metadata := buildFormatMetadata(vf, boolPtr(false), true)
		variantID := "v-" + shortDigest(opts.PlatformID+":dashv-file:"+entryKey+":"+videoKey)
		if seenVariantIDs[variantID] {
			continue
		}
		seenVariantIDs[variantID] = true

		variant := unified.Object{
			"id":       variantID,
			"label":    qualityLabel(vf, formatSilentVideoSuffix(vf)),
			"metadata": metadata,
			"delivery": unified.Object{
				"kind":    "file",
				"sources": sources,
			},
		}
		variants = append(variants, variant)
	}

	// 2. Deliverable Progressive MP4s with Embedded Audio (kind == "file")
	for i, f := range dedupedMP4 {
		formatKey := nonEmptyOr(f.FormatID, fmt.Sprintf("mp4-%d", i+1))
		hasAudio := resolveFormatHasAudio(f.ACodec)
		titleForAttachment := ""
		if hasAudio != nil && *hasAudio {
			titleForAttachment = itemTitle
			if opts.PlatformID == "youtube" && titleForAttachment != "" && shortEdge(f) > 0 {
				titleForAttachment = fmt.Sprintf("%s_%dp", titleForAttachment, shortEdge(f))
			}
		}
		sources := buildFormatSourcesWithContext(f, opts, entryKey, formatKey, titleForAttachment, now)
		if len(sources) == 0 {
			continue
		}
		metadata := buildFormatMetadata(f, hasAudio, true)
		variantID := "v-" + shortDigest(opts.PlatformID+":file:"+entryKey+":"+formatKey)
		if seenVariantIDs[variantID] {
			continue
		}
		seenVariantIDs[variantID] = true

		suffix := ""
		if hasSplitSilentTracks && hasAudio != nil && *hasAudio {
			suffix = "含音频"
		}
		variant := unified.Object{
			"id":       variantID,
			"metadata": metadata,
			"delivery": unified.Object{
				"kind":    "file",
				"sources": sources,
			},
		}
		if label := qualityLabel(f, suffix); label != "" {
			variant["label"] = label
		}
		variants = append(variants, variant)
	}

	// 3. Standalone Audio Tracks (both as downloadable file variants in quality options and as a background_audio attachment)
	if opts.EnableDASHMux && hasSplitSilentTracks && len(dashAudioFormats) > 0 {
		seenAudioContainer := make(map[string]bool, 2)
		for _, candAudio := range dashAudioFormats {
			audioMeta := buildAudioFileMetadata(candAudio)
			audioContainer := unified.Str(audioMeta["container"])
			if audioContainer == "" {
				audioContainer = "mp4"
			}
			if seenAudioContainer[audioContainer] {
				continue
			}
			candKey := nonEmptyOr(candAudio.FormatID, "audio-"+audioContainer)
			audioSources := buildFormatSourcesWithContext(candAudio, opts, entryKey, candKey, "", now)
			if len(audioSources) == 0 {
				continue
			}
			audioVariantID := "v-" + shortDigest(opts.PlatformID+":dasha-file:"+entryKey+":"+candKey)
			if seenVariantIDs[audioVariantID] {
				continue
			}
			seenAudioContainer[audioContainer] = true
			seenVariantIDs[audioVariantID] = true
			audioVariant := unified.Object{
				"id":       audioVariantID,
				"label":    formatAudioVariantLabel(candAudio),
				"metadata": audioMeta,
				"delivery": unified.Object{
					"kind":    "file",
					"sources": audioSources,
				},
			}
			variants = append(variants, audioVariant)
		}
		bestAudio := dashAudioFormats[0]
		audioKey := nonEmptyOr(bestAudio.FormatID, "audio")
		if audioResource := buildMuxTrackResourceWithContext(bestAudio, "audio", opts, entryKey, audioKey, "", now); audioResource != nil {
			attID := "att-" + shortDigest(opts.PlatformID+":att-audio:"+opts.ContentID+":"+entryKey)
			attTitle := formatAudioAttachmentTitle(itemTitle, bestAudio)
			audioAttachment = unified.Object{
				"id":       attID,
				"role":     "background_audio",
				"title":    attTitle,
				"resource": audioResource,
			}
			if author := buildAuthorObject(parent); author != nil {
				audioAttachment["author"] = author
			}
			if cover := pickSingleCover(entry, opts.AllowedMediaURL, now); cover != nil {
				audioAttachment["cover"] = cover
			} else if cover := pickSingleCover(parent, opts.AllowedMediaURL, now); cover != nil {
				audioAttachment["cover"] = cover
			}
		}
	}

	// 4. Non-single-file HLS variants (kind == "hls", scope == "adaptive")
	if opts.EnableHLS && len(hlsVideoFormats) > 0 {
		var bestHLSAudio *Format
		if len(hlsAudioFormats) > 0 {
			bestHLSAudio = &hlsAudioFormats[0]
		}
		dedupedHLS := deduplicateProgressiveFormats(hlsVideoFormats)
		for i, f := range dedupedHLS {
			formatKey := nonEmptyOr(f.FormatID, fmt.Sprintf("hls-%d", i+1))
			sources := buildFormatSourcesWithContext(f, opts, entryKey, formatKey, "", now)
			if len(sources) == 0 {
				continue
			}
			hasEmbeddedAudio := !isNoCodec(f.ACodec) && strings.TrimSpace(f.ACodec) != ""
			metadata := buildFormatMetadata(f, boolPtr(hasEmbeddedAudio), false)
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

	return variants, audioAttachment
}

func buildMuxTrackResource(f Format, role string, now int64) unified.Object {
	return buildMuxTrackResourceWithTitle(f, role, "", now)
}

func buildMuxTrackResourceWithTitle(f Format, role, itemTitle string, now int64) unified.Object {
	return buildMuxTrackResourceWithContext(f, role, AdaptOptions{}, "", f.FormatID, itemTitle, now)
}

func buildMuxTrackResourceWithContext(f Format, role string, opts AdaptOptions, entryKey, formatKey, itemTitle string, now int64) unified.Object {
	sources := buildFormatSourcesWithContext(f, opts, entryKey, formatKey, itemTitle, now)
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

func formatSilentVideoSuffix(vf Format) string {
	var parts []string
	if shortEdge(vf) >= 2160 || longEdge(vf) >= 3840 {
		parts = append(parts, "4K")
	} else if shortEdge(vf) >= 1440 || longEdge(vf) >= 2560 {
		parts = append(parts, "2K")
	}
	if hdr := mapDynamicRange(vf.DynamicRange); hdr != "" && hdr != "sdr" {
		parts = append(parts, "HDR")
	}
	parts = append(parts, "仅视频无声")
	return strings.Join(parts, " · ")
}

func formatAudioVariantLabel(af Format) string {
	ext := strings.ToUpper(strings.TrimSpace(af.Ext))
	if ext == "" || ext == "MP4" {
		ext = "M4A"
	}
	if abr := derefFloat(pickBitrateKbps(af.ABR, af.TBR)); abr > 0 {
		return fmt.Sprintf("仅音频 %s (%.0f kbps)", ext, math.Round(abr))
	}
	return "仅音频 " + ext
}

func formatAudioAttachmentTitle(itemTitle string, af Format) string {
	base := strings.TrimSpace(itemTitle)
	if base == "" {
		base = "原声轨"
	}
	ext := strings.ToUpper(strings.TrimSpace(af.Ext))
	if ext == "" || ext == "MP4" {
		ext = "M4A"
	}
	if abr := derefFloat(pickBitrateKbps(af.ABR, af.TBR)); abr > 0 {
		return fmt.Sprintf("%s (%s · %.0f kbps)", base, ext, math.Round(abr))
	}
	return fmt.Sprintf("%s (%s)", base, ext)
}

func buildAudioFileMetadata(af Format) unified.Object {
	ext := strings.ToLower(strings.TrimSpace(af.Ext))
	container := "mp4"
	extension := "m4a"
	mimeType := "audio/mp4"
	if ext == "webm" {
		container = "webm"
		extension = "webm"
		mimeType = "audio/webm"
	}
	meta := unified.Object{
		"has_audio": true,
		"evidence":  "upstream",
		"container": container,
		"extension": extension,
		"mime_type": mimeType,
	}
	if acodec := cleanCodec(af.ACodec); acodec != "" {
		meta["audio_codec"] = acodec
	}
	if abr := kbpsToBps(pickBitrateKbps(af.ABR, af.TBR)); abr > 0 {
		meta["audio_bitrate_bps"] = abr
	}
	if af.ASR != nil && *af.ASR > 0 {
		meta["sample_rate_hz"] = *af.ASR
	}
	if af.AudioChannels != nil && *af.AudioChannels > 0 {
		meta["audio_channels"] = *af.AudioChannels
	}
	return meta
}

func buildFormatSources(f Format, now int64) unified.Array {
	return buildFormatSourcesWithTitle(f, "", now)
}

func buildFormatSourcesWithTitle(f Format, itemTitle string, now int64) unified.Array {
	return buildFormatSourcesWithContext(f, AdaptOptions{}, "", f.FormatID, itemTitle, now)
}

func buildFormatSourcesWithContext(f Format, opts AdaptOptions, entryKey, formatKey, itemTitle string, now int64) unified.Array {
	rawSource := unified.Object{
		"url": decorateYouTubeSourceURL(f.URL, itemTitle),
	}
	urlClen := extractURLClen(f.URL)
	var sizeConflict int64
	if f.Filesize != nil && *f.Filesize > 0 && *f.Filesize <= maxSafeInteger {
		if urlClen > 0 && *f.Filesize != urlClen {
			sizeConflict = *f.Filesize
		} else {
			rawSource["size_bytes"] = *f.Filesize
		}
	}
	if id := buildYTSourceIdentity(f, opts.PlatformID, opts.ContentID, entryKey, formatKey, sizeConflict); id != "" {
		rawSource["private_source_identity"] = id
	}
	return unified.Sources(unified.Array{rawSource}, now)
}

func extractURLClen(rawURL string) int64 {
	u, err := url.Parse(rawURL)
	if err != nil || u.RawQuery == "" {
		return 0
	}
	clenStr := strings.TrimSpace(u.Query().Get("clen"))
	if clenStr == "" {
		return 0
	}
	n, ok := parsePositiveContentLength(clenStr)
	if !ok {
		return 0
	}
	return n
}

func buildYTSourceIdentity(f Format, platformID, contentID, entryKey, formatKey string, sizeConflict int64) string {
	if platformID == "" || contentID == "" || entryKey == "" || f.URL == "" {
		return ""
	}
	canonicalURL := canonicalRenewalURLIdentity(f.URL)
	if canonicalURL == "" {
		return ""
	}
	identity := unified.Array{
		"ytengine-source",
		platformID,
		contentID,
		entryKey,
		formatKey,
		strings.ToLower(strings.TrimSpace(f.Ext)),
		cleanCodec(f.VCodec),
		cleanCodec(f.ACodec),
		derefInt(f.Width),
		derefInt(f.Height),
		canonicalURL,
	}
	if sizeConflict > 0 {
		identity = append(identity, sizeConflict)
	}
	return unified.PHPIdentityJSON(identity, true)
}

func canonicalRenewalURLIdentity(rawURL string) string {
	stripped := unified.SourceIdentity(rawURL)
	u, err := url.Parse(stripped)
	if err != nil {
		return stripped
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	switch {
	case googlevideoEdgePattern.MatchString(host):
		host = "googlevideo.com"
	case instagramEdgePattern.MatchString(host):
		if strings.HasSuffix(host, ".cdninstagram.com") {
			host = "cdninstagram.com"
		} else if strings.HasSuffix(host, ".fbcdn.net") {
			host = "fbcdn.net"
		}
	}
	base := strings.ToLower(u.Scheme) + "://" + host + u.EscapedPath()
	if u.RawQuery == "" {
		return base
	}
	parts := strings.Split(u.RawQuery, "&")
	filtered := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			filtered = append(filtered, p)
		}
	}
	sort.Strings(filtered)
	if len(filtered) == 0 {
		return base
	}
	return base + "?" + strings.Join(filtered, "&")
}

func decorateYouTubeSourceURL(rawURL, itemTitle string) string {
	if strings.TrimSpace(itemTitle) == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.RawQuery == "" {
		return rawURL
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host != "googlevideo.com" && !strings.HasSuffix(host, ".googlevideo.com") {
		return rawURL
	}
	q := u.Query()
	if q.Get("title") != "" {
		return rawURL
	}
	for _, param := range strings.Split(q.Get("sparams"), ",") {
		if strings.EqualFold(strings.TrimSpace(param), "title") {
			return rawURL
		}
	}
	cleanTitle := sanitizeDownloadTitle(itemTitle)
	if cleanTitle == "" {
		return rawURL
	}
	return rawURL + "&title=" + url.QueryEscape(cleanTitle)
}

func sanitizeDownloadTitle(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	count := 0
	for _, r := range trimmed {
		if count >= 80 {
			break
		}
		switch {
		case r < 0x20 || r == 0x7f,
			r == '/', r == '\\', r == ':', r == '*', r == '?', r == '"', r == '<', r == '>', r == '|':
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
				count++
			}
		case r == ' ' || r == '\t':
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
				count++
			}
		default:
			b.WriteRune(r)
			lastUnderscore = false
			count++
		}
	}
	return strings.Trim(b.String(), "_.")
}

func buildFormatMetadata(f Format, hasAudio *bool, isFile bool) unified.Object {
	meta := unified.Object{
		"evidence": "upstream",
	}
	if hasAudio != nil {
		meta["has_audio"] = *hasAudio
	}
	if isFile {
		ext := strings.ToLower(strings.TrimSpace(f.Ext))
		switch ext {
		case "", "mp4":
			meta["container"] = "mp4"
			meta["extension"] = "mp4"
			meta["mime_type"] = "video/mp4"
		case "webm":
			meta["container"] = "webm"
			meta["extension"] = "webm"
			meta["mime_type"] = "video/webm"
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
	if hasAudio != nil && *hasAudio {
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
		// Prefer MP4/M4A AAC audio for universal playback compatibility
		aM4A := strings.EqualFold(a.Ext, "m4a") || strings.EqualFold(a.Ext, "mp4")
		bM4A := strings.EqualFold(b.Ext, "m4a") || strings.EqualFold(b.Ext, "mp4")
		if aM4A != bM4A {
			return aM4A
		}
		aAAC := strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.ACodec)), "mp4a")
		bAAC := strings.HasPrefix(strings.ToLower(strings.TrimSpace(b.ACodec)), "mp4a")
		if aAAC != bAAC {
			return aAAC
		}
		aOpus := strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.ACodec)), "opus")
		bOpus := strings.HasPrefix(strings.ToLower(strings.TrimSpace(b.ACodec)), "opus")
		if aOpus != bOpus {
			return aOpus
		}
		// Prefer standard stereo (<= 2ch) over 5.1 surround (6ch) for browser <audio> support
		aStereo := derefInt(a.AudioChannels) == 0 || derefInt(a.AudioChannels) <= 2
		bStereo := derefInt(b.AudioChannels) == 0 || derefInt(b.AudioChannels) <= 2
		if aStereo != bStereo {
			return aStereo
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
		hdr := mapDynamicRange(f.DynamicRange)
		if w > 0 && h > 0 {
			key := fmt.Sprintf("%dx%d:%s", w, h, hdr)
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
