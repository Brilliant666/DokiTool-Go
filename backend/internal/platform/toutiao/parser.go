// Package toutiao migrates the frozen PHP page parser and its anonymous
// session flow. Requests are injected and no cookies survive a Parse call.
package toutiao

import (
	"context"
	"fmt"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type Parser struct {
	executor fetch.Executor
	clock    func() time.Time
}

func New(executor fetch.Executor) *Parser {
	return NewWithClock(executor, time.Now)
}

// NewWithClock exposes only the shared request budget clock for deterministic
// contract checks. The actual cancellation deadline always uses real time.
func NewWithClock(executor fetch.Executor, clock func() time.Time) *Parser {
	return &Parser{executor: executor, clock: clock}
}

func (p *Parser) Parse(ctx context.Context, input string, now int64) (unified.Object, error) {
	legacy, err := p.parseLegacy(ctx, input)
	if err != nil {
		return nil, err
	}
	return adaptLegacy(legacy, input, now), nil
}

// parseLegacy keeps the original parser result available for independent PHP
// comparison. The monotonic deadline covers redirects, registration and page.
func (p *Parser) parseLegacy(parent context.Context, input string) (unified.Object, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if !AllowedURL(input) {
		return failure(400, "请提供有效的今日头条视频链接"), nil
	}
	if p == nil || p.executor == nil {
		return nil, fmt.Errorf("toutiao request executor is not configured")
	}
	clock := p.clock
	if clock == nil {
		clock = time.Now
	}
	deadline := clock().Add(35 * time.Second)
	ctx, cancel := context.WithTimeout(parent, 35*time.Second)
	defer cancel()
	session := requestSession{executor: p.executor, clock: clock, deadline: deadline}
	current, id := input, VideoID(input)
	for redirect := 0; id == "" && redirect < 3; redirect++ {
		response := session.request(ctx, current, "", false)
		if err := parent.Err(); err != nil {
			return nil, err
		}
		if !response.ok {
			return requestFailure(response, "今日头条分享链接请求失败，请稍后重试"), nil
		}
		if response.status < 300 || response.status >= 400 {
			break
		}
		current = RedirectURL(current, response.location)
		if current == "" {
			return failure(400, "今日头条分享链接重定向地址无效"), nil
		}
		id = VideoID(current)
	}
	if id == "" {
		return failure(400, "无法识别视频 ID，请复制今日头条视频页面链接"), nil
	}

	// The PHP implementation stores only the ttwid value in this parse. Other
	// registration cookies, prior share cookies and other requests are isolated.
	registration := session.request(ctx, registrationURL, "", true)
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if !registration.ok || registration.status != 200 || registration.ttwid == "" || registration.ttwid == "0" {
		return requestFailure(registration, "今日头条匿名会话初始化失败，请稍后重试"), nil
	}
	page := session.request(ctx, "https://www.toutiao.com/video/"+id+"/", registration.ttwid, false)
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if !page.ok {
		return requestFailure(page, "今日头条页面请求失败，请稍后重试"), nil
	}
	switch page.status {
	case 403, 429:
		return failure(429, "今日头条限制了本次访问，请稍后重试"), nil
	case 404:
		return failure(404, "今日头条视频页面不存在或不可访问"), nil
	case 200:
		return responseFromPage(page.body, id), nil
	default:
		return failure(502, "今日头条页面暂时不可用，请稍后重试"), nil
	}
}

func requestFailure(response response, message string) unified.Object {
	if response.budgetExhausted {
		message = "今日头条解析超时，请稍后重试"
	}
	return failure(502, message)
}

func failure(code int, message string) unified.Object {
	return unified.Object{"code": code, "msg": message}
}

// adaptLegacy is the existing snapAdaptParser/snapAdaptToutiao boundary.
// Candidate ordering belongs to extraction; final default selection remains
// exclusively in the shared unified layer.
func adaptLegacy(legacy unified.Object, source string, now int64) unified.Object {
	code, valid := unified.PHPInt(legacy["code"])
	if !valid {
		return unified.ContractError("UPSTREAM_INVALID_RESPONSE", "解析器返回格式异常", false)
	}
	raw := unified.Map(legacy["data"])
	if code != 200 || !phpArray(legacy["data"]) {
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
		message, ok := legacy["msg"].(string)
		if !ok {
			message = "上游解析暂时不可用"
		}
		retryable := kind == "RATE_LIMITED" || kind == "UPSTREAM_TIMEOUT" || kind == "UPSTREAM_UNAVAILABLE"
		return unified.ContractError(kind, message, retryable)
	}
	copy := unified.Clone(raw)
	if copy == nil {
		copy = unified.Object{}
	}
	copy["id"] = raw["itemId"]
	copy["author"] = unified.Object{
		"name":        firstDefined(raw["author"], ""),
		"id":          firstDefined(raw["userID"], ""),
		"avatar":      firstDefined(raw["avatar"], ""),
		"description": firstDefined(raw["author_description"], ""),
	}
	data := unified.SimpleData("toutiao", copy, source, now)
	if len(unified.Slice(data["items"])) == 0 {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false)
	}
	return unified.FinalizeData(data, now)
}
