// Adapted from wader/goutubedl and lrstanley/go-ytdlp (MIT License).
package ytengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// PinnedVersion is the locked yt-dlp release approved for this build.
const PinnedVersion = "2026.10.26"

const (
	defaultMaxConcurrency   = 2
	maxAllowedConcurrency   = 8
	defaultExtractTimeout   = 25 * time.Second
	minExtractTimeout       = 2 * time.Second
	maxExtractTimeout       = 40 * time.Second
	defaultSocketTimeoutSec = 15
	defaultMaxStdoutBytes   = 16 << 20 // 16 MiB
	defaultMaxStderrBytes   = 64 << 10 // 64 KiB
	maxBinaryFileBytes      = 128 << 20
)

// OfficialSHA256Sums20261026 contains the official SHA2-256SUMS for yt-dlp 2026.10.26:
// https://github.com/yt-dlp/yt-dlp/releases/download/2026.10.26/SHA2-256SUMS
var OfficialSHA256Sums20261026 = map[string]string{
	"e51e69e89489f199530f13d32a4342da3ddb2cf34842f364603a066db05afbf3": "yt-dlp",
	"767ba9dce7052aa196f8b12652f433d76de128ba168966fa84b6394d5e6c6795": "yt-dlp_linux",
	"e87e9533e89fe5889abaa21f5114dfe7756d6aa6219b83551add04d48b18e483": "yt-dlp_linux_aarch64",
	"0f922c15ccc0b83eefa1c8bc9104e434318c4bf16e408e1ee93b7745a6efe0aa": "yt-dlp_macos",
	"f5389d3698ecde4ef06f4e6b1ce0294fe9ac1396228c8990b8d3416d6d177a7d": "yt-dlp.exe",
	"0066dbac3c124028e2dcae2a197fccee33cd91f456313d24f7af89fd54590e98": "yt-dlp_arm64.exe",
}

// Config configures a verified yt-dlp metadata extraction engine.
type Config struct {
	BinaryPath       string
	AllowedHashes    map[string]string
	AllowedURL       func(string) bool
	MaxConcurrency   int
	Timeout          time.Duration
	SocketTimeoutSec int
	MaxStdoutBytes   int64
	ProxyURL         string
	EnvPath          string
}

// Engine executes a SHA-256 verified yt-dlp binary in isolated metadata-only mode.
type Engine struct {
	enabled          bool
	disableReason    string
	binaryPath       string
	binarySHA256     string
	binaryAsset      string
	allowedURL       func(string) bool
	sem              chan struct{}
	timeout          time.Duration
	socketTimeoutSec int
	maxStdoutBytes   int64
	proxyURL         string
	envPath          string
}

// NewDisabled returns an Engine that is explicitly disabled with the given reason.
func NewDisabled(reason string) *Engine {
	if strings.TrimSpace(reason) == "" {
		reason = "yt-dlp engine is not configured"
	}
	return &Engine{
		enabled:       false,
		disableReason: reason,
	}
}

// VerifyBinary verifies that binaryPath is a regular executable file whose SHA-256
// matches one of the allowed digests (defaulting to OfficialSHA256Sums20261026).
func VerifyBinary(binaryPath string, allowedHashes map[string]string) (string, string, string, error) {
	trimmed := strings.TrimSpace(binaryPath)
	if trimmed == "" {
		return "", "", "", errors.New("yt-dlp binary path is empty")
	}
	absPath, err := filepath.Abs(trimmed)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve yt-dlp binary path: %w", err)
	}
	stat, err := os.Stat(absPath)
	if err != nil {
		return "", "", "", fmt.Errorf("stat yt-dlp binary: %w", err)
	}
	if !stat.Mode().IsRegular() {
		return "", "", "", errors.New("yt-dlp binary must be a regular file")
	}
	if stat.Size() <= 0 || stat.Size() > maxBinaryFileBytes {
		return "", "", "", fmt.Errorf("yt-dlp binary size %d is out of bounds", stat.Size())
	}
	file, err := os.Open(absPath)
	if err != nil {
		return "", "", "", fmt.Errorf("open yt-dlp binary: %w", err)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, io.LimitReader(file, maxBinaryFileBytes+1)); err != nil {
		return "", "", "", fmt.Errorf("hash yt-dlp binary: %w", err)
	}
	digest := hex.EncodeToString(hasher.Sum(nil))

	hashes := allowedHashes
	if len(hashes) == 0 {
		hashes = OfficialSHA256Sums20261026
	}
	asset, ok := hashes[strings.ToLower(digest)]
	if !ok {
		return "", digest, "", fmt.Errorf("yt-dlp binary sha256 %s does not match pinned release %s", digest, PinnedVersion)
	}
	return absPath, digest, asset, nil
}

// New verifies the configured yt-dlp binary and returns an Engine.
// When verification fails, New returns both a disabled *Engine and the verification
// error so the caller can log a startup warning without crashing the API process.
func New(cfg Config) (*Engine, error) {
	absPath, digest, asset, err := VerifyBinary(cfg.BinaryPath, cfg.AllowedHashes)
	if err != nil {
		return NewDisabled(err.Error()), err
	}
	concurrency := cfg.MaxConcurrency
	if concurrency <= 0 {
		concurrency = defaultMaxConcurrency
	}
	if concurrency > maxAllowedConcurrency {
		return NewDisabled("yt-dlp concurrency exceeds limit"), fmt.Errorf("yt-dlp concurrency %d exceeds max %d", concurrency, maxAllowedConcurrency)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultExtractTimeout
	}
	if timeout < minExtractTimeout || timeout > maxExtractTimeout {
		return NewDisabled("yt-dlp timeout out of bounds"), fmt.Errorf("yt-dlp timeout %s must be between %s and %s", timeout, minExtractTimeout, maxExtractTimeout)
	}
	socketTimeout := cfg.SocketTimeoutSec
	if socketTimeout <= 0 {
		socketTimeout = defaultSocketTimeoutSec
	}
	if socketTimeout > 30 {
		socketTimeout = 30
	}
	maxStdout := cfg.MaxStdoutBytes
	if maxStdout <= 0 {
		maxStdout = defaultMaxStdoutBytes
	}
	proxyURL := strings.TrimSpace(cfg.ProxyURL)
	if proxyURL != "" {
		if err := validateProxyURL(proxyURL); err != nil {
			return NewDisabled(err.Error()), err
		}
	}
	envPath := cfg.EnvPath
	if envPath == "" {
		envPath = os.Getenv("PATH")
	}
	return &Engine{
		enabled:          true,
		binaryPath:       absPath,
		binarySHA256:     digest,
		binaryAsset:      asset,
		allowedURL:       cfg.AllowedURL,
		sem:              make(chan struct{}, concurrency),
		timeout:          timeout,
		socketTimeoutSec: socketTimeout,
		maxStdoutBytes:   maxStdout,
		proxyURL:         proxyURL,
		envPath:          envPath,
	}, nil
}

// NewFromEnv constructs an Engine from DOKITOOL_YTDLP_* environment variables.
func NewFromEnv(getenv func(string) string, allowedURL func(string) bool) (*Engine, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	concurrency := defaultMaxConcurrency
	if raw := strings.TrimSpace(getenv("DOKITOOL_YTDLP_CONCURRENCY")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxAllowedConcurrency {
			err := fmt.Errorf("DOKITOOL_YTDLP_CONCURRENCY must be an integer between 1 and %d", maxAllowedConcurrency)
			return NewDisabled(err.Error()), err
		}
		concurrency = n
	}
	timeout := defaultExtractTimeout
	if raw := strings.TrimSpace(getenv("DOKITOOL_YTDLP_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			if sec, secErr := strconv.Atoi(raw); secErr == nil && sec > 0 {
				parsed = time.Duration(sec) * time.Second
				err = nil
			}
		}
		if err != nil || parsed < minExtractTimeout || parsed > maxExtractTimeout {
			err := fmt.Errorf("DOKITOOL_YTDLP_TIMEOUT must be a duration between %s and %s", minExtractTimeout, maxExtractTimeout)
			return NewDisabled(err.Error()), err
		}
		timeout = parsed
	}
	return New(Config{
		BinaryPath:     getenv("DOKITOOL_YTDLP_BINARY"),
		AllowedHashes:  OfficialSHA256Sums20261026,
		AllowedURL:     allowedURL,
		MaxConcurrency: concurrency,
		Timeout:        timeout,
		ProxyURL:       getenv("DOKITOOL_YTDLP_PROXY"),
		EnvPath:        getenv("PATH"),
	})
}

func (e *Engine) Enabled() bool {
	return e != nil && e.enabled
}

func (e *Engine) DisableReason() string {
	if e == nil {
		return "yt-dlp engine is nil"
	}
	return e.disableReason
}

func (e *Engine) BinarySHA256() string {
	if e == nil {
		return ""
	}
	return e.binarySHA256
}

func (e *Engine) BinaryAsset() string {
	if e == nil {
		return ""
	}
	return e.binaryAsset
}

// CommandArgs returns the hardened CLI argument slice for a single URL extraction.
func (e *Engine) CommandArgs(targetURL string) []string {
	socketSec := defaultSocketTimeoutSec
	if e != nil && e.socketTimeoutSec > 0 {
		socketSec = e.socketTimeoutSec
	}
	args := []string{
		"--ignore-config",
		"--no-plugin-dirs",
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--no-cache-dir",
		"--no-cookies-from-browser",
		"--socket-timeout", strconv.Itoa(socketSec),
	}
	if e != nil && e.proxyURL != "" {
		args = append(args, "--proxy", e.proxyURL)
	}
	args = append(args, "-J", "--", targetURL)
	return args
}

// Extract invokes yt-dlp -J in an isolated temporary directory and parses the subset Info.
func (e *Engine) Extract(ctx context.Context, rawURL string) (*Info, error) {
	if e == nil || !e.enabled {
		return nil, &Error{
			Code:      CodeServiceNotConfigured,
			Message:   "当前平台解析服务暂不可用",
			Retryable: false,
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, classifyContextErr(err)
	}
	if err := validateInputURL(rawURL, e.allowedURL); err != nil {
		return nil, err
	}

	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return nil, classifyContextErr(ctx.Err())
	}

	callCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	tempDir, err := os.MkdirTemp("", "dokitool-ytdlp-*")
	if err != nil {
		return nil, &Error{
			Code:      CodeUpstreamUnavailable,
			Message:   "解析环境初始化失败",
			Retryable: true,
			Cause:     err,
		}
	}
	defer os.RemoveAll(tempDir)

	for _, sub := range []string{"cache", "config", "data", "tmp"} {
		if err := os.MkdirAll(filepath.Join(tempDir, sub), 0o700); err != nil {
			return nil, &Error{
				Code:      CodeUpstreamUnavailable,
				Message:   "解析环境初始化失败",
				Retryable: true,
				Cause:     err,
			}
		}
	}

	args := e.CommandArgs(rawURL)
	cmd := exec.CommandContext(callCtx, e.binaryPath, args...)
	cmd.Dir = tempDir
	cmd.Stdin = nil
	cmd.Env = buildIsolatedEnv(tempDir, e.envPath)
	configureProcessGroup(cmd)

	stdout := &limitedBuffer{max: e.maxStdoutBytes}
	stderr := &limitedBuffer{max: defaultMaxStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	if callCtx.Err() != nil {
		return nil, classifyContextErr(callCtx.Err())
	}
	if stdout.overflow {
		return nil, &Error{
			Code:      CodeUpstreamInvalidResponse,
			Message:   "上游返回的元数据超出大小限制",
			Retryable: false,
		}
	}
	if runErr != nil {
		return nil, classifyStderr(stderr.String(), runErr)
	}

	rawJSON := bytes.TrimSpace(stdout.Bytes())
	if len(rawJSON) == 0 {
		return nil, &Error{
			Code:      CodeUpstreamInvalidResponse,
			Message:   "上游未返回有效的元数据",
			Retryable: false,
		}
	}
	var info Info
	if err := json.Unmarshal(rawJSON, &info); err != nil {
		return nil, &Error{
			Code:      CodeUpstreamInvalidResponse,
			Message:   "上游返回的元数据格式异常",
			Retryable: false,
			Cause:     err,
		}
	}
	if info.ID == "" && len(info.Formats) == 0 && len(info.Entries) == 0 {
		return nil, &Error{
			Code:      CodeContentUnavailable,
			Message:   "未找到可用的作品内容",
			Retryable: false,
		}
	}
	return &info, nil
}

func validateInputURL(raw string, allowed func(string) bool) *Error {
	if raw == "" || len(raw) > 2048 || strings.HasPrefix(raw, "-") {
		return &Error{Code: CodeInvalidInput, Message: "请输入对应平台的有效分享链接"}
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return &Error{Code: CodeInvalidInput, Message: "请输入对应平台的有效分享链接"}
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return &Error{Code: CodeInvalidInput, Message: "请输入对应平台的有效分享链接"}
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Hostname() == "" {
		return &Error{Code: CodeInvalidInput, Message: "请输入对应平台的有效分享链接"}
	}
	if allowed != nil && !allowed(raw) {
		return &Error{Code: CodeInvalidInput, Message: "请输入对应平台的有效分享链接"}
	}
	return nil
}

func validateProxyURL(raw string) error {
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f {
			return errors.New("DOKITOOL_YTDLP_PROXY contains invalid characters")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || u.Fragment != "" {
		return errors.New("DOKITOOL_YTDLP_PROXY must be a valid proxy URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return nil
	default:
		return errors.New("DOKITOOL_YTDLP_PROXY scheme must be http, https, socks5 or socks5h")
	}
}

func buildIsolatedEnv(tempDir, envPath string) []string {
	tmpSub := filepath.Join(tempDir, "tmp")
	env := []string{
		"HOME=" + tempDir,
		"XDG_CACHE_HOME=" + filepath.Join(tempDir, "cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(tempDir, "config"),
		"XDG_DATA_HOME=" + filepath.Join(tempDir, "data"),
		"TMPDIR=" + tmpSub,
		"TEMP=" + tmpSub,
		"TMP=" + tmpSub,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"PYTHONIOENCODING=utf-8",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONNOUSERSITE=1",
		"NO_COLOR=1",
	}
	if envPath != "" {
		env = append(env, "PATH="+envPath)
	}
	if sysRoot := os.Getenv("SYSTEMROOT"); sysRoot != "" {
		env = append(env, "SYSTEMROOT="+sysRoot)
	}
	return env
}

func classifyContextErr(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{
			Code:      CodeUpstreamTimeout,
			Message:   "上游解析超时，请稍后重试",
			Retryable: true,
			Cause:     err,
		}
	}
	return &Error{
		Code:      CodeUpstreamTimeout,
		Message:   "解析请求已取消或超时",
		Retryable: true,
		Cause:     err,
	}
}

func classifyStderr(stderrText string, runErr error) *Error {
	lower := strings.ToLower(stderrText)
	switch {
	case strings.Contains(lower, "429") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "rate limit"):
		return &Error{
			Code:      CodeRateLimited,
			Message:   "上游访问频率受限，请稍后重试",
			Retryable: true,
			Cause:     runErr,
		}
	case strings.Contains(lower, "timed out") ||
		strings.Contains(lower, "timeout"):
		return &Error{
			Code:      CodeUpstreamTimeout,
			Message:   "上游连接超时，请稍后重试",
			Retryable: true,
			Cause:     runErr,
		}
	case strings.Contains(lower, "protected tweet") ||
		strings.Contains(lower, "nsfw tweet") ||
		strings.Contains(lower, "age-restricted") ||
		strings.Contains(lower, "login required") ||
		strings.Contains(lower, "sign in") ||
		strings.Contains(lower, "http error 401") ||
		strings.Contains(lower, "http error 403"):
		return &Error{
			Code:      CodeUpstreamBlocked,
			Message:   "该内容受访问限制（如私密账号或年龄限制），无法无凭据解析",
			Retryable: false,
			Cause:     runErr,
		}
	case strings.Contains(lower, "no video could be found") ||
		strings.Contains(lower, "there's nothing here") ||
		strings.Contains(lower, "status is not available") ||
		strings.Contains(lower, "tweet not found") ||
		strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "http error 404") ||
		strings.Contains(lower, "unavailable"):
		return &Error{
			Code:      CodeContentUnavailable,
			Message:   "未找到可用的视频或该推文已不可访问",
			Retryable: false,
			Cause:     runErr,
		}
	default:
		return &Error{
			Code:      CodeUpstreamUnavailable,
			Message:   "当前平台解析服务暂不可用，请稍后重试",
			Retryable: true,
			Cause:     runErr,
		}
	}
}

type limitedBuffer struct {
	buf      bytes.Buffer
	max      int64
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.overflow {
		return len(p), nil
	}
	remaining := b.max - int64(b.buf.Len())
	if int64(len(p)) > remaining {
		if remaining > 0 {
			_, _ = b.buf.Write(p[:remaining])
		}
		b.overflow = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}
