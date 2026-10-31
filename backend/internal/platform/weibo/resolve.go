package weibo

import (
	"context"
	"fmt"
	"regexp"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var weiboMid = regexp.MustCompile("^[0-9]{1,24}$")

// resolveLegacy performs the actual fixed-host status/TV request choreography.
// The exchange is injected; the network implementation owns the per-parse
// Cookie jar, 35-second deadline and bounded, public-IP/TLS-bound requests.
// The returned document is the private frozen PHP response, not unified-v1.
func (s *guestSession) resolveLegacy(ctx context.Context, target *Target) (unified.Object, error) {
	if target == nil || (target.Kind != "status" && target.Kind != "video") {
		return nil, fmt.Errorf("invalid weibo target")
	}
	if s == nil || s.exchange == nil {
		return nil, fmt.Errorf("weibo HTTP exchange is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if target.Kind == "status" {
		status := s.jsonRequest(ctx, "https://weibo.com/ajax/statuses/show?id="+unified.RFC3986Query(target.ID), nil, "https://weibo.com/")
		if failure := unified.Map(status["failure"]); failure != nil {
			return failure, nil
		}
		return legacyWeiboFormatStatus(unified.Map(status["json"])), nil
	}
	componentURL := "https://weibo.com/tv/api/component?page=" + unified.RFC3986Query("/tv/show/"+target.ID)
	data := "{\"Component_Play_Playinfo\":{\"oid\":\"" + target.ID + "\"}}"
	post := "data=" + unified.RFC3986Query(data)
	referrer := "https://weibo.com/tv/show/" + target.ID
	component := s.jsonRequest(ctx, componentURL, &post, referrer)
	if failure := unified.Map(component["failure"]); failure != nil {
		return failure, nil
	}
	rawComponent := s.lastRawJSON
	keys, err := weiboURLKeysFromRaw([]byte(rawComponent), "data", "Component_Play_Playinfo", "urls")
	if err != nil {
		return nil, fmt.Errorf("invalid ordered Weibo TV source: %w", err)
	}
	body := unified.Map(component["json"])
	result := legacyWeiboFormatComponent(body, nil, keys)
	if !unified.Equal(result["code"], 200) {
		return result, nil
	}
	mid := unified.Map(unified.Map(body["data"])["Component_Play_Playinfo"])["mid"]
	id := weiboPHPString(mid)
	if !weiboMid.MatchString(id) {
		return result, nil
	}
	supplemental := s.jsonRequest(ctx, "https://weibo.com/ajax/statuses/show?id="+unified.RFC3986Query(id), nil, "https://weibo.com/")
	status := unified.Map(supplemental["json"])
	page := unified.Map(status["page_info"])
	if media, present := page["media_info"]; present && media != nil {
		return legacyWeiboFormatComponent(body, status, keys), nil
	}
	return result, nil
}
