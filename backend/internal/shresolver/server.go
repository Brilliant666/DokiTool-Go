package shresolver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/douyin"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const (
	Path           = "/v1/douyin/original"
	ProtocolV2     = 2
	ProtocolHeader = "X-DokiTool-Shanghai-Protocol"

	// ProbeTimeout bounds Shanghai's two-dispatch cold-storage MP4 probe.
	// Empirical 376 MiB cold originals take 6.6-7.7s; 10s leaves ~30% margin.
	ProbeTimeout = 10 * time.Second

	// WorkTimeout bounds Shanghai's combined detail lookup (~0.26s typical,
	// 4s cap) + cold-storage original probe (10s cap) + callback delivery,
	// and serves as Japan's hard maximum wait before falling back to local data.
	WorkTimeout = 14 * time.Second
)

var videoIDPattern = regexp.MustCompile(`^v[0-9a-zA-Z]{10,127}$`)

func ValidVideoID(s string) bool { return videoIDPattern.MatchString(s) }

type Candidate struct {
	URL          string         `json:"url"`
	ObtainedAt   int64          `json:"obtained_at"`
	ExpiresAt    int64          `json:"expires_at,omitempty"`
	PrefixSHA256 string         `json:"prefix_sha256"`
	Metadata     unified.Object `json:"metadata"`
}

type Response struct {
	VideoID    string      `json:"video_id"`
	Candidates []Candidate `json:"candidates"`
}

type WorkResponse struct {
	Protocol   int                 `json:"protocol"`
	AwemeID    string              `json:"aweme_id"`
	VideoID    string              `json:"video_id,omitempty"`
	Renditions map[string][]string `json:"renditions,omitempty"`
	Candidates []Candidate         `json:"candidates"`
}

type originalProbe interface {
	Originals(context.Context, string) ([]unified.Object, error)
}

type server struct {
	token  string
	detail fetch.Executor
	probe  originalProbe
}

// NewHandler authorizes the legacy v1 video_id metadata-only endpoint; the
// command binds to loopback behind a certificate-validating HTTPS ingress.
func NewHandler(token string, p originalProbe) (http.Handler, error) {
	if !ValidToken(token) || p == nil {
		return nil, errors.New("missing or invalid Shanghai resolver configuration")
	}
	return &server{token: token, probe: p}, nil
}

// NewHandlerWithDetail authorizes both v1 (video_id) and v2 (aweme_id) requests.
func NewHandlerWithDetail(token string, detail fetch.Executor, p originalProbe) (http.Handler, error) {
	if !ValidToken(token) || detail == nil || p == nil {
		return nil, errors.New("missing or invalid Shanghai resolver configuration")
	}
	return &server{token: token, detail: detail, probe: p}, nil
}

func ValidToken(token string) bool {
	if len(token) < 32 || len(token) > 256 {
		return false
	}
	for _, c := range token {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.EscapedPath() != Path || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte("Bearer "+s.token)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		Protocol int    `json:"protocol"`
		VideoID  string `json:"video_id"`
		AwemeID  string `json:"aweme_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch req.Protocol {
	case 0:
		if req.AwemeID != "" || !ValidVideoID(req.VideoID) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		out, err := probeOriginal(r.Context(), s.probe, req.VideoID)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	case ProtocolV2:
		if s.detail == nil || req.VideoID != "" || !douyin.ValidAwemeID(req.AwemeID) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		out, err := resolveWork(r.Context(), s.detail, s.probe, req.AwemeID)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func resolveWork(ctx context.Context, detailExec fetch.Executor, p originalProbe, awemeID string) (WorkResponse, error) {
	detail, err := douyin.FetchShanghaiDetail(ctx, detailExec, awemeID)
	if err != nil {
		return WorkResponse{}, err
	}
	out := WorkResponse{
		Protocol:   ProtocolV2,
		AwemeID:    awemeID,
		Renditions: detail.Renditions,
		Candidates: []Candidate{},
	}
	if detail.IsAlbum {
		return out, nil
	}
	if ValidVideoID(detail.VideoID) {
		orig, origErr := probeOriginal(ctx, p, detail.VideoID)
		if err := ctx.Err(); err != nil {
			return WorkResponse{}, err
		}
		if origErr == nil {
			out.VideoID = detail.VideoID
			out.Candidates = orig.Candidates
		} else if len(out.Renditions) == 0 {
			return WorkResponse{}, origErr
		}
	}
	return out, nil
}

// Both delivery modes construct the official play URL here. Control-plane
// credentials and requests never enter the media probe.
func probeOriginal(ctx context.Context, p originalProbe, videoID string) (Response, error) {
	observations, err := p.Originals(ctx, douyin.OriginalPlayURL(videoID))
	if err != nil {
		return Response{}, err
	}
	out := Response{VideoID: videoID, Candidates: []Candidate{}}
	seen := map[string]bool{}
	for _, obs := range observations {
		raw := unified.Str(obs["url"])
		meta := unified.Map(obs["metadata"])
		prefix := unified.Str(obs["prefix_sha256"])
		width, widthOK := unified.PHPInt(meta["width"])
		height, heightOK := unified.PHPInt(meta["height"])
		if !widthOK || !heightOK || min(width, height) < 2160 || max(width, height) < 3840 || meta["has_audio"] != true || len(prefix) != 64 || seen[raw] {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || unified.DouyinStoragePath(raw) == "" {
			continue
		}
		at := time.Now().Unix()
		exp := explicitExpiry(u)
		if exp != 0 && exp <= at {
			continue
		}
		out.Candidates = append(out.Candidates, Candidate{URL: raw, ObtainedAt: at, ExpiresAt: exp, PrefixSHA256: prefix, Metadata: meta})
		seen[raw] = true
	}
	return out, nil
}

// Only explicit, unambiguous epoch seconds are authoritative. Signature/path
// segments are opaque and cannot supply an expiration.
func explicitExpiry(u *url.URL) int64 {
	var exp int64
	for key, values := range u.Query() {
		switch strings.ToLower(key) {
		case "expires", "x-expires":
		default:
			continue
		}
		if len(values) != 1 {
			return 0
		}
		n, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || n < 1000000000 || n > 4102444800 || exp != 0 && exp != n {
			return 0
		}
		exp = n
	}
	return exp
}
