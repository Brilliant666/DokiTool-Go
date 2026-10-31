// Package bilibili preserves the PHP parser and unified-v1 adapter while
// resolving only the requested part. Network execution remains injected.
package bilibili

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

type Parser struct { executor fetch.Executor }

func New(executor fetch.Executor) *Parser { return &Parser{executor: executor} }

var partNumber = regexp.MustCompile(`^[1-9][0-9]{0,5}$`)
var videoID = regexp.MustCompile(`^BV[a-zA-Z0-9]{10}$`)
var videoPath = regexp.MustCompile(`/video/(BV[a-zA-Z0-9]{10})(?:/|$)`)
var videoMeta = regexp.MustCompile(`(?i)<meta\s+[^>]*itemprop="url"\s+[^>]*content="https://www\.bilibili\.com/video/(BV[a-zA-Z0-9]{10})/?"[^>]*>`)

func (p *Parser) Parse(ctx context.Context, input string, now int64) (unified.Object, error) {
	legacy, err := p.parseLegacy(ctx, input)
	if err != nil { return nil, err }
	legacy, err = legacyWire(legacy)
	if err != nil { return nil, err }
	code := intValue(legacy["code"])
	raw := unified.Map(legacy["data"])
	if code != 200 || raw == nil { return legacyError(code, unified.Str(legacy["msg"])), nil }
	data := adapt(raw, input, now)
	if len(unified.Slice(data["items"])) == 0 {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false), nil
	}
	return unified.FinalizeData(data, now), nil
}

// parseLegacy retains the private parser boundary for frozen PHP comparison.
// Its bounded context is shared by redirects, the page fallback and both APIs.
func (p *Parser) parseLegacy(parent context.Context, source string) (object, error) {
	if err := parent.Err(); err != nil { return nil, err }
	ctx, cancel := context.WithTimeout(parent, 35*time.Second)
	defer cancel()
	rawURL := cleanURL(source)
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" { return legacyFailure(-1, "视频链接不正确"), nil }
	if p == nil || p.executor == nil { return nil, fmt.Errorf("bilibili request executor is not configured") }
	if strings.EqualFold(u.Hostname(), "b23.tv") {
		response, requestErr := p.executor.Do(ctx, shareRequest(rawURL))
		if err := parent.Err(); err != nil { return nil, err }
		if requestErr != nil || response.URL == "" {
			return legacyFailure(502, "B站短链接解析失败，请重试或使用完整视频链接"), nil
		}
		rawURL = redirectTarget(rawURL, response.URL)
		u, err = url.Parse(rawURL)
	}
	if err != nil || u == nil || !allowedVideoURL(rawURL) || !strings.HasPrefix(u.EscapedPath(), "/video/") {
		return legacyFailure(-1, "好像不是视频链接"), nil
	}
	part, present, scalar := queryPart(u.RawQuery)
	if present && (!scalar || !partNumber.MatchString(part)) { return legacyFailure(400, "分P序号无效"), nil }
	requested := int64(1)
	if present { requested, _ = strconv.ParseInt(part, 10, 64) }
	bvid := ""
	if match := videoPath.FindStringSubmatch(u.EscapedPath()); match != nil { bvid = match[1] }
	if bvid == "" {
		timeout := remaining(ctx, 10*time.Second)
		if timeout < time.Second { timeout = time.Second }
		response, requestErr := p.executor.Do(ctx, pageRequest(rawURL, timeout))
		if err := parent.Err(); err != nil { return nil, err }
		if requestErr == nil {
			if match := videoMeta.FindSubmatch(response.Body); match != nil { bvid = string(match[1]) }
		}
	}
	if bvid == "" { return legacyFailure(404, "获取视频ID失败"), nil }
	view := p.apiRequest(ctx, viewRequest(bvid, remaining(ctx, 20*time.Second)))
	if err := parent.Err(); err != nil { return nil, err }
	if !strictInteger(view["code"], 0) || !phpArray(view["data"]) {
		return failure(view, "视频信息"), nil
	}
	detail := unified.Map(view["data"])
	var selected object
	for i, value := range unified.Slice(detail["pages"]) {
		page := unified.Map(value)
		if page != nil && intValue(pick(page["page"], i+1)) == requested { selected = page; break }
	}
	if len(selected) == 0 || !truthy(selected["cid"]) { return legacyFailure(404, "请求的分P不存在"), nil }
	play := p.apiRequest(ctx, playRequest(bvid, selected["cid"], remaining(ctx, 20*time.Second)))
	if err := parent.Err(); err != nil { return nil, err }
	return buildResponse(detail, bvid, requested, selected, play), nil
}

func buildResponse(detail object, bvid string, requested int64, selected, play object) object {
	pages := unified.Slice(detail["pages"])
	if pages == nil { pages = array{} }
	playData := unified.Map(play["data"])
	if playData == nil { playData = object{} }
	privatePlay := any(array{})
	if phpArray(play["data"]) { privatePlay = play["data"] }
	stream := unified.Map(first(playData["durl"]))
	mediaURL := unified.MediaURL(stream["url"])
	pageURL := "https://www.bilibili.com/video/" + unified.RFC3986Query(bvid) + "/"
	videos := array{}
	for i, value := range pages {
		page := unified.Map(value)
		if page == nil { continue }
		number := intValue(pick(page["page"], i+1))
		duration := intValue(pick(page["duration"], 0))
		if duration < 0 { duration = 0 }
		currentURL := ""
		if number == requested { currentURL = mediaURL }
		videos = append(videos, object{
			"title": stringValue(pick(page["part"], "P"+strconv.FormatInt(number, 10))), "duration": duration,
			"durationFormat": time.Unix(duration, 0).UTC().Format("15:04:05"), "index": number, "page": number,
			"cid": stringValue(pick(page["cid"], "")), "url": currentURL,
			"pageUrl": pageURL + "?p=" + strconv.FormatInt(number, 10), "lazyResolve": number != requested,
		})
	}
	label := quality(playData)
	backups := array{}
	for _, backup := range unified.Slice(stream["backup_url"]) {
		if normalized := unified.MediaURL(backup); normalized != "" && backup != mediaURL {
			backups = append(backups, object{"url": normalized, "label": "备用线路", "quality": label})
		}
	}
	owner := unified.Map(detail["owner"])
	raw := object{
		"type": "video", "title": pick(detail["title"], ""), "cover": pick(detail["pic"], ""),
		"description": pick(detail["desc"], ""), "url": mediaURL, "pageUrl": pageURL + "?p=" + strconv.FormatInt(requested, 10),
		"user": object{"name": pick(owner["name"], ""), "avatar": pick(owner["face"], "")},
		"video_backup": backups, "duration": selected["duration"], "quality": label,
		"extra": object{"pages": pages, "current_page": requested, "bvid": bvid}, "private_play": privatePlay,
	}
	if !strictInteger(play["code"], 0) || mediaURL == "" && !truthy(child(playData, "dash", "video")) {
		raw["private_error"] = failure(play, "当前分P播放源")
	}
	if phpArray(detail["ugc_season"]) { raw["private_collection"] = detail["ugc_season"] }
	extra := unified.Map(raw["extra"])
	if phpArray(selected["dimension"]) { extra["source_dimension"] = selected["dimension"] }
	if _, ok := unified.Numeric(detail["pubdate"]); ok { raw["create_time"] = intValue(detail["pubdate"]) }
	statistics := object{}
	for _, pair := range [][2]string{{"view", "play_count"}, {"reply", "comment_count"}, {"favorite", "collect_count"}, {"share", "share_count"}, {"like", "digg_count"}} {
		value := child(detail, "stat", pair[0])
		if _, ok := unified.Numeric(value); ok { statistics[pair[1]] = value }
	}
	if len(statistics) > 0 { extra["statistics"] = statistics }
	if len(videos) > 1 { raw["videos"], raw["totalVideos"] = videos, len(videos) }
	return object{"code": 200, "msg": "解析成功", "data": raw}
}

func legacyFailure(code int64, message string) object { return object{"code": code, "msg": message} }

func failure(response object, stage string) object {
	httpStatus := intValue(pick(response["_http_status"], 200))
	upstream := response["code"]
	if httpStatus == 401 || httpStatus == 403 || httpStatus == 412 || httpStatus == 429 || anyStrictInteger(upstream, -352, -412, -401, -403, -101) {
		return legacyFailure(503, "B站暂时限制当前服务器访问（"+stage+"），请稍后重试")
	}
	if anyStrictInteger(upstream, -400, -404, 62002, 62004) || httpStatus == 404 { return legacyFailure(404, "B站作品不存在、已删除或当前不可见") }
	if !strictInteger(pick(response["_transport_errno"], 0), 0) || httpStatus != 200 || upstream == nil {
		return legacyFailure(502, "B站上游连接或响应异常（"+stage+"），请稍后重试")
	}
	return legacyFailure(404, "请求的分P暂时无法获取视频链接")
}

func legacyError(code int64, message string) object {
	contractCode := "UPSTREAM_UNAVAILABLE"
	switch code {
	case -1, 0, 201, 400: contractCode = "INVALID_INPUT"
	case 401, 403, 412: contractCode = "UPSTREAM_BLOCKED"
	case 404: contractCode = "CONTENT_UNAVAILABLE"
	case 429: contractCode = "RATE_LIMITED"
	case 408, 504: contractCode = "UPSTREAM_TIMEOUT"
	case 502: contractCode = "UPSTREAM_INVALID_RESPONSE"
	}
	return unified.ContractError(contractCode, message, contractCode == "RATE_LIMITED" || contractCode == "UPSTREAM_TIMEOUT" || contractCode == "UPSTREAM_UNAVAILABLE")
}

func quality(play object) string {
	labels := map[int64]string{6: "240p", 16: "360p", 32: "480p", 64: "720p", 74: "720p60", 80: "1080p", 112: "1080p+", 116: "1080p60", 120: "4K", 125: "HDR", 126: "Dolby Vision", 127: "8K"}
	if label := labels[intValue(play["quality"])]; label != "" { return label }
	return "unknown"
}
