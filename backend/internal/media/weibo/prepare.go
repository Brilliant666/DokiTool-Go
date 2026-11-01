package weibo

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// DNSLookup makes the single media DNS resolution injectable for deterministic
// PHP/Go comparisons. The production resolver is never allowed to be bypassed
// by a public query parameter.
type DNSLookup func(context.Context, string) ([]netip.Addr, error)

func defaultDNS(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func prepareFailure(status int, message string) unified.Object {
	return unified.Object{"ok": false, "status": status, "message": message}
}

// Prepare verifies the PHP-compatible ticket before parsing Range or using
// DNS. Every DNS result must be a public address. The selected IPv4-preferred
// address must be pinned by the subsequent transport, never resolved again.
func Prepare(ctx context.Context, method string, query unified.Object, rangeHeader, key string, now int64, lookup DNSLookup) unified.Object {
	if method != http.MethodGet && method != http.MethodHead {
		return prepareFailure(405, "仅支持 GET 和 HEAD")
	}
	if !validKey(key) {
		return prepareFailure(503, "微博媒体服务未配置完成")
	}
	ticket, valid := query["ticket"].(string)
	if len(query) != 1 || !valid {
		return prepareFailure(400, "媒体链接参数无效，请重新解析")
	}
	payload, valid := Verify(ticket, key, now)
	if !valid {
		return prepareFailure(403, "媒体链接无效或已过期，请重新解析")
	}
	var parsed any
	if method == http.MethodGet {
		value, err := ParseRange(rangeHeader)
		if err != nil {
			return prepareFailure(416, "视频请求范围无效")
		}
		if value != nil {
			parsed = value
		}
	}
	if lookup == nil {
		lookup = defaultDNS
	}
	addresses, err := lookup(ctx, "f.video.weibocdn.com")
	if err != nil || len(addresses) == 0 {
		return prepareFailure(502, "微博媒体地址暂不可用，请稍后重试")
	}
	chosen := netip.Addr{}
	for _, address := range addresses {
		if !fetch.PublicIP(address) {
			return prepareFailure(502, "微博媒体地址暂不可用，请稍后重试")
		}
		if !chosen.IsValid() || address.Is4() && !chosen.Is4() {
			chosen = address
		}
	}
	return unified.Object{"ok": true, "method": method, "url": payload["u"], "ip": chosen.String(), "range": parsed}
}

// PHPQuery reconstructs the single PHP $_GET ticket scalar from an HTTP query.
// Duplicate assignments use the final value; arrays and unrelated parameters
// are rejected. Malformed percent escapes fail closed rather than creating a
// second, ambiguous interpretation of a signed token.
func PHPQuery(raw string) (unified.Object, bool) {
	query := unified.Object{}
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		decodedName, nameErr := url.QueryUnescape(name)
		decodedValue, valueErr := url.QueryUnescape(value)
		if nameErr != nil || valueErr != nil || decodedName != "ticket" {
			return nil, false
		}
		query["ticket"] = decodedValue
	}
	return query, true
}
