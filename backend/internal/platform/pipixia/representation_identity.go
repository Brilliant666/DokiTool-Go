package pipixia

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var ppxObjectHost = regexp.MustCompile(`^v[0-9]+-cdn-tos\.ppxvod\.com$`)
var ppxObjectPath = regexp.MustCompile(`^/[a-f0-9]{32}/[a-f0-9]{8}(/video/tos/cn/tos-cn-ve-[a-z0-9-]+/[A-Za-z0-9_-]+/?)$`)

// representationIdentity recognizes the object shared by a rendition's CDN
// mirrors. Every URL must agree: neither the first URL, the video-level URI nor
// the resolution identifies a file. A changed/unknown raw query stays distinct.
// This does NOT change source IDs, request URLs or explicit-source matching.
func representationIdentity(urls []string) string {
	identity := ""
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" ||
			(u.Port() != "" && u.Port() != "443") || !ppxObjectHost.MatchString(strings.ToLower(u.Hostname())) {
			return ""
		}
		path := ppxObjectPath.FindStringSubmatch(u.EscapedPath())
		if path == nil {
			return ""
		}
		// Keep signed/unknown query bytes, duplicate parameters and encoding as
		// observed. Only the known CDN host and signing-prefix path vary here.
		key := "pipixia:tos:" + unified.PHPIdentityJSON(unified.Array{path[1], u.RawQuery, u.ForceQuery}, false)
		if identity != "" && identity != key {
			return ""
		}
		identity = key
	}
	return identity
}
