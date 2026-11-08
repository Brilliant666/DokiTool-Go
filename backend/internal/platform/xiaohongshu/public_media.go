package xiaohongshu

import (
	"context"
	"net/url"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// PreparePublic prepares adapter-selected Live Photo motion URLs for HTTPS pages.
// Upstream CDN membership is data, not a hard-coded node/domain list. This pure
// delivery transform issues no requests and does not authorize server-side fetches.
// TLS/CORS/readability must still be established by the actual browser transfer.
func PreparePublic(ctx context.Context, platform string, payload unified.Object, now int64) (unified.Object, error) {
	if platform != "xiaohongshu" || payload["code"] != "OK" {
		return payload, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := unified.Clone(payload)
	for _, rawItem := range unified.Slice(unified.Map(result["data"])["items"]) {
		item := unified.Map(rawItem)
		if item["type"] != "live_photo" {
			continue
		}
		for _, v := range unified.Slice(item["variants"]) {
			upgradePair(unified.Map(unified.Map(v)["delivery"]))
		}
	}
	return result, nil
}

func upgradePair(pair unified.Object) {
	if pair["kind"] != "pair" {
		return
	}
	for _, r := range unified.Slice(pair["resources"]) {
		resource := unified.Map(r)
		if resource["role"] != "motion" {
			continue
		}
		file := unified.Map(resource["delivery"])
		if file["kind"] != "file" {
			continue
		}
		for _, rawSource := range unified.Slice(file["sources"]) {
			source := unified.Map(rawSource)
			if source["access"] != nil && source["access"] != "direct" {
				continue
			}
			if raw, ok := source["url"].(string); ok {
				source["url"] = httpsMotionURL(raw)
			}
			fallbacks := unified.Slice(source["fallback_urls"])
			for i, value := range fallbacks {
				if raw, ok := value.(string); ok {
					fallbacks[i] = httpsMotionURL(raw)
				}
			}
		}
	}
}

func httpsMotionURL(raw string) string {
	if len(raw) < 7 || !strings.EqualFold(raw[:7], "http://") || strings.IndexFunc(raw, func(r rune) bool {
		return r <= 0x20 || r == 0x7f || r == '\\'
	}) >= 0 {
		return raw
	}
	u, err := url.Parse(raw)
	// An explicit port cannot be silently changed while preserving signed URL
	// bytes. Leave unusual/invalid authorities as declared instead of guessing.
	if err != nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" || u.Port() != "" || strings.HasSuffix(u.Host, ":") {
		return raw
	}
	for _, b := range []byte(u.Host) {
		if b > 127 {
			return raw
		}
	}
	// Do not round-trip through URL.String: only the scheme changes; path,
	// escaped bytes, query order and signature text stay byte-for-byte intact.
	return "https" + raw[4:]
}
