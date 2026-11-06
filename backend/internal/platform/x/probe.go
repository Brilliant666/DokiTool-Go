package x

import (
	"context"
	"sync"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
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

func allowedXMediaURL(raw string) bool {
	return sanitizeXMediaURL(raw) != ""
}

// NewSizeProber creates a bounded, no-redirect HTTP Range size prober backed by
// fetch.Executor (enforcing public-IP DNS verification, pinned IP dialing, and
// no environment proxy inheritance) restricted to AllowedXMediaURL hosts.
func NewSizeProber(executor fetch.Executor) SizeProber {
	prober := ytengine.NewSizeProber(executor, allowedXMediaURL)
	return func(ctx context.Context, rawURL string) (int64, bool) {
		return prober(ctx, rawURL)
	}
}

// NewHTTPSizeProber creates a bounded, no-redirect HTTP size prober restricted
// to AllowedXMediaURL hosts (video.twimg.com / pbs.twimg.com).
func NewHTTPSizeProber(proxyURL string) SizeProber {
	prober := ytengine.NewHTTPSizeProber(proxyURL, allowedXMediaURL)
	return func(ctx context.Context, rawURL string) (int64, bool) {
		return prober(ctx, rawURL)
	}
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
