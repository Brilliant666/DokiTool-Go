package xiaohongshu

import (
	"context"
	"net/url"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// PreparePublic upgrades only known Live Photo motion CDN sources on a copy.
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
		}
	}
}

func httpsMotionURL(raw string) string {
	if !strings.HasPrefix(raw, "http://") || strings.ContainsAny(raw, "\\\r\n\t ") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return raw
	}
	for _, b := range []byte(u.Host) {
		if b > 127 {
			return raw
		}
	}
	if !strings.EqualFold(u.Host, "sns-v11.rednotecdn.com") && !strings.EqualFold(u.Host, "sns-v27.rednotecdn.com") {
		return raw
	}
	return "https" + raw[4:]
}
