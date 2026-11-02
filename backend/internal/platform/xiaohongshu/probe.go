package xiaohongshu

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/probe"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var mediaPath = regexp.MustCompile(`^/[A-Za-z0-9_/-]+$`)

func AllowedMediaURL(raw string) bool {
	if len(raw) > 8192 || strings.Contains(raw, "#") || unified.MediaURL(raw) == "" {
		return false
	}
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f || r == '\\' {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") || !mediaPath.MatchString(u.EscapedPath()) || strings.Contains(u.EscapedPath(), "..") {
		return false
	}
	switch u.Hostname() {
	case "sns-video-qn.xhscdn.com", "sns-video-al.xhscdn.com", "sns-video-qc.xhscdn.com", "sns-video-bd.xhscdn.com":
		return true
	}
	return false
}

type observingProbe struct {
	executor  fetch.Executor
	status    any
	bytes     int64
	container string
}

func (p *observingProbe) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	r, err := p.executor.Do(ctx, request)
	if err == nil {
		p.status = r.StatusCode
		p.bytes += int64(len(r.Body))
		if strings.HasPrefix(request.Header.Get("Range"), "bytes=0-") && len(r.Body) >= 12 && string(r.Body[4:8]) == "ftyp" {
			p.container = "mp4"
			if string(r.Body[8:12]) == "qt  " {
				p.container = "mov"
			}
		}
	}
	return r, err
}

// Every inspection has its own observed status/bytes/container. The shared
// bounded probe retains entry authorization and validates every redirected CDN.
func (p *Parser) inspect(ctx context.Context, address string) (unified.Object, error) {
	observer := &observingProbe{executor: p.executor}
	result, err := probe.New(observer, AllowedMediaURL).Probe(ctx, address)
	if err != nil {
		return nil, err
	}
	if metadata := unified.Map(result["metadata"]); len(metadata) != 0 && observer.container != "" {
		metadata["container"] = observer.container
		metadata["mime_type"] = "video/mp4"
		if observer.container == "mov" {
			metadata["mime_type"] = "video/quicktime"
		}
	}
	return unified.Object{"result": result, "http_status": observer.status, "read_bytes": observer.bytes}, nil
}
