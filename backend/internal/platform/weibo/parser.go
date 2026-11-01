package weibo

import (
	"context"
	"fmt"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// Parser connects the independently checked status/TV resolver to the actual
// unified-v1 product adapter. Every Parse owns a fresh anonymous Cookie jar.
type Parser struct { executor fetch.Executor }

func New(executor fetch.Executor) *Parser { return &Parser{executor: executor} }

func (p *Parser) Parse(parent context.Context, input string, now int64) (unified.Object, error) {
	if err := parent.Err(); err != nil { return nil, err }
	target := ParseTarget(input)
	if target == nil {
		return unified.ContractError("INVALID_INPUT", "请输入有效的微博视频或微博正文链接", false), nil
	}
	if p == nil || p.executor == nil { return nil, fmt.Errorf("weibo request executor is not configured") }
	ctx, cancel := context.WithTimeout(parent, 35*time.Second)
	defer cancel()
	return parseWithSession(ctx, newNetworkGuestSession(p.executor), target, input, now)
}

func parseWithSession(ctx context.Context, session *guestSession, target *Target, input string, now int64) (unified.Object, error) {
	if err := ctx.Err(); err != nil { return nil, err }
	legacy, err := session.resolveLegacy(ctx, target)
	if err != nil { return nil, err }
	if err := ctx.Err(); err != nil { return nil, err }
	return adaptLegacy(legacy, input, now), nil
}

// This is the frozen PHP snapAdaptParser/snapAdaptWeibo private boundary.
// The parser's video_backup already contains its selected primary source;
// prepending raw.url would duplicate it and change stable variant identities.
func adaptLegacy(legacy unified.Object, sourceURL string, now int64) unified.Object {
	code, valid := unified.Numeric(legacy["code"])
	if !valid { return unified.ContractError("UPSTREAM_INVALID_RESPONSE", "解析器返回格式异常", false) }
	raw := unified.Map(legacy["data"])
	if int64(code) != 200 || raw == nil {
		problem := "UPSTREAM_UNAVAILABLE"
		switch int64(code) {
		case -1, 0, 201, 400: problem = "INVALID_INPUT"
		case 401, 403, 412: problem = "UPSTREAM_BLOCKED"
		case 404: problem = "CONTENT_UNAVAILABLE"
		case 429: problem = "RATE_LIMITED"
		case 408, 504: problem = "UPSTREAM_TIMEOUT"
		case 502: problem = "UPSTREAM_INVALID_RESPONSE"
		}
		message, ok := legacy["msg"].(string)
		if !ok { message = "上游解析暂时不可用" }
		retryable := problem == "RATE_LIMITED" || problem == "UPSTREAM_TIMEOUT" || problem == "UPSTREAM_UNAVAILABLE"
		return unified.ContractError(problem, message, retryable)
	}
	copied := unified.Clone(raw)
	if candidates := unified.Slice(copied["video_backup"]); len(candidates) != 0 {
		copied["video_candidates"] = unified.CloneValue(candidates)
	}
	data := unified.SimpleData("weibo", copied, sourceURL, now)
	if len(unified.Slice(data["items"])) == 0 {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false)
	}
	return unified.FinalizeData(data, now)
}
