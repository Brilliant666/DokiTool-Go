// Package youtube implements the YouTube video & Shorts parser and the
// browser-assisted YouTube context/finalize helper backed by a zero-permission
// deno + yt-dlp/ejs sandbox.
package youtube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// PinnedDenoVersion and PinnedEJSVersion lock the Deno runtime and yt-dlp/ejs
// solver assets corresponding to production yt-dlp 2026.10.26.
const (
	PinnedDenoVersion = "2.9.8"
	PinnedEJSVersion  = "0.8.0"

	OfficialEJSLibSHA256  = "c55987fe697e5b9ee18830163f7af85327e9bb5c3e674b969d38c8d205eaa577"
	OfficialEJSCoreSHA256 = "18da6ce0758b416e7ae645084f4f8801f9f9d59d6c477c05eaa0ff94ebd8cc00"

	DefaultEJSConcurrency    = 4
	maxAllowedEJSConcurrency = 16
	DefaultEJSTimeout        = 5 * time.Second
	minEJSTimeout            = 500 * time.Millisecond
	maxEJSTimeout            = 15 * time.Second

	maxDenoBinaryBytes = 160 << 20 // 160 MiB
	maxEJSFileBytes    = 2 << 20   // 2 MiB
	maxEJSStdoutBytes  = 12 << 20  // 12 MiB (preprocessed base.js is ~3.8 MiB)
	maxEJSStderrBytes  = 64 << 10  // 64 KiB
)

// OfficialDenoSHA256Sums2098 maps the SHA-256 digests of the extracted official
// Deno v2.9.8 binaries to their release asset identifiers.
var OfficialDenoSHA256Sums2098 = map[string]string{
	"368bca08999e02c6911b8f248167b582276b1f547db5712b6e736d7eda1dbda7": "deno-x86_64-unknown-linux-gnu",
	"b484f780e5149ca05c673f1f79f7fb466f12a2295933bdad917af4a8a5c020e8": "deno-aarch64-unknown-linux-gnu",
	"d1e99265e5e6ee096c5780d88ff7d3e6cfdf33b1953a118b4889cab8ca6980ba": "deno-aarch64-apple-darwin",
	"5d206ba69742549cc161db06e86f1d93c5622e1dbc8ba9d15a618e538fbc92f4": "deno-x86_64-apple-darwin",
}

// SandboxConfig configures the zero-permission Deno + yt-dlp/ejs runner.
type SandboxConfig struct {
	DenoBinary        string
	EJSDir            string
	AllowedDenoHashes map[string]string
	ExpectedLibHash   string
	ExpectedCoreHash  string
	MaxConcurrency    int
	Timeout           time.Duration
}

// EJSSandbox executes yt-dlp/ejs inside a zero-permission Deno subprocess.
type EJSSandbox struct {
	denoPath   string
	denoSHA256 string
	denoAsset  string
	libSHA256  string
	coreSHA256 string
	bundleJS   string
	sem        chan struct{}
	timeout    time.Duration
}

// NewEJSSandboxFromEnv constructs and verifies an EJSSandbox from environment variables:
//   - DOKITOOL_DENO_BINARY (required)
//   - DOKITOOL_EJS_DIR (required)
//   - DOKITOOL_EJS_CONCURRENCY (optional, default 4)
//   - DOKITOOL_EJS_TIMEOUT (optional, default 5s)
func NewEJSSandboxFromEnv(getenv func(string) string) (*EJSSandbox, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	concurrency := DefaultEJSConcurrency
	if raw := strings.TrimSpace(getenv("DOKITOOL_EJS_CONCURRENCY")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxAllowedEJSConcurrency {
			return nil, fmt.Errorf("DOKITOOL_EJS_CONCURRENCY must be an integer in [1, %d]", maxAllowedEJSConcurrency)
		}
		concurrency = n
	}
	timeout := DefaultEJSTimeout
	if raw := strings.TrimSpace(getenv("DOKITOOL_EJS_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < minEJSTimeout || d > maxEJSTimeout {
			return nil, fmt.Errorf("DOKITOOL_EJS_TIMEOUT must be between %s and %s", minEJSTimeout, maxEJSTimeout)
		}
		timeout = d
	}
	return NewEJSSandbox(SandboxConfig{
		DenoBinary:     strings.TrimSpace(getenv("DOKITOOL_DENO_BINARY")),
		EJSDir:         strings.TrimSpace(getenv("DOKITOOL_EJS_DIR")),
		MaxConcurrency: concurrency,
		Timeout:        timeout,
	})
}

// NewEJSSandbox verifies the Deno binary and ejs scripts against pinned SHA-256 digests.
func NewEJSSandbox(cfg SandboxConfig) (*EJSSandbox, error) {
	denoPath := strings.TrimSpace(cfg.DenoBinary)
	if denoPath == "" {
		return nil, errors.New("DOKITOOL_DENO_BINARY is not set")
	}
	absDeno, err := filepath.Abs(denoPath)
	if err != nil {
		return nil, fmt.Errorf("invalid DOKITOOL_DENO_BINARY path: %w", err)
	}
	resolvedDeno, err := filepath.EvalSymlinks(absDeno)
	if err != nil {
		return nil, fmt.Errorf("DOKITOOL_DENO_BINARY cannot be resolved: %w", err)
	}
	allowedDeno := cfg.AllowedDenoHashes
	if len(allowedDeno) == 0 {
		allowedDeno = OfficialDenoSHA256Sums2098
	}
	denoSum, err := verifyExecutableFileSHA256(resolvedDeno, maxDenoBinaryBytes)
	if err != nil {
		return nil, fmt.Errorf("DOKITOOL_DENO_BINARY verification failed: %w", err)
	}
	denoAsset, ok := allowedDeno[denoSum]
	if !ok {
		return nil, fmt.Errorf("DOKITOOL_DENO_BINARY sha256 %s does not match pinned deno %s digests", denoSum, PinnedDenoVersion)
	}
	if err := verifyDenoVersion(resolvedDeno, PinnedDenoVersion); err != nil {
		return nil, err
	}

	ejsDir := strings.TrimSpace(cfg.EJSDir)
	if ejsDir == "" {
		return nil, errors.New("DOKITOOL_EJS_DIR is not set")
	}
	absEJSDir, err := filepath.Abs(ejsDir)
	if err != nil {
		return nil, fmt.Errorf("invalid DOKITOOL_EJS_DIR path: %w", err)
	}
	expectedLib := strings.ToLower(strings.TrimSpace(cfg.ExpectedLibHash))
	if expectedLib == "" {
		expectedLib = OfficialEJSLibSHA256
	}
	expectedCore := strings.ToLower(strings.TrimSpace(cfg.ExpectedCoreHash))
	if expectedCore == "" {
		expectedCore = OfficialEJSCoreSHA256
	}

	libCode, libSum, err := loadVerifiedEJSAsset(absEJSDir, []string{"yt.solver.lib.min.js", "lib.min.js"}, expectedLib)
	if err != nil {
		return nil, fmt.Errorf("ejs lib verification failed: %w", err)
	}
	coreCode, coreSum, err := loadVerifiedEJSAsset(absEJSDir, []string{"yt.solver.core.min.js", "core.min.js"}, expectedCore)
	if err != nil {
		return nil, fmt.Errorf("ejs core verification failed: %w", err)
	}

	maxConc := cfg.MaxConcurrency
	if maxConc <= 0 {
		maxConc = DefaultEJSConcurrency
	}
	if maxConc > maxAllowedEJSConcurrency {
		maxConc = maxAllowedEJSConcurrency
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultEJSTimeout
	}

	bundle := libCode + "\nObject.assign(globalThis, lib);\n" + coreCode + "\n"
	return &EJSSandbox{
		denoPath:   resolvedDeno,
		denoSHA256: denoSum,
		denoAsset:  denoAsset,
		libSHA256:  libSum,
		coreSHA256: coreSum,
		bundleJS:   bundle,
		sem:        make(chan struct{}, maxConc),
		timeout:    timeout,
	}, nil
}

// DenoSHA256 returns the verified SHA-256 hex digest of the Deno binary.
func (s *EJSSandbox) DenoSHA256() string { return s.denoSHA256 }

// DenoAsset returns the matched official Deno asset name.
func (s *EJSSandbox) DenoAsset() string { return s.denoAsset }

// LibSHA256 returns the verified SHA-256 hex digest of yt.solver.lib.min.js.
func (s *EJSSandbox) LibSHA256() string { return s.libSHA256 }

// CoreSHA256 returns the verified SHA-256 hex digest of yt.solver.core.min.js.
func (s *EJSSandbox) CoreSHA256() string { return s.coreSHA256 }

func verifyExecutableFileSHA256(path string, maxBytes int64) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("binary path is not a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("binary is not executable")
	}
	if info.Size() < 1 || info.Size() > maxBytes {
		return "", fmt.Errorf("binary size %d out of bounds", info.Size())
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyDenoVersion(denoPath, expectedVersion string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, denoPath, "--version")
	cmd.Env = []string{"NO_COLOR=1", "DENO_NO_UPDATE_CHECK=1", "HOME=/nonexistent"}
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("deno --version failed: %w", err)
	}
	firstLine := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	wantPrefix := "deno " + expectedVersion
	if firstLine != wantPrefix && !strings.HasPrefix(firstLine, wantPrefix+" ") {
		return fmt.Errorf("deno version mismatch: got %q, want %q", firstLine, wantPrefix)
	}
	return nil
}

func loadVerifiedEJSAsset(dir string, names []string, expectedSHA256 string) (string, string, error) {
	var chosenPath string
	for _, name := range names {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			chosenPath = candidate
			break
		}
	}
	if chosenPath == "" {
		return "", "", fmt.Errorf("none of %v found in %s", names, dir)
	}
	info, err := os.Stat(chosenPath)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxEJSFileBytes {
		return "", "", fmt.Errorf("invalid ejs file %s", chosenPath)
	}
	raw, err := os.ReadFile(chosenPath)
	if err != nil {
		return "", "", err
	}
	sumBytes := sha256.Sum256(raw)
	sumHex := hex.EncodeToString(sumBytes[:])
	if !strings.EqualFold(sumHex, expectedSHA256) {
		return "", "", fmt.Errorf("sha256 mismatch for %s: got %s, want %s", filepath.Base(chosenPath), sumHex, expectedSHA256)
	}
	return string(raw), sumHex, nil
}

// RunRawScript executes a JavaScript snippet inside the zero-permission Deno
// sandbox with stdin input, bounded stdout/stderr, global concurrency semaphore,
// --v8-flags=--max-old-space-size=256, and strict per-invocation timeout.
func (s *EJSSandbox) RunRawScript(parent context.Context, script string) ([]byte, error) {
	if s == nil || s.denoPath == "" {
		return nil, errors.New("ejs sandbox is not configured")
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}

	waitCtx, waitCancel := context.WithTimeout(parent, s.timeout)
	defer waitCancel()

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-waitCtx.Done():
		return nil, fmt.Errorf("ejs concurrency limit reached: %w", waitCtx.Err())
	}

	runCtx, runCancel := context.WithTimeout(parent, s.timeout)
	defer runCancel()

	args := []string{
		"run",
		"--ext=js",
		"--no-code-cache",
		"--no-prompt",
		"--no-remote",
		"--no-lock",
		"--node-modules-dir=none",
		"--no-config",
		"--no-npm",
		"--cached-only",
		"--deny-net",
		"--deny-read",
		"--deny-write",
		"--deny-env",
		"--deny-run",
		"--deny-ffi",
		"--deny-sys",
		"--deny-import",
		"--v8-flags=--max-old-space-size=256",
		"-",
	}

	cmd := exec.CommandContext(runCtx, s.denoPath, args...)
	cmd.Env = []string{
		"NO_COLOR=1",
		"DENO_NO_UPDATE_CHECK=1",
		"DENO_NO_PROMPT=1",
		"HOME=/nonexistent",
		"LC_ALL=C.UTF-8",
	}
	cmd.Stdin = strings.NewReader(script)
	cmd.WaitDelay = 500 * time.Millisecond

	var stdout limitedBuffer
	stdout.max = maxEJSStdoutBytes
	var stderr limitedBuffer
	stderr.max = maxEJSStderrBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if stdout.overflow {
		return nil, errors.New("ejs sandbox stdout exceeded limit")
	}
	if err != nil {
		errText := strings.TrimSpace(stderr.buf.String())
		if errText != "" {
			return nil, fmt.Errorf("ejs sandbox execution failed: %w (%s)", err, errText)
		}
		return nil, fmt.Errorf("ejs sandbox execution failed: %w", err)
	}
	return stdout.buf.Bytes(), nil
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
	if int64(b.buf.Len()+len(p)) > b.max {
		remain := b.max - int64(b.buf.Len())
		if remain > 0 {
			_, _ = b.buf.Write(p[:remain])
		}
		b.overflow = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

type ejsRequestItem struct {
	Type       string   `json:"type"`
	Challenges []string `json:"challenges"`
}

type ejsInputPayload struct {
	Type               string           `json:"type"`
	Player             string           `json:"player,omitempty"`
	PreprocessedPlayer string           `json:"preprocessed_player,omitempty"`
	Requests           []ejsRequestItem `json:"requests"`
	OutputPreprocessed bool             `json:"output_preprocessed,omitempty"`
}

type ejsResponseItem struct {
	Type  string            `json:"type"`
	Data  map[string]string `json:"data,omitempty"`
	Error string            `json:"error,omitempty"`
}

type ejsOutputPayload struct {
	Type               string            `json:"type"`
	Responses          []ejsResponseItem `json:"responses"`
	PreprocessedPlayer string            `json:"preprocessed_player,omitempty"`
	Error              string            `json:"error,omitempty"`
}

func (s *EJSSandbox) runJSC(ctx context.Context, input ejsInputPayload) (*ejsOutputPayload, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var script strings.Builder
	script.Grow(len(s.bundleJS) + len(inputJSON) + 128)
	script.WriteString(s.bundleJS)
	script.WriteString("const _in = ")
	script.Write(inputJSON)
	script.WriteString(";\nconst _out = jsc(_in);\nconsole.log(JSON.stringify(_out));\n")

	rawOut, err := s.RunRawScript(ctx, script.String())
	if err != nil {
		return nil, err
	}
	var out ejsOutputPayload
	if err := json.Unmarshal(bytes.TrimSpace(rawOut), &out); err != nil {
		return nil, fmt.Errorf("invalid ejs output json: %w", err)
	}
	if out.Type != "result" {
		return nil, fmt.Errorf("ejs returned error: %s", out.Error)
	}
	return &out, nil
}

// PreprocessPlayer parses raw YouTube base.js in the Deno sandbox and returns
// the preprocessed JavaScript representation for fast subsequent solving.
func (s *EJSSandbox) PreprocessPlayer(ctx context.Context, rawPlayerJS string) (string, error) {
	if strings.TrimSpace(rawPlayerJS) == "" {
		return "", errors.New("empty player js")
	}
	out, err := s.runJSC(ctx, ejsInputPayload{
		Type:               "player",
		Player:             rawPlayerJS,
		Requests:           []ejsRequestItem{},
		OutputPreprocessed: true,
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out.PreprocessedPlayer) == "" {
		return "", errors.New("ejs did not return preprocessed_player")
	}
	return out.PreprocessedPlayer, nil
}

// Solve evaluates n and sig challenges against a cached preprocessed_player.
func (s *EJSSandbox) Solve(ctx context.Context, preprocessedPlayerJS string, nChallenges, sigChallenges []string) (map[string]string, map[string]string, error) {
	if strings.TrimSpace(preprocessedPlayerJS) == "" {
		return nil, nil, errors.New("empty preprocessed player js")
	}
	var reqs []ejsRequestItem
	if len(nChallenges) > 0 {
		reqs = append(reqs, ejsRequestItem{Type: "n", Challenges: nChallenges})
	}
	if len(sigChallenges) > 0 {
		reqs = append(reqs, ejsRequestItem{Type: "sig", Challenges: sigChallenges})
	}
	nSolved := make(map[string]string, len(nChallenges))
	sigSolved := make(map[string]string, len(sigChallenges))
	if len(reqs) == 0 {
		return nSolved, sigSolved, nil
	}

	out, err := s.runJSC(ctx, ejsInputPayload{
		Type:               "preprocessed",
		PreprocessedPlayer: preprocessedPlayerJS,
		Requests:           reqs,
	})
	if err != nil {
		return nil, nil, err
	}
	if len(out.Responses) != len(reqs) {
		return nil, nil, fmt.Errorf("ejs response count mismatch: got %d, want %d", len(out.Responses), len(reqs))
	}
	for i, req := range reqs {
		resp := out.Responses[i]
		if resp.Type != "result" {
			return nil, nil, fmt.Errorf("ejs %s challenge failed: %s", req.Type, resp.Error)
		}
		target := nSolved
		if req.Type == "sig" {
			target = sigSolved
		}
		for _, ch := range req.Challenges {
			val := strings.TrimSpace(resp.Data[ch])
			if val == "" {
				return nil, nil, fmt.Errorf("ejs %s challenge %q returned empty result", req.Type, ch)
			}
			target[ch] = val
		}
	}
	return nSolved, sigSolved, nil
}
