// Package instagram implements the Instagram Reels & Post video parser backed
// by the hardened internal yt-dlp metadata engine and unified-v1 contract.
package instagram

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

var shortcodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{5,32}$`)

// Parser adapts yt-dlp -J metadata for Instagram Reels and Posts into unified-v1.
type Parser struct {
	engine     ytengine.Extractor
	sizeProber ytengine.SizeProber
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

// Parse extracts Instagram Reel/Post metadata via ytengine and formats a unified-v1 envelope.
func (p *Parser) Parse(ctx context.Context, source string, now int64) (unified.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	shortcode, canonicalURL, ok := ExtractPostTarget(source)
	if !ok {
		return unified.ContractError("INVALID_INPUT", "请输入有效的 Instagram 帖子或 Reels 链接", false), nil
	}
	if p == nil || p.engine == nil || !p.engine.Enabled() {
		return unified.ContractError("SERVICE_NOT_CONFIGURED", "当前平台解析服务暂不可用", false), nil
	}

	info, err := p.engine.Extract(ctx, canonicalURL)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
			return nil, ctxErr
		}
		var engineErr *ytengine.Error
		if errors.As(err, &engineErr) {
			return unified.ContractError(string(engineErr.Code), engineErr.Message, engineErr.Retryable), nil
		}
		return unified.ContractError("UPSTREAM_UNAVAILABLE", "当前平台解析服务暂不可用，请稍后重试", true), nil
	}

	opts := ytengine.AdaptOptions{
		PlatformID:                   "instagram",
		ContentID:                    shortcode,
		SourceURL:                    source,
		MaxEntries:                   10,
		AllowedMediaURL:              AllowedMediaURL,
		EnableDASHMux:                true,
		EnableHLS:                    false,
		AllowCodecLessProgressiveMP4: true,
	}
	ytengine.PopulateMissingMP4Sizes(ctx, info, p.sizeProber, opts)
	return ytengine.AdaptInfo(info, opts, now), nil
}
