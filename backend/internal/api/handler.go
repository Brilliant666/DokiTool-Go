// Package api preserves the public unified-v1 HTTP boundary.
// Adapters execute in process; no PHP loopback or compatibility response is used.
package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type Adapter interface {
	Parse(context.Context, string, int64) (unified.Object, error)
}
type AdapterFunc func(context.Context, string, int64) (unified.Object, error)

func (f AdapterFunc) Parse(ctx context.Context, source string, now int64) (unified.Object, error) {
	return f(ctx, source, now)
}

// PublicMedia applies provider-specific source eligibility/signing before public
// filtering. A future Weibo registration must provide this boundary as well.
type PublicMedia func(context.Context, string, unified.Object, int64) (unified.Object, error)

type Handler struct {
	catalog     *catalog.Catalog
	adapters    map[string]Adapter
	now         func() time.Time
	publicMedia PublicMedia
}

func New(c *catalog.Catalog, adapters map[string]Adapter, now func() time.Time, media PublicMedia) (*Handler, error) {
	if c == nil {
		return nil, fmt.Errorf("missing catalog")
	}
	registered := make(map[string]Adapter, len(adapters))
	for id, adapter := range adapters {
		if !c.Has(id) || adapter == nil {
			return nil, fmt.Errorf("invalid adapter registration %q", id)
		}
		if id == "weibo" && media == nil {
			return nil, fmt.Errorf("weibo requires signed-media preparation")
		}
		registered[id] = adapter
	}
	if now == nil {
		now = time.Now
	}
	return &Handler{catalog: c, adapters: registered, now: now, publicMedia: media}, nil
}

var shareURL = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)

func InputURL(input string) string {
	if len(input) > 8192 {
		return ""
	}
	return strings.TrimRight(shareURL.FindString(strings.TrimSpace(input)), "'\"，。；,.;!?！？）)")
}

// PHP query variables use the last assignment and reject arrays where scalars
// are required. Keep that behavior for url[]=, repeated keys and debug=1.
func queryValue(raw, name string) (string, bool) {
	value, scalar := "", false
	for _, pair := range strings.Split(raw, "&") {
		parts := strings.SplitN(pair, "=", 2)
		key := formDecode(parts[0])
		if key == name {
			value, scalar = "", true
			if len(parts) == 2 {
				value = formDecode(parts[1])
			}
		} else if strings.HasPrefix(key, name+"[") {
			value, scalar = "", false
		}
	}
	return value, scalar
}

func formDecode(value string) string {
	if decoded, err := url.QueryUnescape(value); err == nil {
		return decoded
	}
	// urldecode keeps malformed percent sequences but still decodes valid ones.
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '+' {
			out.WriteByte(' ')
			continue
		}
		if value[i] == '%' && i+2 < len(value) {
			if decoded, err := url.QueryUnescape(value[i : i+3]); err == nil {
				out.WriteString(decoded)
				i += 2
				continue
			}
		}
		out.WriteByte(value[i])
	}
	return out.String()
}

func Status(payload unified.Object) int {
	switch unified.Str(payload["code"]) {
	case "OK":
		return 200
	case "INVALID_INPUT", "UNSUPPORTED_PLATFORM":
		return 400
	case "NO_MEDIA", "CONTENT_UNAVAILABLE":
		return 404
	case "UPSTREAM_BLOCKED":
		return 403
	case "RATE_LIMITED":
		return 429
	case "UPSTREAM_TIMEOUT":
		return 504
	case "SERVICE_NOT_CONFIGURED":
		return 503
	case "INTERNAL_ERROR":
		return 500
	default:
		return 502
	}
}

func send(w http.ResponseWriter, status int, payload unified.Object) {
	body, err := unified.Encode(payload)
	if err != nil {
		status = 500
		body = []byte(`{"schema_version":"1.0","code":"INTERNAL_ERROR","error":{"code":"INTERNAL_ERROR","message":"响应编码失败"}}`)
	}
	w.Header().Del("Access-Control-Allow-Origin")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func sendError(w http.ResponseWriter, status int, message string) {
	code := "UPSTREAM_INVALID_RESPONSE"
	switch status {
	case 400, 404, 405, 409, 410:
		code = "INVALID_INPUT"
	case 403:
		code = "UPSTREAM_BLOCKED"
	case 429:
		code = "RATE_LIMITED"
	case 500:
		code = "INTERNAL_ERROR"
	case 503:
		code = "SERVICE_NOT_CONFIGURED"
	case 504:
		code = "UPSTREAM_TIMEOUT"
	}
	send(w, status, unified.ContractError(code, message, status == 429 || status == 502 || status == 504))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	switch path {
	case "/api/short_videos", "/api/douyin", "/api/ksjx", "/api/bilibili", "/api/xhsjx", "/api/toutiao":
		sendError(w, 410, "接口已更新，请等待当前下载结束后刷新页面")
		return
	case "/api/media/weibo":
		// This phase has no relay implementation. Never fall back to an arbitrary
		// URL proxy or return a successful normalized response for it.
		sendError(w, 503, "微博播放服务尚未配置，请联系站点维护者")
		return
	case "/api/parse", "/api/resolve", "/api/platforms":
	default:
		sendError(w, 404, "接口不存在")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		sendError(w, 405, "仅支持 GET 请求")
		return
	}
	version, scalar := queryValue(r.URL.RawQuery, "schema_version")
	if !scalar || version != unified.SchemaVersion {
		sendError(w, 409, "接口版本已更新，请等待当前下载结束后刷新页面")
		return
	}
	if path == "/api/platforms" {
		send(w, 200, h.catalog.Envelope())
		return
	}
	input, scalar := queryValue(r.URL.RawQuery, "url")
	source := InputURL(input)
	platform := h.catalog.PlatformForURL(source)
	requested, requestedScalar := queryValue(r.URL.RawQuery, "platform")
	// Absence is different from an explicit array assignment.
	if !queryPresent(r.URL.RawQuery, "platform") {
		requested, requestedScalar = "all", true
	}
	if !scalar || source == "" || platform == "" || !requestedScalar || (requested != "all" && requested != platform) {
		sendError(w, 400, "请输入对应平台的有效分享链接")
		return
	}
	itemID, scalarID := queryValue(r.URL.RawQuery, "item_id")
	if path == "/api/resolve" && (!scalarID || itemID == "" || len(itemID) > 256) {
		sendError(w, 400, "缺少有效的媒体条目标识")
		return
	}
	adapter := h.adapters[platform]
	if adapter == nil {
		sendError(w, 503, "解析服务未配置完成")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	now := h.now().Unix()
	payload, err := adapter.Parse(ctx, source, now)
	if err != nil {
		sendError(w, 502, "上游返回不完整或不符合媒体合同的数据")
		return
	}
	if err := unified.ValidateEnvelopeAt(payload, now); err != nil {
		sendError(w, 502, "解析接口合同校验失败")
		return
	}
	status := Status(payload)
	if status != 200 {
		send(w, status, payload)
		return
	}
	data := unified.Map(payload["data"])
	if unified.Str(data["platform"]) != platform {
		sendError(w, 502, "内部解析接口平台不一致")
		return
	}
	if h.publicMedia != nil {
		payload, err = h.publicMedia(ctx, platform, payload, now)
		if err != nil {
			sendError(w, 503, "媒体服务尚未配置，请联系站点维护者")
			return
		}
		if err := unified.ValidateEnvelopeAt(payload, now); err != nil {
			sendError(w, 502, "解析接口合同校验失败")
			return
		}
		if status := Status(payload); status != 200 {
			send(w, status, payload)
			return
		}
		if unified.Str(unified.Map(payload["data"])["platform"]) != platform {
			sendError(w, 502, "内部解析接口平台不一致")
			return
		}
	}
	debug, _ := queryValue(r.URL.RawQuery, "debug")
	payload = unified.PublicView(payload, debug == "1", now)
	if path == "/api/resolve" {
		found := false
		data = unified.Map(payload["data"])
		for _, value := range unified.Slice(data["items"]) {
			item := unified.Map(value)
			if unified.Str(item["id"]) == itemID && (item["state"] == nil || item["state"] == "ready") {
				found = true
				break
			}
		}
		if !found {
			send(w, 404, unified.ContractError("CONTENT_UNAVAILABLE", "请求的媒体条目暂时不可用", false))
			return
		}
		data["default_item_id"] = itemID
	}
	if err := unified.ValidateEnvelopeAt(payload, now); err != nil {
		sendError(w, 502, "解析接口合同校验失败")
		return
	}
	send(w, 200, payload)
}

func queryPresent(raw, name string) bool {
	for _, pair := range strings.Split(raw, "&") {
		key := formDecode(strings.SplitN(pair, "=", 2)[0])
		if key == name || strings.HasPrefix(key, name+"[") {
			return true
		}
	}
	return false
}
