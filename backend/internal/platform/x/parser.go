// Package x implements the X / Twitter video parser backed by the hardened
// internal yt-dlp metadata engine.
package x

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

const maxTweetVideos = 4

var (
	tweetIDPattern    = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	screenNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,30}$`)
	reservedHandles   = map[string]bool{
		"home":          true,
		"explore":       true,
		"search":        true,
		"notifications": true,
		"messages":      true,
		"settings":      true,
		"compose":       true,
		"intent":        true,
		"hashtag":       true,
		"tos":           true,
		"privacy":       true,
	}
)

// Parser adapts yt-dlp -J metadata for X / Twitter status URLs into unified-v1.
type Parser struct {
	engine     ytengine.Extractor
	sizeProber SizeProber
}

// New creates an X platform parser bound to the given ytengine.Extractor.
func New(engine ytengine.Extractor) *Parser {
	return &Parser{engine: engine}
}

// NewWithSizeProber creates an X platform parser that optionally probes exact byte
// lengths for progressive MP4 formats when yt-dlp leaves Filesize unset.
func NewWithSizeProber(engine ytengine.Extractor, prober SizeProber) *Parser {
	return &Parser{engine: engine, sizeProber: prober}
}

// ExtractTweetTarget validates an X / Twitter status URL and returns:
//   - tweetID: the numeric status ID
//   - canonicalURL: the sanitized URL safe to pass to yt-dlp
//   - selectedIndex: optional 1-based /video/<N> or /photo/<N> index (0 if absent)
//   - ok: whether the input URL is a valid X status URL
func ExtractTweetTarget(raw string) (string, string, int, bool) {
	if unified.MediaURL(raw) == "" || len(raw) > 2048 {
		return "", "", 0, false
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return "", "", 0, false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return "", "", 0, false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", 0, false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || (scheme == "https" && n != 443) || (scheme == "http" && n != 80) {
			return "", "", 0, false
		}
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "x.com", "www.x.com", "mobile.x.com", "m.x.com",
		"twitter.com", "www.twitter.com", "mobile.twitter.com", "m.twitter.com":
	default:
		return "", "", 0, false
	}

	segments := splitNonEmptyPath(u.EscapedPath())
	var tweetID string
	var tail []string

	switch {
	case len(segments) >= 3 && strings.EqualFold(segments[0], "i") &&
		(strings.EqualFold(segments[1], "status") || strings.EqualFold(segments[1], "statuses")):
		tweetID = segments[2]
		tail = segments[3:]
	case len(segments) >= 4 && strings.EqualFold(segments[0], "i") &&
		strings.EqualFold(segments[1], "web") &&
		(strings.EqualFold(segments[2], "status") || strings.EqualFold(segments[2], "statuses")):
		tweetID = segments[3]
		tail = segments[4:]
	case len(segments) >= 3 &&
		(strings.EqualFold(segments[1], "status") || strings.EqualFold(segments[1], "statuses")):
		handle := segments[0]
		if !screenNamePattern.MatchString(handle) || reservedHandles[strings.ToLower(handle)] {
			return "", "", 0, false
		}
		tweetID = segments[2]
		tail = segments[3:]
	default:
		return "", "", 0, false
	}

	if !tweetIDPattern.MatchString(tweetID) {
		return "", "", 0, false
	}

	selectedIndex := 0
	if len(tail) > 0 {
		if len(tail) != 2 {
			return "", "", 0, false
		}
		kind := strings.ToLower(tail[0])
		if kind != "video" && kind != "photo" {
			return "", "", 0, false
		}
		idx, err := strconv.Atoi(tail[1])
		if err != nil || idx < 1 || idx > maxTweetVideos {
			return "", "", 0, false
		}
		selectedIndex = idx
	}

	canonicalURL := "https://x.com/i/status/" + tweetID
	return tweetID, canonicalURL, selectedIndex, true
}

// AllowedTargetURL enforces that only canonical X status URLs reach ytengine.
func AllowedTargetURL(raw string) bool {
	_, canonical, _, ok := ExtractTweetTarget(raw)
	return ok && raw == canonical
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

// Parse extracts X / Twitter status metadata via ytengine and formats a unified-v1 envelope.
func (p *Parser) Parse(ctx context.Context, source string, now int64) (unified.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tweetID, canonicalURL, selectedIndex, ok := ExtractTweetTarget(source)
	if !ok {
		return unified.ContractError("INVALID_INPUT", "请输入有效的 X (Twitter) 推文链接", false), nil
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
	populateMissingMP4Sizes(ctx, info, p.sizeProber)
	return AdaptInfo(info, tweetID, source, selectedIndex, now), nil
}
