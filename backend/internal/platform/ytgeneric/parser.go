// Package ytgeneric provides a universal, table-driven yt-dlp platform adapter
// for long-tail platforms without requiring a dedicated Go package per site.
package ytgeneric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

const fbWatchUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var (
	dailyShortPath   = regexp.MustCompile(`^/([A-Za-z0-9]{1,64})/?$`)
	fbWatchShortPath = regexp.MustCompile(`^/(?:v/)?[A-Za-z0-9_-]{1,128}/?$`)
)

// SiteSpec declaratively defines an allowlisted long-tail yt-dlp platform.
type SiteSpec struct {
	PlatformID                   string   `json:"platform_id"`
	Name                         string   `json:"name"`
	SiteURL                      string   `json:"site_url"`
	Icon                         string   `json:"icon"`
	CatalogDomains               []string `json:"catalog_domains"`
	CatalogExactDomains          []string `json:"catalog_exact_domains,omitempty"`
	InputHosts                   []string `json:"input_hosts"`
	OutboundHosts                []string `json:"outbound_hosts"`
	MediaHostSuffix              []string `json:"media_host_suffix"`
	Extractors                   []string `json:"extractors"`
	MaxEntries                   int      `json:"max_entries"`
	EnableDASHMux                bool     `json:"enable_dash_mux"`
	EnableHLS                    bool     `json:"enable_hls"`
	AllowCodecLessProgressiveMP4 bool     `json:"allow_codecless_progressive_mp4"`
	SingleStreamHLSOnly          bool     `json:"single_stream_hls_only"`
}

// CatalogEntry returns the catalog registration record for this SiteSpec.
func (s SiteSpec) CatalogEntry() unified.Object {
	icon := strings.TrimSpace(s.Icon)
	if icon == "" {
		icon = s.PlatformID
	}
	rawDomains := s.CatalogDomains
	if len(rawDomains) == 0 && len(s.CatalogExactDomains) == 0 {
		for _, h := range s.InputHosts {
			clean := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), ".")
			if clean != "" {
				rawDomains = append(rawDomains, clean)
			}
		}
	}
	domains := make(unified.Array, 0, len(rawDomains))
	seen := make(map[string]bool, len(rawDomains))
	for _, d := range rawDomains {
		clean := strings.ToLower(strings.TrimSpace(d))
		if clean != "" && !seen[clean] {
			seen[clean] = true
			domains = append(domains, clean)
		}
	}
	entry := unified.Object{
		"id":           s.PlatformID,
		"name":         s.Name,
		"site_url":     s.SiteURL,
		"icon":         icon,
		"capabilities": unified.Array{"video"},
		"domains":      domains,
	}
	if len(s.CatalogExactDomains) > 0 {
		exact := make(unified.Array, 0, len(s.CatalogExactDomains))
		seenExact := make(map[string]bool, len(s.CatalogExactDomains))
		for _, d := range s.CatalogExactDomains {
			clean := strings.ToLower(strings.TrimSpace(d))
			if clean != "" && !seenExact[clean] {
				seenExact[clean] = true
				exact = append(exact, clean)
			}
		}
		if len(exact) > 0 {
			entry["exact_domains"] = exact
		}
	}
	return entry
}

func hostMatchesRule(host, rule string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	r := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rule), "."))
	if h == "" || r == "" {
		return false
	}
	if strings.HasPrefix(r, ".") {
		return strings.HasSuffix(h, r) && len(h) > len(r)
	}
	return h == r
}

// Parser implements api.Adapter for any SiteSpec using the shared ytengine pipeline.
type Parser struct {
	spec       SiteSpec
	engine     ytengine.Extractor
	sizeProber ytengine.SizeProber
	resolver   ytengine.Resolver
	fetcher    fetch.Executor
}

// WithFetcher attaches a bounded HTTP executor for short-link preflight (e.g. fb.watch).
func (p *Parser) WithFetcher(fetcher fetch.Executor) *Parser {
	if p != nil {
		p.fetcher = fetcher
	}
	return p
}

// AllowedTargetURL checks whether rawURL matches canonical yt-dlp target hosts (spec.InputHosts ONLY).
func (p *Parser) AllowedTargetURL(raw string) bool {
	_, ok := p.sanitizeTargetURL(raw)
	return ok
}

// New creates a universal yt-dlp parser for the given SiteSpec.
func New(spec SiteSpec, engine ytengine.Extractor, prober ytengine.SizeProber) *Parser {
	return NewWithResolver(spec, engine, prober, nil)
}

// NewWithResolver creates a universal yt-dlp parser with an explicit DNS resolver
// that enforces public-IP resolution on input and media hosts.
func NewWithResolver(spec SiteSpec, engine ytengine.Extractor, prober ytengine.SizeProber, resolver ytengine.Resolver) *Parser {
	if spec.MaxEntries <= 0 {
		spec.MaxEntries = 4
	}
	return &Parser{
		spec:       spec,
		engine:     engine,
		sizeProber: prober,
		resolver:   resolver,
	}
}

// AllowedInputURL checks whether rawURL is a safe HTTP/HTTPS URL accepted by this platform
// (including canonicalized bare/mobile aliases and approved short links).
func (p *Parser) AllowedInputURL(raw string) bool {
	_, _, ok := p.normalizeInputURL(raw)
	return ok
}

// AllowedOutboundHost checks whether host matches spec.InputHosts, spec.OutboundHosts, or spec.MediaHostSuffix.
func (p *Parser) AllowedOutboundHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if p == nil || h == "" || net.ParseIP(h) != nil {
		return false
	}
	for _, allowed := range p.spec.InputHosts {
		if hostMatchesRule(h, allowed) {
			return true
		}
	}
	for _, allowed := range p.spec.OutboundHosts {
		if hostMatchesRule(h, allowed) {
			return true
		}
	}
	for _, suffix := range p.spec.MediaHostSuffix {
		if hostMatchesRule(h, suffix) {
			return true
		}
	}
	return false
}

func (p *Parser) verifyPublicHost(ctx context.Context, host string) bool {
	if p == nil || p.resolver == nil {
		return true
	}
	cleanHost := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if cleanHost == "" || net.ParseIP(cleanHost) != nil {
		return false
	}
	ips, err := p.resolver.LookupNetIP(ctx, "ip", cleanHost)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !fetch.PublicIP(ip) {
			return false
		}
	}
	return true
}

// AllowedMediaURL checks whether a media/thumbnail URL belongs to spec.MediaHostSuffix.
func (p *Parser) AllowedMediaURL(raw string) bool {
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
	if host == "" || net.ParseIP(host) != nil {
		return false
	}
	if p == nil || len(p.spec.MediaHostSuffix) == 0 {
		return false
	}
	matched := false
	for _, suffix := range p.spec.MediaHostSuffix {
		if hostMatchesRule(host, suffix) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	return p.verifyPublicHost(context.Background(), host)
}

func parseSafeHTTPURL(raw string) (*url.URL, bool) {
	if unified.MediaURL(raw) == "" || len(raw) > 2048 {
		return nil, false
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return nil, false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return nil, false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || (scheme == "https" && n != 443) || (scheme == "http" && n != 80) {
			return nil, false
		}
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || net.ParseIP(host) != nil || strings.Trim(u.EscapedPath(), "/") == "" {
		return nil, false
	}
	u.Fragment = ""
	return u, true
}

func allowExactFBWatchURL(raw string) bool {
	u, ok := parseSafeHTTPURL(raw)
	if !ok || strings.ToLower(u.Hostname()) != "fb.watch" {
		return false
	}
	return fbWatchShortPath.MatchString(u.EscapedPath())
}

func isFacebookLoginPath(escapedPath string) bool {
	lower := strings.ToLower(escapedPath)
	return lower == "/login" || strings.HasPrefix(lower, "/login/") ||
		lower == "/checkpoint" || strings.HasPrefix(lower, "/checkpoint/")
}

func isFacebookLoginRedirect(raw string) bool {
	u, ok := parseSafeHTTPURL(raw)
	if !ok {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "www.facebook.com", "www.fb.watch", "fb.watch":
		return isFacebookLoginPath(u.EscapedPath())
	default:
		return false
	}
}

func (p *Parser) sanitizeTargetURL(raw string) (string, bool) {
	if p == nil {
		return "", false
	}
	u, ok := parseSafeHTTPURL(raw)
	if !ok {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	matched := false
	for _, allowed := range p.spec.InputHosts {
		if hostMatchesRule(host, allowed) {
			matched = true
			break
		}
	}
	if !matched {
		return "", false
	}
	return u.String(), true
}

func (p *Parser) normalizeInputURL(raw string) (string, bool, bool) {
	if p == nil {
		return "", false, false
	}
	u, ok := parseSafeHTTPURL(raw)
	if !ok {
		return "", false, false
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range p.spec.InputHosts {
		if hostMatchesRule(host, allowed) {
			return u.String(), false, true
		}
	}
	switch p.spec.PlatformID {
	case "dailymotion":
		if host == "dailymotion.com" {
			u.Scheme = "https"
			u.Host = "www.dailymotion.com"
			return u.String(), false, true
		}
		if host == "dai.ly" {
			m := dailyShortPath.FindStringSubmatch(u.EscapedPath())
			if m == nil {
				return "", false, false
			}
			rewritten := "https://www.dailymotion.com/video/" + m[1]
			if u.RawQuery != "" {
				rewritten += "?" + u.RawQuery
			}
			return rewritten, false, true
		}
	case "tumblr":
		if host == "tumblr.com" {
			u.Scheme = "https"
			u.Host = "www.tumblr.com"
			return u.String(), false, true
		}
	case "facebook":
		if host == "facebook.com" || host == "m.facebook.com" {
			u.Scheme = "https"
			u.Host = "www.facebook.com"
			return u.String(), false, true
		}
		if host == "fb.watch" && fbWatchShortPath.MatchString(u.EscapedPath()) {
			u.Scheme = "https"
			u.Host = "fb.watch"
			return u.String(), true, true
		}
	}
	return "", false, false
}

func (p *Parser) resolveFBWatch(ctx context.Context, shortURL string) (string, unified.Object, error) {
	if !p.verifyPublicHost(ctx, "fb.watch") {
		return "", unified.ContractError("UPSTREAM_BLOCKED", "目标地址解析到受限网络，已拒绝访问", false), nil
	}
	if p.fetcher == nil {
		return "", unified.ContractError("SERVICE_NOT_CONFIGURED", "当前平台解析服务暂不可用", false), nil
	}
	resp, err := p.fetcher.Do(ctx, fetch.Request{
		Method: http.MethodGet,
		URL:    shortURL,
		Header: http.Header{
			"User-Agent": {fbWatchUserAgent},
			"Accept":     {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
		},
		Policy: fetch.Policy{
			Purpose:          "facebook-fbwatch",
			AllowedURL:       allowExactFBWatchURL,
			Timeout:          10 * time.Second,
			MaxBytes:         64 << 10,
			FollowRedirects:  false,
			ReadRedirectBody: false,
		},
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
			return "", nil, ctxErr
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", unified.ContractError("UPSTREAM_TIMEOUT", "上游连接超时，请稍后重试", true), nil
		}
		if errors.Is(err, fetch.ErrPolicy) {
			return "", unified.ContractError("UPSTREAM_BLOCKED", "目标地址解析到受限网络，已拒绝访问", false), nil
		}
		return "", unified.ContractError("UPSTREAM_UNAVAILABLE", "当前平台解析服务暂不可用，请稍后重试", true), nil
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", unified.ContractError("UPSTREAM_BLOCKED", "该内容受访问限制，无法无凭据解析", false), nil
	case http.StatusNotFound, http.StatusGone:
		return "", unified.ContractError("CONTENT_UNAVAILABLE", "未找到可用的媒体，或该内容已不可访问", false), nil
	case http.StatusTooManyRequests:
		return "", unified.ContractError("RATE_LIMITED", "上游访问频率受限，请稍后重试", true), nil
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		locations := resp.Header.Values("Location")
		if len(locations) != 1 {
			return "", unified.ContractError("INVALID_INPUT", "分享短链未跳转到受支持的 Facebook 页面", false), nil
		}
		loc := strings.TrimSpace(locations[0])
		if isFacebookLoginRedirect(loc) {
			return "", unified.ContractError("UPSTREAM_BLOCKED", "该内容需要登录或受访问限制，无法无凭据解析", false), nil
		}
		canonical, ok := p.sanitizeTargetURL(loc)
		if !ok {
			return "", unified.ContractError("INVALID_INPUT", "分享短链未跳转到受支持的 Facebook 页面", false), nil
		}
		return canonical, nil, nil
	default:
		if resp.StatusCode >= 500 {
			return "", unified.ContractError("UPSTREAM_UNAVAILABLE", "当前平台解析服务暂不可用，请稍后重试", true), nil
		}
		return "", unified.ContractError("INVALID_INPUT", "分享短链未跳转到受支持的 Facebook 页面", false), nil
	}
}

// Parse extracts metadata via ytengine and formats a unified-v1 envelope.
func (p *Parser) Parse(ctx context.Context, source string, now int64) (unified.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cleanURL, isFBWatch, ok := p.normalizeInputURL(source)
	if !ok {
		return unified.ContractError("INVALID_INPUT", "请输入对应平台的有效分享链接", false), nil
	}
	if p == nil || p.engine == nil || !p.engine.Enabled() {
		return unified.ContractError("SERVICE_NOT_CONFIGURED", "当前平台解析服务暂不可用", false), nil
	}
	if isFBWatch {
		resolvedTarget, errObj, err := p.resolveFBWatch(ctx, cleanURL)
		if err != nil {
			return nil, err
		}
		if errObj != nil {
			return errObj, nil
		}
		cleanURL = resolvedTarget
	}
	parsedURL, err := url.Parse(cleanURL)
	if err != nil || !p.verifyPublicHost(ctx, parsedURL.Hostname()) {
		return unified.ContractError("UPSTREAM_BLOCKED", "目标地址解析到受限网络，已拒绝访问", false), nil
	}
	if p.spec.PlatformID == "facebook" && isFacebookLoginPath(parsedURL.EscapedPath()) {
		return unified.ContractError("UPSTREAM_BLOCKED", "该内容需要登录或受访问限制，无法无凭据解析", false), nil
	}

	info, err := p.engine.Extract(ctx, cleanURL)
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

	contentID := strings.TrimSpace(info.DisplayID)
	if contentID == "" {
		contentID = strings.TrimSpace(info.ID)
	}
	if contentID == "" {
		sum := sha256.Sum256([]byte(cleanURL))
		contentID = hex.EncodeToString(sum[:])[:16]
	}

	opts := ytengine.AdaptOptions{
		PlatformID:                   p.spec.PlatformID,
		ContentID:                    contentID,
		SourceURL:                    source,
		MaxEntries:                   p.spec.MaxEntries,
		AllowedMediaURL:              p.AllowedMediaURL,
		EnableDASHMux:                p.spec.EnableDASHMux,
		EnableHLS:                    p.spec.EnableHLS,
		AllowCodecLessProgressiveMP4: p.spec.AllowCodecLessProgressiveMP4,
		SingleStreamHLSOnly:          p.spec.SingleStreamHLSOnly,
	}
	ytengine.PopulateMissingMP4Sizes(ctx, info, p.sizeProber, opts)
	return ytengine.AdaptInfo(info, opts, now), nil
}
