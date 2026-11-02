package weibo

import (
	"context"
	"time"

	media "github.com/Brilliant666/DokiTool-Go/backend/internal/media/weibo"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// PreparePublic signs only fixed-provider sources without altering identity,
// source order or the full candidate inventory. PublicView remains shared.
func PreparePublic(key string) func(context.Context, string, unified.Object, int64) (unified.Object, error) {
	return func(ctx context.Context, platform string, payload unified.Object, now int64) (unified.Object, error) {
		if platform != "weibo" || payload["code"] != "OK" {
			return payload, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(key) < 32 || len(key) > 512 {
			return unified.ContractError("SERVICE_NOT_CONFIGURED", "微博播放服务尚未配置，请联系站点维护者", false), nil
		}
		result := unified.Clone(payload)
		recognized := 0
		items := unified.Slice(unified.Map(result["data"])["items"])
		for index, rawItem := range items {
			item := unified.Map(rawItem)
			for _, rawVariant := range unified.Slice(item["variants"]) {
				variant := unified.Map(rawVariant)
				if variant["state"] != nil && variant["state"] != "ready" {
					continue
				}
				signDeliverySources(unified.Map(variant["delivery"]), key, now, &recognized)
			}
			// The original PHP public boundary reselects defaults after signing.
			// Without this, the intermediate envelope references stale direct URLs
			// and fails the unchanged HTTP gateway validation before PublicView.
			items[index] = unified.SelectDefault(item, now)
		}
		if recognized == 0 {
			return unified.ContractError("UPSTREAM_INVALID_RESPONSE", "微博暂未提供本站支持的视频源，请稍后重试", true), nil
		}
		return result, nil
	}
}

// Apply to file, segments, mux and pair sources. Every invalid source is marked
// unavailable, never redirected to an arbitrary fetch proxy.
func signDeliverySources(delivery unified.Object, key string, now int64, recognized *int) {
	if delivery == nil {
		return
	}
	for _, value := range unified.Slice(delivery["sources"]) {
		signSource(unified.Map(value), key, now, recognized)
	}
	if init := unified.Map(delivery["init"]); init != nil {
		for _, value := range unified.Slice(init["sources"]) {
			signSource(unified.Map(value), key, now, recognized)
		}
	}
	for _, rawPiece := range unified.Slice(delivery["pieces"]) {
		for _, value := range unified.Slice(unified.Map(rawPiece)["sources"]) {
			signSource(unified.Map(value), key, now, recognized)
		}
	}
	for _, rawResource := range unified.Slice(delivery["resources"]) {
		signDeliverySources(unified.Map(unified.Map(rawResource)["delivery"]), key, now, recognized)
	}
}

func signSource(source unified.Object, key string, now int64, recognized *int) {
	if source == nil {
		return
	}
	raw := unified.Str(source["url"])
	if !media.AllowedURL(raw) {
		source["availability"] = "unavailable"
		return
	}
	*recognized = *recognized + 1
	if !unified.SourceUsable(source, now) {
		return
	}
	expiry := now + media.TicketTTL
	if previous, ok := source["expires_at"]; ok && previous != nil {
		t, err := time.Parse(time.RFC3339Nano, unified.Str(previous))
		if err != nil {
			source["availability"] = "unavailable"
			return
		}
		if t.Unix() < expiry {
			expiry = t.Unix()
		}
	}
	ticket, ok := media.Sign(raw, key, now, &expiry)
	if !ok {
		source["availability"] = "unavailable"
		return
	}
	source["url"] = ticket
	source["access"] = "relay"
	source["expires_at"] = time.Unix(expiry, 0).UTC().Format(time.RFC3339)
}
