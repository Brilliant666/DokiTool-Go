// Package weibo retains the PHP URL identity grammar before issuing a fixed-host request.
// This is an input-only stage; no arbitrary user-supplied URL is fetched.
package weibo

import (
	"net/url"
	"regexp"
	"strings"
)

// Target is a verified Weibo status identity or TV-component fid.
type Target struct {
	Kind string
	ID   string
}

var (
	tvPath      = regexp.MustCompile("^/tv/(?:show|v)/([^/]+)/?$")
	tvQueryPath = regexp.MustCompile("^/tv/(?:show|v)/?$")
	statusPath  = regexp.MustCompile("^/[0-9]+/([A-Za-z0-9]{1,32})/?$")
	mobilePath  = regexp.MustCompile("^/(?:status|detail)/([A-Za-z0-9]{1,32})/?$")
	fidSyntax   = regexp.MustCompile("^[0-9]{1,8}:(?:[0-9]{16,24}|[a-fA-F0-9]{32})$")
)

// PHP's parse_str takes the last scalar assignment but an array assignment
// replaces that scalar with an array. Unclosed '[' is normalized to '_'.
func queryFID(raw string) (string, bool) {
	value, scalar := "", false
	for _, pair := range strings.Split(raw, "&") {
		key, encoded, _ := strings.Cut(pair, "=")
		key, _ = url.QueryUnescape(strings.ReplaceAll(key, "+", " "))
		key, _, _ = strings.Cut(key, "\x00")
		key = strings.TrimLeft(key, " ")
		key = strings.NewReplacer(" ", "_", ".", "_").Replace(key)
		switch {
		case key == "fid":
			if decoded, err := url.QueryUnescape(encoded); err == nil {
				value, scalar = decoded, true
			} else {
				// PHP urldecode tolerates malformed '%' in values. An invalid
				// fid cannot pass the strict grammar regardless of that suffix.
				value, scalar = encoded, true
			}
		case strings.HasPrefix(key, "fid[") && strings.Contains(key, "]"):
			value, scalar = "", false
		}
	}
	return value, scalar
}

// ParseTarget reproduces the unmodified PHP weiboTarget product function.
// A nil target must terminate before any network request is attempted.
func ParseTarget(input any) *Target {
	raw, ok := input.(string)
	if !ok || len(raw) > 8192 {
		return nil
	}
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f || r == '\\' {
			return nil
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" {
		return nil
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	path := u.Path // PHP rawurldecode(parse_url(..., PHP_URL_PATH))
	fid := ""
	switch host {
	case "video.weibo.com":
		if strings.TrimRight(path, "/") == "/show" {
			if value, scalar := queryFID(u.RawQuery); scalar {
				fid = value
			}
		}
	case "weibo.com", "www.weibo.com":
		if match := tvPath.FindStringSubmatch(path); match != nil {
			fid = match[1]
		} else if tvQueryPath.MatchString(path) {
			if value, scalar := queryFID(u.RawQuery); scalar {
				fid = value
			}
		} else if match := statusPath.FindStringSubmatch(path); match != nil {
			return &Target{Kind: "status", ID: match[1]}
		}
	case "m.weibo.cn":
		if match := mobilePath.FindStringSubmatch(path); match != nil {
			return &Target{Kind: "status", ID: match[1]}
		}
	}
	if fidSyntax.MatchString(fid) {
		return &Target{Kind: "video", ID: fid}
	}
	return nil
}
