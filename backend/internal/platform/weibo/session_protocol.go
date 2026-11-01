package weibo

import (
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// This private protocol stage follows the frozen PHP weibo.php product helpers.
// It does not authenticate, fetch remote URLs, or publish media by itself.
// Transport requests and guest cookies are added in the next parser stage.

func phpGuestTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != "" && v != "0"
	}
	if entries := unified.Map(value); entries != nil {
		return len(entries) > 0
	}
	if entries := unified.Slice(value); entries != nil {
		return len(entries) > 0
	}
	if number, ok := unified.Numeric(value); ok {
		return number != 0
	}
	return true
}

func phpGuestNumber(value any) int64 {
	switch v := value.(type) {
	case bool:
		if v {
			return 1
		}
		return 0
	}
	if number, ok := unified.Numeric(value); ok {
		if number >= math.MaxInt64 {
			return math.MaxInt64
		}
		if number <= math.MinInt64 {
			return math.MinInt64
		}
		return int64(number)
	}
	return 0
}

func phpGuestIntegerZero(value any) bool {
	switch v := value.(type) {
	case int:
		return v == 0
	case int64:
		return v == 0
	case json.Number:
		return v.String() == "0"
	}
	return false
}

// legacyTransportFailure retains the PHP's strict status=0 distinction.
// In particular the string "0" is not the integer 0 at the first branch.
func legacyTransportFailure(response unified.Object) unified.Object {
	if response == nil {
		response = unified.Object{}
	}
	status, exists := response["status"]
	if phpGuestTruthy(response["error"]) || !exists || status == nil || phpGuestIntegerZero(status) {
		return unified.Object{"code": 503, "msg": "微博接口暂时无法连接，请稍后重试"}
	}
	code := phpGuestNumber(status)
	switch code {
	case 401, 403, 429:
		return unified.Object{"code": 503, "msg": "微博当前限制匿名访问，请稍后重试"}
	default:
		if code >= 400 {
			return unified.Object{"code": 502, "msg": "微博接口请求失败（HTTP " + strconv.FormatInt(code, 10) + "）"}
		}
		return nil
	}
}

var visitorDocument = regexp.MustCompile(`(?i)^\s*(?:<!DOCTYPE[^>]*>\s*)?<(?:html|title)\b`)

// legacyIsVisitor detects the fixed passport host or the Sina HTML challenge.
// It must not treat a JSON string mentioning that phrase as a challenge.
func legacyIsVisitor(response unified.Object) bool {
	raw := unified.Str(response["url"])
	if parsed, err := url.Parse(raw); err == nil && strings.EqualFold(parsed.Hostname(), "passport.weibo.com") {
		return true
	}
	body := unified.Str(response["body"])
	return visitorDocument.MatchString(body) && strings.Contains(body, "Sina Visitor System")
}

func phpGuestAssociative(value any) any {
	if fields := unified.Map(value); fields != nil {
		if len(fields) == 0 {
			return []any{}
		}
		for key, child := range fields {
			fields[key] = phpGuestAssociative(child)
		}
		return fields
	}
	if items := unified.Slice(value); items != nil {
		for i, child := range items {
			items[i] = phpGuestAssociative(child)
		}
		return items
	}
	return value
}

// legacyJSONP parses only official callback-wrapped JSON objects, or ordinary
// JSON object/array bodies. It never evaluates executable JavaScript.
func legacyJSONP(body, callback string) any {
	quoted := regexp.QuoteMeta(callback)
	wrapped := regexp.MustCompile(`(?s)^\s*(?:(?:window\.)?` + quoted + `\s*&&\s*)?` + quoted + `\s*\((\{.*\})\);?\s*$`)
	if matches := wrapped.FindStringSubmatch(body); matches != nil {
		body = matches[1]
	}
	decoded, err := unified.DecodeValue([]byte(body))
	if err != nil {
		return nil
	}
	if unified.Map(decoded) == nil && unified.Slice(decoded) == nil {
		return nil
	}
	return phpGuestAssociative(decoded)
}
