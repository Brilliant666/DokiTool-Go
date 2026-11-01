// Package kuaishou migrates the frozen PHP INIT_STATE/APOLLO parser. The
// existing public catalog remains video/image; no new platform capability is
// inferred from page fields.
package kuaishou

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type Parser struct{ executor fetch.Executor }

func New(executor fetch.Executor) *Parser { return &Parser{executor: executor} }

const userAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1 Edg/122.0.0.0"

var allowedURL = fetch.AllowHosts(".kuaishou.com")
var contentPaths = []struct {
	kind    string
	pattern *regexp.Regexp
}{
	{"short-video", regexp.MustCompile(`short-video/([^?]+)`)},
	{"long-video", regexp.MustCompile(`long-video/([^?]+)`)},
	{"photo", regexp.MustCompile(`photo/([^?]+)`)},
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

func (p *Parser) parseLegacy(parent context.Context, input string) (unified.Object, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if !allowedURL(input) {
		return failure(400, "请输入对应平台的有效分享链接"), nil
	}
	if p == nil || p.executor == nil {
		return nil, fmt.Errorf("kuaishou request executor is not configured")
	}
	// Original HEAD and GET each have a 20-second budget including redirects.
	// The combined real deadline cannot outlive both bounded requests.
	ctx, cancel := context.WithTimeout(parent, 40*time.Second)
	defer cancel()
	head, err := p.request(ctx, input, http.MethodHead)
	if cancelled := parent.Err(); cancelled != nil {
		return nil, cancelled
	}
	if err != nil || head.URL == "" {
		return failure(400, "无法获取有效链接"), nil
	}
	page, err := p.request(ctx, head.URL, http.MethodGet)
	if cancelled := parent.Err(); cancelled != nil {
		return nil, cancelled
	}
	if err != nil {
		return failure(500, "页面内容获取失败"), nil
	}
	// PHP extracts from the HEAD effective URL after retrieving the page. It
	// does not silently normalize a trailing slash or select another work from
	// the GET redirect's URL.
	kind, id := contentID(head.URL)
	if id == "" || id == "0" {
		return failure(400, "无法识别的链接类型"), nil
	}
	if result, err := fromInitState(string(page.Body)); result != nil || err != nil {
		return result, err
	}
	if result, err := fromApolloState(string(page.Body), id, kind); result != nil || err != nil {
		return result, err
	}
	return failure(404, "未找到有效媒体信息"), nil
}

func (p *Parser) request(ctx context.Context, target, method string) (fetch.Response, error) {
	if !allowedURL(target) {
		return fetch.Response{}, fetch.ErrPolicy
	}
	headers := http.Header{"User-Agent": {userAgent}, "Accept": {"*/*"}}
	if method == http.MethodGet {
		headers = http.Header{
			"User-Agent":                {userAgent},
			"Accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
			"Accept-Language":           {"zh-CN,zh;q=0.9"},
			"Cache-Control":             {"no-cache"},
			"Connection":                {"keep-alive"},
			"Pragma":                    {"no-cache"},
			"Sec-Fetch-Dest":            {"document"},
			"Sec-Fetch-Mode":            {"navigate"},
			"Sec-Fetch-Site":            {"none"},
			"Sec-Fetch-User":            {"?1"},
			"Upgrade-Insecure-Requests": {"1"},
			"Sec-Ch-Ua":                 {`"Google Chrome";v="143", "Chromium";v="143", "Not A(Brand";v="24"`},
			"Sec-Ch-Ua-Mobile":          {"?0"},
			"Sec-Ch-Ua-Platform":        {`"Windows"`},
			"Cookie":                    {""},
		}
	}
	request := fetch.Request{URL: target, Method: method, Header: headers,
		Policy: fetch.Policy{Purpose: "kuaishou-page", AllowedURL: allowedURL, Timeout: 20 * time.Second,
			MaxBytes: 8 << 20, FollowRedirects: true, AutoReferer: method == http.MethodGet, MaxRedirects: 5}}
	if method == http.MethodHead {
		request.Policy.Purpose = "kuaishou-share"
	}
	result, err := p.executor.Do(ctx, request)
	if err != nil {
		return fetch.Response{}, err
	}
	if !allowedURL(result.URL) || int64(len(result.Body)) > request.Policy.MaxBytes {
		return fetch.Response{}, fetch.ErrPolicy
	}
	for _, hop := range result.History {
		if !allowedURL(hop) {
			return fetch.Response{}, fetch.ErrPolicy
		}
	}
	return result, nil
}

func contentID(raw string) (kind, id string) {
	for _, candidate := range contentPaths {
		if match := candidate.pattern.FindStringSubmatch(raw); match != nil {
			return candidate.kind, match[1]
		}
	}
	return "", ""
}

func failure(code int, message string) unified.Object {
	return unified.Object{"code": code, "msg": message}
}

func adaptLegacy(legacy unified.Object, source string, now int64) unified.Object {
	code, ok := unified.PHPInt(legacy["code"])
	if !ok {
		return unified.ContractError("UPSTREAM_INVALID_RESPONSE", "解析器返回格式异常", false)
	}
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
		message, valid := legacy["msg"].(string)
		if !valid {
			message = "上游解析暂时不可用"
		}
		return unified.ContractError(kind, message, kind == "RATE_LIMITED" || kind == "UPSTREAM_TIMEOUT" || kind == "UPSTREAM_UNAVAILABLE")
	}
	data := unified.SimpleData("kuaishou", raw, source, now)
	if len(unified.Slice(data["items"])) == 0 {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false)
	}
	return unified.FinalizeData(data, now)
}
