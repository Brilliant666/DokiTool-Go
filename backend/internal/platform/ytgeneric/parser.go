// Package ytgeneric provides a universal, table-driven yt-dlp platform adapter
// for long-tail platforms without requiring a dedicated Go package per site.
package ytgeneric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

// SiteSpec declaratively defines an allowlisted long-tail yt-dlp platform.
type SiteSpec struct {
	PlatformID                   string   `json:"platform_id"`
	Name                         string   `json:"name"`
	SiteURL                      string   `json:"site_url"`
	InputHosts                   []string `json:"input_hosts"`
	MediaHostSuffix              []string `json:"media_host_suffix"`
	MaxEntries                   int      `json:"max_entries"`
	EnableDASHMux                bool     `json:"enable_dash_mux"`
	EnableHLS                    bool     `json:"enable_hls"`
	AllowCodecLessProgressiveMP4 bool     `json:"allow_codecless_progressive_mp4"`
}

// Parser implements api.Adapter for any SiteSpec using the shared ytengine pipeline.
type Parser struct {
	spec       SiteSpec
	engine     ytengine.Extractor
	sizeProber ytengine.SizeProber
}

// New creates a universal yt-dlp parser for the given SiteSpec.
func New(spec SiteSpec, engine ytengine.Extractor, prober ytengine.SizeProber) *Parser {
	if spec.MaxEntries <= 0 {
		spec.MaxEntries = 4
	}
	return &Parser{
		spec:       spec,
		engine:     engine,
		sizeProber: prober,
	}
}

// AllowedInputURL checks whether rawURL is a safe HTTP/HTTPS URL belonging to spec.InputHosts.
func (p *Parser) AllowedInputURL(raw string) bool {
	_, ok := p.sanitizeInputURL(raw)
	return ok
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
	for _, suffix := range p.spec.MediaHostSuffix {
		s := strings.ToLower(strings.TrimSpace(suffix))
		if s == "" {
			continue
		}
		if host == strings.TrimPrefix(s, ".") || (strings.HasPrefix(s, ".") && strings.HasSuffix(host, s) && len(host) > len(s)) {
			return true
		}
	}
	return false
}

func (p *Parser) sanitizeInputURL(raw string) (string, bool) {
	if p == nil || unified.MediaURL(raw) == "" || len(raw) > 2048 {
		return "", false
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return "", false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || (scheme == "https" && n != 443) || (scheme == "http" && n != 80) {
			return "", false
		}
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || net.ParseIP(host) != nil || strings.Trim(u.EscapedPath(), "/") == "" {
		return "", false
	}
	matched := false
	for _, allowed := range p.spec.InputHosts {
		ah := strings.ToLower(strings.TrimSpace(allowed))
		if ah == "" {
			continue
		}
		if host == ah || (strings.HasPrefix(ah, ".") && strings.HasSuffix(host, ah) && len(host) > len(ah)) {
			matched = true
			break
		}
	}
	if !matched {
		return "", false
	}
	u.Fragment = ""
	return u.String(), true
}

// Parse extracts metadata via ytengine and formats a unified-v1 envelope.
func (p *Parser) Parse(ctx context.Context, source string, now int64) (unified.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cleanURL, ok := p.sanitizeInputURL(source)
	if !ok {
		return unified.ContractError("INVALID_INPUT", "请输入对应平台的有效分享链接", false), nil
	}
	if p == nil || p.engine == nil || !p.engine.Enabled() {
		return unified.ContractError("SERVICE_NOT_CONFIGURED", "当前平台解析服务暂不可用", false), nil
	}

	info, err := p.engine.Extract(ctx, cleanURL)
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
	}
	ytengine.PopulateMissingMP4Sizes(ctx, info, p.sizeProber, opts)
	return ytengine.AdaptInfo(info, opts, now), nil
}
