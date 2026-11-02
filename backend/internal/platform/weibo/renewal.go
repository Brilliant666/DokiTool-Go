package weibo

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	media "github.com/Brilliant666/DokiTool-Go/backend/internal/media/weibo"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var renditionPath = regexp.MustCompile(`^/[A-Za-z0-9]{2}/[A-Za-z0-9_-]+\.mp4$`)
var numericMediaID = regexp.MustCompile(`^[0-9]+$`)

// For this signed rendition URL shape, a refresh can change lp while serving
// identical file bytes. Keep the lp-bearing route separate from the route
// without it. Preserve the exact path and other content-query bytes.
func renewableFileKey(raw string) string {
	if !media.AllowedURL(raw) {
		return ""
	}
	base, query, ok := strings.Cut(unified.SourceIdentity(raw), "?")
	if !ok {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || !renditionPath.MatchString(u.EscapedPath()) {
		return ""
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ""
	}
	for _, name := range []string{"lp", "media_id", "template", "Expires", "ssig", "KID"} {
		if len(values[name]) != 1 || values[name][0] == "" {
			return ""
		}
	}
	if !numericMediaID.MatchString(values.Get("media_id")) {
		return ""
	}
	fields := strings.Split(query, "&")
	for i, field := range fields {
		if strings.HasPrefix(field, "lp=") {
			fields[i] = "lp"
			return "weibo-file:" + base + "?" + strings.Join(fields, "&")
		}
	}
	return ""
}

// Never merge distinct routes present together just because removing their
// changing lp values would give them the same key.
func markRenewableFiles(files unified.Array) {
	groups := make(map[string][]unified.Object)
	for _, entry := range files {
		file := unified.Map(entry)
		if key := renewableFileKey(unified.Str(file["url"])); key != "" {
			groups[key] = append(groups[key], file)
		}
	}
	for key, group := range groups {
		first := unified.SourceIdentity(unified.Str(group[0]["url"]))
		if slices.ContainsFunc(group[1:], func(file unified.Object) bool {
			return unified.SourceIdentity(unified.Str(file["url"])) != first
		}) {
			continue
		}
		for _, file := range group {
			file["private_source_identity"] = key
		}
	}
}
