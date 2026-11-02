// Package douyin preserves the PHP Douyin formatter and unified-v1 adapter.
// Every request is injected and scoped to its purpose; no process-global
// cookies, redirect state, parse results, or probe budget are retained.
package douyin

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type Prober interface {
	Probe(context.Context, string) (unified.Object, error)
}

type Parser struct {
	executor fetch.Executor
	prober   Prober
}

func New(executor fetch.Executor, prober Prober) *Parser {
	return &Parser{executor: executor, prober: prober}
}

var shareURL = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
var awemeID = regexp.MustCompile(`^[1-9][0-9]{15,19}$`)

func extractShareURL(input string) string {
	return strings.TrimRight(shareURL.FindString(input), "'\"，。；,.;")
}

func extractAwemeID(raw string) string {
	base, _, _ := strings.Cut(raw, "#")
	path, query, _ := strings.Cut(base, "?")
	entries := queryEntries(query)
	for _, key := range []string{"vid", "id", "modal_id", "v", "s", "pid"} {
		if value := entries[key]; value.scalar && truthy(value.value) {
			return value.value
		}
	}
	if scheme := strings.Index(path, "://"); scheme >= 0 {
		path = path[scheme+3:]
		if slash := strings.IndexByte(path, '/'); slash >= 0 {
			path = path[slash:]
		} else {
			return ""
		}
	}
	segments := strings.Split(path, "/")
	for i := len(segments) - 1; i >= 0; i-- {
		if truthy(segments[i]) {
			return strings.TrimSuffix(segments[i], ".html")
		}
	}
	return ""
}

func (p *Parser) Parse(ctx context.Context, input string, now int64) (unified.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if input == "" || input == "0" {
		return legacyError(400, "请输入抖音链接"), nil
	}
	sourceURL := extractShareURL(input)
	if sourceURL == "" {
		return legacyError(400, "未在文本中识别到有效链接"), nil
	}
	if !allowedShareURL(sourceURL) {
		return legacyError(400, "请输入对应平台的有效分享链接"), nil
	}
	if p == nil || p.executor == nil {
		return nil, fmt.Errorf("douyin request executor is not configured")
	}
	resolved := sourceURL
	host := urlHost(resolved)
	if host == "v.douyin.com" || !strings.Contains(host, "douyin.com") || extractAwemeID(resolved) == "" {
		var err error
		resolved, err = p.resolveRedirect(ctx, resolved)
		if err != nil {
			return nil, err
		}
	}
	id := extractAwemeID(resolved)
	if id == "" {
		return legacyError(400, "链接格式错误，无法提取作品 ID"), nil
	}
	if !awemeID.MatchString(id) {
		return legacyError(500, "请输入作品 aweme_id，而不是合集 mix_id"), nil
	}
	detail, reason, err := p.fetchDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		return legacyError(500, reason), nil
	}
	raw, err := p.formatData(ctx, detail, id)
	if err != nil {
		return nil, err
	}
	return adapt(raw, sourceURL, now), nil
}

func (p *Parser) fetchDetail(ctx context.Context, id string) (object, string, error) {
	response, err := p.executor.Do(ctx, detailRequest(id))
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if err != nil {
		return nil, "详情请求失败", nil
	}
	if !utf8.Valid(response.Body) {
		return nil, "详情接口缺少有效 status_code", nil
	}
	payload, err := unified.Decode(response.Body)
	if err != nil {
		return nil, "详情接口缺少有效 status_code", nil
	}
	_, ok := unified.Numeric(payload["status_code"])
	if !ok {
		return nil, "详情接口缺少有效 status_code", nil
	}
	status, _ := unified.PHPInt(payload["status_code"])
	if status != 0 {
		return nil, fmt.Sprintf("详情接口 status_code=%d", status), nil
	}
	detail := unified.Map(payload["aweme_detail"])
	if len(detail) == 0 {
		return nil, "详情接口未返回 aweme_detail（不能据此断言作品已删除）", nil
	}
	if stringValue(detail["aweme_id"]) != id {
		return nil, "详情接口返回的作品 ID 不匹配", nil
	}
	if err := normalizeAudioURLLists(response.Body, detail); err != nil {
		return nil, "详情接口音轨来源格式无效", nil
	}
	return detail, "", nil
}

// Error mappings match snapAdaptParser. A detail endpoint's HTTP status alone
// never proves that a work was deleted; its legacy 500 remains retryable.
func legacyError(code int, message string) object {
	contractCode := "UPSTREAM_UNAVAILABLE"
	switch code {
	case -1, 0, 201, 400:
		contractCode = "INVALID_INPUT"
	case 401, 403, 412:
		contractCode = "UPSTREAM_BLOCKED"
	case 404:
		contractCode = "CONTENT_UNAVAILABLE"
	case 429:
		contractCode = "RATE_LIMITED"
	case 408, 504:
		contractCode = "UPSTREAM_TIMEOUT"
	case 502:
		contractCode = "UPSTREAM_INVALID_RESPONSE"
	}
	retryable := contractCode == "RATE_LIMITED" || contractCode == "UPSTREAM_TIMEOUT" || contractCode == "UPSTREAM_UNAVAILABLE"
	return unified.ContractError(contractCode, message, retryable)
}

func adapt(raw object, sourceURL string, now int64) object {
	raw = unified.Clone(raw)
	if unified.MediaURL(raw["url"]) != "" {
		candidates := append(array{unified.Clone(raw)}, unified.Slice(raw["video_backup"])...)
		for _, value := range candidates {
			candidate := unified.Map(value)
			if unified.Str(candidate["private_delivery_kind"]) != "video_track" {
				continue
			}
			audioID := pick(candidate["private_audio_id"], "")
			matched := array{}
			for _, audio := range unified.Slice(raw["private_audio_candidates"]) {
				if audioID != "" && unified.Equal(unified.Map(audio)["private_audio_id"], audioID) {
					matched = append(matched, unified.CloneValue(audio))
				}
			}
			candidate["private_audio_candidates"] = matched
		}
		raw["video_candidates"] = candidates
	}
	data := unified.SimpleData("douyin", raw, sourceURL, now)
	if len(unified.Slice(data["items"])) == 0 {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false)
	}
	return unified.FinalizeData(data, now)
}
