// Package tbvideo describes browser-only downloads for the two Xiaochuan parsers.
package tbvideo

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var decimalID = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

// Candidate builds an unsigned download URL from videos[vid].url. Only the
// escaped path belongs to the storage key: never copy credentials or a fragment.
func Candidate(platform, raw string) string {
	u, err := url.Parse(raw)
	if err != nil || unified.MediaURL(raw) != raw || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	host := strings.ToLower(u.Host)
	switch platform {
	case "pipigx":
		if host != "video01.szsttkj.com" && host != "video03.szsttkj.com" && host != "xlal-ppvideo.szsttkj.com" {
			return ""
		}
	case "zuiyou":
		if host != "web-v01.izuiyou.com" {
			return ""
		}
	default:
		return ""
	}
	object := u.EscapedPath()
	if object == "" || object == "/" || strings.Contains(u.Path, "\\") {
		return ""
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || segment == "zywmvd" {
			return ""
		}
	}
	return "https://tbvideo.ixiaochuan.cn" + object
}

// Post follows the parser's private video order across its legacy wire step.
// The upstream video key never replaces the public item or source identities.
type Post struct {
	Platform string
	Enabled  bool
	PID      string
	vids     []string
	Items    unified.Object
}

func (p *Post) Observe(vid string) {
	if p != nil {
		p.vids = append(p.vids, vid)
	}
}

func (p *Post) Bind(index int, itemID string, variants unified.Array, rawURL any) {
	if p == nil || !decimalID.MatchString(p.PID) || index >= len(p.vids) || !decimalID.MatchString(p.vids[index]) {
		return
	}
	routes := unified.Object{}
	raw, _ := rawURL.(string)
	if candidate := Candidate(p.Platform, raw); p.Enabled && candidate != "" {
		for _, v := range variants {
			delivery := unified.Map(unified.Map(v)["delivery"])
			if delivery["kind"] != "file" {
				continue
			}
			for _, s := range unified.Slice(delivery["sources"]) {
				source := unified.Map(s)
				if source["url"] == raw {
					routes[unified.Str(source["id"])] = candidate
				}
			}
		}
	}
	if p.Items == nil {
		p.Items = unified.Object{}
	}
	p.Items[itemID] = unified.Object{"vid": p.vids[index], "sources": routes}
}
func (p *Post) Publish(data unified.Object) {
	if p == nil || len(p.Items) == 0 {
		return
	}
	extra := unified.Map(data["extra"])
	if extra == nil {
		extra = unified.Object{}
		data["extra"] = extra
	}
	extra["tbvideo"] = unified.Object{"pid": p.PID, "items": p.Items}
}
