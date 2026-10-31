// Package probe reads bounded ISO BMFF metadata. It never downloads or caches a
// complete large media file, and it exposes no caller-controlled relay endpoint.
package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const (
	MaxBytes       int64 = 1 << 20
	MaxRequests          = 12
	MaxRedirects         = 3
	Timeout              = 6 * time.Second
	maxFileBytes   int64 = 1 << 32
	prefixBytes    int64 = 64 << 10
	maxHeaderBytes       = 16 << 10
	maxTopAtoms          = 24
	userAgent            = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/123.0.0.0 Safari/537.36"
)

// Probe has no mutable request state. Every call receives a fresh byte, request,
// redirect and time budget, including when one instance is used concurrently.
type Probe struct {
	executor   fetch.Executor
	allowedURL func(string) bool
}

func New(executor fetch.Executor, allowedURL func(string) bool) *Probe {
	return &Probe{executor: executor, allowedURL: allowedURL}
}

// Probe returns a confirmed media URL and measured metadata, or nil when no
// media response was confirmed. Unsupported/truncated metadata and exhausted
// optional probe budgets preserve the last successfully read URL with empty
// metadata. A caller's cancellation propagates; ordinary enhancement failures
// do not turn a successful platform parse into a failure.
func (p *Probe) Probe(parent context.Context, rawURL string) (unified.Object, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if p == nil || p.executor == nil || p.allowedURL == nil || !p.allowedURL(rawURL) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(parent, Timeout)
	defer cancel()
	r := reader{probe: p, ctx: ctx}
	current := rawURL
	prefix := r.read(&current, 0, prefixBytes)
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if len(prefix) < 16 || string(prefix[4:8]) != "ftyp" {
		return nil, nil
	}
	result := unified.Object{"url": current, "metadata": unified.Object{}}
	var offset int64
	for i := 0; i < maxTopAtoms && offset < r.size; i++ {
		if ctx.Err() != nil {
			break
		}
		var head []byte
		if offset+16 <= int64(len(prefix)) {
			head = prefix[offset : offset+16]
		} else {
			head = r.read(&current, offset, min(int64(16), r.size-offset))
		}
		if head == nil {
			break
		}
		// A later redirect is committed only after a successful bounded read.
		result["url"] = current
		box, ok := atom(head, 0, r.size-offset, false)
		if !ok {
			break
		}
		if box.kind == "moov" {
			if box.size > MaxBytes-r.bytes {
				break
			}
			var moov []byte
			if offset+box.size <= int64(len(prefix)) {
				moov = prefix[offset : offset+box.size]
			} else {
				moov = r.read(&current, offset, box.size)
			}
			if moov != nil {
				result["url"] = current
				if metadata := Metadata(moov, r.size); metadata != nil {
					result["metadata"] = metadata
				}
			}
			break
		}
		offset += box.size
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

type reader struct {
	probe     *Probe
	ctx       context.Context
	bytes     int64
	requests  int
	redirects int
	size      int64
}

var contentRange = regexp.MustCompile(`^bytes ([0-9]{1,10})-([0-9]{1,10})/([0-9]{1,10})$`)
var decimal = regexp.MustCompile(`^[0-9]+$`)

// Entry authorization belongs to the platform. A validated response may choose
// another CDN, including on a later Range read. Every such request still uses
// HTTPS and fetch.Client's public-DNS, pinned-socket and TLS checks.
func safeTransportURL(raw string) bool {
	if len(raw) > 8192 || !strings.HasPrefix(raw, "https://") || strings.Contains(raw, "#") {
		return false
	}
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f || r == '\\' {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(strings.TrimRight(u.Hostname(), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return fetch.PublicIP(ip)
	}
	// A colon that did not parse as an IP is not a DNS hostname (for example,
	// scoped IPv6). DNS targets are resolved and checked by the actual executor.
	return !strings.HasPrefix(u.Host, "[") && !strings.ContainsAny(host, ":%")
}

func (r *reader) read(current *string, offset, length int64) []byte {
	if offset < 0 || offset > maxFileBytes || length < 1 || length > MaxBytes-r.bytes {
		return nil
	}
	for r.requests < MaxRequests && r.ctx.Err() == nil {
		if !safeTransportURL(*current) {
			return nil
		}
		deadline, ok := r.ctx.Deadline()
		if !ok {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		r.requests++
		response, err := r.probe.executor.Do(r.ctx, fetch.Request{
			URL: *current, Method: http.MethodGet,
			Header: http.Header{
				"Range":           {fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)},
				"User-Agent":      {userAgent},
				"Accept-Encoding": {"identity"},
				"Accept":          {"video/mp4, application/octet-stream"},
			},
			Policy: fetch.Policy{
				Purpose: "media-probe", AllowedURL: safeTransportURL,
				Timeout: remaining, MaxBytes: length, MaxHeaderBytes: maxHeaderBytes,
				StrictMediaHeaders: true,
				FollowRedirects:    false, MaxRedirects: 0,
			},
		})
		if err != nil || r.ctx.Err() != nil || int64(len(response.Body)) > length {
			return nil
		}
		// No-follow is part of Executor's contract. Do not accept an injected
		// transport silently following uncounted or unapproved redirect hops.
		if len(response.History) != 0 || (response.URL != "" && response.URL != *current) {
			return nil
		}
		r.bytes += int64(len(response.Body))
		headers, ok := criticalHeaders(response.Header)
		if !ok {
			return nil
		}
		status := response.StatusCode
		if status == 301 || status == 302 || status == 303 || status == 307 || status == 308 {
			r.redirects++
			next, present := headers["location"]
			if r.redirects > MaxRedirects || !present {
				return nil
			}
			switch {
			case strings.HasPrefix(next, "//"):
				next = "https:" + next
			case strings.HasPrefix(next, "/"):
				u, err := url.Parse(*current)
				if err != nil || u.Hostname() == "" {
					return nil
				}
				host := u.Hostname()
				if strings.Contains(host, ":") {
					host = "[" + host + "]"
				}
				next = "https://" + host + next
			case strings.HasPrefix(next, "http://"):
				next = "https://" + strings.TrimPrefix(next, "http://")
			}
			if !safeTransportURL(next) {
				return nil
			}
			*current = next
			continue
		}
		if status != http.StatusOK && status != http.StatusPartialContent {
			return nil
		}
		if encoding, present := headers["content-encoding"]; present && encoding != "identity" {
			return nil
		}
		mediaType := strings.ToLower(trimHeader(strings.SplitN(headers["content-type"], ";", 2)[0]))
		if mediaType != "video/mp4" && mediaType != "video/quicktime" && mediaType != "application/octet-stream" && mediaType != "video/x-m4v" {
			return nil
		}
		actual := int64(len(response.Body))
		var size int64
		if status == http.StatusPartialContent {
			match := contentRange.FindStringSubmatch(headers["content-range"])
			if match == nil {
				return nil
			}
			start, _ := strconv.ParseInt(match[1], 10, 64)
			end, _ := strconv.ParseInt(match[2], 10, 64)
			size, _ = strconv.ParseInt(match[3], 10, 64)
			if start != offset || end != offset+actual-1 || end >= size || actual != min(length, size-offset) {
				return nil
			}
		} else {
			// Ignored Range is safe only for a complete small file at offset zero.
			value := headers["content-length"]
			declared, err := strconv.ParseInt(value, 10, 64)
			if offset != 0 || !decimal.MatchString(value) || err != nil || declared != actual {
				return nil
			}
			size = actual
		}
		if size < 8 || size > maxFileBytes || (r.size != 0 && r.size != size) {
			return nil
		}
		r.size = size
		return response.Body
	}
	return nil
}

// Header maps can contain differently cased keys in test/custom transports.
// Inspect all entries, not Header.Get, before accepting security-relevant fields.
func criticalHeaders(header http.Header) (map[string]string, bool) {
	out := make(map[string]string, 5)
	bytes := 2 // Final CRLF; transport also counts the actual status/header bytes.
	for name, values := range header {
		for _, value := range values {
			bytes += len(name) + len(value) + 4
			if bytes > maxHeaderBytes {
				return nil, false
			}
		}
		key := strings.ToLower(name)
		if key != "location" && key != "content-range" && key != "content-length" && key != "content-type" && key != "content-encoding" {
			continue
		}
		if _, exists := out[key]; exists || len(values) != 1 {
			return nil, false
		}
		out[key] = trimHeader(values[0])
	}
	return out, true
}

func trimHeader(value string) string { return strings.Trim(value, " \t\r\n\v\x00") }
