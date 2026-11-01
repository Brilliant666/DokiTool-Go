// Package pipigx preserves the fixed PHP single-POST video-only parser.
package pipigx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const endpoint = "https://h5.pipigx.com/ppapi/share/fetch_content"

type Parser struct{ executor fetch.Executor }

func New(executor fetch.Executor) *Parser { return &Parser{executor: executor} }

var pidPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
var midPattern = regexp.MustCompile(`^[0-9]{1,19}$`)

// The legacy guard compares doubles before casting the retained decimal
// strings to int64. Preserve that rounding boundary rather than silently
// changing the accepted parameter set during a language migration.
func ExtractParams(raw string) (int64, int64, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, 0, false
	}
	pid, pp, ps := queryValue(u.RawQuery, "pid")
	mid, mp, ms := queryValue(u.RawQuery, "mid")
	if !pp || !ps || !mp || !ms || !pidPattern.MatchString(pid) || !midPattern.MatchString(mid) {
		return 0, 0, false
	}
	pn, _ := strconv.ParseFloat(pid, 64)
	mn, _ := strconv.ParseFloat(mid, 64)
	if pn > float64(math.MaxInt64) || mn > float64(math.MaxInt64) {
		return 0, 0, false
	}
	p, _ := unified.PHPInt(pid)
	m, _ := unified.PHPInt(mid)
	return p, m, true
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
	if input == "" {
		return failure(400, "未提供 url 参数"), nil
	}
	pid, mid, ok := ExtractParams(input)
	if !ok {
		return failure(400, "提取参数出错"), nil
	}
	if p == nil || p.executor == nil {
		return nil, fmt.Errorf("pipigx request executor is not configured")
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	body := []byte(fmt.Sprintf(`{"pid":%d,"mid":%d,"type":"post"}`, pid, mid))
	request := fetch.Request{URL: endpoint, Method: http.MethodPost, Body: body, Header: http.Header{"Content-Type": {"application/json"}, "Content-Length": {strconv.Itoa(len(body))}, "Accept": {"*/*"}, "User-Agent": {""}}, Policy: fetch.Policy{Purpose: "pipigx-post", AllowedURL: func(raw string) bool { return raw == endpoint }, Timeout: 20 * time.Second, MaxBytes: 8 << 20, ReadRedirectBody: true}}
	reply, err := p.executor.Do(ctx, request)
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if err != nil || int64(len(reply.Body)) > request.Policy.MaxBytes || (reply.URL != "" && reply.URL != endpoint) {
		return failure(500, "HTTP 错误发生: HTTP 状态码 500"), nil
	}
	if reply.StatusCode >= 400 {
		return failure(reply.StatusCode, "HTTP 错误发生: HTTP 状态码 "+strconv.Itoa(reply.StatusCode)), nil
	}
	decoded, err := decodePHP(reply.Body, false, 512)
	if err != nil || decoded == nil {
		return failure(502, "响应内容不是有效的 JSON 数据"), nil
	}
	post := field(field(decoded, "data"), "post")
	if post == nil {
		return failure(500, "响应中缺少 data.post 字段"), nil
	}
	videos := unified.Array{}
	for _, video := range values(field(post, "videos")) {
		if isArray(video) {
			videos = append(videos, video)
		}
	}
	var firstVideo any
	if len(videos) > 0 {
		firstVideo = videos[0]
	}
	mediaURL := unified.MediaURL(field(firstVideo, "url"))
	if mediaURL == "" {
		return failure(404, "作品没有可用的视频地址"), nil
	}
	data := unified.Object{"type": "video", "title": plain(first(field(post, "content"), "")), "cover": "https://file.ippzone.com/img/frame/id/" + scalarString(field(firstVideo, "thumb")), "url": mediaURL, "private_videos": plain(videos)}
	return unified.Object{"code": 200, "msg": "解析成功", "data": data}, nil
}
func failure(code int, message string) unified.Object {
	return unified.Object{"code": code, "msg": message, "data": unified.Array{}}
}

func adaptLegacy(legacy unified.Object, source string, now int64) unified.Object {
	code, ok := unified.PHPInt(legacy["code"])
	if !ok {
		return unified.ContractError("UPSTREAM_INVALID_RESPONSE", "解析器返回格式异常", false)
	}
	if code != 200 || unified.Map(legacy["data"]) == nil {
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
	raw := unified.Map(legacy["data"])
	data := unified.SimpleData("pipigx", raw, source, now)
	items := unified.Array{}
	for index, video := range unified.Slice(raw["private_videos"]) {
		variants := unified.FileVariants(unified.Array{video}, now)
		if len(variants) == 0 {
			continue
		}
		id := "video-" + strconv.Itoa(index+1)
		if upstream, ok := unified.Text(unified.Map(video)["private_video_id"]); ok {
			hash := sha256.Sum256([]byte(upstream))
			id = "video-" + hex.EncodeToString(hash[:])[:24]
		}
		items = append(items, unified.Object{"id": id, "type": "video", "default": nil, "variants": variants, "resolve_url": unified.ResolveURL(source, id)})
	}
	if len(items) > 0 {
		for _, value := range unified.Slice(data["items"]) {
			if unified.Map(value)["type"] != "video" {
				items = append(items, value)
			}
		}
		data["items"], data["default_item_id"] = items, unified.Map(items[0])["id"]
	}
	if len(unified.Slice(data["items"])) == 0 {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false)
	}
	return unified.FinalizeData(data, now)
}
