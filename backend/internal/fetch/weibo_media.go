package fetch

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The only streaming export is purpose-specific and never accepts an
// arbitrary host, method, proxy, redirects, cookies, or caller headers.
// Normal metadata Executor.Do remains bounded to its existing 8 MiB limit.
var weiboRangeHeader = regexp.MustCompile(`^bytes=(?:[0-9]+-[0-9]*|-[0-9]+)$`)

func validWeiboTarget(raw string) bool {
	if len(raw) > 4096 || strings.Contains(raw, "#") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Scheme != "https" || u.Hostname() != "f.video.weibocdn.com" || !strings.HasPrefix(u.EscapedPath(), "/") {
		return false
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number != 443 {
			return false
		}
	}
	return true
}

// OpenWeiboMedia makes exactly one request to a pre-validated, pinned public
// address. DNS must have been checked by weibo.Prepare; it is not repeated
// here. The URL hostname is preserved for TLS SNI and CA verification. The
// returned body is streamed and MUST be closed by the caller.
func OpenWeiboMedia(ctx context.Context, raw string, ip netip.Addr, rangeHeader string) (*http.Response, error) {
	if !validWeiboTarget(raw) || !PublicIP(ip) || len(rangeHeader) > 80 || rangeHeader != "" && !weiboRangeHeader.MatchString(rangeHeader) {
		return nil, ErrPolicy
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Close = true
	req.Header.Set("Accept", "video/mp4,application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Referer", "https://weibo.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	// HTTP/1.1 strict raw-header inspection retains duplicate-field evidence
	// that the standard HTTP stack could otherwise normalize away.
	policy := Policy{MaxHeaderBytes: 64 << 10, StrictMediaHeaders: true}
	response, err := roundTripWithDial(req, []netip.Addr{ip}, policy, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
	if err != nil {
		return nil, err
	}
	// ResponsePlan requires explicit bounded Content-Length. Chunked
	// streaming or unknown framing is not a safe replacement.
	if len(response.TransferEncoding) != 0 {
		_ = response.Body.Close()
		return nil, ErrMediaHeaders
	}
	return response, nil
}
