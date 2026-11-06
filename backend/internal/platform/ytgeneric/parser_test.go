package ytgeneric

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

type stubResolver struct {
	ips map[string][]netip.Addr
}

func (s stubResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if addrs, ok := s.ips[host]; ok {
		return addrs, nil
	}
	return nil, errors.New("not found")
}

type stubExtractor struct {
	enabled bool
	extract func(context.Context, string) (*ytengine.Info, error)
}

func (s *stubExtractor) Enabled() bool { return s != nil && s.enabled }

func (s *stubExtractor) Extract(ctx context.Context, rawURL string) (*ytengine.Info, error) {
	if s.extract == nil {
		return nil, &ytengine.Error{Code: ytengine.CodeServiceNotConfigured, Message: "disabled"}
	}
	return s.extract(ctx, rawURL)
}

func TestUniversalAdapterAllowlistAndUnifiedMapping(t *testing.T) {
	const now int64 = 1762300800
	spec := SiteSpec{
		PlatformID:      "vimeo",
		Name:            "Vimeo",
		SiteURL:         "https://vimeo.com",
		InputHosts:      []string{"vimeo.com", ".vimeo.com"},
		MediaHostSuffix: []string{".vimeocdn.com", "vimeo.com"},
		EnableDASHMux:   true,
		EnableHLS:       true,
	}

	w, h := int64(1920), int64(1080)
	sz := int64(15000000)
	parser := New(spec, &stubExtractor{
		enabled: true,
		extract: func(ctx context.Context, rawURL string) (*ytengine.Info, error) {
			return &ytengine.Info{
				ID:        "987654321",
				Title:     "Vimeo Sample",
				Uploader:  "Creator",
				Thumbnail: "https://i.vimeocdn.com/video/thumb.jpg",
				Formats: []ytengine.Format{
					{
						FormatID: "http-1080p",
						URL:      "https://vod-progressive.akamaized.vimeocdn.com/exp=1/sample1080.mp4",
						Ext:      "mp4",
						Protocol: "https",
						Width:    &w,
						Height:   &h,
						VCodec:   "avc1.640028",
						ACodec:   "mp4a.40.2",
						Filesize: &sz,
					},
				},
			}, nil
		},
	}, nil)

	// Reject SSRF / unlisted domains
	for _, bad := range []string{
		"https://127.0.0.1/video/1",
		"https://evil.com/video/1",
		"https://vimeo.com/",
		"file:///etc/passwd",
	} {
		if parser.AllowedInputURL(bad) {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}

	payload, err := parser.Parse(context.Background(), "https://vimeo.com/987654321", now)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if err := unified.ValidateEnvelopeAt(payload, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt failed: %v", err)
	}
	data := unified.Map(payload["data"])
	if unified.Str(data["platform"]) != "vimeo" || unified.Str(data["content_id"]) != "987654321" {
		t.Fatalf("unexpected platform/content_id: %+v", data)
	}
}

func TestUniversalAdapterBlocksPrivateIPResolution(t *testing.T) {
	const now int64 = 1762300800
	spec := SiteSpec{
		PlatformID:      "vimeo",
		Name:            "Vimeo",
		SiteURL:         "https://vimeo.com",
		InputHosts:      []string{"vimeo.com", ".vimeo.com"},
		MediaHostSuffix: []string{".vimeocdn.com"},
	}

	for _, privateIP := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1"} {
		resolver := stubResolver{
			ips: map[string][]netip.Addr{
				"vimeo.com":        {netip.MustParseAddr(privateIP)},
				"cdn.vimeocdn.com": {netip.MustParseAddr(privateIP)},
			},
		}
		parser := NewWithResolver(spec, &stubExtractor{enabled: true}, nil, resolver)
		payload, err := parser.Parse(context.Background(), "https://vimeo.com/123456", now)
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if unified.Str(payload["code"]) != "UPSTREAM_BLOCKED" {
			t.Fatalf("expected UPSTREAM_BLOCKED when input host resolves to %s, got %v", privateIP, payload["code"])
		}
		if parser.AllowedMediaURL("https://cdn.vimeocdn.com/video.mp4") {
			t.Fatalf("expected AllowedMediaURL to reject media host resolving to %s", privateIP)
		}
	}
}
