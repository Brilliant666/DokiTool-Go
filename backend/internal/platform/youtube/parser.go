// Package youtube implements the YouTube video & Shorts parser backed by the
// hardened internal yt-dlp metadata engine and unified-v1 contract.
package youtube

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

var youtubeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// Parser adapts yt-dlp -J metadata for YouTube watch/Shorts URLs into unified-v1.
type Parser struct {
	engine     ytengine.Extractor
	sizeProber ytengine.SizeProber
}

// New creates a YouTube platform parser bound to the given ytengine.Extractor.
func New(engine ytengine.Extractor) *Parser {
	return &Parser{engine: engine}
}

// NewWithSizeProber creates a YouTube platform parser that optionally probes
// exact byte lengths for progressive MP4 formats when yt-dlp leaves Filesize unset.
func NewWithSizeProber(engine ytengine.Extractor, prober ytengine.SizeProber) *Parser {
	return &Parser{engine: engine, sizeProber: prober}
}

// ExtractVideoTarget validates a YouTube watch / Shorts / youtu.be URL and returns:
//   - videoID: the 11-character YouTube video ID
//   - canonicalURL: the sanitized URL safe to pass to yt-dlp
//   - ok: whether the input URL is a valid YouTube video URL
func ExtractVideoTarget(raw string) (string, string, bool) {
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
	segments := splitNonEmptyPath(u.EscapedPath())
	var videoID string

	switch host {
	case "youtu.be", "www.youtu.be":
		if len(segments) == 1 {
			videoID = segments[0]
		}
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com":
		switch {
		case len(segments) == 1 && strings.EqualFold(segments[0], "watch"):
			videoID = u.Query().Get("v")
		case len(segments) == 2 && (strings.EqualFold(segments[0], "shorts") ||
			strings.EqualFold(segments[0], "embed") ||
			strings.EqualFold(segments[0], "live") ||
			strings.EqualFold(segments[0], "v")):
			videoID = segments[1]
		}
	default:
		return "", "", false
	}

	if !youtubeIDPattern.MatchString(videoID) {
		return "", "", false
	}
	canonicalURL := "https://www.youtube.com/watch?v=" + videoID
	return videoID, canonicalURL, true
}

// AllowedTargetURL enforces that only canonical YouTube watch URLs reach ytengine.
func AllowedTargetURL(raw string) bool {
	_, canonical, ok := ExtractVideoTarget(raw)
	return ok && raw == canonical
}

// AllowedOutboundHost restricts yt-dlp subprocess outbound connections to official YouTube domains.
func AllowedOutboundHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "youtubei.googleapis.com" {
		return true
	}
	for _, domain := range []string{"youtube.com", "youtu.be", "googlevideo.com", "ytimg.com", "ggpht.com", "googleusercontent.com"} {
		if h == domain || strings.HasSuffix(h, "."+domain) {
			return true
		}
	}
	return false
}

// AllowedMediaURL restricts public media and cover URLs to official YouTube CDN hosts.
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
	for _, suffix := range []string{".googlevideo.com", ".ytimg.com", ".ggpht.com", ".googleusercontent.com", ".youtube.com"} {
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return true
		}
	}
	return host == "i.ytimg.com" || host == "img.youtube.com"
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

// Parse extracts YouTube metadata via ytengine and formats a unified-v1 envelope
// with progressive MP4 (file) and high-resolution 4K/2K/1080p/720p DASH (mux) variants.
func (p *Parser) Parse(ctx context.Context, source string, now int64) (unified.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	videoID, canonicalURL, ok := ExtractVideoTarget(source)
	if !ok {
		return unified.ContractError("INVALID_INPUT", "请输入有效的 YouTube 视频或 Shorts 链接", false), nil
	}
	if p == nil || p.engine == nil || !p.engine.Enabled() {
		return unified.ContractError("SERVICE_NOT_CONFIGURED", "当前平台解析服务暂不可用", false), nil
	}

	info, err := ytengine.ExtractYouTubeWithCascade(ctx, p.engine, canonicalURL)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
			return nil, ctxErr
		}
		var engineErr *ytengine.Error
		if errors.As(err, &engineErr) {
			resp := unified.ContractError(string(engineErr.Code), engineErr.Message, engineErr.Retryable)
			if engineErr.Retryable && engineErr.RetryAfter > 0 {
				unified.Map(resp["error"])["retry_after_seconds"] = engineErr.RetryAfter
			}
			return resp, nil
		}
		return unified.ContractError("UPSTREAM_UNAVAILABLE", "当前平台解析服务暂不可用，请稍后重试", true), nil
	}

	opts := ytengine.AdaptOptions{
		PlatformID:      "youtube",
		ContentID:       videoID,
		SourceURL:       source,
		MaxEntries:      1,
		AllowedMediaURL: AllowedMediaURL,
		EnableDASHMux:   true,
		EnableHLS:       false,
	}
	ytengine.PopulateMissingMP4Sizes(ctx, info, p.sizeProber, opts)
	return ytengine.AdaptInfo(info, opts, now), nil
}
