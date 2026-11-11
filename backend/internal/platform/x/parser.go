// Package x implements the X / Twitter video parser backed by the hardened
// internal yt-dlp metadata engine.
package x

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
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

// Parser adapts yt-dlp -J metadata and public syndication photo metadata for
// X / Twitter status URLs into unified-v1.
type Parser struct {
	engine     ytengine.Extractor
	sizeProber SizeProber
	fetcher    fetch.Executor
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

// NewWithFetcher creates an X platform parser with yt-dlp video extraction,
// optional progressive MP4 size probing, and SSRF-safe syndication photo extraction.
func NewWithFetcher(engine ytengine.Extractor, prober SizeProber, fetcher fetch.Executor) *Parser {
	return &Parser{engine: engine, sizeProber: prober, fetcher: fetcher}
}

// WithFetcher returns a copy of the Parser configured with an SSRF-safe fetch.Executor
// for anonymous syndication photo extraction on pure-image, mixed, and /photo/<N> URLs.
func (p *Parser) WithFetcher(fetcher fetch.Executor) *Parser {
	if p == nil {
		return &Parser{fetcher: fetcher}
	}
	cp := *p
	cp.fetcher = fetcher
	return &cp
}

// ExtractTweetTarget validates an X / Twitter status URL and returns:
//   - tweetID: the numeric status ID
//   - canonicalURL: the sanitized URL safe to pass to yt-dlp
//   - selectedIndex: optional 1-based /video/<N> or /photo/<N> index (0 if absent)
//   - ok: whether the input URL is a valid X status URL
func ExtractTweetTarget(raw string) (string, string, int, bool) {
	tweetID, canonicalURL, _, selectedIndex, ok := extractTweetTarget(raw)
	return tweetID, canonicalURL, selectedIndex, ok
}

func extractTweetTarget(raw string) (string, string, string, int, bool) {
	if unified.MediaURL(raw) == "" || len(raw) > 2048 {
		return "", "", "", 0, false
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return "", "", "", 0, false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return "", "", "", 0, false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", "", 0, false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || (scheme == "https" && n != 443) || (scheme == "http" && n != 80) {
			return "", "", "", 0, false
		}
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "x.com", "www.x.com", "mobile.x.com", "m.x.com",
		"twitter.com", "www.twitter.com", "mobile.twitter.com", "m.twitter.com":
	default:
		return "", "", "", 0, false
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
			return "", "", "", 0, false
		}
		tweetID = segments[2]
		tail = segments[3:]
	default:
		return "", "", "", 0, false
	}

	if !tweetIDPattern.MatchString(tweetID) {
		return "", "", "", 0, false
	}

	selectedKind := ""
	selectedIndex := 0
	if len(tail) > 0 {
		if len(tail) != 2 {
			return "", "", "", 0, false
		}
		kind := strings.ToLower(tail[0])
		if kind != "video" && kind != "photo" {
			return "", "", "", 0, false
		}
		idx, err := strconv.Atoi(tail[1])
		if err != nil || idx < 1 || idx > maxTweetVideos {
			return "", "", "", 0, false
		}
		selectedKind = kind
		selectedIndex = idx
	}

	canonicalURL := "https://x.com/i/status/" + tweetID
	return tweetID, canonicalURL, selectedKind, selectedIndex, true
}

// AllowedTargetURL enforces that only canonical X status URLs reach ytengine.
func AllowedTargetURL(raw string) bool {
	_, canonical, _, ok := ExtractTweetTarget(raw)
	return ok && raw == canonical
}

// AllowedOutboundHost restricts yt-dlp subprocess outbound connections to official X/Twitter domains.
func AllowedOutboundHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, domain := range []string{"x.com", "twitter.com", "twimg.com"} {
		if h == domain || strings.HasSuffix(h, "."+domain) {
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

// Parse extracts X / Twitter status metadata via ytengine + public syndication
// and formats a unified-v1 envelope.
func (p *Parser) Parse(ctx context.Context, source string, now int64) (unified.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tweetID, canonicalURL, selectedKind, selectedIndex, ok := extractTweetTarget(source)
	if !ok {
		return unified.ContractError("INVALID_INPUT", "请输入有效的 X (Twitter) 推文链接", false), nil
	}
	if p == nil || p.engine == nil || !p.engine.Enabled() {
		return unified.ContractError("SERVICE_NOT_CONFIGURED", "当前平台解析服务暂不可用", false), nil
	}

	// /photo/<N>: fetch photo list directly from public syndication without spawning yt-dlp.
	if selectedKind == "photo" && p.fetcher != nil {
		st, synErr := fetchSyndicationTweet(ctx, p.fetcher, tweetID)
		if synErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
				return nil, ctxErr
			}
			return syndicationContractError(synErr), nil
		}
		photo, found := st.photoAt(selectedIndex)
		if !found {
			return unified.ContractError("CONTENT_UNAVAILABLE", fmt.Sprintf("所选第 %d 张图片不存在或不可用", selectedIndex), false), nil
		}
		return buildSyndicationPhotoEnvelope(st, []syndicationPhoto{photo}, tweetID, source, now), nil
	}

	info, err := p.engine.Extract(ctx, canonicalURL)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
			return nil, ctxErr
		}
		var engineErr *ytengine.Error
		if errors.As(err, &engineErr) {
			if engineErr.LocalBusy {
				resp := unified.ContractError(string(engineErr.Code), engineErr.Message, engineErr.Retryable)
				if engineErr.Retryable && engineErr.RetryAfter > 0 {
					unified.Map(resp["error"])["retry_after_seconds"] = engineErr.RetryAfter
				}
				return resp, nil
			}
			if engineErr.Code == ytengine.CodeContentUnavailable && p.fetcher != nil && selectedKind != "video" {
				st, synErr := fetchSyndicationTweet(ctx, p.fetcher, tweetID)
				if synErr != nil {
					if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
						return nil, ctxErr
					}
					if !errors.Is(synErr, errSyndicationNotFound) {
						return syndicationContractError(synErr), nil
					}
				} else if len(st.Photos) > 0 {
					return buildSyndicationPhotoEnvelope(st, st.Photos, tweetID, source, now), nil
				}
			}
			resp := unified.ContractError(string(engineErr.Code), engineErr.Message, engineErr.Retryable)
			if engineErr.Retryable && engineErr.RetryAfter > 0 {
				unified.Map(resp["error"])["retry_after_seconds"] = engineErr.RetryAfter
			}
			return resp, nil
		}
		return unified.ContractError("UPSTREAM_UNAVAILABLE", "当前平台解析服务暂不可用，请稍后重试", true), nil
	}

	populateMissingMP4Sizes(ctx, info, p.sizeProber)
	if p.fetcher != nil && selectedKind == "" {
		st, synErr := fetchSyndicationTweet(ctx, p.fetcher, tweetID)
		if synErr != nil && errors.Is(synErr, context.Canceled) {
			return nil, synErr
		}
		if synErr == nil && len(st.Photos) > 0 {
			adapted := adaptInfoWithPhotos(info, st.Photos, tweetID, source, selectedIndex, now)
			if unified.Str(adapted["code"]) == "NO_MEDIA" {
				return buildSyndicationPhotoEnvelope(st, st.Photos, tweetID, source, now), nil
			}
			return adapted, nil
		}
		adapted := AdaptInfo(info, tweetID, source, selectedIndex, now)
		if unified.Str(adapted["code"]) == "NO_MEDIA" && synErr != nil && !errors.Is(synErr, errSyndicationNotFound) {
			return syndicationContractError(synErr), nil
		}
		return adapted, nil
	}
	return AdaptInfo(info, tweetID, source, selectedIndex, now), nil
}
