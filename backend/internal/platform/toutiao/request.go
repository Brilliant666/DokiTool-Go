package toutiao

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const desktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36"
const registrationURL = "https://ttwid.bytedance.com/ttwid/union/register/"
const registrationBody = `{"aid":24,"needFid":false,"region":"cn","service":"www.toutiao.com","union":true}`

var idPath = regexp.MustCompile(`^/(?:video/|group/|i)([0-9]{6,22})/?$`)
var decimalID = regexp.MustCompile(`^[0-9]{6,22}$`)
var ttwidCookie = regexp.MustCompile(`(?i)^ttwid=([^;\r\n]+)`)

// AllowedURL mirrors the platform input boundary. Authorization for each
// metadata request remains a separate fetch policy with DNS and IP checks.
func AllowedURL(raw string) bool {
	if unified.MediaURL(raw) == "" {
		return false
	}
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f || r == '\\' {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "toutiao.com" && !strings.HasSuffix(host, ".toutiao.com") {
		return false
	}
	return u.Scheme == "https" && (u.Port() == "" || u.Port() == "443") || u.Scheme == "http" && (u.Port() == "" || u.Port() == "80")
}

func VideoID(raw string) string {
	if !AllowedURL(raw) {
		return ""
	}
	u, _ := url.Parse(raw)
	if found := idPath.FindStringSubmatch(u.EscapedPath()); found != nil {
		return found[1]
	}
	for _, key := range []string{"group_id", "item_id"} {
		value, present, scalar := queryValue(u.RawQuery, key)
		if present && scalar && decimalID.MatchString(value) {
			return value
		}
	}
	return ""
}

func RedirectURL(current, location string) string {
	if strings.HasPrefix(location, "//") {
		location = "https:" + location
	} else if strings.HasPrefix(location, "/") {
		base, err := url.Parse(current)
		if err != nil {
			return ""
		}
		location = "https://" + base.Hostname() + location
	}
	if !AllowedURL(location) {
		return ""
	}
	return location
}

// parse_str uses the last assignment, turns dotted/spaced names into
// underscores, and rejects array-shaped IDs even when they contain one value.
func queryValue(query, wanted string) (value string, present, scalar bool) {
	query, _, _ = strings.Cut(query, "\x00")
	for _, pair := range strings.Split(query, "&") {
		key, raw, _ := strings.Cut(pair, "=")
		key = percentDecode(strings.ReplaceAll(key, "+", " "))
		key, _, _ = strings.Cut(key, "\x00")
		key = strings.TrimLeft(key, " ")
		array := false
		if bracket := strings.IndexByte(key, '['); bracket >= 0 {
			if strings.Contains(key[bracket+1:], "]") {
				key, array = key[:bracket], true
			} else {
				key = strings.ReplaceAll(key, "[", "_")
			}
		}
		key = strings.NewReplacer(" ", "_", ".", "_").Replace(key)
		if key == wanted {
			value, present, scalar = percentDecode(strings.ReplaceAll(raw, "+", " ")), true, !array
		}
	}
	return
}

type response struct {
	ok              bool
	status          int
	body            string
	location        string
	ttwid           string
	budgetExhausted bool
}

type requestSession struct {
	executor fetch.Executor
	clock    func() time.Time
	deadline time.Time
}

func (s *requestSession) request(ctx context.Context, target, cookie string, registration bool) response {
	remaining := s.deadline.Sub(s.clock()) / time.Second
	if remaining < 1 || ctx.Err() != nil {
		return response{budgetExhausted: true}
	}
	if remaining > 20 {
		remaining = 20
	}
	request := fetch.Request{
		URL: target, Method: http.MethodGet,
		Header: http.Header{"User-Agent": {desktopUA}, "Accept": {"*/*"}},
		Policy: fetch.Policy{Purpose: "toutiao-page", AllowedURL: AllowedURL, Timeout: remaining * time.Second, MaxBytes: 2 << 20},
	}
	if registration {
		request.Method, request.Body = http.MethodPost, []byte(registrationBody)
		request.Header.Set("Content-Type", "application/json")
		request.Policy.Purpose = "toutiao-session"
		request.Policy.MaxBytes = 65536
		request.Policy.AllowedURL = func(raw string) bool { return raw == registrationURL }
	} else if cookie != "" {
		request.Header.Set("Cookie", "ttwid="+cookie)
		request.Policy.AllowedURL = func(raw string) bool { return raw == target && AllowedURL(raw) }
	} else {
		request.Policy.Purpose = "toutiao-share"
	}
	upstream, err := s.executor.Do(ctx, request)
	if err != nil || upstream.URL != "" && upstream.URL != target {
		return response{}
	}
	if int64(len(upstream.Body)) > request.Policy.MaxBytes {
		return response{}
	}
	result := response{ok: true, status: upstream.StatusCode, body: string(upstream.Body)}
	for _, value := range upstream.Header.Values("Location") {
		result.location = strings.Trim(value, " \t\r\n\v\f")
	}
	for _, value := range upstream.Header.Values("Set-Cookie") {
		if match := ttwidCookie.FindStringSubmatch(strings.TrimLeft(value, " \t\r\n\v\f")); match != nil {
			result.ttwid = match[1]
			if len(result.ttwid) > 8192 {
				result.ttwid = result.ttwid[:8192]
			}
		}
	}
	return result
}
