// Package ytengine provides a hardened, self-contained wrapper around a pinned
// yt-dlp binary in metadata-only (-J) mode.
//
// Adapted from wader/goutubedl and lrstanley/go-ytdlp (MIT License).
package ytengine

import (
	"context"
	"fmt"
)

// ErrorCode classifies engine-level failures into unified-v1 contract codes.
type ErrorCode string

const (
	CodeInvalidInput            ErrorCode = "INVALID_INPUT"
	CodeContentUnavailable      ErrorCode = "CONTENT_UNAVAILABLE"
	CodeUpstreamBlocked         ErrorCode = "UPSTREAM_BLOCKED"
	CodeRateLimited             ErrorCode = "RATE_LIMITED"
	CodeUpstreamTimeout         ErrorCode = "UPSTREAM_TIMEOUT"
	CodeUpstreamInvalidResponse ErrorCode = "UPSTREAM_INVALID_RESPONSE"
	CodeUpstreamUnavailable     ErrorCode = "UPSTREAM_UNAVAILABLE"
	CodeServiceNotConfigured    ErrorCode = "SERVICE_NOT_CONFIGURED"
)

// Error represents a classified engine failure safe for adapter handling.
type Error struct {
	Code       ErrorCode
	Message    string
	Retryable  bool
	RetryAfter int
	LocalBusy  bool
	Cause      error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Extractor is the narrow interface consumed by platform adapters.
type Extractor interface {
	Enabled() bool
	Extract(ctx context.Context, rawURL string) (*Info, error)
}

// ExtractOptions configures per-invocation extractor arguments for yt-dlp.
type ExtractOptions struct {
	ExtractorArgs []string
}

// OptionsExtractor is an optional extension implemented by Engine for callers
// that pass per-invocation ExtractorArgs.
type OptionsExtractor interface {
	Extractor
	ExtractWithOptions(ctx context.Context, rawURL string, opts ExtractOptions) (*Info, error)
}

// Thumbnail models a single thumbnail entry in yt-dlp -J output.
type Thumbnail struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Width  int64  `json:"width"`
	Height int64  `json:"height"`
}

// Format models the subset of yt-dlp -J format fields needed for quality mapping.
//
// Filesize and FilesizeApprox are strictly separated: FilesizeApprox must never
// be used as an exact byte length for Worker/Range integrity checks.
// HTTPHeaders are retained only in memory and must never be emitted to public JSON.
type Format struct {
	FormatID       string            `json:"format_id"`
	FormatNote     string            `json:"format_note"`
	URL            string            `json:"url"`
	ManifestURL    string            `json:"manifest_url"`
	Ext            string            `json:"ext"`
	VideoExt       string            `json:"video_ext"`
	AudioExt       string            `json:"audio_ext"`
	Protocol       string            `json:"protocol"`
	Width          *int64            `json:"width"`
	Height         *int64            `json:"height"`
	FPS            *float64          `json:"fps"`
	TBR            *float64          `json:"tbr"`
	VBR            *float64          `json:"vbr"`
	ABR            *float64          `json:"abr"`
	ASR            *int64            `json:"asr"`
	AudioChannels  *int64            `json:"audio_channels"`
	DynamicRange   string            `json:"dynamic_range"`
	VCodec         string            `json:"vcodec"`
	ACodec         string            `json:"acodec"`
	Container      string            `json:"container"`
	Resolution     string            `json:"resolution"`
	Language       string            `json:"language"`
	Filesize       *int64            `json:"filesize"`
	FilesizeApprox *int64            `json:"filesize_approx"`
	HTTPHeaders    map[string]string `json:"http_headers"`
}

// Info models the subset of yt-dlp -J top-level and playlist entry fields.
type Info struct {
	ID           string      `json:"id"`
	DisplayID    string      `json:"display_id"`
	Type         string      `json:"_type"`
	Title        string      `json:"title"`
	Fulltitle    string      `json:"fulltitle"`
	Description  string      `json:"description"`
	Uploader     string      `json:"uploader"`
	UploaderID   string      `json:"uploader_id"`
	UploaderURL  string      `json:"uploader_url"`
	Channel      string      `json:"channel"`
	ChannelID    string      `json:"channel_id"`
	Width        *int64      `json:"width"`
	Height       *int64      `json:"height"`
	Timestamp    *float64    `json:"timestamp"`
	Duration     *float64    `json:"duration"`
	ViewCount    *int64      `json:"view_count"`
	LikeCount    *int64      `json:"like_count"`
	RepostCount  *int64      `json:"repost_count"`
	CommentCount *int64      `json:"comment_count"`
	Tags         []string    `json:"tags"`
	Thumbnail    string      `json:"thumbnail"`
	Thumbnails   []Thumbnail `json:"thumbnails"`
	Formats      []Format    `json:"formats"`
	Entries      []Info      `json:"entries"`
	WebpageURL   string      `json:"webpage_url"`
	Extractor    string      `json:"extractor"`
	ExtractorKey string      `json:"extractor_key"`
}
