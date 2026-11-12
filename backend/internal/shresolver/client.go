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
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/douyin"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/probe"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const (
	clientTimeout      = probe.Timeout + 2*time.Second
	maxV1ResponseBytes = 32 << 10
	maxResponseBytes   = 128 << 10
)

var errUnavailable = errors.New("Shanghai original address unavailable")

type Client struct {
	responseValidator
	endpoint string
	token    string
	executor fetch.Executor
	local    *http.Client
}

// Both delivery modes validate untrusted wire observations before they reach the formatter.
type responseValidator struct {
	lookup func(context.Context, string, string) ([]netip.Addr, error)
	now    func() time.Time
}

func newResponseValidator() responseValidator {
	return responseValidator{lookup: net.DefaultResolver.LookupNetIP, now: time.Now}
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
	c := &Client{endpoint: endpoint, token: token, executor: executor, responseValidator: newResponseValidator()}
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
	payload, status, err := c.request(ctx, body, headers, clientTimeout)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil || status != http.StatusOK {
		return nil, errUnavailable
	}
	return c.validateResponse(ctx, videoID, payload)
}

// ResolveWork requests all domestic rendition URLs and 4K original candidates
// for awemeID within WorkTimeout. If the remote endpoint speaks only v1 and
// rejects the v2 request with HTTP 400, ErrProtocolV1 is returned so the
// caller can fall back to ResolveOriginal.
func (c *Client) ResolveWork(parent context.Context, awemeID string) (*douyin.ShanghaiWork, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if !douyin.ValidAwemeID(awemeID) {
		return nil, errUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, WorkTimeout)
	defer cancel()
	body, _ := json.Marshal(struct {
		Protocol int    `json:"protocol"`
		AwemeID  string `json:"aweme_id"`
	}{Protocol: ProtocolV2, AwemeID: awemeID})
	headers := http.Header{"Authorization": {"Bearer " + c.token}, "Content-Type": {"application/json"}, "Accept": {"application/json"}}
	payload, status, err := c.request(ctx, body, headers, WorkTimeout)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if status == http.StatusBadRequest {
		return nil, douyin.ErrProtocolV1
	}
	if err != nil || status != http.StatusOK {
		return nil, errUnavailable
	}
	return c.validateWorkResponse(ctx, awemeID, payload)
}

func (c responseValidator) validateResponse(ctx context.Context, videoID string, payload []byte) ([]unified.Object, error) {
	if len(payload) > maxV1ResponseBytes {
		return nil, errUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(payload))
	d.UseNumber()
	d.DisallowUnknownFields()
	var result Response
	if d.Decode(&result) != nil || d.Decode(&struct{}{}) != io.EOF || result.VideoID != videoID || len(result.Candidates) < 1 || len(result.Candidates) > 2 {
		return nil, errUnavailable
	}
	return c.validateCandidates(ctx, result.Candidates, c.now().Unix(), nil)
}

func (c responseValidator) validateWorkResponse(ctx context.Context, awemeID string, payload []byte) (*douyin.ShanghaiWork, error) {
	if len(payload) == 0 || len(payload) > maxResponseBytes || bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return nil, errUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(payload))
	d.UseNumber()
	d.DisallowUnknownFields()
	var result WorkResponse
	if d.Decode(&result) != nil || d.Decode(&struct{}{}) != io.EOF {
		return nil, errUnavailable
	}
	// Legacy v1 receipt shape (no protocol field): handle with v1 rules.
	if result.Protocol == 0 {
		if result.AwemeID != "" || result.Renditions != nil || !ValidVideoID(result.VideoID) {
			return nil, errUnavailable
		}
		rows, err := c.validateResponse(ctx, result.VideoID, payload)
		if err != nil {
			return nil, err
		}
		return &douyin.ShanghaiWork{VideoID: result.VideoID, Candidates: rows}, nil
	}
	if result.Protocol != ProtocolV2 || result.AwemeID != awemeID ||
		len(result.Candidates) > 2 || len(result.Renditions) > douyin.MaxShanghaiRenditionObjects ||
		(result.VideoID != "" && !ValidVideoID(result.VideoID)) ||
		(len(result.Candidates) > 0 && result.VideoID == "") {
		return nil, errUnavailable
	}
	now := c.now().Unix()
	dnsOK := map[string]bool{}
	var candidates []unified.Object
	if len(result.Candidates) > 0 {
		var err error
		candidates, err = c.validateCandidates(ctx, result.Candidates, now, dnsOK)
		if err != nil {
			return nil, err
		}
	}
	var renditions map[string]unified.Array
	for declaredKey, urls := range result.Renditions {
		if len(urls) > douyin.MaxShanghaiRenditionURLs {
			return nil, errUnavailable
		}
		if declaredKey == "" {
			continue
		}
		var kept unified.Array
		seen := map[string]bool{}
		for _, raw := range urls {
			normalized, ok := douyin.NormalizeShanghaiRenditionURL(raw)
			if !ok || unified.DouyinStoragePath(normalized) != declaredKey || seen[normalized] {
				continue
			}
			u, err := url.Parse(normalized)
			if err != nil {
				continue
			}
			exp := explicitExpiry(u)
			if exp != 0 && exp <= now {
				continue
			}
			host := strings.ToLower(u.Hostname())
			publicHost, checked := dnsOK[host]
			if !checked {
				publicHost, err = c.checkPublicHost(ctx, host)
				if err != nil {
					return nil, err
				}
				dnsOK[host] = publicHost
			}
			if !publicHost {
				continue
			}
			seen[normalized] = true
			entry := unified.Object{"url": normalized}
			if exp > 0 {
				entry["expires_at"] = time.Unix(exp, 0).UTC().Format(time.RFC3339)
			}
			kept = append(kept, entry)
		}
		if len(kept) > 0 {
			if renditions == nil {
				renditions = map[string]unified.Array{}
			}
			renditions[declaredKey] = kept
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &douyin.ShanghaiWork{
		VideoID:    result.VideoID,
		Candidates: candidates,
		Renditions: renditions,
	}, nil
}

func (c responseValidator) checkPublicHost(ctx context.Context, host string) (bool, error) {
	ips, err := c.lookup(ctx, "ip", host)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err != nil || len(ips) == 0 {
		return false, nil
	}
	for _, ip := range ips {
		if !fetch.PublicIP(ip) {
			return false, nil
		}
	}
	return true, nil
}

func (c responseValidator) validateCandidates(ctx context.Context, candidates []Candidate, now int64, dnsOK map[string]bool) ([]unified.Object, error) {
	var rows []unified.Object
	seen := map[string]bool{}
	for _, v := range candidates {
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
		publicHost, checked := dnsOK[route]
		if !checked {
			publicHost, err = c.checkPublicHost(ctx, route)
			if err != nil {
				return nil, err
			}
			if dnsOK != nil {
				dnsOK[route] = publicHost
			}
		}
		if !publicHost {
			return nil, errUnavailable
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

func (c *Client) request(ctx context.Context, body []byte, headers http.Header, timeout time.Duration) ([]byte, int, error) {
	if c.local == nil {
		r, err := c.executor.Do(ctx, fetch.Request{
			URL: c.endpoint, Method: http.MethodPost, Header: headers, Body: body,
			Policy: fetch.Policy{Purpose: "douyin-shanghai-original",
				AllowedURL: func(raw string) bool { return raw == c.endpoint },
				Timeout:    timeout, MaxBytes: maxResponseBytes, MaxHeaderBytes: 8192},
		})
		if err != nil {
			return nil, 0, errUnavailable
		}
		if len(r.Body) > maxResponseBytes {
			return nil, r.StatusCode, errUnavailable
		}
		return r.Body, r.StatusCode, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, errUnavailable
	}
	req.Header = headers
	resp, err := c.local.Do(req)
	if err != nil {
		return nil, 0, errUnavailable
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(out) > maxResponseBytes {
		return nil, resp.StatusCode, errUnavailable
	}
	return out, resp.StatusCode, nil
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
