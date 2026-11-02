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

// For this signed rendition, repeated responses have delivered identical file
// bytes across changes to lp, uid and ab. Preserve the route shape, path, and
// every other raw query field. This key is never used as a delivery URL.
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
	for _, name := range []string{"uid", "ab"} {
		if vs, ok := values[name]; ok && (len(vs) != 1 || vs[0] == "") {
			return "" // Don't interpret ambiguous or empty client fields.
		}
	}
	fields := strings.Split(query, "&")
	hasLP := false
	for i, field := range fields {
		name, _, _ := strings.Cut(field, "=")
		switch name {
		case "lp":
			hasLP = true
			fields[i] = name
		case "uid", "ab":
			fields[i] = name
		}
	}
	if !hasLP {
		return ""
	}
	return "weibo-file:" + base + "?" + strings.Join(fields, "&")
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
