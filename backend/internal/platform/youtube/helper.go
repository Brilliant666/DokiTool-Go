package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

const (
	// TVHTML5ClientName and TVHTML5ClientVersion are the only allowed Innertube
	// client parameters exposed by /api/youtube/context in Phase 1.
	TVHTML5ClientName    = "TVHTML5"
	TVHTML5ClientVersion = "5.20260707"

	// MaxFinalizeBodyBytes caps POST /api/youtube/finalize request bodies at 512 KiB.
	MaxFinalizeBodyBytes = 512 << 10

	// MaxFinalizeFormatEntries caps combined formats[] + adaptiveFormats[] entries at 64.
	MaxFinalizeFormatEntries = 64

	// DefaultPlayerCacheTTL is the default lifetime of a cached player_id entry.
	DefaultPlayerCacheTTL = 6 * time.Hour

	maxCachedPlayers = 8
)

var (
	playerIDPattern           = regexp.MustCompile(`^[0-9a-fA-F]{8}$`)
	iframeAPIPlayerIDPattern  = regexp.MustCompile(`(?:\\/s\\/player\\/|/s/player/|player\\/|player/)([0-9a-fA-F]{8})(?:\\/|/)`)
	signatureTimestampPattern = regexp.MustCompile(`(?:signatureTimestamp|sts)\s*:\s*([0-9]{5})\b`)
	googlevideoHostPattern    = regexp.MustCompile(`^[a-z0-9-]+(?:\.[a-z0-9-]+)*\.googlevideo\.com$`)
	opaquePlaybackIDPattern   = regexp.MustCompile(`^o-[A-Za-z0-9_-]{40,48}$`)
)

// AllowYouTubePlayerFetchURL restricts server-side player JS discovery and
// download requests strictly to https://www.youtube.com.
func AllowYouTubePlayerFetchURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return false
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	if port := u.Port(); port != "" && port != "443" {
		return false
	}
	return fetch.AllowHosts("www.youtube.com")(raw)
}

// AllowGooglevideoPlaybackURL validates that raw is strictly an
// https://*.googlevideo.com/videoplayback URL with no userinfo, fragment, or custom port.
func AllowGooglevideoPlaybackURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 8192 || strings.Contains(trimmed, "#") {
		return false
	}
	for _, r := range trimmed {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return false
		}
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.User != nil || u.Opaque != "" {
		return false
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	if port := u.Port(); port != "" && port != "443" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if !googlevideoHostPattern.MatchString(host) {
		return false
	}
	return u.Path == "/videoplayback" && u.EscapedPath() == "/videoplayback"
}

type playerCacheEntry struct {
	PlayerID             string
	SignatureTimestamp   int
	RawPlayerJS          string
	PreprocessedPlayerJS string
	FetchedAt            time.Time
	ExpiresAt            time.Time
}

// Helper serves /api/youtube/context and /api/youtube/finalize using internal/fetch
// (restricted to www.youtube.com) and the zero-permission Deno + ejs sandbox.
type Helper struct {
	executor fetch.Executor
	sandbox  *EJSSandbox
	now      func() time.Time
	cacheTTL time.Duration

	mu       sync.Mutex
	activeID string
	cache    map[string]*playerCacheEntry
	order    []string
}

// NewHelper creates a YouTube Helper backed by the given fetch.Executor and verified EJSSandbox.
func NewHelper(executor fetch.Executor, sandbox *EJSSandbox, now func() time.Time) (*Helper, error) {
	if executor == nil {
		return nil, errors.New("missing fetch executor")
	}
	if sandbox == nil {
		return nil, errors.New("missing ejs sandbox")
	}
	if now == nil {
		now = time.Now
	}
	return &Helper{
		executor: executor,
		sandbox:  sandbox,
		now:      now,
		cacheTTL: DefaultPlayerCacheTTL,
		cache:    make(map[string]*playerCacheEntry),
	}, nil
}

// NewHelperFromEnv constructs a Helper when DOKITOOL_DENO_BINARY and DOKITOOL_EJS_DIR pass verification.
func NewHelperFromEnv(executor fetch.Executor, getenv func(string) string, now func() time.Time) (*Helper, error) {
	sandbox, err := NewEJSSandboxFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	return NewHelper(executor, sandbox, now)
}

// Sandbox returns the underlying verified EJSSandbox.
func (h *Helper) Sandbox() *EJSSandbox {
	if h == nil {
		return nil
	}
	return h.sandbox
}

// SetCacheTTL overrides the player cache TTL (used in tests).
func (h *Helper) SetCacheTTL(ttl time.Duration) {
	if h == nil || ttl <= 0 {
		return
	}
	h.mu.Lock()
	h.cacheTTL = ttl
	h.mu.Unlock()
}

// Context implements GET /api/youtube/context?schema_version=1.0.
func (h *Helper) Context(ctx context.Context) (unified.Object, int) {
	if h == nil || h.sandbox == nil || h.executor == nil {
		return unified.ContractError("SERVICE_NOT_CONFIGURED", "YouTube 助手服务尚未配置", false), http.StatusServiceUnavailable
	}
	entry, err := h.ensureActivePlayer(ctx)
	if err != nil {
		return unified.ContractError("UPSTREAM_UNAVAILABLE", "无法获取 YouTube 播放器上下文，请稍后重试", true), http.StatusBadGateway
	}
	return unified.Object{
		"schema_version": unified.SchemaVersion,
		"code":           "OK",
		"data": unified.Object{
			"player_id":           entry.PlayerID,
			"signature_timestamp": int64(entry.SignatureTimestamp),
			"client": unified.Object{
				"name":    TVHTML5ClientName,
				"version": TVHTML5ClientVersion,
			},
			"expires_at": entry.ExpiresAt.UTC().Format(time.RFC3339),
		},
	}, http.StatusOK
}

func (h *Helper) ensureActivePlayer(ctx context.Context) (*playerCacheEntry, error) {
	now := h.now()
	h.mu.Lock()
	if h.activeID != "" {
		if existing, ok := h.cache[h.activeID]; ok && now.Before(existing.ExpiresAt) {
			h.mu.Unlock()
			return existing, nil
		}
	}
	h.mu.Unlock()

	playerID, err := h.fetchCurrentPlayerID(ctx)
	if err != nil {
		return nil, err
	}

	now = h.now()
	h.mu.Lock()
	if existing, ok := h.cache[playerID]; ok && now.Before(existing.ExpiresAt) {
		h.activeID = playerID
		h.mu.Unlock()
		return existing, nil
	}
	h.mu.Unlock()

	rawJS, sts, err := h.fetchPlayerJS(ctx, playerID)
	if err != nil {
		return nil, err
	}
	preprocessed, err := h.sandbox.PreprocessPlayer(ctx, rawJS)
	if err != nil {
		return nil, fmt.Errorf("preprocess player %s: %w", playerID, err)
	}

	now = h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := &playerCacheEntry{
		PlayerID:             playerID,
		SignatureTimestamp:   sts,
		RawPlayerJS:          rawJS,
		PreprocessedPlayerJS: preprocessed,
		FetchedAt:            now,
		ExpiresAt:            now.Add(h.cacheTTL),
	}
	h.storeCacheEntryLocked(entry)
	h.activeID = playerID
	return entry, nil
}

func (h *Helper) storeCacheEntryLocked(entry *playerCacheEntry) {
	if _, exists := h.cache[entry.PlayerID]; !exists {
		h.order = append(h.order, entry.PlayerID)
	}
	h.cache[entry.PlayerID] = entry
	for len(h.order) > maxCachedPlayers {
		evict := h.order[0]
		h.order = h.order[1:]
		if evict != entry.PlayerID {
			delete(h.cache, evict)
		}
	}
}

func (h *Helper) lookupCachedPlayer(playerID string, now time.Time) (*playerCacheEntry, bool) {
	id := strings.ToLower(strings.TrimSpace(playerID))
	if !playerIDPattern.MatchString(id) {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.cache[id]
	if !ok || !now.Before(entry.ExpiresAt) {
		return nil, false
	}
	return entry, true
}

func (h *Helper) fetchCurrentPlayerID(ctx context.Context) (string, error) {
	resp, err := h.executor.Do(ctx, fetch.Request{
		Method: http.MethodGet,
		URL:    "https://www.youtube.com/iframe_api",
		Header: http.Header{
			"Accept":          {"*/*"},
			"Accept-Language": {"en-US,en;q=0.9"},
		},
		Policy: fetch.Policy{
			Purpose:         "youtube.context.iframe_api",
			AllowedURL:      AllowYouTubePlayerFetchURL,
			Timeout:         10 * time.Second,
			MaxBytes:        256 << 10,
			FollowRedirects: true,
			MaxRedirects:    2,
		},
	})
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("iframe_api returned status %d", resp.StatusCode)
	}
	m := iframeAPIPlayerIDPattern.FindSubmatch(resp.Body)
	if m == nil {
		return "", errors.New("player_id not found in iframe_api")
	}
	return strings.ToLower(string(m[1])), nil
}

func (h *Helper) fetchPlayerJS(ctx context.Context, playerID string) (string, int, error) {
	targetURL := fmt.Sprintf("https://www.youtube.com/s/player/%s/player_ias.vflset/en_US/base.js", playerID)
	resp, err := h.executor.Do(ctx, fetch.Request{
		Method: http.MethodGet,
		URL:    targetURL,
		Header: http.Header{
			"Accept":          {"*/*"},
			"Accept-Language": {"en-US,en;q=0.9"},
		},
		Policy: fetch.Policy{
			Purpose:         "youtube.context.player_js",
			AllowedURL:      AllowYouTubePlayerFetchURL,
			Timeout:         15 * time.Second,
			MaxBytes:        6 << 20,
			FollowRedirects: true,
			MaxRedirects:    2,
		},
	})
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("player base.js returned status %d", resp.StatusCode)
	}
	m := signatureTimestampPattern.FindSubmatch(resp.Body)
	if m == nil {
		return "", 0, errors.New("signatureTimestamp not found in player base.js")
	}
	sts, err := strconv.Atoi(string(m[1]))
	if err != nil || sts <= 0 {
		return "", 0, errors.New("invalid signatureTimestamp in player base.js")
	}
	return string(resp.Body), sts, nil
}

type finalizeRequest struct {
	VideoID         string                `json:"video_id"`
	PlayerID        string                `json:"player_id"`
	Formats         []finalizeFormatInput `json:"formats"`
	AdaptiveFormats []finalizeFormatInput `json:"adaptiveFormats"`
}

// finalizeFormatInput retains only the 12 allowed format fields and discards all others.
type finalizeFormatInput struct {
	Itag            int
	URL             string
	SignatureCipher string
	MimeType        string
	Width           *int64
	Height          *int64
	FPS             *float64
	Bitrate         *int64
	ContentLength   string
	AudioQuality    string
	AudioSampleRate string
	AudioChannels   *int64
}

type rawFinalizeFormatFields struct {
	Itag            int             `json:"itag"`
	URL             string          `json:"url"`
	SignatureCipher string          `json:"signatureCipher"`
	MimeType        string          `json:"mimeType"`
	Width           *int64          `json:"width"`
	Height          *int64          `json:"height"`
	FPS             *float64        `json:"fps"`
	Bitrate         *int64          `json:"bitrate"`
	ContentLength   json.RawMessage `json:"contentLength"`
	AudioQuality    string          `json:"audioQuality"`
	AudioSampleRate json.RawMessage `json:"audioSampleRate"`
	AudioChannels   *int64          `json:"audioChannels"`
}

func (f *finalizeFormatInput) UnmarshalJSON(data []byte) error {
	var raw rawFinalizeFormatFields
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	cl, err := parseOptionalDigitString(raw.ContentLength)
	if err != nil {
		return fmt.Errorf("invalid contentLength: %w", err)
	}
	asr, err := parseOptionalDigitString(raw.AudioSampleRate)
	if err != nil {
		return fmt.Errorf("invalid audioSampleRate: %w", err)
	}
	*f = finalizeFormatInput{
		Itag:            raw.Itag,
		URL:             strings.TrimSpace(raw.URL),
		SignatureCipher: strings.TrimSpace(raw.SignatureCipher),
		MimeType:        strings.TrimSpace(raw.MimeType),
		Width:           raw.Width,
		Height:          raw.Height,
		FPS:             raw.FPS,
		Bitrate:         raw.Bitrate,
		ContentLength:   cl,
		AudioQuality:    strings.TrimSpace(raw.AudioQuality),
		AudioSampleRate: asr,
		AudioChannels:   raw.AudioChannels,
	}
	return nil
}

func parseOptionalDigitString(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return "", err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return "", nil
		}
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return "", err
		}
		return s, nil
	}
	var n int64
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return "", err
	}
	if n < 0 {
		return "", errors.New("negative integer")
	}
	return strconv.FormatInt(n, 10), nil
}

type parsedStreamEntry struct {
	input      finalizeFormatInput
	baseURL    string
	nChallenge string
	sChallenge string
	sigParam   string
}

// Finalize implements POST /api/youtube/finalize?schema_version=1.0.
// It never makes any network request to googlevideo.com.
func (h *Helper) Finalize(ctx context.Context, body []byte, debug bool) (unified.Object, int) {
	if h == nil || h.sandbox == nil {
		return unified.ContractError("SERVICE_NOT_CONFIGURED", "YouTube 助手服务尚未配置", false), http.StatusServiceUnavailable
	}
	if len(body) == 0 || len(body) > MaxFinalizeBodyBytes {
		return unified.ContractError("INVALID_INPUT", "请求体大小超出限制或为空", false), http.StatusBadRequest
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req finalizeRequest
	if err := dec.Decode(&req); err != nil {
		return unified.ContractError("INVALID_INPUT", "请求体格式无效或包含未允许的字段", false), http.StatusBadRequest
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return unified.ContractError("INVALID_INPUT", "请求体包含多余的 JSON 数据", false), http.StatusBadRequest
	}

	videoID := strings.TrimSpace(req.VideoID)
	if !youtubeIDPattern.MatchString(videoID) {
		return unified.ContractError("INVALID_INPUT", "无效的 YouTube video_id", false), http.StatusBadRequest
	}

	nowTime := h.now()
	cachedPlayer, ok := h.lookupCachedPlayer(req.PlayerID, nowTime)
	if !ok {
		return unified.ContractError("INVALID_INPUT", "player_id 不在缓存中或已过期，请重新获取上下文", false), http.StatusBadRequest
	}

	totalEntries := len(req.Formats) + len(req.AdaptiveFormats)
	if totalEntries == 0 || totalEntries > MaxFinalizeFormatEntries {
		return unified.ContractError("INVALID_INPUT", "媒体流条目数量超出限制", false), http.StatusBadRequest
	}

	allInputs := make([]finalizeFormatInput, 0, totalEntries)
	allInputs = append(allInputs, req.Formats...)
	allInputs = append(allInputs, req.AdaptiveFormats...)

	entries := make([]parsedStreamEntry, 0, totalEntries)
	var nChallenges []string
	nSeen := make(map[string]bool)
	var sigChallenges []string
	sigSeen := make(map[string]bool)
	expectedStreamID := ""

	for _, item := range allInputs {
		if item.Itag <= 0 || item.MimeType == "" {
			return unified.ContractError("INVALID_INPUT", "媒体流条目缺少有效的 itag 或 mimeType", false), http.StatusBadRequest
		}
		hasURL := item.URL != ""
		hasCipher := item.SignatureCipher != ""
		if hasURL == hasCipher {
			return unified.ContractError("INVALID_INPUT", "媒体流条目必须且只能提供 url 或 signatureCipher 之一", false), http.StatusBadRequest
		}

		streamURL := item.URL
		sigChallenge := ""
		sigParam := "sig"
		if hasCipher {
			cipherVals, err := url.ParseQuery(item.SignatureCipher)
			if err != nil {
				return unified.ContractError("INVALID_INPUT", "signatureCipher 格式无效", false), http.StatusBadRequest
			}
			urls := cipherVals["url"]
			sigs := cipherVals["s"]
			if len(urls) != 1 || strings.TrimSpace(urls[0]) == "" || len(sigs) != 1 || strings.TrimSpace(sigs[0]) == "" {
				return unified.ContractError("INVALID_INPUT", "signatureCipher 缺少有效的 url 或 s 参数", false), http.StatusBadRequest
			}
			streamURL = strings.TrimSpace(urls[0])
			sigChallenge = strings.TrimSpace(sigs[0])
			if spVals := cipherVals["sp"]; len(spVals) > 0 {
				if len(spVals) != 1 || (spVals[0] != "sig" && spVals[0] != "signature") {
					return unified.ContractError("INVALID_INPUT", "signatureCipher 包含非法的 sp 参数", false), http.StatusBadRequest
				}
				sigParam = spVals[0]
			}
		}

		if !AllowGooglevideoPlaybackURL(streamURL) {
			return unified.ContractError("INVALID_INPUT", "媒体链接必须为 https://*.googlevideo.com/videoplayback", false), http.StatusBadRequest
		}

		parsedU, err := url.Parse(streamURL)
		if err != nil {
			return unified.ContractError("INVALID_INPUT", "媒体链接解析失败", false), http.StatusBadRequest
		}
		q := parsedU.Query()

		// Validate the stream's id parameter:
		// Real YouTube TVHTML5 videoplayback URLs carry a signed per-playback session ID
		// (id=o-<44 base64url chars>, signed under sparams=...,id,...) shared across all
		// streams in the response, while synthetic/test URLs may carry id=<video_id>.
		// Reject if id is missing, does not match either video_id or a valid o- playback ID,
		// or differs across streams in the same finalize request.
		idVals := q["id"]
		if len(idVals) != 1 || strings.TrimSpace(idVals[0]) == "" {
			return unified.ContractError("INVALID_INPUT", "媒体链接缺少有效的 id 参数", false), http.StatusBadRequest
		}
		streamID := strings.TrimSpace(idVals[0])
		if streamID != videoID && !opaquePlaybackIDPattern.MatchString(streamID) {
			return unified.ContractError("INVALID_INPUT", "媒体链接参数 id 与 video_id 不一致", false), http.StatusBadRequest
		}
		if expectedStreamID == "" {
			expectedStreamID = streamID
		} else if streamID != expectedStreamID {
			return unified.ContractError("INVALID_INPUT", "媒体链接参数 id 与 video_id 不一致", false), http.StatusBadRequest
		}

		if itagVals := q["itag"]; len(itagVals) != 1 || itagVals[0] != strconv.Itoa(item.Itag) {
			return unified.ContractError("INVALID_INPUT", "媒体链接参数 itag 与条目不一致", false), http.StatusBadRequest
		}

		nVal := ""
		if nVals := q["n"]; len(nVals) > 0 {
			if len(nVals) != 1 || strings.TrimSpace(nVals[0]) == "" {
				return unified.ContractError("INVALID_INPUT", "媒体链接参数 n 无效", false), http.StatusBadRequest
			}
			nVal = strings.TrimSpace(nVals[0])
			if !nSeen[nVal] {
				nSeen[nVal] = true
				nChallenges = append(nChallenges, nVal)
			}
		}
		if sigChallenge != "" && !sigSeen[sigChallenge] {
			sigSeen[sigChallenge] = true
			sigChallenges = append(sigChallenges, sigChallenge)
		}

		entries = append(entries, parsedStreamEntry{
			input:      item,
			baseURL:    streamURL,
			nChallenge: nVal,
			sChallenge: sigChallenge,
			sigParam:   sigParam,
		})
	}

	nSolved, sigSolved, err := h.sandbox.Solve(ctx, cachedPlayer.PreprocessedPlayerJS, nChallenges, sigChallenges)
	if err != nil {
		return unified.ContractError("UPSTREAM_INVALID_RESPONSE", "YouTube 媒体签名挑战解密失败", false), http.StatusBadGateway
	}

	ytFormats := make([]ytengine.Format, 0, len(entries))
	for _, entry := range entries {
		finalURL := entry.baseURL
		if entry.nChallenge != "" {
			solvedN := nSolved[entry.nChallenge]
			if solvedN == "" {
				return unified.ContractError("UPSTREAM_INVALID_RESPONSE", "YouTube n 参数解密结果缺失", false), http.StatusBadGateway
			}
			updated, err := setURLQueryParamPreserveOrder(finalURL, "n", solvedN)
			if err != nil {
				return unified.ContractError("INVALID_INPUT", "媒体链接更新失败", false), http.StatusBadRequest
			}
			finalURL = updated
		}
		if entry.sChallenge != "" {
			solvedSig := sigSolved[entry.sChallenge]
			if solvedSig == "" {
				return unified.ContractError("UPSTREAM_INVALID_RESPONSE", "YouTube sig 参数解密结果缺失", false), http.StatusBadGateway
			}
			updated, err := setURLQueryParamPreserveOrder(finalURL, entry.sigParam, solvedSig)
			if err != nil {
				return unified.ContractError("INVALID_INPUT", "媒体链接更新失败", false), http.StatusBadRequest
			}
			finalURL = updated
		}
		if !AllowGooglevideoPlaybackURL(finalURL) {
			return unified.ContractError("INVALID_INPUT", "解密后的媒体链接不合法", false), http.StatusBadRequest
		}

		yf, ok := convertToYTFormat(entry.input, finalURL)
		if !ok {
			return unified.ContractError("INVALID_INPUT", "媒体流 mimeType 或参数无效", false), http.StatusBadRequest
		}
		ytFormats = append(ytFormats, yf)
	}

	canonicalWatchURL := "https://www.youtube.com/watch?v=" + videoID
	info := &ytengine.Info{
		ID:         videoID,
		Title:      videoID,
		WebpageURL: canonicalWatchURL,
		Formats:    ytFormats,
	}
	opts := ytengine.AdaptOptions{
		PlatformID:      "youtube",
		ContentID:       videoID,
		SourceURL:       canonicalWatchURL,
		MaxEntries:      1,
		AllowedMediaURL: AllowedMediaURL,
		EnableDASHMux:   true,
		EnableHLS:       false,
	}

	nowUnix := nowTime.Unix()
	payload := ytengine.AdaptInfo(info, opts, nowUnix)
	if err := unified.ValidateEnvelopeAt(payload, nowUnix); err != nil {
		return unified.ContractError("INTERNAL_ERROR", "解析接口合同校验失败", false), http.StatusBadGateway
	}
	status := http.StatusOK
	if unified.Str(payload["code"]) != "OK" {
		status = http.StatusNotFound
	}
	payload = unified.PublicView(payload, debug, nowUnix)
	if err := unified.ValidateEnvelopeAt(payload, nowUnix); err != nil {
		return unified.ContractError("INTERNAL_ERROR", "解析接口合同校验失败", false), http.StatusBadGateway
	}
	return payload, status
}

func convertToYTFormat(in finalizeFormatInput, resolvedURL string) (ytengine.Format, bool) {
	ext, vcodec, acodec, hdr, ok := parseYouTubeMimeType(in.MimeType)
	if !ok {
		return ytengine.Format{}, false
	}
	f := ytengine.Format{
		FormatID:     strconv.Itoa(in.Itag),
		URL:          resolvedURL,
		Ext:          ext,
		Protocol:     "https",
		VCodec:       vcodec,
		ACodec:       acodec,
		DynamicRange: hdr,
		FormatNote:   in.AudioQuality,
	}
	if in.Width != nil && *in.Width > 0 {
		w := *in.Width
		f.Width = &w
	}
	if in.Height != nil && *in.Height > 0 {
		h := *in.Height
		f.Height = &h
	}
	if in.FPS != nil && *in.FPS > 0 {
		fps := *in.FPS
		f.FPS = &fps
	}
	if in.Bitrate != nil && *in.Bitrate > 0 {
		kbps := float64(*in.Bitrate) / 1000.0
		f.TBR = &kbps
		if vcodec != "none" && acodec == "none" {
			vbr := kbps
			f.VBR = &vbr
		} else if vcodec == "none" && acodec != "none" {
			abr := kbps
			f.ABR = &abr
		}
	}
	if in.ContentLength != "" {
		if cl, err := strconv.ParseInt(in.ContentLength, 10, 64); err == nil && cl > 0 {
			f.Filesize = &cl
		}
	}
	if in.AudioSampleRate != "" {
		if asr, err := strconv.ParseInt(in.AudioSampleRate, 10, 64); err == nil && asr > 0 {
			f.ASR = &asr
		}
	}
	if in.AudioChannels != nil && *in.AudioChannels > 0 {
		ch := *in.AudioChannels
		f.AudioChannels = &ch
	}
	return f, true
}

func parseYouTubeMimeType(raw string) (ext, vcodec, acodec, dynamicRange string, ok bool) {
	semi := strings.SplitN(raw, ";", 2)
	mediaType := strings.ToLower(strings.TrimSpace(semi[0]))
	isVideo := false
	isAudioOnly := false
	switch mediaType {
	case "video/mp4":
		ext = "mp4"
		isVideo = true
	case "video/webm":
		ext = "webm"
		isVideo = true
	case "video/3gpp":
		ext = "3gp"
		isVideo = true
	case "audio/mp4":
		ext = "m4a"
		isAudioOnly = true
	case "audio/webm":
		ext = "webm"
		isAudioOnly = true
	default:
		return "", "", "", "", false
	}

	var codecs []string
	if len(semi) == 2 {
		rest := strings.TrimSpace(semi[1])
		if idx := strings.Index(strings.ToLower(rest), "codecs="); idx >= 0 {
			val := strings.TrimSpace(rest[idx+len("codecs="):])
			val = strings.Trim(val, `"'`)
			for _, part := range strings.Split(val, ",") {
				c := strings.TrimSpace(part)
				if c != "" {
					codecs = append(codecs, c)
				}
			}
		}
	}
	if len(codecs) == 0 {
		return "", "", "", "", false
	}

	if isAudioOnly {
		return ext, "none", codecs[0], "", true
	}
	if isVideo {
		vcodec = codecs[0]
		if len(codecs) >= 2 {
			acodec = codecs[1]
		} else {
			acodec = "none"
		}
		vcLower := strings.ToLower(vcodec)
		switch {
		case strings.HasPrefix(vcLower, "vp9.2"), strings.Contains(vcLower, ".10.0.110.09.16."):
			dynamicRange = "HDR10"
		case strings.Contains(vcLower, ".10.0.110.09.18."):
			dynamicRange = "HLG"
		default:
			dynamicRange = "SDR"
		}
		return ext, vcodec, acodec, dynamicRange, true
	}
	return "", "", "", "", false
}

func setURLQueryParamPreserveOrder(rawURL, key, value string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	encodedKV := url.QueryEscape(key) + "=" + url.QueryEscape(value)
	if u.RawQuery == "" {
		u.RawQuery = encodedKV
		return u.String(), nil
	}
	parts := strings.Split(u.RawQuery, "&")
	replaced := false
	out := make([]string, 0, len(parts)+1)
	for _, part := range parts {
		if part == "" {
			continue
		}
		k, _, _ := strings.Cut(part, "=")
		decodedK, err := url.QueryUnescape(k)
		if err != nil {
			decodedK = k
		}
		if decodedK == key {
			if !replaced {
				out = append(out, encodedKV)
				replaced = true
			}
			continue
		}
		out = append(out, part)
	}
	if !replaced {
		out = append(out, encodedKV)
	}
	u.RawQuery = strings.Join(out, "&")
	return u.String(), nil
}
