package bilibili

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const desktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

var videoHosts = fetch.AllowHosts("bilibili.com", "www.bilibili.com", "m.bilibili.com")
var shareHosts = fetch.AllowHosts("b23.tv", "bilibili.com", "www.bilibili.com", "m.bilibili.com")

func allowedVideoURL(raw string) bool { return allowedURL(raw) && videoHosts(raw) }
func allowedShareURL(raw string) bool { return allowedURL(raw) && shareHosts(raw) }

func allowedURL(raw string) bool {
	if len(raw) > 8192 || strings.Contains(raw, "\\") || unified.MediaURL(raw) == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return false
	}
	return u.Scheme == "https" && (u.Port() == "" || u.Port() == "443") || u.Scheme == "http" && (u.Port() == "" || u.Port() == "80")
}

func allowedAPIURL(raw string) bool {
	if strings.Contains(raw, "#") || !allowedURL(raw) {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() == "api.bilibili.com" && (u.EscapedPath() == "/x/web-interface/view" || u.EscapedPath() == "/x/player/playurl")
}

func shareRequest(raw string) fetch.Request {
	return fetch.Request{URL: raw, Method: http.MethodHead, Header: http.Header{"User-Agent": {""}},
		Policy: fetch.Policy{Purpose: "bilibili-share", AllowedURL: allowedShareURL, Timeout: 10 * time.Second, MaxBytes: 1, FollowRedirects: true, MaxRedirects: 5}}
}

func pageRequest(raw string, timeout time.Duration) fetch.Request {
	return fetch.Request{URL: raw, Method: http.MethodGet,
		Header: http.Header{"User-Agent": {desktopUA}, "Accept-Encoding": {"gzip,deflate"}},
		Policy: fetch.Policy{Purpose: "bilibili-page", AllowedURL: allowedShareURL, Timeout: timeout, MaxBytes: 8 << 20, FollowRedirects: true, MaxRedirects: 5}}
}

func viewRequest(bvid string, timeout time.Duration) fetch.Request {
	return apiRequest("bilibili-view", "https://api.bilibili.com/x/web-interface/view?bvid="+bvid, timeout)
}

func playRequest(bvid string, cid any, timeout time.Duration) fetch.Request {
	return apiRequest("bilibili-play", "https://api.bilibili.com/x/player/playurl?otype=json&fnver=0&fnval=3&player=3&qn=112&bvid="+bvid+"&cid="+url.QueryEscape(stringValue(cid))+"&platform=html5&high_quality=1", timeout)
}

func apiRequest(purpose, raw string, timeout time.Duration) fetch.Request {
	return fetch.Request{URL: raw, Method: http.MethodGet, Header: http.Header{"User-Agent": {""}, "Accept-Encoding": {"gzip,deflate"}},
		Policy: fetch.Policy{Purpose: purpose, AllowedURL: allowedAPIURL, Timeout: timeout, MaxBytes: 8 << 20}}
}

func remaining(ctx context.Context, maximum time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		// PHP floors its remaining budget to whole seconds for API requests.
		left := time.Until(deadline) / time.Second * time.Second
		if left < maximum {
			return left
		}
	}
	return maximum
}

func (p *Parser) apiRequest(ctx context.Context, request fetch.Request) object {
	if request.Policy.Timeout < time.Second {
		return decodeResponse(nil, 0, 28)
	}
	response, err := p.executor.Do(ctx, request)
	errno := int64(0)
	if err != nil {
		errno = 1
	}
	return decodeResponse(response.Body, response.StatusCode, errno)
}

func decodeResponse(body []byte, status int, errno int64) object {
	var result object
	if utf8.Valid(body) {
		result, _ = unified.Decode(body)
	}
	if result == nil {
		result = object{}
	}
	result["_http_status"], result["_transport_errno"] = status, errno
	if (errno != 0 || status != 200) && strictInteger(pick(result["code"], 0), 0) {
		result["code"] = -1
	}
	return result
}

func cleanURL(raw string) string {
	raw = strings.Trim(raw, " \t\n\r\x00\x0b")
	raw, _, _ = strings.Cut(raw, "#")
	return raw
}

// The parser consumes p with PHP's last-assignment semantics. Array-shaped p
// remains invalid even when it contains just one number; it cannot select P1.
func queryPart(query string) (value string, present, scalar bool) {
	query, _, _ = strings.Cut(query, "\x00")
	for _, pair := range strings.Split(query, "&") {
		key, raw, _ := strings.Cut(pair, "=")
		name, isArray := queryName(key)
		if name == "p" {
			value, present, scalar = formDecode(raw), true, !isArray
		}
	}
	return
}

// PHP parameter names are C strings after URL decoding. An unterminated '['
// becomes '_' instead of creating an array; neither spelling may erase a
// previously selected p. Values remain binary-safe and still undergo p's
// strict decimal validation.
func queryName(encoded string) (string, bool) {
	key := formDecode(encoded)
	key, _, _ = strings.Cut(key, "\x00")
	key = strings.TrimLeft(key, " ")
	if bracket := strings.IndexByte(key, '['); bracket >= 0 {
		if strings.Contains(key[bracket+1:], "]") {
			return strings.NewReplacer(" ", "_", ".", "_").Replace(key[:bracket]), true
		}
		key = strings.ReplaceAll(key, "[", "_")
	}
	return strings.NewReplacer(" ", "_", ".", "_").Replace(key), false
}

func redirectTarget(source, target string) string {
	target = cleanURL(target)
	sourceURL, sourceErr := url.Parse(source)
	targetURL, targetErr := url.Parse(target)
	if sourceErr != nil || targetErr != nil {
		return target
	}
	part, exists, scalar := queryPart(sourceURL.RawQuery)
	if !exists {
		return target
	}
	// Only p is consumed downstream. Keep the first position of scalar keys
	// when replacing their value, and retain the ordered values of tracking
	// arrays instead of collapsing repeated foo[] assignments in a map.
	type entry struct{ name, value string }
	entries := []entry{}
	positions := map[string]int{}
	nextIndexes := map[string]int64{}
	set := func(name, value string) {
		if position, exists := positions[name]; exists {
			entries[position].value = value
			return
		}
		positions[name] = len(entries)
		entries = append(entries, entry{name, value})
	}
	for _, pair := range strings.Split(targetURL.RawQuery, "&") {
		key, value, _ := strings.Cut(pair, "=")
		name, isArray := queryName(key)
		if name == "" {
			continue
		}
		if isArray && name != "p" {
			decoded := strings.TrimLeft(formDecode(key), " ")
			decoded, _, _ = strings.Cut(decoded, "\x00")
			open := strings.IndexByte(decoded, '[')
			close := open + 1 + strings.IndexByte(decoded[open+1:], ']')
			index := decoded[open+1 : close]
			if close+1 < len(decoded) && decoded[close+1] == '[' {
				// Preserve unfamiliar nested tracking parameters losslessly and
				// in order; only the selected p controls media identity here.
				entries = append(entries, entry{name + decoded[open:], formDecode(value)})
				continue
			}
			if index == "" || len(index) == 1 && strings.ContainsRune(" \t\r\n\v\f", rune(index[0])) {
				index = strconv.FormatInt(nextIndexes[name], 10)
				nextIndexes[name]++
			} else if number, err := strconv.ParseInt(index, 10, 64); err == nil && strconv.FormatInt(number, 10) == index {
				if next, exists := nextIndexes[name]; !exists || number >= next {
					nextIndexes[name] = number + 1
				}
			}
			set(name+"["+index+"]", formDecode(value))
			continue
		}
		set(name, formDecode(value))
	}
	set("p", part)
	pairs := make([]string, 0, len(entries))
	for _, entry := range entries {
		encodedKey := entry.name
		if entry.name == "p" && !scalar {
			encodedKey = "p[0]"
		}
		pairs = append(pairs, formEncode(encodedKey)+"="+formEncode(entry.value))
	}
	base, _, _ := strings.Cut(target, "?")
	return base + "?" + strings.Join(pairs, "&")
}

func formEncode(value string) string { return strings.ReplaceAll(url.QueryEscape(value), "~", "%7E") }

func formDecode(raw string) string {
	if value, err := url.QueryUnescape(raw); err == nil {
		return value
	}
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '+' {
			out.WriteByte(' ')
			continue
		}
		if raw[i] == '%' && i+2 < len(raw) {
			if value, err := url.QueryUnescape(raw[i : i+3]); err == nil {
				out.WriteString(value)
				i += 2
				continue
			}
		}
		out.WriteByte(raw[i])
	}
	return out.String()
}
