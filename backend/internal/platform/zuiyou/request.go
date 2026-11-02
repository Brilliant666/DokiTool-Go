package zuiyou

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const desktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"
const detailURL = "https://share.xiaochuankeji.cn/planck/share/post/detail_h5"

var shareHosts = fetch.AllowHosts(".izuiyou.com")

func allowedShare(raw string) bool { return unified.MediaURL(raw) != "" && shareHosts(raw) }

func allowedDetail(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.User == nil && u.Scheme == "https" && u.Hostname() == "share.xiaochuankeji.cn" &&
		(u.Port() == "" || u.Port() == "443") && u.EscapedPath() == "/planck/share/post/detail_h5" && u.Fragment == "" &&
		fetch.AllowHosts("share.xiaochuankeji.cn")(raw)
}

func shareRequest(raw string) fetch.Request {
	return fetch.Request{URL: raw, Method: http.MethodHead, Header: http.Header{"User-Agent": {desktopUA}, "Accept": {"*/*"}},
		Policy: fetch.Policy{Purpose: "zuiyou-share", AllowedURL: allowedShare, Timeout: 12 * time.Second, MaxBytes: 8 << 20}}
}

func detailRequest(body string) fetch.Request {
	return fetch.Request{URL: detailURL, Method: http.MethodPost, Body: []byte(body),
		Header: http.Header{"User-Agent": {desktopUA}, "Accept": {"application/json"}, "Content-Type": {"application/json"}},
		Policy: fetch.Policy{Purpose: "zuiyou-detail", AllowedURL: allowedDetail, Timeout: 20 * time.Second, MaxBytes: 8 << 20, FollowRedirects: true, MaxRedirects: 5}}
}

// parse_str keeps the final assignment and rejects arrays for a scalar pid.
// Spaces/dots in variable names and valid percent escapes follow the PHP entry.
func queryPID(query string) string {
	query, _, _ = strings.Cut(query, "\x00")
	value := ""
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
		if key == "pid" {
			value = ""
			if !array {
				value = percentDecode(strings.ReplaceAll(raw, "+", " "))
			}
		}
	}
	return value
}
