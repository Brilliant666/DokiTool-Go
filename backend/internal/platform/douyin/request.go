package douyin

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const (
	detailEndpoint   = "https://api.amemv.com/aweme/v1/aweme/detail/"
	originalEndpoint = "https://aweme.snssdk.com/aweme/v1/play/"
	mobileUA         = "Mozilla/5.0 (Linux; Android 13; 2210132C Build/TKQ1.220829.002) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Mobile Safari/537.36"
	desktopUA        = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36"
	vidResolveMax    = 30
)

var shareHosts = fetch.AllowHosts(".douyin.com", ".iesdouyin.com")
var detailHost = fetch.AllowHosts("api.amemv.com")
var mediaHost = regexp.MustCompile(`^v[0-9][a-z0-9-]*\.(?:douyinvod\.com|zjcdn\.com)$`)

func allowedShareURL(raw string) bool {
	return len(raw) <= 8192 && shareHosts(raw)
}

func allowedDetailURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && detailHost(raw) && u.Scheme == "https" && u.EscapedPath() == "/aweme/v1/aweme/detail/" && u.Fragment == ""
}

// AllowedMediaURL authorizes initial bounded-probe entries and the live-photo
// redirect resolver. The probe separately validates subsequent public CDN hops.
// It does not limit browser URLs or authorize arbitrary metadata API requests.
func AllowedMediaURL(raw string) bool {
	if len(raw) > 8192 || strings.Contains(raw, "#") || unified.MediaURL(raw) == "" {
		return false
	}
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f || r == '\\' {
			return false
		}
	}
	scheme, remainder, ok := strings.Cut(raw, "://")
	if !ok || scheme != "https" {
		return false
	}
	authority := remainder
	if index := strings.IndexAny(authority, "/?"); index >= 0 {
		authority = authority[:index]
	}
	if strings.Contains(authority, "@") {
		return false
	}
	host, port, hasPort := strings.Cut(authority, ":")
	if hasPort && port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value != 443 {
			return false
		}
	}
	path := remainder[len(authority):]
	path, _, _ = strings.Cut(path, "?")
	return host == "aweme.snssdk.com" && path == "/aweme/v1/play/" || mediaHost.MatchString(host)
}

func detailRequest(id string) fetch.Request {
	return fetch.Request{
		URL:    detailEndpoint + "?aweme_id=" + id + "&aid=6383&device_platform=web&version_code=190500&version_name=19.5.0",
		Method: http.MethodGet,
		Header: http.Header{"User-Agent": {mobileUA}, "Accept": {"application/json, text/plain, */*"}, "Accept-Encoding": {"gzip,deflate"}},
		Policy: fetch.Policy{Purpose: "douyin-detail", AllowedURL: allowedDetailURL, Timeout: 20 * time.Second, MaxBytes: 8 << 20, FollowRedirects: true, MaxRedirects: 5},
	}
}

func (p *Parser) resolveRedirect(ctx context.Context, raw string) (string, error) {
	response, err := p.executor.Do(ctx, fetch.Request{
		URL: raw, Method: http.MethodGet, Header: http.Header{"User-Agent": {desktopUA}},
		Policy: fetch.Policy{Purpose: "douyin-share", AllowedURL: allowedShareURL, Timeout: 20 * time.Second, MaxBytes: 2 << 20, FollowRedirects: true, MaxRedirects: 5},
	})
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err == nil && len(response.History) > 0 {
		for _, location := range response.History {
			if extractAwemeID(location) != "" {
				return location, nil
			}
		}
		return response.History[0], nil
	}
	// The PHP entrypoint tries GET headers first, then follows HEAD redirects.
	// Shared fetch bounds both attempts and checks every redirect/DNS result.
	response, err = p.executor.Do(ctx, fetch.Request{
		URL: raw, Method: http.MethodHead, Header: http.Header{"User-Agent": {desktopUA}},
		Policy: fetch.Policy{Purpose: "douyin-share-head", AllowedURL: allowedShareURL, Timeout: 10 * time.Second, MaxBytes: 1, FollowRedirects: true, MaxRedirects: 5},
	})
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err == nil && response.URL != "" {
		return response.URL, nil
	}
	return raw, nil
}

func (p *Parser) followLocation(ctx context.Context, raw string) (string, error) {
	current, redirected := raw, false
	for i := 0; i < 3; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if p.executor == nil || !AllowedMediaURL(current) {
			break
		}
		response, err := p.executor.Do(ctx, fetch.Request{
			URL: current, Method: http.MethodHead, Header: http.Header{"User-Agent": {desktopUA}},
			Policy: fetch.Policy{Purpose: "douyin-live-original", AllowedURL: AllowedMediaURL, Timeout: 10 * time.Second, MaxBytes: 1, FollowRedirects: false, MaxRedirects: 0},
		})
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil || response.StatusCode < 300 || response.StatusCode >= 400 {
			break
		}
		locations := response.Header.Values("Location")
		if len(locations) != 1 {
			break
		}
		parts := strings.Fields(locations[0])
		if len(parts) == 0 {
			break
		}
		location := parts[0]
		if !strings.HasPrefix(location, "http") {
			u, err := url.Parse(current)
			if err != nil || u.Scheme == "" || u.Hostname() == "" {
				break
			}
			separator := "/"
			if strings.HasPrefix(location, "/") {
				separator = ""
			}
			location = u.Scheme + "://" + u.Hostname() + separator + location
		}
		// Invalid destinations cannot become a source or be requested on a later
		// hop. Keep the last approved URL, then the original fallback if none.
		if !AllowedMediaURL(location) {
			break
		}
		current, redirected = location, true
	}
	if redirected {
		return current, nil
	}
	return "", nil
}

func (p *Parser) resolveVidBatch(ctx context.Context, vids []string) (map[string]string, error) {
	cache := map[string]string{}
	for _, vid := range vids {
		if vid == "" || vid == "0" {
			continue
		}
		if _, exists := cache[vid]; exists {
			continue
		}
		if len(cache) >= vidResolveMax {
			break
		}
		fallback := buildOriginalPlayURL(vid)
		resolved, err := p.followLocation(ctx, fallback)
		if err != nil {
			return nil, err
		}
		if resolved == "" {
			resolved = fallback
		}
		cache[vid] = resolved
	}
	return cache, nil
}
