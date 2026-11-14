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
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

const (
	maxTweetVideos = 4

	TombstoneFallbackOff       = "off"
	TombstoneFallbackFxTwitter = "fxtwitter"
)

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
	engine            ytengine.Extractor
	sizeProber        SizeProber
	fetcher           fetch.Executor
	supplementTimeout time.Duration
	tombstoneFallback string
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

// ValidateTombstoneFallback validates the -x-tombstone-fallback startup flag.
func ValidateTombstoneFallback(mode string) error {
	switch mode {
	case TombstoneFallbackOff, TombstoneFallbackFxTwitter:
		return nil
	default:
		return fmt.Errorf("x-tombstone-fallback must be %s or %s", TombstoneFallbackOff, TombstoneFallbackFxTwitter)
	}
}

// WithTombstoneFallback returns a copy of the Parser configured with the given
// TweetTombstone fallback mode ("off" or "fxtwitter").
func (p *Parser) WithTombstoneFallback(mode string) (*Parser, error) {
	if err := ValidateTombstoneFallback(mode); err != nil {
		return nil, err
	}
	var cp Parser
	if p != nil {
		cp = *p
	}
	cp.tombstoneFallback = mode
	return &cp, nil
}

// TombstoneFallback reports the configured TweetTombstone fallback mode ("off" or "fxtwitter").
func (p *Parser) TombstoneFallback() string {
	if p != nil && p.tombstoneFallback == TombstoneFallbackFxTwitter {
		return TombstoneFallbackFxTwitter
	}
	return TombstoneFallbackOff
}

func (p *Parser) fxtwitterEnabled() bool {
	return p != nil && p.tombstoneFallback == TombstoneFallbackFxTwitter
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

// AllowedExtractors returns the Twitter-family yt-dlp extractor names verified
// from pinned yt-dlp 2026.10.26 (--list-extractors), excluding generic and
// twitter:shortener (t.co) so embedded external links never fall back to generic.
func AllowedExtractors() []string {
	return []string{
		"twitter",
		"twitter:amplify",
		"twitter:broadcast",
		"twitter:card",
		"twitter:spaces",
	}
}

func isPureSyndicationPhotoTweet(st *syndicationTweet) bool {
	return st != nil && !st.Tombstone && len(st.Photos) > 0 && !st.HasVideo
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

func (p *Parser) effectiveSupplementTimeout() time.Duration {
	if p != nil && p.supplementTimeout > 0 {
		return p.supplementTimeout
	}
	return syndicationSupplementTimeout
}

func (p *Parser) handleExtractError(ctx context.Context, err error) (unified.Object, error) {
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

// Parse extracts X / Twitter status metadata, prioritizing the official
// login-free syndication endpoint for video and photo tweets before falling
// back to ytengine when syndication fails or lacks complete video metadata.
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
		if st != nil && st.Tombstone {
			return p.resolveTombstone(ctx, tweetID, source, "photo", selectedIndex, now)
		}
		photo, found := st.photoAt(selectedIndex)
		if !found {
			return unified.ContractError("CONTENT_UNAVAILABLE", fmt.Sprintf("所选第 %d 张图片不存在或不可用", selectedIndex), false), nil
		}
		return buildSyndicationPhotoEnvelope(st, []syndicationPhoto{photo}, tweetID, source, now), nil
	}

	if p.fetcher == nil {
		info, err := p.engine.Extract(ctx, canonicalURL)
		if err != nil {
			return p.handleExtractError(ctx, err)
		}
		populateMissingMP4Sizes(ctx, info, p.sizeProber)
		return AdaptInfo(info, tweetID, source, selectedIndex, now), nil
	}

	canFastPath := func(st *syndicationTweet) bool {
		if st == nil || st.Tombstone {
			return false
		}
		return st.canUseSyndicationVideos() || isPureSyndicationPhotoTweet(st)
	}

	executeFastPath := func(st *syndicationTweet) (unified.Object, bool) {
		if st == nil || st.Tombstone {
			return nil, false
		}
		if st.canUseSyndicationVideos() {
			info := st.toYTInfo(tweetID)
			populateMissingMP4Sizes(ctx, info, p.sizeProber)
			adapted := adaptInfoWithPhotos(info, st.Photos, tweetID, source, selectedIndex, now)
			if unified.Str(adapted["code"]) == "OK" {
				return adapted, true
			}
			return nil, false
		}
		if isPureSyndicationPhotoTweet(st) {
			if selectedKind == "video" {
				return unified.ContractError("CONTENT_UNAVAILABLE", "未找到可用的视频或该推文已不可访问", false), true
			}
			return buildSyndicationPhotoEnvelope(st, st.Photos, tweetID, source, now), true
		}
		return nil, false
	}

	type syndicationResult struct {
		tweet *syndicationTweet
		err   error
	}
	type ytExtractResult struct {
		info *ytengine.Info
		err  error
	}

	synCtx, cancelSyn := context.WithCancel(ctx)
	defer cancelSyn()

	supplementTimer := time.NewTimer(p.effectiveSupplementTimeout())
	defer supplementTimer.Stop()

	synCh := make(chan syndicationResult, 1)
	fetcher := p.fetcher
	go func() {
		st, synErr := fetchSyndicationTweet(synCtx, fetcher, tweetID)
		synCh <- syndicationResult{tweet: st, err: synErr}
	}()

	var (
		gotSyn            bool
		synRes            syndicationResult
		supplementExpired bool
	)

	// Stage 1: Try official login-free syndication first without spawning yt-dlp.
	select {
	case synRes = <-synCh:
		gotSyn = true
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
			return nil, ctxErr
		}
		if synRes.err == nil && canFastPath(synRes.tweet) {
			if fastEnv, ok := executeFastPath(synRes.tweet); ok {
				if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
					return nil, ctxErr
				}
				return fastEnv, nil
			}
		}
	case <-supplementTimer.C:
		supplementExpired = true
	case <-ctx.Done():
		cancelSyn()
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		return unified.ContractError("UPSTREAM_TIMEOUT", "上游解析超时，请稍后重试", true), nil
	}

	// Stage 2: Syndication failed, timed out past supplementTimeout, returned
	// Tombstone, or lacked complete video metadata -> fall back to yt-dlp.
	ytCtx, cancelYT := context.WithCancel(ctx)
	defer cancelYT()

	ytCh := make(chan ytExtractResult, 1)
	engine := p.engine
	go func() {
		info, ytErr := engine.Extract(ytCtx, canonicalURL)
		ytCh <- ytExtractResult{info: info, err: ytErr}
	}()

	var (
		gotYT bool
		ytRes ytExtractResult
	)

	awaitFullSyndication := func() (*syndicationTweet, error) {
		if !gotSyn {
			select {
			case synRes = <-synCh:
				gotSyn = true
			case <-ctx.Done():
				cancelSyn()
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return nil, errSyndicationTimeout
				}
				return nil, ctx.Err()
			}
		}
		return synRes.tweet, synRes.err
	}

	if !gotSyn {
		select {
		case synRes = <-synCh:
			gotSyn = true
			if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
				cancelYT()
				return nil, ctxErr
			}
			if synRes.err == nil && canFastPath(synRes.tweet) {
				select {
				case ytRes = <-ytCh:
					gotYT = true
					var engineErr *ytengine.Error
					if ytRes.err != nil && errors.As(ytRes.err, &engineErr) && engineErr.LocalBusy {
						return p.handleExtractError(ctx, ytRes.err)
					}
				default:
				}
				cancelYT()
				if fastEnv, ok := executeFastPath(synRes.tweet); ok {
					if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
						return nil, ctxErr
					}
					return fastEnv, nil
				}
			}
		case ytRes = <-ytCh:
			gotYT = true
		case <-ctx.Done():
			cancelYT()
			cancelSyn()
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, ctx.Err()
			}
			return unified.ContractError("UPSTREAM_TIMEOUT", "上游解析超时，请稍后重试", true), nil
		}
	}

	if !gotYT {
		select {
		case ytRes = <-ytCh:
			gotYT = true
		case <-ctx.Done():
			cancelYT()
			cancelSyn()
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, ctx.Err()
			}
			return unified.ContractError("UPSTREAM_TIMEOUT", "上游解析超时，请稍后重试", true), nil
		}
	}

	if ytRes.err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
			return nil, ctxErr
		}
		var engineErr *ytengine.Error
		hasEngineErr := errors.As(ytRes.err, &engineErr)
		if hasEngineErr && engineErr.LocalBusy {
			cancelSyn()
			return p.handleExtractError(ctx, ytRes.err)
		}

		st, synErr := awaitFullSyndication()
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
			return nil, ctxErr
		}
		if synErr == nil && canFastPath(st) {
			if fastEnv, ok := executeFastPath(st); ok {
				if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
					return nil, ctxErr
				}
				return fastEnv, nil
			}
		}
		if synErr == nil && st != nil && st.Tombstone {
			return p.resolveTombstone(ctx, tweetID, source, selectedKind, selectedIndex, now)
		}
		if selectedKind == "" && hasEngineErr && engineErr.Code == ytengine.CodeContentUnavailable && synErr == nil && isPureTextSyndicationTweet(st) {
			return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false), nil
		}
		if selectedKind == "" && hasEngineErr && engineErr.Code == ytengine.CodeContentUnavailable && synErr != nil && !errors.Is(synErr, errSyndicationNotFound) {
			return syndicationContractError(synErr), nil
		}
		return p.handleExtractError(ctx, ytRes.err)
	}

	info := ytRes.info
	populateMissingMP4Sizes(ctx, info, p.sizeProber)
	adapted := AdaptInfo(info, tweetID, source, selectedIndex, now)
	if unified.Str(adapted["code"]) == "OK" {
		if !gotSyn {
			if supplementExpired {
				select {
				case synRes = <-synCh:
					gotSyn = true
				default:
					cancelSyn()
				}
			} else {
				select {
				case synRes = <-synCh:
					gotSyn = true
				case <-supplementTimer.C:
					cancelSyn()
				case <-ctx.Done():
					cancelSyn()
				}
			}
		}
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
			return nil, ctxErr
		}
		if selectedKind == "" && gotSyn && synRes.err == nil && synRes.tweet != nil && len(synRes.tweet.Photos) > 0 {
			withPhotos := adaptInfoWithPhotos(info, synRes.tweet.Photos, tweetID, source, selectedIndex, now)
			if unified.Str(withPhotos["code"]) == "OK" {
				return withPhotos, nil
			}
		}
		return adapted, nil
	}

	st, synErr := awaitFullSyndication()
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
		return nil, ctxErr
	}
	if synErr == nil && canFastPath(st) {
		if fastEnv, ok := executeFastPath(st); ok {
			if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
				return nil, ctxErr
			}
			return fastEnv, nil
		}
	}
	if synErr == nil && st != nil && st.Tombstone {
		return p.resolveTombstone(ctx, tweetID, source, selectedKind, selectedIndex, now)
	}
	if selectedKind == "" && unified.Str(adapted["code"]) == "NO_MEDIA" && synErr == nil && isPureTextSyndicationTweet(st) {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false), nil
	}
	if selectedKind == "" && unified.Str(adapted["code"]) == "NO_MEDIA" && synErr != nil && !errors.Is(synErr, errSyndicationNotFound) {
		return syndicationContractError(synErr), nil
	}
	return adapted, nil
}

func isPureTextSyndicationTweet(st *syndicationTweet) bool {
	return st != nil && !st.Tombstone && len(st.Photos) == 0 && !st.HasVideo && !st.HasCard && !st.HasQuotedMedia
}
