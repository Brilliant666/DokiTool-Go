package x

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

const (
	sizeProbeTotalTimeout       = 1500 * time.Millisecond
	sizeProbeConcurrency        = 4
	maxProbedURLs               = 12
	maxSafeInteger        int64 = 9007199254740991
)

// SizeProber resolves the exact byte length of a progressive MP4 URL on X's CDN.
// Returning (0, false) leaves the format's Filesize unset (fail-soft).
type SizeProber func(ctx context.Context, rawURL string) (int64, bool)

// NewHTTPSizeProber creates a bounded, no-redirect HTTP size prober restricted
// to AllowedXMediaURL hosts (video.twimg.com / pbs.twimg.com).
func NewHTTPSizeProber(proxyURL string) SizeProber {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 1200 * time.Millisecond,
	}
	if trimmed := strings.TrimSpace(proxyURL); trimmed != "" {
		if parsed, err := url.Parse(trimmed); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "socks5" || parsed.Scheme == "socks5h") {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   sizeProbeTotalTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return func(ctx context.Context, rawURL string) (int64, bool) {
		cleanURL := sanitizeXMediaURL(rawURL)
		if cleanURL == "" {
			return 0, false
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cleanURL, nil)
		if err != nil {
			return 0, false
		}
		req.Header.Set("Range", "bytes=0-0")
		req.Header.Set("Accept", "*/*")

		resp, err := client.Do(req)
		if err != nil {
			return 0, false
		}
		defer func() {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64))
			_ = resp.Body.Close()
		}()

		switch resp.StatusCode {
		case http.StatusPartialContent:
			if size, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
				return size, true
			}
		case http.StatusOK:
			if size, ok := parsePositiveContentLength(resp.Header.Get("Content-Length")); ok {
				return size, true
			}
		}
		return 0, false
	}
}

func parseContentRangeTotal(raw string) (int64, bool) {
	// Format: "bytes 0-0/1404023"
	slash := strings.LastIndexByte(strings.TrimSpace(raw), '/')
	if slash < 0 {
		return 0, false
	}
	totalStr := strings.TrimSpace(raw[slash+1:])
	if totalStr == "" || totalStr == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(totalStr, 10, 64)
	if err != nil || n <= 0 || n > maxSafeInteger {
		return 0, false
	}
	return n, true
}

func parsePositiveContentLength(raw string) (int64, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || n <= 0 || n > maxSafeInteger {
		return 0, false
	}
	return n, true
}

// populateMissingMP4Sizes concurrently probes exact byte lengths for progressive MP4
// formats that lack an exact Filesize from yt-dlp. Any probe failure or timeout is
// ignored (fail-soft).
func populateMissingMP4Sizes(ctx context.Context, info *ytengine.Info, prober SizeProber) {
	if info == nil || prober == nil {
		return
	}

	var entries []*ytengine.Info
	if len(info.Entries) > 0 {
		limit := len(info.Entries)
		if limit > maxTweetVideos {
			limit = maxTweetVideos
		}
		for i := 0; i < limit; i++ {
			entries = append(entries, &info.Entries[i])
		}
	} else {
		entries = []*ytengine.Info{info}
	}

	var uniqueURLs []string
	seen := make(map[string]bool)
	for _, entry := range entries {
		for _, f := range entry.Formats {
			if f.Filesize != nil && *f.Filesize > 0 {
				continue
			}
			cleanURL := sanitizeXMediaURL(f.URL)
			if cleanURL == "" {
				continue
			}
			fCopy := f
			fCopy.URL = cleanURL
			if !isProgressiveMP4(fCopy) {
				continue
			}
			if !seen[cleanURL] {
				seen[cleanURL] = true
				uniqueURLs = append(uniqueURLs, cleanURL)
				if len(uniqueURLs) >= maxProbedURLs {
					break
				}
			}
		}
		if len(uniqueURLs) >= maxProbedURLs {
			break
		}
	}

	if len(uniqueURLs) == 0 {
		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, sizeProbeTotalTimeout)
	defer cancel()

	var (
		mu      sync.Mutex
		results = make(map[string]int64, len(uniqueURLs))
		sem     = make(chan struct{}, sizeProbeConcurrency)
		wg      sync.WaitGroup
	)

	for _, targetURL := range uniqueURLs {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-probeCtx.Done():
				return
			}
			if size, ok := prober(probeCtx, u); ok && size > 0 && size <= maxSafeInteger {
				mu.Lock()
				results[u] = size
				mu.Unlock()
			}
		}(targetURL)
	}
	wg.Wait()

	if len(results) == 0 {
		return
	}

	for _, entry := range entries {
		for i := range entry.Formats {
			f := &entry.Formats[i]
			if f.Filesize != nil && *f.Filesize > 0 {
				continue
			}
			cleanURL := sanitizeXMediaURL(f.URL)
			if size, ok := results[cleanURL]; ok {
				sz := size
				f.Filesize = &sz
			}
		}
	}
}
