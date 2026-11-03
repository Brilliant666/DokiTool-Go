package shresolver

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/probe"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const (
	clientTimeout    = probe.Timeout + 2*time.Second
	maxResponseBytes = 32 << 10
)

var errUnavailable = errors.New("Shanghai original address unavailable")

type Client struct {
	endpoint string
	token    string
	executor fetch.Executor
	local    *http.Client
	lookup   func(context.Context, string, string) ([]netip.Addr, error)
	now      func() time.Time
}

// NewClient pins the service URL from operator configuration. Only a literal
// loopback HTTPS endpoint may use a nonstandard port (isolated SSH forwarding).
// Its certificate is verified with the same system trust as a public endpoint.
func NewClient(endpoint, token string, executor fetch.Executor) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.EscapedPath() != Path ||
		u.User != nil || u.ForceQuery || u.RawQuery != "" || strings.Contains(endpoint, "#") ||
		!ValidToken(token) || executor == nil {
		return nil, errUnavailable
	}
	c := &Client{endpoint: endpoint, token: token, executor: executor, lookup: net.DefaultResolver.LookupNetIP, now: time.Now}
	ip, err := netip.ParseAddr(u.Hostname())
	if err == nil && ip.IsLoopback() && ip.Zone() == "" {
		c.local = &http.Client{
			Transport: &http.Transport{DisableKeepAlives: true, DisableCompression: true,
				MaxResponseHeaderBytes: 8192, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	} else if !fetch.AllowHosts(u.Hostname())(endpoint) || err == nil && !fetch.PublicIP(ip) {
		return nil, errUnavailable
	}
	return c, nil
}

// The resolve request and DNS verification of both answers share one deadline.
// No credentials, full media bytes or arbitrary fetch target cross this boundary.
func (c *Client) ResolveOriginal(parent context.Context, videoID string) ([]unified.Object, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if !ValidVideoID(videoID) {
		return nil, errUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, clientTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"video_id": videoID})
	headers := http.Header{"Authorization": {"Bearer " + c.token}, "Content-Type": {"application/json"}, "Accept": {"application/json"}}
	payload, err := c.request(ctx, body, headers)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(payload))
	d.UseNumber()
	d.DisallowUnknownFields()
	var result Response
	if d.Decode(&result) != nil || d.Decode(&struct{}{}) != io.EOF || result.VideoID != videoID || len(result.Candidates) < 1 || len(result.Candidates) > 2 {
		return nil, errUnavailable
	}
	now := c.now().Unix()
	var rows []unified.Object
	seen := map[string]bool{}
	for _, v := range result.Candidates {
		u, err := url.Parse(v.URL)
		object := unified.DouyinStoragePath(v.URL)
		if err != nil || object == "" || !validProbeMetadata(v.Metadata) ||
			v.ObtainedAt < now-60 || v.ObtainedAt > now+30 ||
			v.ExpiresAt != explicitExpiry(u) || v.ExpiresAt != 0 && v.ExpiresAt <= now {
			return nil, errUnavailable
		}
		if prefix, err := hex.DecodeString(v.PrefixSHA256); err != nil || len(prefix) != 32 {
			return nil, errUnavailable
		}
		route := strings.ToLower(u.Hostname())
		prefix := strings.ToLower(v.PrefixSHA256)
		if len(rows) > 0 && (rows[0]["object_key"] != object || rows[0]["prefix_sha256"] != prefix || !unified.Equal(rows[0]["metadata"], v.Metadata)) {
			return nil, errUnavailable
		}
		if seen[route] {
			continue
		}
		ips, err := c.lookup(ctx, "ip", route)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err != nil || len(ips) == 0 {
			return nil, errUnavailable
		}
		for _, ip := range ips {
			if !fetch.PublicIP(ip) {
				return nil, errUnavailable
			}
		}
		row := unified.Object{"url": v.URL, "metadata": v.Metadata, "source_key": route, "object_key": object, "prefix_sha256": prefix, "obtained_at": v.ObtainedAt}
		if v.ExpiresAt > 0 {
			row["expires_at"] = time.Unix(v.ExpiresAt, 0).UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
		seen[route] = true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func (c *Client) request(ctx context.Context, body []byte, headers http.Header) ([]byte, error) {
	if c.local == nil {
		r, err := c.executor.Do(ctx, fetch.Request{
			URL: c.endpoint, Method: http.MethodPost, Header: headers, Body: body,
			Policy: fetch.Policy{Purpose: "douyin-shanghai-original",
				AllowedURL: func(raw string) bool { return raw == c.endpoint },
				Timeout:    clientTimeout, MaxBytes: maxResponseBytes, MaxHeaderBytes: 8192},
		})
		if err != nil || r.StatusCode != http.StatusOK || len(r.Body) > maxResponseBytes {
			return nil, errUnavailable
		}
		return r.Body, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errUnavailable
	}
	req.Header = headers
	resp, err := c.local.Do(req)
	if err != nil {
		return nil, errUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errUnavailable
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(out) > maxResponseBytes {
		return nil, errUnavailable
	}
	return out, nil
}

func validProbeMetadata(m unified.Object) bool {
	// Accept exactly the eight measured facts. Prevent response-supplied
	// private identity or default/availability fields reaching the formatter.
	if len(m) != 8 || m["metadata_source"] != "mp4" || m["has_audio"] != true {
		return false
	}
	switch m["codec"] {
	case "h264", "hevc", "av1":
	default:
		return false
	}
	number := func(key string) float64 {
		n, ok := m[key].(json.Number)
		if !ok {
			return 0
		}
		v, err := n.Float64()
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		return v
	}
	w, h, size, rate, duration := number("width"), number("height"), number("size"), number("bit_rate"), number("duration")
	return min(w, h) >= 2160 && max(w, h) >= 3840 && max(w, h) <= 16384 && w == math.Trunc(w) && h == math.Trunc(h) &&
		size > 0 && size <= 1<<32 && size == math.Trunc(size) && rate > 0 && rate == math.Trunc(rate) && duration >= 0.01 && duration <= 86400 &&
		rate*duration <= 8*size+rate*0.0005+duration // MP4 duration and bitrate are rounded.
}
