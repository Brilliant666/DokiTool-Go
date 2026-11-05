// Adapted from wader/goutubedl and lrstanley/go-ytdlp (MIT License).
package ytengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
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

// Resolver resolves outbound hostnames to IP addresses for SSRF and DNS-rebinding checks.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Config configures a verified yt-dlp metadata extraction engine.
type Config struct {
	BinaryPath          string
	AllowedHashes       map[string]string
	AllowedURL          func(string) bool
	AllowedOutboundHost func(string) bool
	Resolver            Resolver
	SharedSemaphore     chan struct{}
	DialAddr            func(netip.Addr, string) string
	MaxConcurrency      int
	Timeout             time.Duration
	SocketTimeoutSec    int
	MaxStdoutBytes      int64
	ProxyURL            string
	EnvPath             string
}

// Engine executes a SHA-256 verified yt-dlp binary in isolated metadata-only mode.
type Engine struct {
	enabled             bool
	disableReason       string
	binaryPath          string
	binarySHA256        string
	binaryAsset         string
	allowedURL          func(string) bool
	allowedOutboundHost func(string) bool
	resolver            Resolver
	dialAddr            func(netip.Addr, string) string
	sem                 chan struct{}
	timeout             time.Duration
	socketTimeoutSec    int
	maxStdoutBytes      int64
	proxyURL            string
	envPath             string
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

func lookupBinaryInPath(envPath string) (string, error) {
	for _, dir := range filepath.SplitList(envPath) {
		trimmedDir := strings.TrimSpace(dir)
		if trimmedDir == "" {
			continue
		}
		for _, candidateName := range []string{"yt-dlp", "yt-dlp.exe"} {
			candidate := filepath.Join(trimmedDir, candidateName)
			stat, err := os.Stat(candidate)
			if err != nil || !stat.Mode().IsRegular() {
				continue
			}
			return candidate, nil
		}
	}
	return "", errors.New("yt-dlp binary not found in DOKITOOL_YTDLP_BINARY or PATH")
}

// New verifies the configured yt-dlp binary and returns an Engine.
// When verification fails, New returns both a disabled *Engine and the verification
// error so the caller can log a startup warning without crashing the API process.
func New(cfg Config) (*Engine, error) {
	envPath := cfg.EnvPath
	if envPath == "" {
		envPath = os.Getenv("PATH")
	}
	binaryPath := strings.TrimSpace(cfg.BinaryPath)
	if binaryPath == "" {
		discovered, err := lookupBinaryInPath(envPath)
		if err != nil {
			return NewDisabled(err.Error()), err
		}
		binaryPath = discovered
	}
	absPath, digest, asset, err := VerifyBinary(binaryPath, cfg.AllowedHashes)
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
		if cfg.AllowedOutboundHost != nil || cfg.Resolver != nil {
			err := errors.New("DOKITOOL_YTDLP_PROXY is not supported when outbound target policy is enabled")
			return NewDisabled(err.Error()), err
		}
	}
	sem := cfg.SharedSemaphore
	if sem == nil {
		sem = make(chan struct{}, concurrency)
	}
	return &Engine{
		enabled:             true,
		binaryPath:          absPath,
		binarySHA256:        digest,
		binaryAsset:         asset,
		allowedURL:          cfg.AllowedURL,
		allowedOutboundHost: cfg.AllowedOutboundHost,
		resolver:            cfg.Resolver,
		dialAddr:            cfg.DialAddr,
		sem:                 sem,
		timeout:             timeout,
		socketTimeoutSec:    socketTimeout,
		maxStdoutBytes:      maxStdout,
		proxyURL:            proxyURL,
		envPath:             envPath,
	}, nil
}

// WithTargetPolicy returns a shallow copy of the Engine bound to the given input URL
// and outbound host policies while sharing the underlying concurrency semaphore.
func (e *Engine) WithTargetPolicy(allowedURL func(string) bool, allowedOutboundHost func(string) bool) *Engine {
	if e == nil {
		return NewDisabled("yt-dlp engine is nil")
	}
	cp := *e
	cp.allowedURL = allowedURL
	cp.allowedOutboundHost = allowedOutboundHost
	if cp.resolver == nil {
		cp.resolver = net.DefaultResolver
	}
	if cp.proxyURL != "" && (cp.allowedOutboundHost != nil || cp.resolver != nil) {
		cp.enabled = false
		cp.disableReason = "DOKITOOL_YTDLP_PROXY is not supported when outbound target policy is enabled"
	}
	return &cp
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
		Resolver:       net.DefaultResolver,
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
	var proxy string
	if e != nil {
		proxy = e.proxyURL
	}
	return e.commandArgsWithProxy(targetURL, proxy)
}

func (e *Engine) commandArgsWithProxy(targetURL, effectiveProxy string) []string {
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
		"--js-runtimes", "deno",
		"--js-runtimes", "node",
		"--socket-timeout", strconv.Itoa(socketSec),
	}
	if effectiveProxy != "" {
		args = append(args, "--proxy", effectiveProxy)
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

	if e.allowedOutboundHost != nil || e.resolver != nil {
		if err := e.verifyPublicTargetHost(callCtx, rawURL); err != nil {
			return nil, err
		}
		if e.proxyURL != "" {
			return nil, &Error{
				Code:      CodeUpstreamBlocked,
				Message:   "受控出站安全策略不允许使用未受控外部代理",
				Retryable: false,
			}
		}
	}

	effectiveProxy := e.proxyURL
	if e.allowedOutboundHost != nil || e.resolver != nil {
		gateURL, closeGate, err := e.startOutboundGate(callCtx)
		if err != nil {
			return nil, &Error{
				Code:      CodeUpstreamUnavailable,
				Message:   "解析安全网关初始化失败",
				Retryable: true,
				Cause:     err,
			}
		}
		defer closeGate()
		effectiveProxy = gateURL
	}

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

	args := e.commandArgsWithProxy(rawURL, effectiveProxy)
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

func (e *Engine) verifyPublicTargetHost(ctx context.Context, rawURL string) *Error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return &Error{Code: CodeInvalidInput, Message: "请输入对应平台的有效分享链接"}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return &Error{Code: CodeInvalidInput, Message: "请输入对应平台的有效分享链接"}
	}
	if e.allowedOutboundHost != nil && !e.allowedOutboundHost(host) {
		return &Error{Code: CodeUpstreamBlocked, Message: "目标地址不在受控出站白名单内", Retryable: false}
	}
	if e.resolver != nil || e.allowedOutboundHost != nil {
		if _, err := e.resolvePublicHostIP(ctx, host); err != nil {
			return &Error{Code: CodeUpstreamBlocked, Message: "目标地址解析到受限网络，已拒绝访问", Retryable: false, Cause: err}
		}
	}
	return nil
}

func (e *Engine) resolvePublicHostIP(ctx context.Context, host string) (netip.Addr, error) {
	cleanHost := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if cleanHost == "" {
		return netip.Addr{}, errors.New("empty host")
	}
	if e.allowedOutboundHost != nil && !e.allowedOutboundHost(cleanHost) {
		return netip.Addr{}, fmt.Errorf("outbound host %s is not allowlisted", cleanHost)
	}
	if ip, err := netip.ParseAddr(cleanHost); err == nil {
		if !fetch.PublicIP(ip) {
			return netip.Addr{}, fmt.Errorf("non-public IP literal %s", ip)
		}
		return ip.Unmap(), nil
	}
	resolver := e.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupNetIP(ctx, "ip", cleanHost)
	if err != nil || len(ips) == 0 {
		return netip.Addr{}, fmt.Errorf("resolve outbound host %s: %w", cleanHost, err)
	}
	for _, ip := range ips {
		if !fetch.PublicIP(ip) {
			return netip.Addr{}, fmt.Errorf("host %s resolved to non-public IP %s", cleanHost, ip)
		}
	}
	return ips[0].Unmap(), nil
}

func (e *Engine) validateOutboundPort(port string) error {
	if e.dialAddr != nil {
		return nil
	}
	if port != "80" && port != "443" {
		return fmt.Errorf("disallowed outbound port %s", port)
	}
	return nil
}

func (e *Engine) dialTargetAddr(ip netip.Addr, port string) string {
	if e.dialAddr != nil {
		if custom := e.dialAddr(ip, port); custom != "" {
			return custom
		}
	}
	return net.JoinHostPort(ip.String(), port)
}

func (e *Engine) startOutboundGate(ctx context.Context) (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	socketTimeout := time.Duration(defaultSocketTimeoutSec) * time.Second
	if e.socketTimeoutSec > 0 {
		socketTimeout = time.Duration(e.socketTimeoutSec) * time.Second
	}

	var (
		mu          sync.Mutex
		nextConnID  uint64
		activeConns = make(map[uint64]func())
		closeOnce   sync.Once
		gateClosed  = make(chan struct{})
	)
	registerTunnel := func(closeTunnel func()) func() {
		mu.Lock()
		select {
		case <-gateClosed:
			mu.Unlock()
			closeTunnel()
			return func() {}
		default:
		}
		if ctx.Err() != nil {
			mu.Unlock()
			closeTunnel()
			return func() {}
		}
		nextConnID++
		id := nextConnID
		activeConns[id] = closeTunnel
		mu.Unlock()
		return func() {
			mu.Lock()
			delete(activeConns, id)
			mu.Unlock()
		}
	}

	srv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				e.handleGateConnect(ctx, w, r, socketTimeout, registerTunnel)
				return
			}
			e.handleGateHTTP(ctx, w, r, socketTimeout)
		}),
	}
	go func() {
		_ = srv.Serve(ln)
	}()

	closeFn := func() {
		closeOnce.Do(func() {
			close(gateClosed)
			_ = ln.Close()
			_ = srv.Close()
			mu.Lock()
			conns := make([]func(), 0, len(activeConns))
			for _, fn := range activeConns {
				conns = append(conns, fn)
			}
			activeConns = make(map[uint64]func())
			mu.Unlock()
			for _, fn := range conns {
				fn()
			}
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			closeFn()
		case <-gateClosed:
		}
	}()
	return "http://" + ln.Addr().String(), closeFn, nil
}

func (e *Engine) handleGateConnect(ctx context.Context, w http.ResponseWriter, r *http.Request, timeout time.Duration, registerTunnel func(func()) func()) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
		port = "443"
	}
	if err := e.validateOutboundPort(port); err != nil {
		http.Error(w, "upstream blocked by outbound security policy: "+err.Error(), http.StatusForbidden)
		return
	}
	ip, err := e.resolvePublicHostIP(ctx, host)
	if err != nil {
		http.Error(w, "upstream blocked by outbound security policy: "+err.Error(), http.StatusForbidden)
		return
	}
	dialer := &net.Dialer{Timeout: timeout}
	upstreamConn, err := dialer.DialContext(ctx, "tcp", e.dialTargetAddr(ip, port))
	if err != nil {
		http.Error(w, "upstream connection failed", http.StatusBadGateway)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstreamConn.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		_ = upstreamConn.Close()
		return
	}

	var closeBothOnce sync.Once
	closeBoth := func() {
		closeBothOnce.Do(func() {
			_ = clientConn.Close()
			_ = upstreamConn.Close()
		})
	}
	if registerTunnel != nil {
		unregister := registerTunnel(closeBoth)
		defer unregister()
	}
	defer closeBoth()

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstreamConn, clientConn)
		closeBoth()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, upstreamConn)
		closeBoth()
	}()
	wg.Wait()
}

func (e *Engine) handleGateHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request, timeout time.Duration) {
	if r.URL == nil || !r.URL.IsAbs() {
		http.Error(w, "upstream blocked by outbound security policy: absolute URL required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodOptions:
	default:
		http.Error(w, "upstream blocked by outbound security policy: method not allowed", http.StatusMethodNotAllowed)
		return
	}

	const (
		maxHops                 = 5
		maxGateRequestBodyBytes = 1 << 20
	)
	var bodyBytes []byte
	if r.Body != nil {
		defer r.Body.Close()
		b, err := io.ReadAll(io.LimitReader(r.Body, maxGateRequestBodyBytes+1))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		if len(b) > maxGateRequestBodyBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		bodyBytes = b
	}

	curURL := r.URL
	method := r.Method
	for hop := 0; hop <= maxHops; hop++ {
		scheme := strings.ToLower(curURL.Scheme)
		if scheme != "http" && scheme != "https" {
			http.Error(w, "upstream blocked by outbound security policy: invalid scheme", http.StatusForbidden)
			return
		}
		host := curURL.Hostname()
		port := curURL.Port()
		if port == "" {
			if scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		if err := e.validateOutboundPort(port); err != nil {
			http.Error(w, "upstream blocked by outbound security policy: "+err.Error(), http.StatusForbidden)
			return
		}
		ip, err := e.resolvePublicHostIP(ctx, host)
		if err != nil {
			http.Error(w, "upstream blocked by outbound security policy: "+err.Error(), http.StatusForbidden)
			return
		}
		targetAddr := e.dialTargetAddr(ip, port)
		transport := &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			DialContext: func(dialCtx context.Context, network, _ string) (net.Conn, error) {
				d := &net.Dialer{Timeout: timeout}
				return d.DialContext(dialCtx, network, targetAddr)
			},
			TLSClientConfig: &tls.Config{
				ServerName:         host,
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: e.dialAddr != nil,
			},
		}
		client := &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		buildOutReq := func(reqURL *url.URL) (*http.Request, error) {
			var bodyReader io.Reader
			if len(bodyBytes) > 0 && method != http.MethodGet && method != http.MethodHead {
				bodyReader = bytes.NewReader(bodyBytes)
			}
			req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), bodyReader)
			if err != nil {
				return nil, err
			}
			if bodyReader != nil {
				req.ContentLength = int64(len(bodyBytes))
			}
			req.Host = curURL.Host
			for k, vals := range r.Header {
				if strings.EqualFold(k, "Proxy-Connection") || strings.EqualFold(k, "Proxy-Authorization") {
					continue
				}
				if bodyReader == nil && (strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Content-Type") || strings.EqualFold(k, "Transfer-Encoding")) {
					continue
				}
				for _, v := range vals {
					req.Header.Add(k, v)
				}
			}
			return req, nil
		}
		outReq, err := buildOutReq(curURL)
		if err != nil {
			http.Error(w, "upstream blocked by outbound security policy", http.StatusBadRequest)
			return
		}
		resp, err := client.Do(outReq)
		if err != nil && scheme == "https" && e.dialAddr != nil {
			plainURL := *curURL
			plainURL.Scheme = "http"
			if fallbackReq, reqErr := buildOutReq(&plainURL); reqErr == nil {
				resp, err = client.Do(fallbackReq)
			}
		}
		if err != nil {
			http.Error(w, "upstream request failed", http.StatusBadGateway)
			return
		}
		if isGateRedirect(resp.StatusCode) {
			loc := strings.TrimSpace(resp.Header.Get("Location"))
			_ = resp.Body.Close()
			if loc == "" {
				http.Error(w, "upstream redirect missing location", http.StatusBadGateway)
				return
			}
			nextURL, err := curURL.Parse(loc)
			if err != nil {
				http.Error(w, "upstream blocked by outbound security policy: bad redirect", http.StatusForbidden)
				return
			}
			curURL = nextURL
			if resp.StatusCode == http.StatusSeeOther ||
				((resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusFound) && method == http.MethodPost) {
				method = http.MethodGet
				bodyBytes = nil
			}
			continue
		}
		defer resp.Body.Close()
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, e.maxStdoutBytes))
		return
	}
	http.Error(w, "upstream blocked by outbound security policy: too many redirects", http.StatusForbidden)
}

func isGateRedirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
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
	default:
		return errors.New("DOKITOOL_YTDLP_PROXY scheme must be http, https, socks5 or socks5h")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		if !ip.IsLoopback() && !fetch.PublicIP(ip) {
			return errors.New("DOKITOOL_YTDLP_PROXY IP literal must be loopback or public")
		}
	}
	return nil
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
		"NO_PROXY=",
		"no_proxy=",
		"HTTP_PROXY=",
		"HTTPS_PROXY=",
		"ALL_PROXY=",
		"http_proxy=",
		"https_proxy=",
		"all_proxy=",
	}
	if envPath != "" {
		env = append(env, "PATH="+envPath)
	} else {
		env = append(env, "PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin")
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
	case strings.Contains(lower, "upstream blocked by outbound security policy"):
		return &Error{
			Code:      CodeUpstreamBlocked,
			Message:   "目标地址或重定向跳点超出安全出站边界，已拒绝访问",
			Retryable: false,
			Cause:     runErr,
		}
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
