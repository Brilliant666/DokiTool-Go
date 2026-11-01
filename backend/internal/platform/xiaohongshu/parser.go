// Package xiaohongshu preserves the frozen PHP desktop-page and media contract.
// New TLS transports and live 4K acceptance are separate migration work.
package xiaohongshu

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const desktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var notePath = regexp.MustCompile(`/(?:discovery/item|explore|item|note)/([a-zA-Z0-9]+)(?:/|$)`)
var authority = regexp.MustCompile(`(?i)^https?://[^/?#]+`)

type Parser struct {
	executor fetch.Executor
	clock    func() time.Time
}

func New(executor fetch.Executor) *Parser { return NewWithClock(executor, time.Now) }
func NewWithClock(executor fetch.Executor, clock func() time.Time) *Parser {
	if clock == nil {
		clock = time.Now
	}
	return &Parser{executor: executor, clock: clock}
}

func AllowedURL(raw string) bool {
	if unified.MediaURL(raw) == "" {
		return false
	}
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f || r == '\\' {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") || (u.Port() != "" && u.Port() != "80" && u.Port() != "443") {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "xiaohongshu.com", "www.xiaohongshu.com", "rednote.com", "www.rednote.com", "xhslink.com", "www.xhslink.com", "xhs.com", "www.xhs.com":
		return true
	}
	return false
}

func extractID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if match := notePath.FindStringSubmatch(u.EscapedPath()); match != nil {
		return match[1]
	}
	return ""
}

// Replace only the authority; signed query bytes and '+' remain unchanged.
func desktopURL(raw, host string) string { return authority.ReplaceAllString(raw, "https://"+host) }

type pageResponse struct {
	status int
	body   string
	url    string
}

func (p *Parser) page(parent context.Context, address string) pageResponse {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	deadline := p.clock().Add(10 * time.Second)
	for redirects := 0; redirects <= 5 && AllowedURL(address); redirects++ {
		remaining := deadline.Sub(p.clock()).Truncate(time.Millisecond)
		if remaining < time.Millisecond || ctx.Err() != nil {
			break
		}
		r, err := p.executor.Do(ctx, fetch.Request{URL: address, Method: http.MethodGet,
			Header: http.Header{"User-Agent": {desktopUA}, "Accept": {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
				"Accept-Language": {"zh-CN,zh;q=0.9,en;q=0.8"}, "Accept-Encoding": {"gzip,deflate"}},
			Policy: fetch.Policy{Purpose: "xiaohongshu-page", AllowedURL: AllowedURL, Timeout: remaining, MaxBytes: 8 << 20}})
		if err != nil {
			return pageResponse{url: address}
		}
		if len(r.History) != 0 || r.URL != "" && r.URL != address || len(r.Body) > 8<<20 {
			return pageResponse{status: 502, url: address}
		}
		switch r.StatusCode {
		case 301, 302, 303, 307, 308:
			locations := r.Header.Values("Location")
			if len(locations) != 0 {
				next := strings.Trim(locations[len(locations)-1], " \t\r\n\v\x00")
				if strings.HasPrefix(next, "//") {
					next = "https:" + next
				} else if strings.HasPrefix(next, "/") {
					current, _ := url.Parse(address)
					next = "https://" + current.Hostname() + next
				}
				if !AllowedURL(next) {
					return pageResponse{status: 502, url: address}
				}
				address = next
				continue
			}
		}
		return pageResponse{status: r.StatusCode, body: string(r.Body), url: address}
	}
	return pageResponse{status: 502, url: address}
}

func (p *Parser) Parse(ctx context.Context, input string, now int64) (unified.Object, error) {
	legacy, err := p.parseLegacy(ctx, input)
	if err != nil {
		return nil, err
	}
	wire, err := legacyWire(legacy)
	if err != nil {
		return nil, err
	}
	return adaptLegacy(wire, input, now), nil
}

func (p *Parser) parseLegacy(ctx context.Context, input string) (unified.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !AllowedURL(input) {
		return legacyResult(400, "请输入有效的小红书或 rednote 分享链接", nil), nil
	}
	if p == nil || p.executor == nil {
		return nil, fmt.Errorf("xiaohongshu request executor is not configured")
	}
	address := input
	u, _ := url.Parse(address)
	if host := strings.ToLower(u.Hostname()); host == "xhs.com" || host == "www.xhs.com" {
		address = desktopURL(address, "xhslink.com")
	}
	id := extractID(address)
	if id == "" {
		redirected := p.page(ctx, address)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		address = redirected.url
		id = extractID(address)
		if !AllowedURL(address) || id == "" {
			return legacyResult(400, "分享链接未指向可识别的笔记", nil), nil
		}
	}
	lastStatus := 0
	for _, host := range []string{"www.rednote.com", "www.xiaohongshu.com"} {
		response := p.page(ctx, desktopURL(address, host))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lastStatus = response.status
		if lastStatus != 200 {
			continue
		}
		if note := extractNote(response.body, id); note != nil {
			data, err := p.formatNote(ctx, note)
			if err != nil {
				return nil, err
			}
			return legacyResult(200, "解析成功", data), nil
		}
	}
	code := 404
	if lastStatus == 0 || lastStatus >= 500 {
		code = 502
	} else if lastStatus == 403 || lastStatus == 429 {
		code = lastStatus
	}
	return legacyResult(code, "未取得可用笔记内容，请检查分享 token 是否有效；来源可能要求验证或暂时限制访问", nil), nil
}

func legacyResult(code int, message string, data unified.Object) unified.Object {
	var body any = data
	if data == nil {
		body = unified.Array{}
	}
	return unified.Object{"code": code, "msg": message, "data": body}
}

func adaptLegacy(legacy unified.Object, input string, now int64) unified.Object {
	code, _ := unified.PHPInt(legacy["code"])
	raw := unified.Map(legacy["data"])
	if code != 200 || raw == nil {
		kind := "UPSTREAM_UNAVAILABLE"
		switch code {
		case -1, 0, 201, 400:
			kind = "INVALID_INPUT"
		case 401, 403, 412:
			kind = "UPSTREAM_BLOCKED"
		case 404:
			kind = "CONTENT_UNAVAILABLE"
		case 429:
			kind = "RATE_LIMITED"
		case 408, 504:
			kind = "UPSTREAM_TIMEOUT"
		case 502:
			kind = "UPSTREAM_INVALID_RESPONSE"
		}
		return unified.ContractError(kind, unified.Str(legacy["msg"]), kind == "RATE_LIMITED" || kind == "UPSTREAM_TIMEOUT" || kind == "UPSTREAM_UNAVAILABLE")
	}
	copy := unified.Clone(raw)
	statistics := unified.Object{}
	for _, pair := range [][2]string{{"like_count", "digg_count"}, {"comment_count", "comment_count"}, {"share_count", "share_count"}, {"collect_count", "collect_count"}} {
		if value, exists := unified.Map(raw["statistics"])[pair[0]]; exists {
			statistics[pair[1]] = value
		}
	}
	if len(statistics) != 0 {
		copy["extra"] = unified.Object{"statistics": statistics}
	}
	data := unified.SimpleData("xiaohongshu", copy, input, now)
	if len(unified.Slice(data["items"])) == 0 {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false)
	}
	return unified.FinalizeData(data, now)
}
