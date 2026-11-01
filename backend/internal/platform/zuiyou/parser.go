// Package zuiyou preserves the frozen PHP parser and upstream video-map identity.
package zuiyou

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type Parser struct{ executor fetch.Executor }

func New(executor fetch.Executor) *Parser { return &Parser{executor: executor} }

var detailID = regexp.MustCompile(`/detail/([0-9]+)`)
var validPID = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

func (p *Parser) Parse(ctx context.Context, input string, now int64) (unified.Object, error) {
	legacy, err := p.parseLegacy(ctx, input)
	if err != nil {
		return nil, err
	}
	wire, err := legacyWire(legacy)
	if err != nil {
		return nil, err
	}
	code, _ := unified.PHPInt(wire["code"])
	if code != 200 || unified.Map(wire["data"]) == nil {
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
		retry := kind == "RATE_LIMITED" || kind == "UPSTREAM_TIMEOUT" || kind == "UPSTREAM_UNAVAILABLE"
		return unified.ContractError(kind, unified.Str(wire["msg"]), retry), nil
	}
	data := adapt(unified.Map(wire["data"]), input, now)
	if len(unified.Slice(data["items"])) == 0 {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false), nil
	}
	return unified.FinalizeData(data, now), nil
}

func (p *Parser) parseLegacy(parent context.Context, input string) (unified.Object, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if !allowedShare(input) {
		return failure(400, "请输入有效的URL"), nil
	}
	u, _ := url.Parse(input)
	pid := queryPID(u.RawQuery)
	if emptyString(pid) {
		if match := detailID.FindStringSubmatch(u.EscapedPath()); match != nil {
			pid = match[1]
		}
	}
	if p == nil || p.executor == nil {
		return nil, fmt.Errorf("zuiyou request executor is not configured")
	}
	ctx, cancel := context.WithTimeout(parent, 35*time.Second)
	defer cancel()
	if emptyString(pid) {
		response, err := p.executor.Do(ctx, shareRequest(input))
		if err := parent.Err(); err != nil {
			return nil, err
		}
		if err == nil && response.StatusCode >= 300 && response.StatusCode < 400 {
			// The PHP path extracts pid from one Location only; it never sends
			// a request to that Location. The subsequent API address is fixed.
			if values := response.Header.Values("Location"); len(values) > 0 {
				location := strings.Trim(values[0], " \n\r\t\v\x00")
				if !strings.HasPrefix(location, "http") {
					location = u.Scheme + "://" + u.Hostname() + location
				}
				if target, err := url.Parse(location); err == nil && location != input {
					pid = queryPID(target.RawQuery)
				}
			}
		}
	}
	if !validPID.MatchString(pid) {
		return failure(400, "找不到有效的pid参数"), nil
	}
	number, _ := unified.PHPInt(pid)
	body := `{"pid":` + strconv.FormatInt(number, 10) + `,"h_av":"5.2.13.011"}`
	response, err := p.executor.Do(ctx, detailRequest(body))
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if err != nil || len(response.Body) > 8<<20 {
		return failure(502, "API请求失败"), nil
	}
	root, err := decodeDocument(response.Body)
	if err != nil || !root.isArray() {
		return failure(502, "API响应解析失败"), nil
	}
	return formatResponse(root), nil
}

func emptyString(value string) bool { return value == "" || value == "0" }

func failure(code int, message string) unified.Object {
	return unified.Object{"code": code, "msg": message, "data": unified.Array{}}
}
