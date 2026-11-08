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

	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/douyin"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const Path = "/v1/douyin/original"

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

type originalProbe interface {
	Originals(context.Context, string) ([]unified.Object, error)
}

type server struct {
	token string
	probe originalProbe
}

// NewHandler authorizes the single metadata-only endpoint; the command binds
// to loopback behind a certificate-validating, source-restricted HTTPS ingress.
func NewHandler(token string, p originalProbe) (http.Handler, error) {
	if !ValidToken(token) || p == nil {
		return nil, errors.New("missing or invalid Shanghai resolver configuration")
	}
	return &server{token: token, probe: p}, nil
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
		VideoID string `json:"video_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil || decoder.Decode(&struct{}{}) != io.EOF || !ValidVideoID(req.VideoID) {
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
