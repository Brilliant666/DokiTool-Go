package x

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const (
	syndicationEndpointURL       = "https://cdn.syndication.twimg.com/tweet-result"
	syndicationHost              = "cdn.syndication.twimg.com"
	pbsMediaHost                 = "pbs.twimg.com"
	syndicationTimeout           = 10 * time.Second
	syndicationSupplementTimeout = 3 * time.Second
	syndicationMaxBytes          = 512 << 10 // 512 KiB
	base36Alphabet               = "0123456789abcdefghijklmnopqrstuvwxyz"
)

var (
	photoKeyPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	trailingTCoRegex = regexp.MustCompile(`(?:\s+https?://t\.co/[A-Za-z0-9]+)+\s*$`)

	errSyndicationNotFound    = errors.New("x syndication tweet not found")
	errSyndicationInvalid     = errors.New("x syndication invalid or oversized response")
	errSyndicationRateLimited = errors.New("x syndication rate limited")
	errSyndicationBlocked     = errors.New("x syndication blocked")
	errSyndicationTimeout     = errors.New("x syndication timeout")
	errSyndicationUnavailable = errors.New("x syndication unavailable")
)

type syndicationPhoto struct {
	Key     string
	OrigURL string
	Ext     string
	Width   int64
	Height  int64
}

type syndicationTweet struct {
	Text            string
	HasVideo        bool
	Tombstone       bool
	Photos          []syndicationPhoto
	ByAttachmentPos map[int]syndicationPhoto
	AttachmentCount int
}

func (st *syndicationTweet) photoAt(oneBased int) (syndicationPhoto, bool) {
	if st == nil || oneBased < 1 {
		return syndicationPhoto{}, false
	}
	if len(st.ByAttachmentPos) > 0 {
		if p, ok := st.ByAttachmentPos[oneBased]; ok {
			return p, true
		}
		if oneBased > st.AttachmentCount && oneBased > len(st.Photos) {
			return syndicationPhoto{}, false
		}
	}
	if oneBased <= len(st.Photos) {
		return st.Photos[oneBased-1], true
	}
	return syndicationPhoto{}, false
}

// Only read photo list, video marker, and tweet text per Issue #62 boundary.
type syndicationTweetPayload struct {
	Typename     string                  `json:"__typename"`
	Text         string                  `json:"text"`
	Video        json.RawMessage         `json:"video"`
	Photos       []syndicationPhotoEntry `json:"photos"`
	MediaDetails []syndicationMediaEntry `json:"mediaDetails"`
}

type syndicationPhotoEntry struct {
	URL    string `json:"url"`
	Width  int64  `json:"width"`
	Height int64  `json:"height"`
}

type syndicationMediaEntry struct {
	Type          string                 `json:"type"`
	MediaURLHTTPS string                 `json:"media_url_https"`
	OriginalInfo  syndicationMediaBounds `json:"original_info"`
}

type syndicationMediaBounds struct {
	Width  int64 `json:"width"`
	Height int64 `json:"height"`
}

// SyndicationToken computes the public syndication query token from a numeric tweet ID.
func SyndicationToken(tweetID string) (string, bool) {
	if !tweetIDPattern.MatchString(tweetID) {
		return "", false
	}
	id, err := strconv.ParseUint(tweetID, 10, 64)
	if err != nil || id == 0 {
		return "", false
	}
	val := (float64(id) / 1e15) * math.Pi
	if math.IsNaN(val) || math.IsInf(val, 0) || val <= 0 {
		return "", false
	}

	intPart := uint64(val)
	frac := val - float64(intPart)

	var intBuf [32]byte
	pos := len(intBuf)
	if intPart == 0 {
		pos--
		intBuf[pos] = '0'
	} else {
		for n := intPart; n > 0; n /= 36 {
			pos--
			intBuf[pos] = base36Alphabet[n%36]
		}
	}

	var b strings.Builder
	b.Grow(24)
	for _, ch := range intBuf[pos:] {
		if ch != '0' {
			b.WriteByte(ch)
		}
	}
	for i := 0; i < 11; i++ {
		frac *= 36
		digit := int(frac)
		if digit < 0 {
			digit = 0
		} else if digit >= 36 {
			digit = 35
		}
		ch := base36Alphabet[digit]
		if ch != '0' {
			b.WriteByte(ch)
		}
		frac -= float64(digit)
		if frac <= 0 {
			break
		}
	}
	if b.Len() == 0 {
		return "a", true
	}
	return b.String(), true
}

// AllowedSyndicationURL restricts X syndication fetches strictly to
// https://cdn.syndication.twimg.com/tweet-result on default HTTPS port 443.
func AllowedSyndicationURL(raw string) bool {
	u, ok := parseStrictHTTPSURL(raw)
	if !ok {
		return false
	}
	if !strings.EqualFold(u.Hostname(), syndicationHost) {
		return false
	}
	return u.EscapedPath() == "/tweet-result"
}

// NormalizePhotoURL validates an X photo URL against https://pbs.twimg.com/media/<key>
// and rewrites it to https://pbs.twimg.com/media/<key>?format=<ext>&name=orig.
func NormalizePhotoURL(raw string) (string, string, bool) {
	origURL, _, ext, ok := normalizePhotoURLWithKey(raw)
	return origURL, ext, ok
}

func normalizePhotoURLWithKey(raw string) (string, string, string, bool) {
	u, ok := parseStrictHTTPSURL(raw)
	if !ok {
		return "", "", "", false
	}
	if !strings.EqualFold(u.Hostname(), pbsMediaHost) {
		return "", "", "", false
	}
	escapedPath := u.EscapedPath()
	if !strings.HasPrefix(escapedPath, "/media/") {
		return "", "", "", false
	}
	if strings.Contains(escapedPath, "%") || strings.Contains(escapedPath, "..") {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(escapedPath, "/media/")
	if rest == "" || strings.Contains(rest, "/") {
		return "", "", "", false
	}

	key := rest
	pathExt := ""
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		key = rest[:dot]
		pathExt = rest[dot+1:]
		if pathExt == "" || strings.Contains(pathExt, ".") {
			return "", "", "", false
		}
	}
	if !photoKeyPattern.MatchString(key) {
		return "", "", "", false
	}

	rawExt := strings.TrimSpace(u.Query().Get("format"))
	if rawExt == "" {
		rawExt = pathExt
	}
	var ext string
	switch strings.ToLower(rawExt) {
	case "jpg", "jpeg":
		ext = "jpg"
	case "png":
		ext = "png"
	case "webp":
		ext = "webp"
	default:
		return "", "", "", false
	}

	origURL := "https://" + pbsMediaHost + "/media/" + key + "?format=" + ext + "&name=orig"
	return origURL, key, ext, true
}

func parseStrictHTTPSURL(raw string) (*url.URL, bool) {
	if raw == "" || len(raw) > 2048 {
		return nil, false
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return nil, false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return nil, false
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n != 443 {
			return nil, false
		}
	}
	host := u.Hostname()
	if host == "" || strings.HasSuffix(host, ".") {
		return nil, false
	}
	return u, true
}

func fetchSyndicationTweet(ctx context.Context, fetcher fetch.Executor, tweetID string) (*syndicationTweet, error) {
	if fetcher == nil {
		return nil, errSyndicationUnavailable
	}
	token, ok := SyndicationToken(tweetID)
	if !ok {
		return nil, errSyndicationInvalid
	}
	reqURL := syndicationEndpointURL + "?id=" + url.QueryEscape(tweetID) + "&token=" + url.QueryEscape(token)
	hdr := make(http.Header)
	hdr.Set("Accept", "application/json")
	hdr.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := fetcher.Do(ctx, fetch.Request{
		Method: http.MethodGet,
		URL:    reqURL,
		Header: hdr,
		Policy: fetch.Policy{
			Purpose:         "x_syndication_tweet",
			AllowedURL:      AllowedSyndicationURL,
			Timeout:         syndicationTimeout,
			MaxBytes:        syndicationMaxBytes,
			FollowRedirects: false,
			MaxRedirects:    0,
		},
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
			return nil, ctxErr
		}
		if errors.Is(err, fetch.ErrLimit) || errors.Is(err, fetch.ErrPolicy) {
			return nil, errSyndicationInvalid
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errSyndicationTimeout
		}
		return nil, errSyndicationUnavailable
	}

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, errSyndicationNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, errSyndicationRateLimited
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, errSyndicationBlocked
	case resp.StatusCode >= 500:
		return nil, errSyndicationUnavailable
	case resp.StatusCode != http.StatusOK || len(resp.Body) == 0:
		return nil, errSyndicationInvalid
	}

	trimmed := bytes.TrimSpace(resp.Body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errSyndicationInvalid
	}
	var payload syndicationTweetPayload
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil, errSyndicationInvalid
	}
	if strings.EqualFold(payload.Typename, "TweetTombstone") {
		return &syndicationTweet{Tombstone: true}, nil
	}

	out := &syndicationTweet{
		Text:            strings.TrimSpace(payload.Text),
		ByAttachmentPos: make(map[int]syndicationPhoto),
		AttachmentCount: len(payload.MediaDetails),
	}
	rawVideo := bytes.TrimSpace(payload.Video)
	if len(rawVideo) > 0 && !bytes.Equal(rawVideo, []byte("null")) {
		out.HasVideo = true
	}

	seenKeys := make(map[string]int)
	addPhoto := func(rawURL string, w, h int64) (syndicationPhoto, bool) {
		origURL, key, ext, ok := normalizePhotoURLWithKey(rawURL)
		if !ok {
			return syndicationPhoto{}, false
		}
		p := syndicationPhoto{
			Key:     key,
			OrigURL: origURL,
			Ext:     ext,
			Width:   w,
			Height:  h,
		}
		if idx, exists := seenKeys[key]; exists {
			if out.Photos[idx].Width <= 0 && w > 0 && h > 0 {
				out.Photos[idx].Width = w
				out.Photos[idx].Height = h
			}
			return out.Photos[idx], true
		}
		if len(out.Photos) >= maxTweetVideos {
			return p, true
		}
		seenKeys[key] = len(out.Photos)
		out.Photos = append(out.Photos, p)
		return p, true
	}

	for _, pe := range payload.Photos {
		addPhoto(pe.URL, pe.Width, pe.Height)
	}
	for idx, md := range payload.MediaDetails {
		kind := strings.ToLower(strings.TrimSpace(md.Type))
		switch kind {
		case "video", "animated_gif":
			out.HasVideo = true
		case "photo":
			if p, ok := addPhoto(md.MediaURLHTTPS, md.OriginalInfo.Width, md.OriginalInfo.Height); ok {
				out.ByAttachmentPos[idx+1] = p
			}
		}
	}

	return out, nil
}

func syndicationContractError(err error) unified.Object {
	switch {
	case errors.Is(err, errSyndicationNotFound):
		return unified.ContractError("CONTENT_UNAVAILABLE", "未找到可用的媒体或该推文已不可访问", false)
	case errors.Is(err, errSyndicationInvalid):
		return unified.ContractError("UPSTREAM_INVALID_RESPONSE", "上游图片接口响应异常或超出大小限制", false)
	case errors.Is(err, errSyndicationRateLimited):
		return unified.ContractError("RATE_LIMITED", "上游访问频率受限，请稍后重试", true)
	case errors.Is(err, errSyndicationBlocked):
		return unified.ContractError("UPSTREAM_BLOCKED", "该内容受访问限制，无法无凭据解析", false)
	case errors.Is(err, errSyndicationTimeout):
		return unified.ContractError("UPSTREAM_TIMEOUT", "上游连接超时，请稍后重试", true)
	default:
		return unified.ContractError("UPSTREAM_UNAVAILABLE", "当前平台解析服务暂不可用，请稍后重试", true)
	}
}

func buildSyndicationPhotoEnvelope(st *syndicationTweet, photos []syndicationPhoto, tweetID, sourceURL string, now int64) unified.Object {
	if len(photos) == 0 {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false)
	}
	contentID := strings.TrimSpace(tweetID)
	items := make(unified.Array, 0, len(photos))
	imageItemIDs := make(unified.Array, 0, len(photos))
	for i, p := range photos {
		if len(items) >= maxTweetVideos {
			break
		}
		item := buildXPhotoItem(p, contentID, sourceURL, i+1, now)
		if item == nil {
			continue
		}
		items = append(items, item)
		imageItemIDs = append(imageItemIDs, unified.Str(item["id"]))
	}
	if len(items) == 0 {
		return unified.ContractError("NO_MEDIA", "作品未提供可用媒体", false)
	}

	title := tweetTextTitle(st.Text, contentID)
	data := unified.Object{
		"platform":        "x",
		"content_id":      contentID,
		"source_url":      sourceURL,
		"refresh_url":     "/api/parse?schema_version=1.0&url=" + unified.RFC3986Query(sourceURL),
		"title":           title,
		"items":           items,
		"default_item_id": unified.Str(unified.Map(items[0])["id"]),
	}
	if desc := strings.TrimSpace(st.Text); desc != "" {
		data["description"] = desc
	}
	if cover := unified.Source(photos[0].OrigURL); cover != nil {
		data["cover"] = cover
	}
	if len(imageItemIDs) > 0 && len(items) > 1 {
		data["groups"] = unified.Array{
			unified.Object{
				"id":          "album",
				"kind":        "album",
				"item_ids":    imageItemIDs,
				"total_count": int64(len(imageItemIDs)),
				"complete":    true,
			},
		}
	}
	return unified.FinalizeData(data, now)
}

func buildXPhotoItem(photo syndicationPhoto, contentID, sourceURL string, position int, now int64) unified.Object {
	if photo.OrigURL == "" || photo.Key == "" {
		return nil
	}
	container, mimeType := xImageContainerAndMIME(photo.Ext)
	rawSource := unified.Object{
		"url": photo.OrigURL,
		"private_source_identity": unified.PHPIdentityJSON(unified.Array{
			"x-photo",
			contentID,
			photo.Key,
			photo.Ext,
		}, true),
	}
	sources := unified.Sources(unified.Array{rawSource}, now)
	if len(sources) == 0 {
		return nil
	}
	meta := unified.Object{
		"evidence":  "upstream",
		"container": container,
		"extension": photo.Ext,
		"mime_type": mimeType,
	}
	if photo.Width > 0 && photo.Height > 0 {
		meta["width"] = photo.Width
		meta["height"] = photo.Height
	}
	variantID := "img-" + shortDigest("x:image:"+contentID+":"+photo.Key)
	variant := unified.Object{
		"id":       variantID,
		"metadata": meta,
		"delivery": unified.Object{
			"kind":    "file",
			"sources": sources,
		},
	}
	itemID := "image-" + shortDigest("x:photo-item:"+contentID+":"+photo.Key)
	return unified.Object{
		"id":          itemID,
		"type":        "image",
		"index":       position,
		"default":     nil,
		"variants":    unified.Array{variant},
		"resolve_url": unified.ResolveURL(sourceURL, itemID),
	}
}

func xImageContainerAndMIME(ext string) (string, string) {
	switch ext {
	case "png":
		return "png", "image/png"
	case "webp":
		return "webp", "image/webp"
	default:
		return "jpeg", "image/jpeg"
	}
}

func tweetTextTitle(rawText, tweetID string) string {
	cleaned := strings.TrimSpace(trailingTCoRegex.ReplaceAllString(rawText, ""))
	for _, line := range strings.Split(cleaned, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			runes := []rune(trimmed)
			if len(runes) > 80 {
				return strings.TrimSpace(string(runes[:80]))
			}
			return trimmed
		}
	}
	return fmt.Sprintf("X Post %s", tweetID)
}
