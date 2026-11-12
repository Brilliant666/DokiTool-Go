package douyin

import (
	"context"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// AllowedBrowserFetchURL reports whether raw is a direct HTTPS Douyin CDN media
// file URL (v*.douyinvod.com or v*.zjcdn.com) that supports browser CORS Range
// reads without credentials. Redirect dispatchers such as aweme.snssdk.com or
// www.douyin.com /aweme/v1/play/ are excluded so the browser never mistakes an
// un-CORS-readable redirect hop for a measurable file route.
func AllowedBrowserFetchURL(raw string) bool {
	return AllowedMediaURL(raw) && !isOriginalDispatcher(raw)
}

// PreparePublic annotates direct Douyin CDN file sources with
// browser_fetch="allowed" at the public API boundary while preserving every
// domestic and overseas candidate URL and frozen PHP baseline behavior.
func PreparePublic(ctx context.Context, platform string, payload unified.Object, now int64) (unified.Object, error) {
	if platform != "douyin" || payload["code"] != "OK" {
		return payload, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := unified.Clone(payload)
	data := unified.Map(result["data"])
	for _, rawItem := range unified.Slice(data["items"]) {
		item := unified.Map(rawItem)
		for _, rawVariant := range unified.Slice(item["variants"]) {
			markDeliveryBrowserFetch(unified.Map(unified.Map(rawVariant)["delivery"]))
		}
	}
	for _, rawAttachment := range unified.Slice(data["attachments"]) {
		resource := unified.Map(unified.Map(rawAttachment)["resource"])
		markDeliveryBrowserFetch(unified.Map(resource["delivery"]))
	}
	return result, nil
}

func markDeliveryBrowserFetch(delivery unified.Object) {
	if delivery == nil {
		return
	}
	switch unified.Str(delivery["kind"]) {
	case "file":
		for _, rawSource := range unified.Slice(delivery["sources"]) {
			source := unified.Map(rawSource)
			if source == nil {
				continue
			}
			if source["access"] != nil && source["access"] != "direct" {
				continue
			}
			if source["browser_fetch"] != nil && source["browser_fetch"] != "unknown" {
				continue
			}
			if AllowedBrowserFetchURL(unified.Str(source["url"])) {
				source["browser_fetch"] = "allowed"
			}
		}
	case "pair", "mux":
		for _, rawResource := range unified.Slice(delivery["resources"]) {
			markDeliveryBrowserFetch(unified.Map(unified.Map(rawResource)["delivery"]))
		}
	}
}
