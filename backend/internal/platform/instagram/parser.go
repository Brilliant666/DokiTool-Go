// Package instagram implements the Instagram Reels, Post, and Carousel parser
// backed by the hardened internal yt-dlp metadata engine, an anonymous native
// fallback pipeline (Embed Captioned + GraphQL), and the unified-v1 contract.
package instagram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

var (
	shortcodePattern        = regexp.MustCompile(`^[A-Za-z0-9_-]{5,32}$`)
	embedContextJSONPattern = regexp.MustCompile(`"contextJSON"\s*:\s*("(?:\\.|[^"\\])*")`)
	embedVideoURLPattern    = regexp.MustCompile(`"video_url"\s*:\s*("(?:\\.|[^"\\])*")`)
	embedImageTagPattern    = regexp.MustCompile(`(?is)<img[^>]+class="[^"]*EmbeddedMediaImage[^"]*"[^>]+src="([^"]+)"`)
	embedImageTagAltPattern = regexp.MustCompile(`(?is)<img[^>]+src="([^"]+)"[^>]+class="[^"]*EmbeddedMediaImage[^"]*"`)
	instagramEdgeHostRegex  = regexp.MustCompile(`^(?:scontent|instagram)[a-z0-9.-]*\.(?:cdninstagram\.com|fbcdn\.net)$`)
)

const (
	instagramAppID      = "936619743392459"
	instagramASBDID     = "129477"
	instagramDefaultLSD = "AVqbxe3J_YA"
	instagramGraphDocID = "8845758582119845"
	nativeFetchTimeout  = 10 * time.Second
	nativeMaxBodyBytes  = 2 << 20 // 2 MiB
	maxCarouselEntries  = 10
)

// Parser adapts yt-dlp -J metadata and anonymous native Instagram responses
// (Captioned Embed + GraphQL) for Reels, Posts, and Carousels into unified-v1.
type Parser struct {
	engine     ytengine.Extractor
	sizeProber ytengine.SizeProber
	fetcher    fetch.Executor
}

// New creates an Instagram platform parser bound to the given ytengine.Extractor.
func New(engine ytengine.Extractor) *Parser {
	return &Parser{engine: engine}
}

// NewWithSizeProber creates an Instagram platform parser that probes exact byte
// lengths for progressive MP4 formats on cdninstagram.com.
func NewWithSizeProber(engine ytengine.Extractor, prober ytengine.SizeProber) *Parser {
	return &Parser{engine: engine, sizeProber: prober}
}

// NewWithFetcher creates an Instagram platform parser with both yt-dlp extraction
// and SSRF-safe anonymous native extraction (Embed Captioned + GraphQL).
func NewWithFetcher(engine ytengine.Extractor, prober ytengine.SizeProber, fetcher fetch.Executor) *Parser {
	return &Parser{engine: engine, sizeProber: prober, fetcher: fetcher}
}

// WithFetcher returns a copy of the Parser configured with an SSRF-safe fetch.Executor
// for anonymous native Embed/GraphQL extraction when yt-dlp is unavailable or
// encounters photo/carousel posts or login walls.
func (p *Parser) WithFetcher(fetcher fetch.Executor) *Parser {
	if p == nil {
		return &Parser{fetcher: fetcher}
	}
	cp := *p
	cp.fetcher = fetcher
	return &cp
}

// ExtractPostTarget validates an Instagram Post/Reel URL and returns:
//   - shortcode: the post/reel shortcode
//   - canonicalURL: the sanitized URL safe to pass to yt-dlp
//   - ok: whether the input URL is a valid Instagram post/reel URL
func ExtractPostTarget(raw string) (string, string, bool) {
	if unified.MediaURL(raw) == "" || len(raw) > 2048 {
		return "", "", false
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return "", "", false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return "", "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || (scheme == "https" && n != 443) || (scheme == "http" && n != 80) {
			return "", "", false
		}
	}

	host := strings.ToLower(u.Hostname())
	switch host {
	case "instagram.com", "www.instagram.com", "m.instagram.com":
	default:
		return "", "", false
	}

	segments := splitNonEmptyPath(u.EscapedPath())
	var kind, shortcode string

	switch {
	case len(segments) >= 2 && isInstagramPostKind(segments[0]):
		kind = normalizeKind(segments[0])
		shortcode = segments[1]
	case len(segments) >= 3 && isInstagramPostKind(segments[1]):
		kind = normalizeKind(segments[1])
		shortcode = segments[2]
	default:
		return "", "", false
	}

	if !shortcodePattern.MatchString(shortcode) {
		return "", "", false
	}
	canonicalURL := "https://www.instagram.com/" + kind + "/" + shortcode + "/"
	return shortcode, canonicalURL, true
}

func isInstagramPostKind(seg string) bool {
	switch strings.ToLower(seg) {
	case "p", "reel", "reels", "tv":
		return true
	default:
		return false
	}
}

func normalizeKind(seg string) string {
	switch strings.ToLower(seg) {
	case "reel", "reels":
		return "reel"
	case "tv":
		return "tv"
	default:
		return "p"
	}
}

func isReelOrTVCanonical(canonicalURL string) bool {
	return strings.Contains(canonicalURL, "/reel/") || strings.Contains(canonicalURL, "/tv/")
}

// AllowedTargetURL enforces that only canonical Instagram post/reel URLs reach ytengine.
func AllowedTargetURL(raw string) bool {
	_, canonical, ok := ExtractPostTarget(raw)
	return ok && raw == canonical
}

// AllowedOutboundHost restricts yt-dlp subprocess outbound connections to official Instagram/Meta CDN domains.
func AllowedOutboundHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, domain := range []string{"instagram.com", "cdninstagram.com", "fbcdn.net"} {
		if h == domain || strings.HasSuffix(h, "."+domain) {
			return true
		}
	}
	return false
}

// AllowedPageURL restricts native Embed and GraphQL HTTP requests to official Instagram HTTPS web hosts.
func AllowedPageURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return false
	}
	if strings.ToLower(u.Scheme) != "https" {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n != 443 {
			return false
		}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	return host == "www.instagram.com" || host == "instagram.com" || host == "i.instagram.com"
}

// AllowedMediaURL restricts public media and cover URLs to official Instagram CDN hosts.
func AllowedMediaURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || (scheme == "https" && n != 443) || (scheme == "http" && n != 80) {
			return false
		}
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{".cdninstagram.com", ".fbcdn.net", ".instagram.com"} {
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return true
		}
	}
	return false
}

func splitNonEmptyPath(escapedPath string) []string {
	trimmed := strings.Trim(escapedPath, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return nil
		}
		out = append(out, p)
	}
	return out
}

// Parse extracts Instagram Reel/Post/Carousel metadata via the 3-tier pipeline
// (ytengine yt-dlp -> anonymous Captioned Embed -> anonymous GraphQL) and
// formats a unified-v1 envelope.
func (p *Parser) Parse(ctx context.Context, source string, now int64) (unified.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	shortcode, canonicalURL, ok := ExtractPostTarget(source)
	if !ok {
		return unified.ContractError("INVALID_INPUT", "请输入有效的 Instagram 帖子或 Reels 链接", false), nil
	}
	engineReady := p != nil && p.engine != nil && p.engine.Enabled()
	fetcherReady := p != nil && p.fetcher != nil
	if !engineReady && !fetcherReady {
		return unified.ContractError("SERVICE_NOT_CONFIGURED", "当前平台解析服务暂不可用", false), nil
	}

	opts := ytengine.AdaptOptions{
		PlatformID:                   "instagram",
		ContentID:                    shortcode,
		SourceURL:                    source,
		MaxEntries:                   maxCarouselEntries,
		AllowedMediaURL:              AllowedMediaURL,
		EnableDASHMux:                true,
		EnableHLS:                    false,
		AllowCodecLessProgressiveMP4: true,
	}

	var ytErr error
	if engineReady {
		info, err := p.engine.Extract(ctx, canonicalURL)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
				return nil, ctxErr
			}
			var engineErr *ytengine.Error
			if errors.As(err, &engineErr) && engineErr.LocalBusy {
				resp := unified.ContractError(string(engineErr.Code), engineErr.Message, engineErr.Retryable)
				if engineErr.Retryable && engineErr.RetryAfter > 0 {
					unified.Map(resp["error"])["retry_after_seconds"] = engineErr.RetryAfter
				}
				return resp, nil
			}
			ytErr = err
		} else {
			ytengine.PopulateMissingMP4Sizes(ctx, info, p.sizeProber, opts)
			adapted := ytengine.AdaptInfo(info, opts, now)
			if unified.Str(adapted["code"]) == "OK" {
				return adapted, nil
			}
			if !fetcherReady {
				return adapted, nil
			}
		}
	}

	if fetcherReady {
		if nativeEnv, ok := p.extractNative(ctx, shortcode, canonicalURL, source, now); ok {
			return nativeEnv, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
			return nil, ctxErr
		}
	}

	if ytErr != nil {
		var engineErr *ytengine.Error
		if errors.As(ytErr, &engineErr) {
			resp := unified.ContractError(string(engineErr.Code), engineErr.Message, engineErr.Retryable)
			if engineErr.Retryable && engineErr.RetryAfter > 0 {
				unified.Map(resp["error"])["retry_after_seconds"] = engineErr.RetryAfter
			}
			return resp, nil
		}
	}
	return unified.ContractError("UPSTREAM_UNAVAILABLE", "当前平台解析服务暂不可用，请稍后重试", true), nil
}

type nativeMediaItem struct {
	ID       string
	Kind     string // "video" or "image"
	VideoURL string
	ImageURL string
	Width    int64
	Height   int64
	Duration float64
}

type nativePost struct {
	ID           string
	Shortcode    string
	Title        string
	Description  string
	AuthorID     string
	AuthorName   string
	AuthorAvatar string
	CoverURL     string
	Timestamp    int64
	LikeCount    *int64
	CommentCount *int64
	ViewCount    *int64
	Items        []nativeMediaItem
}

func (np *nativePost) hasVideo() bool {
	if np == nil {
		return false
	}
	for _, it := range np.Items {
		if it.Kind == "video" && it.VideoURL != "" {
			return true
		}
	}
	return false
}

func (p *Parser) extractNative(ctx context.Context, shortcode, canonicalURL, sourceURL string, now int64) (unified.Object, bool) {
	needVideo := isReelOrTVCanonical(canonicalURL)

	// Tier 1: Captioned Embed endpoint (GET /p/{shortcode}/embed/captioned/)
	if post, err := p.fetchEmbedCaptioned(ctx, shortcode); err == nil && post != nil && len(post.Items) > 0 {
		if !needVideo || post.hasVideo() {
			if env := p.buildNativeEnvelope(ctx, post, shortcode, sourceURL, now); unified.Str(env["code"]) == "OK" {
				return env, true
			}
		}
	}

	// Tier 2: Anonymous GraphQL endpoint (POST /api/graphql PolarisPostActionLoadPostQueryQuery)
	if post, err := p.fetchAnonymousGraphQL(ctx, shortcode); err == nil && post != nil && len(post.Items) > 0 {
		if env := p.buildNativeEnvelope(ctx, post, shortcode, sourceURL, now); unified.Str(env["code"]) == "OK" {
			return env, true
		}
	}

	return nil, false
}

func (p *Parser) fetchEmbedCaptioned(ctx context.Context, shortcode string) (*nativePost, error) {
	embedURL := "https://www.instagram.com/p/" + shortcode + "/embed/captioned/"
	hdr := make(http.Header)
	hdr.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	hdr.Set("Accept-Language", "en-US,en;q=0.9")
	hdr.Set("Referer", "https://www.instagram.com/")
	resp, err := p.fetcher.Do(ctx, fetch.Request{
		Method: http.MethodGet,
		URL:    embedURL,
		Header: hdr,
		Policy: fetch.Policy{
			Purpose:         "instagram_embed_captioned",
			AllowedURL:      AllowedPageURL,
			Timeout:         nativeFetchTimeout,
			MaxBytes:        nativeMaxBodyBytes,
			FollowRedirects: true,
			MaxRedirects:    3,
		},
	})
	if err != nil || resp.StatusCode != http.StatusOK || len(resp.Body) == 0 {
		return nil, fmt.Errorf("embed request failed")
	}
	return parseInstagramEmbedHTML(string(resp.Body), shortcode)
}

func (p *Parser) fetchAnonymousGraphQL(ctx context.Context, shortcode string) (*nativePost, error) {
	varsJSON, err := json.Marshal(map[string]any{
		"shortcode": shortcode,
	})
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("av", "0")
	form.Set("lsd", instagramDefaultLSD)
	form.Set("fb_api_caller_class", "RelayModern")
	form.Set("fb_api_req_friendly_name", "PolarisPostActionLoadPostQueryQuery")
	form.Set("variables", string(varsJSON))
	form.Set("server_timestamps", "true")
	form.Set("doc_id", instagramGraphDocID)

	hdr := make(http.Header)
	hdr.Set("Content-Type", "application/x-www-form-urlencoded")
	hdr.Set("Accept", "*/*")
	hdr.Set("Accept-Language", "en-US,en;q=0.9")
	hdr.Set("X-IG-App-ID", instagramAppID)
	hdr.Set("X-FB-LSD", instagramDefaultLSD)
	hdr.Set("X-ASBD-ID", instagramASBDID)
	hdr.Set("X-Requested-With", "XMLHttpRequest")
	hdr.Set("Sec-Fetch-Site", "same-origin")
	hdr.Set("Referer", "https://www.instagram.com/p/"+shortcode+"/")

	resp, err := p.fetcher.Do(ctx, fetch.Request{
		Method: http.MethodPost,
		URL:    "https://www.instagram.com/api/graphql",
		Header: hdr,
		Body:   []byte(form.Encode()),
		Policy: fetch.Policy{
			Purpose:         "instagram_graphql_anonymous",
			AllowedURL:      AllowedPageURL,
			Timeout:         nativeFetchTimeout,
			MaxBytes:        nativeMaxBodyBytes,
			FollowRedirects: false,
		},
	})
	if err != nil || resp.StatusCode != http.StatusOK || len(resp.Body) == 0 {
		return nil, fmt.Errorf("graphql request failed")
	}
	return parseInstagramGraphQLJSON(resp.Body, shortcode)
}

func parseInstagramEmbedHTML(rawHTML, shortcode string) (*nativePost, error) {
	if m := embedContextJSONPattern.FindStringSubmatch(rawHTML); len(m) == 2 {
		var decodedContext string
		if err := json.Unmarshal([]byte(m[1]), &decodedContext); err == nil && decodedContext != "" {
			var root map[string]any
			if err := json.Unmarshal([]byte(decodedContext), &root); err == nil {
				if mediaNode := extractEmbedMediaNode(root); mediaNode != nil {
					if post, err := parseGraphMediaNode(mediaNode, shortcode); err == nil && len(post.Items) > 0 {
						return post, nil
					}
				}
			}
		}
	}

	// Fallback: direct regex extraction of video_url / EmbeddedMediaImage from Embed HTML
	var items []nativeMediaItem
	var coverURL string
	if m := embedImageTagPattern.FindStringSubmatch(rawHTML); len(m) == 2 {
		imgURL := html.UnescapeString(strings.TrimSpace(m[1]))
		if clean := ytengine.SanitizeMediaURL(imgURL, AllowedMediaURL); clean != "" {
			coverURL = clean
		}
	} else if m := embedImageTagAltPattern.FindStringSubmatch(rawHTML); len(m) == 2 {
		imgURL := html.UnescapeString(strings.TrimSpace(m[1]))
		if clean := ytengine.SanitizeMediaURL(imgURL, AllowedMediaURL); clean != "" {
			coverURL = clean
		}
	}

	if m := embedVideoURLPattern.FindStringSubmatch(rawHTML); len(m) == 2 {
		var rawVideoURL string
		if err := json.Unmarshal([]byte(m[1]), &rawVideoURL); err == nil {
			rawVideoURL = html.UnescapeString(strings.TrimSpace(rawVideoURL))
			if clean := ytengine.SanitizeMediaURL(rawVideoURL, AllowedMediaURL); clean != "" {
				items = append(items, nativeMediaItem{
					ID:       shortcode,
					Kind:     "video",
					VideoURL: clean,
					ImageURL: coverURL,
				})
			}
		}
	}
	if len(items) == 0 && coverURL != "" {
		items = append(items, nativeMediaItem{
			ID:       shortcode,
			Kind:     "image",
			ImageURL: coverURL,
		})
	}
	if len(items) == 0 {
		return nil, errors.New("no media found in embed html")
	}
	return &nativePost{
		ID:        shortcode,
		Shortcode: shortcode,
		Title:     "Instagram Post " + shortcode,
		CoverURL:  coverURL,
		Items:     items,
	}, nil
}

func extractEmbedMediaNode(root map[string]any) map[string]any {
	if gql, ok := root["gql_data"].(map[string]any); ok {
		if sm, ok := gql["shortcode_media"].(map[string]any); ok {
			return sm
		}
		if xdt, ok := gql["xdt_shortcode_media"].(map[string]any); ok {
			return xdt
		}
	}
	if sm, ok := root["shortcode_media"].(map[string]any); ok {
		return sm
	}
	if ctxObj, ok := root["context"].(map[string]any); ok {
		if media, ok := ctxObj["media"].(map[string]any); ok {
			return media
		}
	}
	return nil
}

func parseInstagramGraphQLJSON(body []byte, shortcode string) (*nativePost, error) {
	trimmed := strings.TrimSpace(string(body))
	trimmed = strings.TrimPrefix(trimmed, "for (;;);")
	var root map[string]any
	if err := json.Unmarshal([]byte(trimmed), &root); err != nil {
		return nil, err
	}
	data, _ := root["data"].(map[string]any)
	if data == nil {
		return nil, errors.New("missing graphql data")
	}
	for _, key := range []string{"xdt_shortcode_media", "shortcode_media"} {
		if node, ok := data[key].(map[string]any); ok && node != nil {
			return parseGraphMediaNode(node, shortcode)
		}
	}
	return nil, errors.New("missing shortcode_media node")
}

func parseGraphMediaNode(node map[string]any, fallbackShortcode string) (*nativePost, error) {
	if node == nil {
		return nil, errors.New("nil media node")
	}
	shortcode := strField(node, "shortcode", "code")
	if shortcode == "" {
		shortcode = fallbackShortcode
	}
	postID := strField(node, "id", "pk")
	if postID == "" {
		postID = shortcode
	}

	caption := extractCaptionText(node)
	ownerID, ownerName, ownerAvatar := extractOwnerInfo(node)
	coverURL := extractBestDisplayURL(node)
	takenAt := int64Field(node, "taken_at_timestamp", "taken_at")
	likes := optionalCountField(node, "edge_media_preview_like", "likes")
	comments := optionalCountField(node, "edge_media_to_parent_comment", "edge_media_to_comment", "comments")
	views := optionalDirectIntField(node, "video_view_count", "play_count")

	var items []nativeMediaItem
	children := extractSidecarChildren(node)
	if len(children) > 0 {
		for idx, child := range children {
			if len(items) >= maxCarouselEntries {
				break
			}
			item, ok := parseSingleMediaNode(child, fmt.Sprintf("%s-%d", shortcode, idx+1))
			if ok {
				items = append(items, item)
			}
		}
	} else {
		if item, ok := parseSingleMediaNode(node, shortcode); ok {
			items = append(items, item)
		}
	}

	if len(items) == 0 {
		return nil, errors.New("no valid media items in node")
	}
	if coverURL == "" && len(items) > 0 {
		coverURL = items[0].ImageURL
	}
	title := firstLineTitle(caption, ownerName, shortcode)
	return &nativePost{
		ID:           postID,
		Shortcode:    shortcode,
		Title:        title,
		Description:  caption,
		AuthorID:     ownerID,
		AuthorName:   ownerName,
		AuthorAvatar: ownerAvatar,
		CoverURL:     coverURL,
		Timestamp:    takenAt,
		LikeCount:    likes,
		CommentCount: comments,
		ViewCount:    views,
		Items:        items,
	}, nil
}

func parseSingleMediaNode(node map[string]any, defaultID string) (nativeMediaItem, bool) {
	id := strField(node, "id", "pk", "shortcode")
	if id == "" {
		id = defaultID
	}
	width, height := extractDimensions(node)
	displayURL := extractBestDisplayURL(node)
	videoURL := ytengine.SanitizeMediaURL(html.UnescapeString(strField(node, "video_url")), AllowedMediaURL)
	typename := strings.ToLower(strField(node, "__typename"))
	isVideo, _ := node["is_video"].(bool)
	dur := floatField(node, "video_duration")

	if (videoURL != "") && (isVideo || strings.Contains(typename, "video") || videoURL != "") {
		return nativeMediaItem{
			ID:       id,
			Kind:     "video",
			VideoURL: videoURL,
			ImageURL: displayURL,
			Width:    width,
			Height:   height,
			Duration: dur,
		}, true
	}
	if displayURL != "" {
		return nativeMediaItem{
			ID:       id,
			Kind:     "image",
			ImageURL: displayURL,
			Width:    width,
			Height:   height,
		}, true
	}
	return nativeMediaItem{}, false
}

func extractSidecarChildren(node map[string]any) []map[string]any {
	for _, key := range []string{"edge_sidecar_to_children", "carousel_media"} {
		raw := node[key]
		if edgeMap, ok := raw.(map[string]any); ok {
			if edges, ok := edgeMap["edges"].([]any); ok {
				out := make([]map[string]any, 0, len(edges))
				for _, e := range edges {
					if em, ok := e.(map[string]any); ok {
						if child, ok := em["node"].(map[string]any); ok && child != nil {
							out = append(out, child)
						} else {
							out = append(out, em)
						}
					}
				}
				if len(out) > 0 {
					return out
				}
			}
		}
		if arr, ok := raw.([]any); ok {
			out := make([]map[string]any, 0, len(arr))
			for _, elem := range arr {
				if child, ok := elem.(map[string]any); ok && child != nil {
					out = append(out, child)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func extractBestDisplayURL(node map[string]any) string {
	for _, key := range []string{"display_url", "display_src", "thumbnail_src"} {
		if raw := html.UnescapeString(strField(node, key)); raw != "" {
			if clean := ytengine.SanitizeMediaURL(raw, AllowedMediaURL); clean != "" {
				return clean
			}
		}
	}
	if resList, ok := node["display_resources"].([]any); ok {
		var bestURL string
		var bestWidth int64
		for _, r := range resList {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			src := ytengine.SanitizeMediaURL(html.UnescapeString(strField(rm, "src")), AllowedMediaURL)
			if src == "" {
				continue
			}
			w := int64Field(rm, "config_width", "width")
			if bestURL == "" || w >= bestWidth {
				bestWidth = w
				bestURL = src
			}
		}
		if bestURL != "" {
			return bestURL
		}
	}
	return ""
}

func extractDimensions(node map[string]any) (int64, int64) {
	if dim, ok := node["dimensions"].(map[string]any); ok {
		w := int64Field(dim, "width")
		h := int64Field(dim, "height")
		if w > 0 && h > 0 {
			return w, h
		}
	}
	w := int64Field(node, "original_width", "width")
	h := int64Field(node, "original_height", "height")
	if w > 0 && h > 0 {
		return w, h
	}
	return 0, 0
}

func extractCaptionText(node map[string]any) string {
	if capEdge, ok := node["edge_media_to_caption"].(map[string]any); ok {
		if edges, ok := capEdge["edges"].([]any); ok && len(edges) > 0 {
			if em, ok := edges[0].(map[string]any); ok {
				if n, ok := em["node"].(map[string]any); ok {
					if t := strings.TrimSpace(strField(n, "text")); t != "" {
						return t
					}
				}
			}
		}
	}
	if capMap, ok := node["caption"].(map[string]any); ok {
		if t := strings.TrimSpace(strField(capMap, "text")); t != "" {
			return t
		}
	}
	return strings.TrimSpace(strField(node, "caption", "title"))
}

func extractOwnerInfo(node map[string]any) (string, string, string) {
	owner, _ := node["owner"].(map[string]any)
	if owner == nil {
		return "", "", ""
	}
	id := strField(owner, "username", "id")
	name := strField(owner, "full_name", "username")
	if name == "" {
		name = id
	}
	avatar := ytengine.SanitizeMediaURL(html.UnescapeString(strField(owner, "profile_pic_url")), AllowedMediaURL)
	return id, name, avatar
}

func firstLineTitle(caption, authorName, shortcode string) string {
	for _, line := range strings.Split(caption, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if len([]rune(trimmed)) > 80 {
				return string([]rune(trimmed)[:80])
			}
			return trimmed
		}
	}
	if authorName != "" {
		return "Post by " + authorName
	}
	return shortcode
}

func strField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case json.Number:
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		}
	}
	return ""
}

func int64Field(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		if n, ok := unified.Numeric(m[k]); ok && n > 0 {
			if iv, ok := unified.PHPInt(m[k]); ok && iv > 0 {
				return iv
			}
		}
	}
	return 0
}

func floatField(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if n, ok := unified.Numeric(m[k]); ok && n > 0 && !math.IsNaN(n) && !math.IsInf(n, 0) {
			return n
		}
	}
	return 0
}

func optionalCountField(m map[string]any, keys ...string) *int64 {
	for _, k := range keys {
		if sub, ok := m[k].(map[string]any); ok {
			if n, ok := unified.Numeric(sub["count"]); ok && n >= 0 {
				if iv, ok := unified.PHPInt(sub["count"]); ok && iv >= 0 {
					return &iv
				}
			}
		}
	}
	return nil
}

func optionalDirectIntField(m map[string]any, keys ...string) *int64 {
	for _, k := range keys {
		if n, ok := unified.Numeric(m[k]); ok && n >= 0 {
			if iv, ok := unified.PHPInt(m[k]); ok && iv >= 0 {
				return &iv
			}
		}
	}
	return nil
}

func (p *Parser) buildNativeEnvelope(ctx context.Context, post *nativePost, shortcode, sourceURL string, now int64) unified.Object {
	if post == nil || len(post.Items) == 0 {
		return unified.ContractError("NO_MEDIA", "该内容未包含可解析的媒体", false)
	}

	items := make(unified.Array, 0, len(post.Items))
	imageItemIDs := make(unified.Array, 0, len(post.Items))
	total := len(post.Items)

	for idx, entry := range post.Items {
		position := idx + 1
		entryKey := entry.ID
		if entryKey == "" || entryKey == shortcode {
			if u, err := url.Parse(nonEmptyStr(entry.VideoURL, entry.ImageURL)); err == nil && u.Path != "" {
				entryKey = path.Base(u.Path)
			} else {
				entryKey = strconv.Itoa(position)
			}
		}
		itemTitle := post.Title
		if total > 1 {
			itemTitle = fmt.Sprintf("%s #%d", post.Title, position)
		}

		if entry.Kind == "video" && entry.VideoURL != "" {
			cleanVideoURL := ytengine.SanitizeMediaURL(entry.VideoURL, AllowedMediaURL)
			if cleanVideoURL == "" {
				continue
			}
			rawSource := unified.Object{
				"url": cleanVideoURL,
				"private_source_identity": unified.PHPIdentityJSON(unified.Array{
					"instagram-native-video",
					shortcode,
					entryKey,
					canonicalInstagramCDNIdentity(cleanVideoURL),
				}, true),
			}
			if p.sizeProber != nil {
				if sz, ok := p.sizeProber(ctx, cleanVideoURL); ok && sz > 0 {
					rawSource["size_bytes"] = sz
				}
			}
			sources := unified.Sources(unified.Array{rawSource}, now)
			if len(sources) == 0 {
				continue
			}
			meta := unified.Object{
				"evidence":  "upstream",
				"container": "mp4",
				"extension": "mp4",
				"mime_type": "video/mp4",
			}
			label := "默认画质"
			if entry.Width > 0 && entry.Height > 0 {
				meta["width"] = entry.Width
				meta["height"] = entry.Height
				minEdge := entry.Width
				if entry.Height < minEdge {
					minEdge = entry.Height
				}
				label = strconv.FormatInt(minEdge, 10) + "p"
			}
			variantID := "v-" + shortHash("instagram:native-video:"+shortcode+":"+entryKey)
			variant := unified.Object{
				"id":       variantID,
				"label":    label,
				"metadata": meta,
				"delivery": unified.Object{
					"kind":    "file",
					"sources": sources,
				},
			}
			itemID := "video-" + shortHash("instagram:item:"+shortcode+":"+entryKey)
			item := unified.Object{
				"id":          itemID,
				"type":        "video",
				"index":       int64(position),
				"default":     nil,
				"title":       itemTitle,
				"variants":    unified.Array{variant},
				"resolve_url": unified.ResolveURL(sourceURL, itemID),
			}
			if entry.Duration > 0 {
				durMs := int64(math.Round(entry.Duration * 1000))
				if durMs > 0 {
					item["duration_ms"] = durMs
				}
			}
			if coverSrc := buildCoverSource(nonEmptyStr(entry.ImageURL, post.CoverURL), now); coverSrc != nil {
				item["cover"] = coverSrc
			}
			items = append(items, item)
			continue
		}

		if entry.ImageURL != "" {
			cleanImageURL := ytengine.SanitizeMediaURL(entry.ImageURL, AllowedMediaURL)
			if cleanImageURL == "" {
				continue
			}
			rawSource := unified.Object{
				"url": cleanImageURL,
				"private_source_identity": unified.PHPIdentityJSON(unified.Array{
					"instagram-native-image",
					shortcode,
					entryKey,
					canonicalInstagramCDNIdentity(cleanImageURL),
				}, true),
			}
			sources := unified.Sources(unified.Array{rawSource}, now)
			if len(sources) == 0 {
				continue
			}
			container, ext, mimeType := detectImageFormat(cleanImageURL)
			meta := unified.Object{
				"evidence":  "upstream",
				"container": container,
				"extension": ext,
				"mime_type": mimeType,
			}
			if entry.Width > 0 && entry.Height > 0 {
				meta["width"] = entry.Width
				meta["height"] = entry.Height
			}
			variantID := "img-" + shortHash("instagram:native-image:"+shortcode+":"+entryKey)
			variant := unified.Object{
				"id":       variantID,
				"metadata": meta,
				"delivery": unified.Object{
					"kind":    "file",
					"sources": sources,
				},
			}
			itemID := "image-" + shortHash("instagram:item:"+shortcode+":"+entryKey)
			item := unified.Object{
				"id":          itemID,
				"type":        "image",
				"index":       int64(position),
				"default":     nil,
				"title":       itemTitle,
				"variants":    unified.Array{variant},
				"resolve_url": unified.ResolveURL(sourceURL, itemID),
			}
			items = append(items, item)
			imageItemIDs = append(imageItemIDs, itemID)
		}
	}

	if len(items) == 0 {
		return unified.ContractError("NO_MEDIA", "该内容未包含可解析的媒体", false)
	}

	firstItemID := unified.Str(unified.Map(items[0])["id"])
	data := unified.Object{
		"platform":        "instagram",
		"content_id":      shortcode,
		"source_url":      sourceURL,
		"default_item_id": firstItemID,
		"items":           items,
		"generated_at":    time.Unix(now, 0).UTC().Format("2006-01-02T15:04:05Z"),
	}
	if len(imageItemIDs) > 0 && len(items) > 1 {
		data["groups"] = unified.Array{
			unified.Object{
				"id":          "album",
				"kind":        "album",
				"item_ids":    imageItemIDs,
				"total_count": int64(len(imageItemIDs)),
				"complete":    true,
			},
		}
	}
	if post.Title != "" {
		data["title"] = post.Title
	}
	if post.Description != "" {
		data["description"] = post.Description
	}
	if coverSrc := buildCoverSource(post.CoverURL, now); coverSrc != nil {
		data["cover"] = coverSrc
	}
	if post.AuthorID != "" || post.AuthorName != "" {
		author := unified.Object{}
		if post.AuthorName != "" {
			author["name"] = post.AuthorName
		}
		if post.AuthorID != "" {
			author["id"] = post.AuthorID
		}
		if avatarSrc := buildCoverSource(post.AuthorAvatar, now); avatarSrc != nil {
			author["avatar"] = avatarSrc
		}
		data["author"] = author
	}
	if post.Timestamp > 0 {
		data["published_at"] = time.Unix(post.Timestamp, 0).UTC().Format("2006-01-02T15:04:05Z")
	}
	stats := unified.Object{}
	if post.ViewCount != nil && *post.ViewCount >= 0 {
		stats["views"] = unified.Object{"value": *post.ViewCount}
	}
	if post.LikeCount != nil && *post.LikeCount >= 0 {
		stats["likes"] = unified.Object{"value": *post.LikeCount}
	}
	if post.CommentCount != nil && *post.CommentCount >= 0 {
		stats["comments"] = unified.Object{"value": *post.CommentCount}
	}
	if len(stats) > 0 {
		data["statistics"] = stats
	}

	return unified.FinalizeData(data, now)
}

func buildCoverSource(rawURL string, now int64) unified.Object {
	clean := ytengine.SanitizeMediaURL(rawURL, AllowedMediaURL)
	if clean == "" {
		return nil
	}
	sources := unified.Sources(unified.Array{clean}, now)
	if len(sources) == 0 {
		return nil
	}
	return unified.Map(sources[0])
}

func detectImageFormat(rawURL string) (string, string, string) {
	u, err := url.Parse(rawURL)
	p := strings.ToLower(rawURL)
	if err == nil {
		p = strings.ToLower(u.Path)
	}
	switch {
	case strings.HasSuffix(p, ".webp"):
		return "webp", "webp", "image/webp"
	case strings.HasSuffix(p, ".png"):
		return "png", "png", "image/png"
	default:
		return "jpeg", "jpg", "image/jpeg"
	}
}

func canonicalInstagramCDNIdentity(rawURL string) string {
	stripped := unified.SourceIdentity(rawURL)
	u, err := url.Parse(stripped)
	if err != nil {
		return stripped
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if instagramEdgeHostRegex.MatchString(host) {
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

func nonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func shortHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:24]
}
