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
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

const (
	syndicationEndpointURL       = "https://cdn.syndication.twimg.com/tweet-result"
	syndicationHost              = "cdn.syndication.twimg.com"
	pbsMediaHost                 = "pbs.twimg.com"
	videoMediaHost               = "video.twimg.com"
	syndicationTimeout           = 10 * time.Second
	syndicationSupplementTimeout = 3 * time.Second
	syndicationMaxBytes          = 512 << 10 // 512 KiB
	base36Alphabet               = "0123456789abcdefghijklmnopqrstuvwxyz"

	fxTwitterEndpointPrefix = "https://api.fxtwitter.com/i/status/"
	fxTwitterHost           = "api.fxtwitter.com"
	fxTwitterTimeout        = 4 * time.Second
	fxTwitterMaxBytes       = 256 << 10 // 256 KiB
	fxTwitterUserAgent      = "DokiTool-Go (+https://snap.dokitool.com)"
	tombstoneBlockedMessage = "该内容受年龄或敏感内容限制，无法解析"
)

var (
	photoKeyPattern        = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	trailingTCoRegex       = regexp.MustCompile(`(?:\s+https?://t\.co/[A-Za-z0-9]+)+\s*$`)
	syndicationInlineURLRe = regexp.MustCompile(`\s+https?://[^ ]+`)
	videoDimSegmentPattern = regexp.MustCompile(`^([1-9][0-9]{1,4})x([1-9][0-9]{1,4})$`)
	syndicationMediaIDRe   = regexp.MustCompile(`_video/([1-9][0-9]{0,19})/`)
	syndicationThumbIDRe   = regexp.MustCompile(`_video_thumb/([1-9][0-9]{0,19})/`)
	tweetVideoKeyPattern   = regexp.MustCompile(`^/tweet_video/([A-Za-z0-9_-]{1,64})\.mp4$`)
	safeCodecPattern       = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	safeMediaIDPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

	errSyndicationNotFound    = errors.New("x syndication tweet not found")
	errSyndicationInvalid     = errors.New("x syndication invalid or oversized response")
	errSyndicationRateLimited = errors.New("x syndication rate limited")
	errSyndicationBlocked     = errors.New("x syndication blocked")
	errSyndicationTimeout     = errors.New("x syndication timeout")
	errSyndicationUnavailable = errors.New("x syndication unavailable")

	errFxTwitterNotFound    = errors.New("x fxtwitter tweet not found")
	errFxTwitterUnavailable = errors.New("x fxtwitter unavailable or rejected")
)

type syndicationPhoto struct {
	Key     string
	OrigURL string
	Ext     string
	Width   int64
	Height  int64
}

type syndicationTweet struct {
	Text             string
	RawText          string
	Author           unified.Object
	VideoUploader    string
	VideoUploaderID  string
	VideoUploaderURL string
	Timestamp        *float64
	LikeCount        *int64
	CommentCount     *int64
	Tags             []string
	HasVideo         bool
	Tombstone        bool
	Photos           []syndicationPhoto
	Videos           []ytengine.Info
	IncompleteVideo  bool
	HasCard          bool
	HasQuotedMedia   bool
	ByAttachmentPos  map[int]syndicationPhoto
	AttachmentCount  int
}

func (st *syndicationTweet) canUseSyndicationVideos() bool {
	return st != nil && !st.Tombstone && !st.IncompleteVideo && !st.HasQuotedMedia && len(st.Videos) > 0
}

func (st *syndicationTweet) toYTInfo(tweetID string) *ytengine.Info {
	baseTitle := buildSyndicationVideoTitle(st.RawText, st.VideoUploader, tweetID)
	desc := buildSyndicationVideoDescription(st.RawText)
	entries := make([]ytengine.Info, len(st.Videos))
	copy(entries, st.Videos)

	usedIDs := make(map[string]bool, len(entries))
	for i := range entries {
		if entries[i].DisplayID == "" {
			entries[i].DisplayID = tweetID
		}
		if entries[i].ID == "" || usedIDs[entries[i].ID] {
			if len(entries) == 1 && !usedIDs[tweetID] {
				entries[i].ID = tweetID
			} else {
				entries[i].ID = fmt.Sprintf("%s-%d", tweetID, i+1)
			}
		}
		usedIDs[entries[i].ID] = true
		if len(entries) > 1 {
			entries[i].Title = fmt.Sprintf("%s #%d", baseTitle, i+1)
		} else {
			entries[i].Title = baseTitle
		}
	}

	var tagsCopy []string
	if len(st.Tags) > 0 {
		tagsCopy = make([]string, len(st.Tags))
		copy(tagsCopy, st.Tags)
	}

	if len(entries) == 1 {
		single := entries[0]
		single.Title = baseTitle
		single.Description = desc
		single.Uploader = st.VideoUploader
		single.UploaderID = st.VideoUploaderID
		single.UploaderURL = st.VideoUploaderURL
		single.Timestamp = st.Timestamp
		single.LikeCount = st.LikeCount
		single.CommentCount = st.CommentCount
		single.Tags = tagsCopy
		return &single
	}

	return &ytengine.Info{
		ID:           tweetID,
		DisplayID:    tweetID,
		Type:         "playlist",
		Title:        baseTitle,
		Description:  desc,
		Uploader:     st.VideoUploader,
		UploaderID:   st.VideoUploaderID,
		UploaderURL:  st.VideoUploaderURL,
		Timestamp:    st.Timestamp,
		LikeCount:    st.LikeCount,
		CommentCount: st.CommentCount,
		Tags:         tagsCopy,
		Entries:      entries,
	}
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

type syndicationTweetPayload struct {
	Typename          string                         `json:"__typename"`
	IDStr             string                         `json:"id_str"`
	Text              string                         `json:"text"`
	CreatedAt         string                         `json:"created_at"`
	FavoriteCount     *int64                         `json:"favorite_count"`
	ConversationCount *int64                         `json:"conversation_count"`
	User              *syndicationUserPayload        `json:"user"`
	Entities          *syndicationEntitiesPayload    `json:"entities"`
	Video             json.RawMessage                `json:"video"`
	Card              *syndicationCardPayload        `json:"card"`
	QuotedTweet       *syndicationQuotedTweetPayload `json:"quoted_tweet"`
	Photos            []syndicationPhotoEntry        `json:"photos"`
	MediaDetails      []syndicationMediaEntry        `json:"mediaDetails"`
}

type syndicationUserPayload struct {
	Name       string `json:"name"`
	ScreenName string `json:"screen_name"`
}

type syndicationEntitiesPayload struct {
	Hashtags []syndicationHashtagPayload `json:"hashtags"`
}

type syndicationHashtagPayload struct {
	Text string `json:"text"`
}

type syndicationCardPayload struct {
	Name          string                               `json:"name"`
	BindingValues map[string]syndicationBindingPayload `json:"binding_values"`
}

type syndicationBindingPayload struct {
	StringValue string `json:"string_value"`
}

type syndicationQuotedTweetPayload struct {
	Video        json.RawMessage         `json:"video"`
	Card         json.RawMessage         `json:"card"`
	MediaDetails []syndicationMediaEntry `json:"mediaDetails"`
}

type syndicationPhotoEntry struct {
	URL    string `json:"url"`
	Width  int64  `json:"width"`
	Height int64  `json:"height"`
}

type syndicationMediaEntry struct {
	IDStr         string                     `json:"id_str"`
	Type          string                     `json:"type"`
	MediaURLHTTPS string                     `json:"media_url_https"`
	OriginalInfo  syndicationMediaBounds     `json:"original_info"`
	Sizes         map[string]json.RawMessage `json:"sizes"`
	VideoInfo     *syndicationVideoInfo      `json:"video_info"`
}

type syndicationVideoInfo struct {
	DurationMillis *int64                    `json:"duration_millis"`
	Variants       []syndicationVideoVariant `json:"variants"`
}

type syndicationVideoVariant struct {
	Bitrate     *int64 `json:"bitrate"`
	ContentType string `json:"content_type"`
	URL         string `json:"url"`
}

type syndicationMediaBounds struct {
	Width  int64 `json:"width"`
	Height int64 `json:"height"`
}

type syndicationUnifiedCardPayload struct {
	MediaEntities orderedSyndicationMediaEntities `json:"media_entities"`
}

type orderedSyndicationMediaEntities struct {
	Entries []syndicationMediaEntry
}

func (o *orderedSyndicationMediaEntities) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return errors.New("expected JSON object for media_entities")
	}
	for dec.More() {
		if _, err := dec.Token(); err != nil {
			return err
		}
		var entry syndicationMediaEntry
		if err := dec.Decode(&entry); err != nil {
			return err
		}
		o.Entries = append(o.Entries, entry)
	}
	_, err = dec.Token()
	return err
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
		RawText:         sanitizeSyndicationRawText(payload.Text),
		Timestamp:       parseSyndicationTimestamp(payload.CreatedAt),
		ByAttachmentPos: make(map[int]syndicationPhoto),
		AttachmentCount: len(payload.MediaDetails),
	}
	out.VideoUploader, out.VideoUploaderID, out.VideoUploaderURL = sanitizeSyndicationUser(payload.User)
	if payload.FavoriteCount != nil && *payload.FavoriteCount >= 0 && *payload.FavoriteCount <= maxSafeInteger {
		likes := *payload.FavoriteCount
		out.LikeCount = &likes
	}
	if payload.ConversationCount != nil && *payload.ConversationCount >= 0 && *payload.ConversationCount <= maxSafeInteger {
		comments := *payload.ConversationCount
		out.CommentCount = &comments
	}
	if payload.Entities != nil && len(payload.Entities.Hashtags) > 0 {
		for _, ht := range payload.Entities.Hashtags {
			if len(out.Tags) >= 64 {
				break
			}
			if cleanedTag := cleanThirdPartyText(ht.Text, 100, false); cleanedTag != "" {
				out.Tags = append(out.Tags, cleanedTag)
			}
		}
	}

	rawVideo := bytes.TrimSpace(payload.Video)
	if len(rawVideo) > 0 && !bytes.Equal(rawVideo, []byte("null")) {
		out.HasVideo = true
	}

	if payload.QuotedTweet != nil {
		qtVideo := bytes.TrimSpace(payload.QuotedTweet.Video)
		qtCard := bytes.TrimSpace(payload.QuotedTweet.Card)
		if (len(qtVideo) > 0 && !bytes.Equal(qtVideo, []byte("null"))) ||
			(len(qtCard) > 0 && !bytes.Equal(qtCard, []byte("null"))) {
			out.HasQuotedMedia = true
		}
		for _, qm := range payload.QuotedTweet.MediaDetails {
			qKind := strings.ToLower(strings.TrimSpace(qm.Type))
			if qKind != "" && qKind != "photo" {
				out.HasQuotedMedia = true
				break
			}
		}
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
			if len(out.Videos) >= maxTweetVideos {
				continue
			}
			if entry, ok := buildSyndicationVideoEntry(md, tweetID); ok {
				out.Videos = append(out.Videos, entry)
			} else {
				out.IncompleteVideo = true
			}
		case "photo":
			if p, ok := addPhoto(md.MediaURLHTTPS, md.OriginalInfo.Width, md.OriginalInfo.Height); ok {
				out.ByAttachmentPos[idx+1] = p
			}
		}
	}

	if payload.Card != nil {
		cardName := strings.ToLower(strings.TrimSpace(payload.Card.Name))
		if colon := strings.LastIndexByte(cardName, ':'); colon >= 0 {
			cardName = cardName[colon+1:]
		}
		if cardName == "unified_card" {
			rawUC := payload.Card.BindingValues["unified_card"].StringValue
			if rawUC == "" || len(rawUC) > syndicationMaxBytes {
				out.HasCard = true
				out.IncompleteVideo = true
			} else {
				var uc syndicationUnifiedCardPayload
				if err := json.Unmarshal([]byte(rawUC), &uc); err != nil {
					out.HasCard = true
					out.IncompleteVideo = true
				} else {
					sawCardVideo := false
					for _, md := range uc.MediaEntities.Entries {
						kind := strings.ToLower(strings.TrimSpace(md.Type))
						if kind != "video" && kind != "animated_gif" {
							continue
						}
						sawCardVideo = true
						out.HasVideo = true
						if len(out.Videos) >= maxTweetVideos {
							continue
						}
						if entry, ok := buildSyndicationVideoEntry(md, tweetID); ok {
							out.Videos = append(out.Videos, entry)
						} else {
							out.IncompleteVideo = true
						}
					}
					if !sawCardVideo {
						out.HasCard = true
					}
				}
			}
		} else if cardName != "" {
			out.HasCard = true
			out.IncompleteVideo = true
		}
	}

	return out, nil
}

func buildSyndicationVideoEntry(md syndicationMediaEntry, tweetID string) (ytengine.Info, bool) {
	kind := strings.ToLower(strings.TrimSpace(md.Type))
	if kind != "video" && kind != "animated_gif" {
		return ytengine.Info{}, false
	}
	if md.VideoInfo == nil || len(md.VideoInfo.Variants) == 0 {
		return ytengine.Info{}, false
	}

	isGIF := kind == "animated_gif"

	type mp4Candidate struct {
		url     string
		urlPath string
		bitrate int64
		hasRate bool
		gifKey  string
	}

	var (
		mp4Candidates  []mp4Candidate
		seenURLs       = make(map[string]bool)
		mediaIDFromURL string
	)

	for _, vr := range md.VideoInfo.Variants {
		cleanURL, ext, ok := sanitizeFxTwitterVideoURL(vr.URL)
		if !ok {
			continue
		}
		if mediaIDFromURL == "" {
			if m := syndicationMediaIDRe.FindStringSubmatch(cleanURL); len(m) == 2 {
				mediaIDFromURL = m[1]
			}
		}
		if ext != ".mp4" {
			continue
		}
		ct := strings.ToLower(strings.TrimSpace(vr.ContentType))
		if ct != "" && ct != "video/mp4" {
			continue
		}
		if seenURLs[cleanURL] {
			continue
		}
		var bitrate int64
		var hasRate bool
		if vr.Bitrate != nil {
			if *vr.Bitrate < 0 || *vr.Bitrate > 500_000_000 {
				continue
			}
			bitrate = *vr.Bitrate
			hasRate = true
		}
		parsedURL, err := url.Parse(cleanURL)
		if err != nil {
			continue
		}
		var gifKey string
		if strings.Contains(parsedURL.Path, "/tweet_video/") {
			isGIF = true
			if m := tweetVideoKeyPattern.FindStringSubmatch(parsedURL.Path); len(m) == 2 {
				gifKey = m[1]
			}
		}
		seenURLs[cleanURL] = true
		mp4Candidates = append(mp4Candidates, mp4Candidate{
			url:     cleanURL,
			urlPath: parsedURL.Path,
			bitrate: bitrate,
			hasRate: hasRate,
			gifKey:  gifKey,
		})
	}

	if len(mp4Candidates) == 0 {
		return ytengine.Info{}, false
	}

	var durPtr *float64
	if !isGIF {
		if md.VideoInfo.DurationMillis == nil || *md.VideoInfo.DurationMillis <= 0 || *md.VideoInfo.DurationMillis > 86_400_000 {
			return ytengine.Info{}, false
		}
		dur := float64(*md.VideoInfo.DurationMillis) / 1000.0
		durPtr = &dur
	} else if md.VideoInfo.DurationMillis != nil {
		if *md.VideoInfo.DurationMillis < 0 || *md.VideoInfo.DurationMillis > 86_400_000 {
			return ytengine.Info{}, false
		}
		if *md.VideoInfo.DurationMillis > 0 {
			dur := float64(*md.VideoInfo.DurationMillis) / 1000.0
			durPtr = &dur
		}
	}

	usedFormatIDs := make(map[string]bool, len(mp4Candidates))
	nextFormatID := func(kbps int64, seq int) string {
		base := "http"
		if kbps > 0 {
			base = fmt.Sprintf("http-%d", kbps)
		}
		if !usedFormatIDs[base] {
			usedFormatIDs[base] = true
			return base
		}
		var id string
		if kbps > 0 {
			id = fmt.Sprintf("http-%d-%d", kbps, seq)
		} else {
			id = fmt.Sprintf("http-%d", seq)
		}
		usedFormatIDs[id] = true
		return id
	}

	formats := make([]ytengine.Format, 0, len(mp4Candidates))
	var firstGIFKey string
	for i, c := range mp4Candidates {
		if firstGIFKey == "" && c.gifKey != "" {
			firstGIFKey = c.gifKey
		}
		w, h := extractDimensionsFromVideoPath(c.urlPath)
		if (w == 0 || h == 0) && len(mp4Candidates) == 1 &&
			md.OriginalInfo.Width > 0 && md.OriginalInfo.Width <= 65535 &&
			md.OriginalInfo.Height > 0 && md.OriginalInfo.Height <= 65535 {
			w, h = md.OriginalInfo.Width, md.OriginalInfo.Height
		}
		if w <= 0 || h <= 0 {
			return ytengine.Info{}, false
		}

		var tbrPtr *float64
		var kbps int64
		if c.hasRate && c.bitrate > 0 {
			tbr := float64(c.bitrate) / 1000.0
			tbrPtr = &tbr
			kbps = c.bitrate / 1000
		}

		wVal, hVal := w, h
		acodec := ""
		if isGIF {
			acodec = "none"
		}
		formats = append(formats, ytengine.Format{
			FormatID: nextFormatID(kbps, i+1),
			URL:      c.url,
			Ext:      "mp4",
			Protocol: "https",
			Width:    &wVal,
			Height:   &hVal,
			TBR:      tbrPtr,
			ACodec:   acodec,
		})
	}

	coverURL := sanitizeSyndicationCoverURL(md.MediaURLHTTPS, len(md.Sizes) > 0)

	entryID := strings.TrimSpace(md.IDStr)
	if !safeMediaIDPattern.MatchString(entryID) {
		entryID = ""
	}
	if entryID == "" && mediaIDFromURL != "" {
		entryID = mediaIDFromURL
	}
	if entryID == "" && coverURL != "" {
		if m := syndicationThumbIDRe.FindStringSubmatch(coverURL); len(m) == 2 {
			entryID = m[1]
		}
	}
	if entryID == "" && firstGIFKey != "" {
		entryID = firstGIFKey
	}

	return ytengine.Info{
		ID:        entryID,
		DisplayID: tweetID,
		Type:      "video",
		Duration:  durPtr,
		Thumbnail: coverURL,
		Formats:   formats,
	}, true
}

func sanitizeSyndicationCoverURL(raw string, hasSizes bool) string {
	clean := sanitizeFxTwitterCoverURL(raw)
	if clean == "" {
		return ""
	}
	u, err := url.Parse(clean)
	if err != nil {
		return ""
	}
	if u.Query().Get("name") == "" && (hasSizes || strings.Contains(u.Path, "_video_thumb/") || strings.Contains(u.Path, "/media/")) {
		if u.RawQuery == "" {
			clean += "?name=orig"
		} else {
			clean += "&name=orig"
		}
		clean = sanitizeXMediaURL(clean)
		if clean == "" || unified.Source(clean) == nil {
			return ""
		}
	}
	return clean
}

func sanitizeSyndicationUser(u *syndicationUserPayload) (string, string, string) {
	if u == nil {
		return "", "", ""
	}
	name := cleanThirdPartyText(u.Name, 80, false)
	sn := strings.TrimSpace(u.ScreenName)
	var profileURL string
	if screenNamePattern.MatchString(sn) && !reservedHandles[strings.ToLower(sn)] {
		profileURL = "https://twitter.com/" + sn
	} else {
		sn = ""
	}
	return name, sn, profileURL
}

func sanitizeSyndicationRawText(raw string) string {
	if raw == "" {
		return ""
	}
	valid := strings.ToValidUTF8(raw, "")
	valid = strings.ReplaceAll(valid, "\r\n", "\n")
	valid = strings.ReplaceAll(valid, "\r", "\n")
	var b strings.Builder
	b.Grow(len(valid))
	for _, r := range valid {
		if r == '\n' {
			b.WriteByte('\n')
			continue
		}
		if r == '\t' {
			b.WriteByte(' ')
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	cleaned := b.String()
	runes := []rune(cleaned)
	if len(runes) > 4000 {
		cleaned = string(runes[:4000])
	}
	return cleaned
}

func buildSyndicationVideoDescription(rawText string) string {
	return strings.TrimSpace(strings.ReplaceAll(rawText, "\n", " "))
}

func buildSyndicationVideoTitle(rawText, uploader, tweetID string) string {
	flat := strings.TrimSpace(strings.ReplaceAll(rawText, "\n", " "))
	stripped := strings.TrimSpace(syndicationInlineURLRe.ReplaceAllString(flat, ""))
	runes := []rune(stripped)
	if len(runes) > 72 {
		stripped = string(runes[:69]) + "..."
	}
	if uploader != "" {
		if stripped != "" {
			return uploader + " - " + stripped
		}
		return uploader
	}
	return tweetTextTitle(rawText, tweetID)
}

func parseSyndicationTimestamp(raw string) *float64 {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		time.RubyDate,
		"Mon Jan 02 15:04:05 -0700 2006",
	} {
		if t, err := time.Parse(layout, trimmed); err == nil && t.Unix() > 0 {
			ts := float64(t.Unix())
			return &ts
		}
	}
	return nil
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
	if st != nil && len(st.Author) > 0 {
		data["author"] = st.Author
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

// Only unmarshal tweet text, author, media.photos[], and media.videos[] (including GIF) per Issue #88.
type fxTwitterResponse struct {
	Code  int                    `json:"code"`
	Tweet *fxTwitterTweetPayload `json:"tweet"`
}

type fxTwitterTweetPayload struct {
	Text   string                 `json:"text"`
	Author *fxTwitterAuthor       `json:"author"`
	Media  *fxTwitterMediaPayload `json:"media"`
}

type fxTwitterAuthor struct {
	Name       string `json:"name"`
	ScreenName string `json:"screen_name"`
}

type fxTwitterMediaPayload struct {
	Photos []fxTwitterPhotoPayload `json:"photos"`
	Videos []fxTwitterVideoPayload `json:"videos"`
}

type fxTwitterPhotoPayload struct {
	URL    string `json:"url"`
	Width  int64  `json:"width"`
	Height int64  `json:"height"`
}

type fxTwitterVideoPayload struct {
	ID           string                        `json:"id"`
	Type         string                        `json:"type"`
	URL          string                        `json:"url"`
	ThumbnailURL string                        `json:"thumbnail_url"`
	Duration     float64                       `json:"duration"`
	Width        int64                         `json:"width"`
	Height       int64                         `json:"height"`
	Formats      []fxTwitterVideoFormatPayload `json:"formats"`
	Variants     []fxTwitterVideoVariant       `json:"variants"`
}

type fxTwitterVideoFormatPayload struct {
	URL       string `json:"url"`
	Bitrate   int64  `json:"bitrate"`
	Container string `json:"container"`
	Codec     string `json:"codec"`
	Width     int64  `json:"width"`
	Height    int64  `json:"height"`
	Size      int64  `json:"size"`
}

type fxTwitterVideoVariant struct {
	URL         string `json:"url"`
	Bitrate     int64  `json:"bitrate"`
	ContentType string `json:"content_type"`
}

type fxTwitterTweet struct {
	Text             string
	Author           unified.Object
	AuthorName       string
	AuthorScreenName string
	AuthorURL        string
	Photos           []syndicationPhoto
	Videos           []ytengine.Info
}

func (ft *fxTwitterTweet) photoAt(oneBased int) (syndicationPhoto, bool) {
	if ft == nil || oneBased < 1 || oneBased > len(ft.Photos) {
		return syndicationPhoto{}, false
	}
	return ft.Photos[oneBased-1], true
}

func (ft *fxTwitterTweet) toSyndicationTweet() *syndicationTweet {
	return &syndicationTweet{
		Text:   ft.Text,
		Author: ft.Author,
		Photos: ft.Photos,
	}
}

func (ft *fxTwitterTweet) toYTInfo(tweetID string) *ytengine.Info {
	title := tweetTextTitle(ft.Text, tweetID)
	entries := make([]ytengine.Info, len(ft.Videos))
	copy(entries, ft.Videos)
	for i := range entries {
		if entries[i].DisplayID == "" {
			entries[i].DisplayID = tweetID
		}
		if entries[i].Title == "" {
			entries[i].Title = title
		}
	}
	info := &ytengine.Info{
		ID:          tweetID,
		DisplayID:   tweetID,
		Title:       title,
		Description: ft.Text,
		Uploader:    ft.AuthorName,
		UploaderID:  ft.AuthorScreenName,
		UploaderURL: ft.AuthorURL,
	}
	if len(entries) == 1 {
		single := entries[0]
		single.Title = title
		single.Description = ft.Text
		single.Uploader = ft.AuthorName
		single.UploaderID = ft.AuthorScreenName
		single.UploaderURL = ft.AuthorURL
		return &single
	}
	info.Type = "playlist"
	info.Entries = entries
	return info
}

// AllowedFxTwitterURL restricts TweetTombstone fallback fetches strictly to
// https://api.fxtwitter.com/i/status/<numeric tweetID> on the default HTTPS port
// with no userinfo, query string, fragment, or extra path segments.
func AllowedFxTwitterURL(raw string) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '\\' || r == '?' || r == '#' || r == '@' {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Port() != "" {
		return false
	}
	if !strings.EqualFold(u.Host, fxTwitterHost) {
		return false
	}
	escapedPath := u.EscapedPath()
	if strings.Contains(escapedPath, "%") || !strings.HasPrefix(escapedPath, "/i/status/") {
		return false
	}
	id := strings.TrimPrefix(escapedPath, "/i/status/")
	return tweetIDPattern.MatchString(id)
}

func tombstoneBlockedError() unified.Object {
	return unified.ContractError("UPSTREAM_BLOCKED", tombstoneBlockedMessage, false)
}

func (p *Parser) resolveTombstone(ctx context.Context, tweetID, source, selectedKind string, selectedIndex int, now int64) (unified.Object, error) {
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
		return nil, ctxErr
	}
	if !p.fxtwitterEnabled() || p.fetcher == nil {
		return tombstoneBlockedError(), nil
	}

	fx, err := fetchFxTwitterTweet(ctx, p.fetcher, tweetID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
			return nil, ctxErr
		}
		if errors.Is(err, errFxTwitterNotFound) {
			return unified.ContractError("CONTENT_UNAVAILABLE", "未找到可用的媒体或该推文已不可访问", false), nil
		}
		return tombstoneBlockedError(), nil
	}

	if selectedKind == "photo" {
		photo, found := fx.photoAt(selectedIndex)
		if !found {
			return unified.ContractError("CONTENT_UNAVAILABLE", fmt.Sprintf("所选第 %d 张图片不存在或不可用", selectedIndex), false), nil
		}
		return buildSyndicationPhotoEnvelope(fx.toSyndicationTweet(), []syndicationPhoto{photo}, tweetID, source, now), nil
	}

	if selectedKind == "video" {
		if len(fx.Videos) == 0 {
			return unified.ContractError("CONTENT_UNAVAILABLE", "未找到可用的视频或该推文已不可访问", false), nil
		}
		info := fx.toYTInfo(tweetID)
		populateMissingMP4Sizes(ctx, info, p.sizeProber)
		adapted := AdaptInfo(info, tweetID, source, selectedIndex, now)
		if unified.Str(adapted["code"]) != "OK" {
			return tombstoneBlockedError(), nil
		}
		return adapted, nil
	}

	if len(fx.Videos) > 0 {
		info := fx.toYTInfo(tweetID)
		populateMissingMP4Sizes(ctx, info, p.sizeProber)
		adapted := adaptInfoWithPhotos(info, fx.Photos, tweetID, source, selectedIndex, now)
		if unified.Str(adapted["code"]) != "OK" {
			return tombstoneBlockedError(), nil
		}
		return adapted, nil
	}

	return buildSyndicationPhotoEnvelope(fx.toSyndicationTweet(), fx.Photos, tweetID, source, now), nil
}

func fetchFxTwitterTweet(ctx context.Context, fetcher fetch.Executor, tweetID string) (*fxTwitterTweet, error) {
	if fetcher == nil || !tweetIDPattern.MatchString(tweetID) {
		return nil, errFxTwitterUnavailable
	}
	reqURL := fxTwitterEndpointPrefix + tweetID
	hdr := make(http.Header)
	hdr.Set("Accept", "application/json")
	hdr.Set("User-Agent", fxTwitterUserAgent)

	resp, err := fetcher.Do(ctx, fetch.Request{
		Method: http.MethodGet,
		URL:    reqURL,
		Header: hdr,
		Policy: fetch.Policy{
			Purpose:          "x_fxtwitter_tombstone",
			AllowedURL:       AllowedFxTwitterURL,
			Timeout:          fxTwitterTimeout,
			MaxBytes:         fxTwitterMaxBytes,
			FollowRedirects:  false,
			ReadRedirectBody: false,
			AutoReferer:      false,
			MaxRedirects:     0,
		},
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.Canceled) {
			return nil, ctxErr
		}
		return nil, errFxTwitterUnavailable
	}

	if int64(len(resp.Body)) > fxTwitterMaxBytes {
		return nil, errFxTwitterUnavailable
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, errFxTwitterNotFound
	}
	if resp.StatusCode != http.StatusOK || len(resp.Body) == 0 {
		return nil, errFxTwitterUnavailable
	}

	trimmed := bytes.TrimSpace(resp.Body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errFxTwitterUnavailable
	}
	var payload fxTwitterResponse
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil, errFxTwitterUnavailable
	}
	if payload.Code == http.StatusNotFound {
		return nil, errFxTwitterNotFound
	}
	if (payload.Code != 0 && payload.Code != http.StatusOK) || payload.Tweet == nil {
		return nil, errFxTwitterUnavailable
	}

	authorObj, authorName, authorScreenName, authorURL := sanitizeFxTwitterAuthor(payload.Tweet.Author)
	out := &fxTwitterTweet{
		Text:             cleanThirdPartyText(payload.Tweet.Text, 4000, true),
		Author:           authorObj,
		AuthorName:       authorName,
		AuthorScreenName: authorScreenName,
		AuthorURL:        authorURL,
	}

	if payload.Tweet.Media != nil {
		seenPhotoKeys := make(map[string]int)
		for _, pe := range payload.Tweet.Media.Photos {
			origURL, key, ext, ok := normalizePhotoURLWithKey(pe.URL)
			if !ok {
				continue
			}
			var w, h int64
			if pe.Width > 0 && pe.Width <= 65535 && pe.Height > 0 && pe.Height <= 65535 {
				w, h = pe.Width, pe.Height
			}
			if idx, exists := seenPhotoKeys[key]; exists {
				if out.Photos[idx].Width <= 0 && w > 0 && h > 0 {
					out.Photos[idx].Width = w
					out.Photos[idx].Height = h
				}
				continue
			}
			if len(out.Photos) >= maxTweetVideos {
				break
			}
			seenPhotoKeys[key] = len(out.Photos)
			out.Photos = append(out.Photos, syndicationPhoto{
				Key:     key,
				OrigURL: origURL,
				Ext:     ext,
				Width:   w,
				Height:  h,
			})
		}

		for idx, ve := range payload.Tweet.Media.Videos {
			if len(out.Videos) >= maxTweetVideos {
				break
			}
			if entry, ok := buildFxTwitterVideoEntry(ve, tweetID, idx+1); ok {
				out.Videos = append(out.Videos, entry)
			}
		}
	}

	if len(out.Photos) == 0 && len(out.Videos) == 0 {
		return nil, errFxTwitterUnavailable
	}
	return out, nil
}

func buildFxTwitterVideoEntry(v fxTwitterVideoPayload, tweetID string, position int) (ytengine.Info, bool) {
	isGIF := strings.EqualFold(strings.TrimSpace(v.Type), "gif") || strings.EqualFold(strings.TrimSpace(v.Type), "animated_gif")

	cleanTopURL, _, topOK := sanitizeFxTwitterVideoURL(v.URL)
	if topOK && strings.Contains(cleanTopURL, "/tweet_video/") {
		isGIF = true
	}

	type rawCandidate struct {
		url         string
		bitrate     int64
		container   string
		contentType string
		codec       string
		width       int64
		height      int64
		size        int64
	}

	var candidates []rawCandidate
	for _, f := range v.Formats {
		candidates = append(candidates, rawCandidate{
			url:       f.URL,
			bitrate:   f.Bitrate,
			container: f.Container,
			codec:     f.Codec,
			width:     f.Width,
			height:    f.Height,
			size:      f.Size,
		})
	}
	for _, vr := range v.Variants {
		candidates = append(candidates, rawCandidate{
			url:         vr.URL,
			bitrate:     vr.Bitrate,
			contentType: vr.ContentType,
		})
	}
	if v.URL != "" {
		candidates = append(candidates, rawCandidate{
			url:    v.URL,
			width:  v.Width,
			height: v.Height,
		})
	}

	var (
		mp4Formats []ytengine.Format
		hlsFormats []ytengine.Format
		seenURLs   = make(map[string]bool)
		usedIDs    = make(map[string]bool)
	)

	nextFormatID := func(prefix string, kbps int64, seq int) string {
		var base string
		if kbps > 0 {
			base = fmt.Sprintf("%s-%d", prefix, kbps)
		} else {
			base = fmt.Sprintf("%s-%d", prefix, seq)
		}
		if !usedIDs[base] {
			usedIDs[base] = true
			return base
		}
		id := fmt.Sprintf("%s-%d-%d", prefix, kbps, seq)
		usedIDs[id] = true
		return id
	}

	for i, c := range candidates {
		cleanURL, ext, ok := sanitizeFxTwitterVideoURL(c.url)
		if !ok || seenURLs[cleanURL] {
			continue
		}
		ct := strings.ToLower(strings.TrimSpace(c.contentType))
		cont := strings.ToLower(strings.TrimSpace(c.container))
		if ext == ".mp4" && (strings.Contains(ct, "mpegurl") || cont == "m3u8") {
			continue
		}
		if strings.Contains(cleanURL, "/tweet_video/") {
			isGIF = true
		}
		seenURLs[cleanURL] = true

		w, h := int64(0), int64(0)
		if c.width > 0 && c.width <= 65535 && c.height > 0 && c.height <= 65535 {
			w, h = c.width, c.height
		}
		if w == 0 || h == 0 {
			if parsed, err := url.Parse(cleanURL); err == nil {
				w, h = extractDimensionsFromVideoPath(parsed.Path)
			}
		}
		if (w == 0 || h == 0) && topOK && cleanURL == cleanTopURL && v.Width > 0 && v.Width <= 65535 && v.Height > 0 && v.Height <= 65535 {
			w, h = v.Width, v.Height
		}

		vcodec := sanitizeFxTwitterCodec(c.codec)
		if ext == ".m3u8" && vcodec == "" {
			vcodec = "h264"
		}

		var tbrPtr *float64
		var kbps int64
		if c.bitrate > 0 && c.bitrate <= 500_000_000 {
			tbr := float64(c.bitrate) / 1000.0
			tbrPtr = &tbr
			kbps = c.bitrate / 1000
		}

		var sizePtr *int64
		if c.size > 0 && c.size <= maxSafeInteger {
			sz := c.size
			sizePtr = &sz
		}

		var wPtr, hPtr *int64
		if w > 0 && h > 0 {
			wVal, hVal := w, h
			wPtr, hPtr = &wVal, &hVal
		}

		if ext == ".mp4" {
			acodec := ""
			if isGIF {
				acodec = "none"
			}
			mp4Formats = append(mp4Formats, ytengine.Format{
				FormatID: nextFormatID("http", kbps, i+1),
				URL:      cleanURL,
				Ext:      "mp4",
				Protocol: "https",
				Width:    wPtr,
				Height:   hPtr,
				TBR:      tbrPtr,
				VCodec:   vcodec,
				ACodec:   acodec,
				Filesize: sizePtr,
			})
		} else if ext == ".m3u8" {
			acodec := "mp4a.40.2"
			if isGIF {
				acodec = "none"
			}
			hlsFormats = append(hlsFormats, ytengine.Format{
				FormatID: nextFormatID("hls", kbps, i+1),
				URL:      cleanURL,
				Ext:      "mp4",
				Protocol: "m3u8_native",
				Width:    wPtr,
				Height:   hPtr,
				TBR:      tbrPtr,
				VCodec:   vcodec,
				ACodec:   acodec,
			})
		}
	}

	if len(mp4Formats) == 1 && (mp4Formats[0].Width == nil || mp4Formats[0].Height == nil) &&
		v.Width > 0 && v.Width <= 65535 && v.Height > 0 && v.Height <= 65535 {
		wVal, hVal := v.Width, v.Height
		mp4Formats[0].Width = &wVal
		mp4Formats[0].Height = &hVal
	}
	if isGIF {
		for i := range mp4Formats {
			mp4Formats[i].ACodec = "none"
		}
	}

	formats := make([]ytengine.Format, 0, len(mp4Formats)+len(hlsFormats))
	formats = append(formats, mp4Formats...)
	for _, hf := range hlsFormats {
		// Skip resolution-less master .m3u8 playlists when progressive MP4 files exist.
		if len(mp4Formats) > 0 && (hf.Width == nil || hf.Height == nil) {
			continue
		}
		formats = append(formats, hf)
	}
	if len(formats) == 0 {
		return ytengine.Info{}, false
	}

	entryID := strings.TrimSpace(v.ID)
	if !safeMediaIDPattern.MatchString(entryID) {
		entryID = fmt.Sprintf("%s-%d", tweetID, position)
	}

	var durPtr *float64
	if !isGIF && v.Duration > 0 && !math.IsNaN(v.Duration) && !math.IsInf(v.Duration, 0) && v.Duration <= 86400 {
		dur := v.Duration
		durPtr = &dur
	}

	return ytengine.Info{
		ID:        entryID,
		DisplayID: tweetID,
		Type:      "video",
		Duration:  durPtr,
		Thumbnail: sanitizeFxTwitterCoverURL(v.ThumbnailURL),
		Formats:   formats,
	}, true
}

func sanitizeFxTwitterVideoURL(raw string) (string, string, bool) {
	u, ok := parseStrictHTTPSURL(raw)
	if !ok {
		return "", "", false
	}
	if !strings.EqualFold(u.Hostname(), videoMediaHost) {
		return "", "", false
	}
	escapedPath := u.EscapedPath()
	escapedLower := strings.ToLower(escapedPath)
	if escapedPath == "" || escapedPath == "/" ||
		strings.Contains(escapedLower, "%2e") ||
		strings.Contains(escapedLower, "%2f") ||
		strings.Contains(escapedLower, "%5c") {
		return "", "", false
	}
	for _, seg := range strings.Split(escapedPath, "/") {
		if seg == "." || seg == ".." {
			return "", "", false
		}
	}
	ext := strings.ToLower(path.Ext(u.Path))
	if ext != ".mp4" && ext != ".m3u8" {
		return "", "", false
	}
	u.Scheme = "https"
	u.Host = videoMediaHost
	clean := sanitizeXMediaURL(u.String())
	if clean == "" || unified.Source(clean) == nil {
		return "", "", false
	}
	return clean, ext, true
}

func sanitizeFxTwitterCoverURL(raw string) string {
	u, ok := parseStrictHTTPSURL(raw)
	if !ok {
		return ""
	}
	if !strings.EqualFold(u.Hostname(), pbsMediaHost) {
		return ""
	}
	escapedPath := u.EscapedPath()
	escapedLower := strings.ToLower(escapedPath)
	if escapedPath == "" || escapedPath == "/" ||
		strings.Contains(escapedLower, "%2e") ||
		strings.Contains(escapedLower, "%2f") ||
		strings.Contains(escapedLower, "%5c") {
		return ""
	}
	for _, seg := range strings.Split(escapedPath, "/") {
		if seg == "." || seg == ".." {
			return ""
		}
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(u.Path), "."))
	if ext == "" {
		ext = strings.ToLower(strings.TrimSpace(u.Query().Get("format")))
	}
	switch ext {
	case "jpg", "jpeg", "png", "webp":
	default:
		return ""
	}
	u.Scheme = "https"
	u.Host = pbsMediaHost
	clean := sanitizeXMediaURL(u.String())
	if clean == "" || unified.Source(clean) == nil {
		return ""
	}
	return clean
}

func extractDimensionsFromVideoPath(urlPath string) (int64, int64) {
	for _, seg := range strings.Split(urlPath, "/") {
		m := videoDimSegmentPattern.FindStringSubmatch(seg)
		if len(m) != 3 {
			continue
		}
		w, errW := strconv.ParseInt(m[1], 10, 64)
		h, errH := strconv.ParseInt(m[2], 10, 64)
		if errW == nil && errH == nil && w > 0 && w <= 65535 && h > 0 && h <= 65535 {
			return w, h
		}
	}
	return 0, 0
}

func sanitizeFxTwitterCodec(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || isNoCodec(trimmed) || !safeCodecPattern.MatchString(trimmed) {
		return ""
	}
	return trimmed
}

func sanitizeFxTwitterAuthor(a *fxTwitterAuthor) (unified.Object, string, string, string) {
	if a == nil {
		return nil, "", "", ""
	}
	author := unified.Object{}
	screenName := strings.TrimSpace(a.ScreenName)
	var profileURL string
	if screenNamePattern.MatchString(screenName) && !reservedHandles[strings.ToLower(screenName)] {
		author["id"] = screenName
		profileURL = "https://x.com/" + screenName
		author["url"] = profileURL
	} else {
		screenName = ""
	}
	name := cleanThirdPartyText(a.Name, 80, false)
	if name != "" {
		author["name"] = name
	}
	if len(author) == 0 {
		return nil, "", "", ""
	}
	return author, name, screenName, profileURL
}

func cleanThirdPartyText(raw string, maxRunes int, allowNewlines bool) string {
	if raw == "" {
		return ""
	}
	valid := strings.ToValidUTF8(raw, "")
	valid = strings.ReplaceAll(valid, "\r\n", "\n")
	valid = strings.ReplaceAll(valid, "\r", "\n")
	var b strings.Builder
	b.Grow(len(valid))
	for _, r := range valid {
		if r == '\n' {
			if allowNewlines {
				b.WriteRune('\n')
			} else {
				b.WriteByte(' ')
			}
			continue
		}
		if r == '\t' {
			b.WriteByte(' ')
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	cleaned := strings.TrimSpace(b.String())
	if maxRunes > 0 {
		runes := []rune(cleaned)
		if len(runes) > maxRunes {
			cleaned = strings.TrimSpace(string(runes[:maxRunes]))
		}
	}
	return cleaned
}
