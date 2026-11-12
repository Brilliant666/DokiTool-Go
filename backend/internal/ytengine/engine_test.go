// Adapted from wader/goutubedl and lrstanley/go-ytdlp (MIT License).
package ytengine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func writeFakeYTDLP(t *testing.T, scriptBody string) (string, map[string]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture requires unix")
	}
	dir := t.TempDir()
	binPath := filepath.Join(dir, "yt-dlp-fake")
	content := "#!/bin/sh\nset -eu\n" + scriptBody + "\n"
	if err := os.WriteFile(binPath, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	sum := sha256.Sum256([]byte(content))
	digest := hex.EncodeToString(sum[:])
	return binPath, map[string]string{digest: "yt-dlp-fake"}
}

func TestOfficialSHA256VerificationAndMismatchDegradation(t *testing.T) {
	if len(OfficialSHA256Sums20261026) != 6 {
		t.Fatalf("expected 6 official SHA-256 entries for %s, got %d", PinnedVersion, len(OfficialSHA256Sums20261026))
	}

	// Missing binary -> returns disabled engine and error without panicking.
	eng, err := New(Config{
		BinaryPath: filepath.Join(t.TempDir(), "nonexistent-yt-dlp"),
	})
	if err == nil || eng == nil || eng.Enabled() {
		t.Fatalf("expected disabled engine on missing binary, got enabled=%v err=%v", eng != nil && eng.Enabled(), err)
	}
	_, extractErr := eng.Extract(context.Background(), "https://x.com/i/status/123456")
	var eErr *Error
	if extractErr == nil || !strings.Contains(extractErr.Error(), string(CodeServiceNotConfigured)) {
		t.Fatalf("expected SERVICE_NOT_CONFIGURED from disabled engine, got %v (%v)", extractErr, eErr)
	}

	// Tampered binary whose SHA-256 does not match OfficialSHA256Sums20261026 -> rejected.
	binPath, _ := writeFakeYTDLP(t, `echo '{"id":"1"}'`)
	tamperedEng, err := New(Config{
		BinaryPath:    binPath,
		AllowedHashes: OfficialSHA256Sums20261026,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match pinned release "+PinnedVersion) {
		t.Fatalf("expected SHA-256 mismatch error, got %v", err)
	}
	if tamperedEng == nil || tamperedEng.Enabled() {
		t.Fatalf("expected tampered engine to remain disabled")
	}
}

func TestHardenedExecutionIsolatedEnvAndExactFilesize(t *testing.T) {
	script := `
# Verify secret env vars are never inherited by the subprocess
if [ -n "${SNAP_MEDIA_SIGNING_KEY:-}" ] || [ -n "${SNAP_INGRESS_KEY:-}" ]; then
  echo "secret environment leaked" >&2
  exit 2
fi
# Verify isolated HOME exists and is writable
test -d "$HOME"
test -d "$XDG_CACHE_HOME"
echo "$HOME" > "$HOME/home_check.txt"
cat <<'JSON'
{
  "id": "1790637589910654976",
  "display_id": "1790637656616943991",
  "_type": "video",
  "title": "Test Video",
  "formats": [
    {
      "format_id": "http-2176",
      "url": "https://video.twimg.com/amplify_video/1790637589910654976/vid/avc1/728x720/test.mp4?tag=14",
      "ext": "mp4",
      "protocol": "https",
      "width": 728,
      "height": 720,
      "tbr": 2176.0,
      "filesize": null,
      "filesize_approx": 4212736,
      "http_headers": {
        "Cookie": "secret=1"
      }
    }
  ]
}
JSON
`
	binPath, hashes := writeFakeYTDLP(t, script)
	t.Setenv("SNAP_MEDIA_SIGNING_KEY", "super-secret-signing-key-32-bytes-minimum")
	t.Setenv("SNAP_INGRESS_KEY", "super-secret-ingress-key-32-bytes-minimum")

	eng, err := New(Config{
		BinaryPath:    binPath,
		AllowedHashes: hashes,
		AllowedURL: func(u string) bool {
			return u == "https://x.com/i/status/1790637656616943991"
		},
	})
	if err != nil || !eng.Enabled() {
		t.Fatalf("expected enabled engine, got err=%v", err)
	}

	args := eng.CommandArgs("https://x.com/i/status/1790637656616943991")
	wantFlags := []string{
		"--ignore-config",
		"--no-plugin-dirs",
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--no-cache-dir",
		"--no-cookies-from-browser",
		"-J",
		"--",
	}
	joined := strings.Join(args, " ")
	for _, flag := range wantFlags {
		if !strings.Contains(joined, flag) {
			t.Fatalf("missing hardened CLI flag %q in %v", flag, args)
		}
	}

	info, err := eng.Extract(context.Background(), "https://x.com/i/status/1790637656616943991")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if info.ID != "1790637589910654976" || len(info.Formats) != 1 {
		t.Fatalf("unexpected info: %+v", info)
	}
	f := info.Formats[0]
	if f.Filesize != nil {
		t.Fatalf("expected exact Filesize to remain nil when only filesize_approx is present, got %v", *f.Filesize)
	}
	if f.FilesizeApprox == nil || *f.FilesizeApprox != 4212736 {
		t.Fatalf("expected FilesizeApprox=4212736, got %v", f.FilesizeApprox)
	}

	// Reject disallowed URL or option-injection attempt
	for _, bad := range []string{
		"--exec=id",
		"-o/tmp/pwn",
		"file:///etc/passwd",
		"https://user:pass@x.com/i/status/1790637656616943991",
		"https://x.com/other/status/1790637656616943991",
	} {
		if _, err := eng.Extract(context.Background(), bad); err == nil {
			t.Fatalf("expected URL %q to be rejected", bad)
		}
	}
}

func TestSubprocessCancellationAndStderrClassification(t *testing.T) {
	sleepScript := `
sleep 10
`
	binPath, hashes := writeFakeYTDLP(t, sleepScript)
	eng, err := New(Config{
		BinaryPath:    binPath,
		AllowedHashes: hashes,
		Timeout:       2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New engine failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = eng.Extract(ctx, "https://x.com/i/status/1790637656616943991")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error on slow subprocess")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("process cancellation took too long: %s", elapsed)
	}

	// Verify stderr classification
	cases := []struct {
		stderr        string
		wantCode      ErrorCode
		wantRetryable bool
	}{
		{"ERROR: [twitter] 123: HTTP Error 429: Too Many Requests", CodeRateLimited, true},
		{"ERROR: [twitter] 123: No video could be found in this tweet", CodeContentUnavailable, false},
		{"ERROR: [twitter] 123: Protected tweet; please sign in", CodeUpstreamBlocked, false},
		{"ERROR: [generic] gen-ai-pose-image: Unable to download webpage: <urlopen error Tunnel connection failed: 403 Forbidden>", CodeUpstreamBlocked, false},
		{"ERROR: Unsupported URL: https://github.com/wzc520pyfm/gen-ai-pose-image", CodeContentUnavailable, false},
		{"ERROR: No suitable extractor found for URL https://github.com/wzc520pyfm/gen-ai-pose-image/issues/429?timeout=1", CodeContentUnavailable, false},
	}
	for _, tc := range cases {
		classified := classifyStderr(tc.stderr, errors.New("exit status 1"))
		if classified.Code != tc.wantCode || classified.Retryable != tc.wantRetryable {
			t.Fatalf("classifyStderr(%q) = (code=%s, retryable=%v), want (code=%s, retryable=%v)",
				tc.stderr, classified.Code, classified.Retryable, tc.wantCode, tc.wantRetryable)
		}
		errBin, errHashes := writeFakeYTDLP(t, "echo '"+tc.stderr+"' >&2\nexit 1")
		errEng, err := New(Config{BinaryPath: errBin, AllowedHashes: errHashes})
		if err != nil {
			t.Fatal(err)
		}
		_, extractErr := errEng.Extract(context.Background(), "https://x.com/i/status/123")
		var eErr *Error
		if !errors.As(extractErr, &eErr) || eErr.Code != tc.wantCode || eErr.Retryable != tc.wantRetryable {
			t.Fatalf("stderr %q => %#v (%v), want code=%s retryable=%v", tc.stderr, eErr, extractErr, tc.wantCode, tc.wantRetryable)
		}
	}
}

func TestPathDiscoveryAndSHA256Verification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture requires unix")
	}
	dir := t.TempDir()
	pathBin := filepath.Join(dir, "yt-dlp")
	content := []byte("#!/bin/sh\necho '{\"id\":\"1\"}'\n")
	if err := os.WriteFile(pathBin, content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])

	// 1. Valid SHA-256 discovered via EnvPath when BinaryPath is empty
	eng, err := New(Config{
		BinaryPath:    "",
		EnvPath:       dir,
		AllowedHashes: map[string]string{digest: "yt-dlp"},
	})
	if err != nil || !eng.Enabled() || eng.BinarySHA256() != digest {
		t.Fatalf("expected PATH discovery to succeed with matching SHA-256, got enabled=%v err=%v", eng != nil && eng.Enabled(), err)
	}

	// 2. Missing binary in PATH when BinaryPath is empty
	emptyDir := t.TempDir()
	missEng, err := New(Config{
		BinaryPath:    "",
		EnvPath:       emptyDir,
		AllowedHashes: map[string]string{digest: "yt-dlp"},
	})
	if err == nil || missEng.Enabled() || !strings.Contains(err.Error(), "not found in DOKITOOL_YTDLP_BINARY or PATH") {
		t.Fatalf("expected PATH miss error, got enabled=%v err=%v", missEng != nil && missEng.Enabled(), err)
	}

	// 3. Wrong SHA-256 discovered via PATH
	badEng, err := New(Config{
		BinaryPath:    "",
		EnvPath:       dir,
		AllowedHashes: OfficialSHA256Sums20261026,
	})
	if err == nil || badEng.Enabled() || !strings.Contains(err.Error(), "does not match pinned release") {
		t.Fatalf("expected PATH wrong-hash rejection, got enabled=%v err=%v", badEng != nil && badEng.Enabled(), err)
	}
}

func TestCrossPlatformSharedConcurrencyLimitAndRelease(t *testing.T) {
	coordDir := t.TempDir()
	countFile := filepath.Join(coordDir, "invocations.log")
	releaseFile := filepath.Join(coordDir, "release.flag")

	script := fmt.Sprintf(`
echo "1" >> %q
idx=$(wc -l < %q | tr -d ' ')
touch "%s/started_${idx}"
while [ ! -f %q ]; do
  sleep 0.02
done
echo '{"id":"ok","formats":[{"format_id":"18","url":"https://video.twimg.com/ext_tw_video/1/pu/vid/640x360/a.mp4","ext":"mp4","protocol":"https","width":640,"height":360}]}'
`, countFile, countFile, coordDir, releaseFile)

	binPath, hashes := writeFakeYTDLP(t, script)
	baseEng, err := New(Config{
		BinaryPath:     binPath,
		AllowedHashes:  hashes,
		MaxConcurrency: 2,
		Timeout:        5 * time.Second,
	})
	if err != nil || !baseEng.Enabled() {
		t.Fatalf("New baseEng failed: %v", err)
	}

	pubResolver := stubResolver{ips: map[string][]netip.Addr{
		"x.com":             {netip.MustParseAddr("104.244.42.1")},
		"www.youtube.com":   {netip.MustParseAddr("142.250.190.78")},
		"www.instagram.com": {netip.MustParseAddr("157.240.22.174")},
	}}
	baseEng.resolver = pubResolver

	xEng := baseEng.WithTargetPolicy(
		func(u string) bool { return strings.HasPrefix(u, "https://x.com/") },
		func(h string) bool { return h == "x.com" },
	)
	ytEng := baseEng.WithTargetPolicy(
		func(u string) bool { return strings.HasPrefix(u, "https://www.youtube.com/") },
		func(h string) bool { return h == "www.youtube.com" },
	)
	igEng := baseEng.WithTargetPolicy(
		func(u string) bool { return strings.HasPrefix(u, "https://www.instagram.com/") },
		func(h string) bool { return h == "www.instagram.com" },
	)
	xEng.resolver = pubResolver
	ytEng.resolver = pubResolver
	igEng.resolver = pubResolver

	if cap(xEng.sem) != 2 || xEng.sem != ytEng.sem || ytEng.sem != igEng.sem {
		t.Fatalf("expected all 3 platform engines to share a single capacity-2 semaphore")
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = xEng.Extract(context.Background(), "https://x.com/i/status/1")
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = ytEng.Extract(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err1 := os.Stat(filepath.Join(coordDir, "started_1"))
		_, err2 := os.Stat(filepath.Join(coordDir, "started_2"))
		if err1 == nil && err2 == nil && len(baseEng.sem) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(baseEng.sem) != 2 {
		t.Fatalf("expected both semaphore slots to be occupied, got %d", len(baseEng.sem))
	}

	// 3rd concurrent request while 2 slots are occupied must return immediately with LocalBusy
	// without spawning a 3rd yt-dlp subprocess.
	startBusy := time.Now()
	_, busyErr := igEng.Extract(context.Background(), "https://www.instagram.com/reel/C123456/")
	busyElapsed := time.Since(startBusy)
	if busyElapsed > 200*time.Millisecond {
		t.Fatalf("expected immediate non-blocking busy return, took %s", busyElapsed)
	}
	var engineErr *Error
	if !errors.As(busyErr, &engineErr) || engineErr.Code != CodeRateLimited || !engineErr.Retryable || !engineErr.LocalBusy || engineErr.RetryAfter != 1 || engineErr.Message != "本地解析引擎繁忙，请稍后重试" {
		t.Fatalf("expected LocalBusy RATE_LIMITED error, got %#v (%v)", engineErr, busyErr)
	}

	countBytes, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("read invocation log: %v", err)
	}
	if lines := strings.Split(strings.TrimSpace(string(countBytes)), "\n"); len(lines) != 2 {
		t.Fatalf("expected exactly 2 spawned subprocesses while full, got %d (%q)", len(lines), string(countBytes))
	}

	// Release the 2 running subprocesses.
	if err := os.WriteFile(releaseFile, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("in-flight call %d failed: %v", i, e)
		}
	}
	if len(baseEng.sem) != 0 {
		t.Fatalf("expected semaphore to be empty after completion, got %d", len(baseEng.sem))
	}

	// Subsequent request after slots release must succeed cleanly.
	if _, err := igEng.Extract(context.Background(), "https://www.instagram.com/reel/C123456/"); err != nil {
		t.Fatalf("expected subsequent request after slot release to succeed, got %v", err)
	}
	if len(baseEng.sem) != 0 {
		t.Fatalf("expected semaphore to be empty after subsequent call, got %d", len(baseEng.sem))
	}
}

func TestExtractSlotLifecycleAcrossSuccessFailureTimeoutAndCancel(t *testing.T) {
	coordDir := t.TempDir()
	countFile := filepath.Join(coordDir, "invocations.log")
	startedCancelFile := filepath.Join(coordDir, "started_cancel")

	script := fmt.Sprintf(`
echo "1" >> %q
target=""
for arg in "$@"; do
  target="$arg"
done
case "$target" in
  *fail*)
    echo "ERROR: Video unavailable" >&2
    exit 1
    ;;
  *timeout*)
    sleep 5
    ;;
  *cancel*)
    touch %q
    sleep 5
    ;;
  *)
    echo '{"id":"ok","formats":[{"format_id":"18","url":"https://video.twimg.com/ext_tw_video/1/pu/vid/640x360/a.mp4","ext":"mp4","protocol":"https","width":640,"height":360}]}'
    ;;
esac
`, countFile, startedCancelFile)

	binPath, hashes := writeFakeYTDLP(t, script)
	eng, err := New(Config{
		BinaryPath:     binPath,
		AllowedHashes:  hashes,
		MaxConcurrency: 1,
		Timeout:        minExtractTimeout,
		AllowedURL:     func(u string) bool { return strings.HasPrefix(u, "https://x.com/") },
		AllowedOutboundHost: func(h string) bool {
			return h == "x.com"
		},
		Resolver: stubResolver{ips: map[string][]netip.Addr{
			"x.com": {netip.MustParseAddr("104.244.42.1")},
		}},
	})
	if err != nil || !eng.Enabled() {
		t.Fatalf("New eng failed: %v", err)
	}

	// 1. Pre-canceled context does not start execution or acquire/leak slot.
	preCanceledCtx, preCancel := context.WithCancel(context.Background())
	preCancel()
	if _, err := eng.Extract(preCanceledCtx, "https://x.com/i/status/1"); err == nil {
		t.Fatal("expected pre-canceled Extract to fail")
	}
	if _, statErr := os.Stat(countFile); !os.IsNotExist(statErr) {
		t.Fatalf("expected pre-canceled Extract not to spawn subprocess, statErr=%v", statErr)
	}
	if len(eng.sem) != 0 {
		t.Fatalf("expected 0 occupied slots after pre-canceled call, got %d", len(eng.sem))
	}

	// 2. Success releases slot once.
	if _, err := eng.Extract(context.Background(), "https://x.com/i/status/1"); err != nil {
		t.Fatalf("expected success path to pass, got %v", err)
	}
	if len(eng.sem) != 0 {
		t.Fatalf("expected 0 occupied slots after success, got %d", len(eng.sem))
	}

	// 3. Subprocess failure releases slot once.
	if _, err := eng.Extract(context.Background(), "https://x.com/i/status/fail"); err == nil {
		t.Fatal("expected failure path to return error")
	}
	if len(eng.sem) != 0 {
		t.Fatalf("expected 0 occupied slots after failure, got %d", len(eng.sem))
	}

	// 4. In-flight context cancellation releases slot once.
	cancelCtx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(startedCancelFile); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	if _, err := eng.Extract(cancelCtx, "https://x.com/i/status/cancel"); err == nil {
		t.Fatal("expected canceled in-flight path to return error")
	}
	if len(eng.sem) != 0 {
		t.Fatalf("expected 0 occupied slots after in-flight cancel, got %d", len(eng.sem))
	}

	// 5. Timeout releases slot once.
	if _, err := eng.Extract(context.Background(), "https://x.com/i/status/timeout"); err == nil {
		t.Fatal("expected timeout path to return error")
	}
	if len(eng.sem) != 0 {
		t.Fatalf("expected 0 occupied slots after timeout, got %d", len(eng.sem))
	}

	// 6. Subsequent normal request still acquires the single slot and succeeds.
	if _, err := eng.Extract(context.Background(), "https://x.com/i/status/2"); err != nil {
		t.Fatalf("expected subsequent request after all 4 paths to succeed, got %v", err)
	}
	if len(eng.sem) != 0 {
		t.Fatalf("expected 0 occupied slots at end, got %d", len(eng.sem))
	}
}

type stubResolver struct {
	ips map[string][]netip.Addr
}

func (s stubResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if addrs, ok := s.ips[h]; ok {
		return addrs, nil
	}
	return nil, errors.New("host not found: " + h)
}

func TestOutboundSecurityGateAndDNSProtection(t *testing.T) {
	binPath, hashes := writeFakeYTDLP(t, `echo '{"id":"should-not-run"}'`)
	resolver := stubResolver{
		ips: map[string][]netip.Addr{
			"loopback.x.com":  {netip.MustParseAddr("127.0.0.1")},
			"private.x.com":   {netip.MustParseAddr("10.0.0.1")},
			"metadata.x.com":  {netip.MustParseAddr("169.254.169.254")},
			"ipv6loop.x.com":  {netip.MustParseAddr("::1")},
			"public.x.com":    {netip.MustParseAddr("104.244.42.1")},
			"evil.example.io": {netip.MustParseAddr("93.184.216.34")},
		},
	}

	eng, err := New(Config{
		BinaryPath:    binPath,
		AllowedHashes: hashes,
		AllowedURL:    func(string) bool { return true },
		AllowedOutboundHost: func(host string) bool {
			return host == "public.x.com" || strings.HasSuffix(host, ".x.com")
		},
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("New engine failed: %v", err)
	}

	// 1. Pre-spawn DNS check blocks private/loopback/metadata/IPv6-loopback resolution
	for _, blockedURL := range []string{
		"https://loopback.x.com/i/status/1",
		"https://private.x.com/i/status/1",
		"https://metadata.x.com/i/status/1",
		"https://ipv6loop.x.com/i/status/1",
	} {
		_, err := eng.Extract(context.Background(), blockedURL)
		var eErr *Error
		if !errors.As(err, &eErr) || eErr.Code != CodeUpstreamBlocked {
			t.Fatalf("expected CodeUpstreamBlocked for %s, got %v", blockedURL, err)
		}
	}

	// 2. Test in-process outbound proxy gate directly for host allowlist, port restriction, and 302 redirect to private IP
	redirectTarget := "http://169.254.169.254/latest/meta-data/"
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect-private" {
			http.Redirect(w, r, redirectTarget, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstreamSrv.Close()

	srvURL, _ := url.Parse(upstreamSrv.URL)
	eng.dialAddr = func(_ netip.Addr, _ string) string {
		return srvURL.Host
	}

	gateURL, closeGate, err := eng.startOutboundGate(context.Background())
	if err != nil {
		t.Fatalf("startOutboundGate failed: %v", err)
	}
	defer closeGate()

	parsedGate, _ := url.Parse(gateURL)
	gateClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(parsedGate),
		},
		Timeout: 3 * time.Second,
	}

	// 2a. Allowed public host succeeds through gate
	resp, err := gateClient.Get("http://public.x.com/ok")
	if err != nil {
		t.Fatalf("expected allowed public host to succeed via gate, got %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for public.x.com, got %d", resp.StatusCode)
	}

	// 2b. Disallowed host is blocked by gate with 403
	resp, err = gateClient.Get("http://evil.example.io/ok")
	if err != nil {
		t.Fatalf("gate request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for non-allowlisted host, got %d", resp.StatusCode)
	}

	// 2c. Allowlisted host resolving to private IP is blocked by gate with 403
	resp, err = gateClient.Get("http://private.x.com/ok")
	if err != nil {
		t.Fatalf("gate request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for private IP resolution, got %d", resp.StatusCode)
	}

	// 2d. 302 redirect from public host to private/metadata IP is blocked by gate with 403
	resp, err = gateClient.Get("http://public.x.com/redirect-private")
	if err != nil {
		t.Fatalf("gate redirect request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden on redirect to 169.254.169.254, got %d", resp.StatusCode)
	}

	// 3. Verify validateProxyURL rejects private/metadata IP literals
	for _, badProxy := range []string{
		"http://10.0.0.1:8080",
		"http://169.254.169.254:80",
		"socks5://192.168.1.1:1080",
	} {
		if err := validateProxyURL(badProxy); err == nil {
			t.Fatalf("expected validateProxyURL(%q) to reject private/link-local IP literal", badProxy)
		}
	}
	if err := validateProxyURL("http://127.0.0.1:7897"); err != nil {
		t.Fatalf("expected loopback proxy URL to be allowed, got %v", err)
	}
}

type fakeProbeExecutor struct {
	fn func(context.Context, fetch.Request) (fetch.Response, error)
}

func (f fakeProbeExecutor) Do(ctx context.Context, req fetch.Request) (fetch.Response, error) {
	return f.fn(ctx, req)
}

func TestSizeProberRejectsErrorPagesAndInvalidContentRange(t *testing.T) {
	allowed := func(raw string) bool {
		return strings.HasPrefix(raw, "https://rr1---sn-test.googlevideo.com/")
	}
	mediaURL := "https://rr1---sn-test.googlevideo.com/videoplayback?itag=18"

	cases := []struct {
		name     string
		status   int
		headers  http.Header
		body     []byte
		wantSize int64
		wantOK   bool
	}{
		{
			name:   "200-text-html-error-page-rejected",
			status: http.StatusOK,
			headers: http.Header{
				"Content-Type":   {"text/html; charset=utf-8"},
				"Content-Length": {"7"},
			},
			body:     []byte("<html/>"),
			wantSize: 0,
			wantOK:   false,
		},
		{
			name:   "200-application-json-rejected",
			status: http.StatusOK,
			headers: http.Header{
				"Content-Type":   {"application/json"},
				"Content-Length": {"15"},
			},
			body:     []byte(`{"error":"bad"}`),
			wantSize: 0,
			wantOK:   false,
		},
		{
			name:   "206-invalid-content-range-prefix-rejected",
			status: http.StatusPartialContent,
			headers: http.Header{
				"Content-Type":  {"video/mp4"},
				"Content-Range": {"not-a-range/12345"},
			},
			body:     []byte{0x00},
			wantSize: 0,
			wantOK:   false,
		},
		{
			name:   "206-unsatisfied-wildcard-range-rejected",
			status: http.StatusPartialContent,
			headers: http.Header{
				"Content-Type":  {"video/mp4"},
				"Content-Range": {"bytes */12345"},
			},
			body:     []byte{0x00},
			wantSize: 0,
			wantOK:   false,
		},
		{
			name:   "206-wrong-start-offset-rejected",
			status: http.StatusPartialContent,
			headers: http.Header{
				"Content-Type":  {"video/mp4"},
				"Content-Range": {"bytes 1-1/12345"},
			},
			body:     []byte{0x00},
			wantSize: 0,
			wantOK:   false,
		},
		{
			name:   "206-duplicate-content-range-rejected",
			status: http.StatusPartialContent,
			headers: http.Header{
				"Content-Type":  {"video/mp4"},
				"Content-Range": {"bytes 0-0/12345", "bytes 0-0/99999"},
			},
			body:     []byte{0x00},
			wantSize: 0,
			wantOK:   false,
		},
		{
			name:   "206-gzip-content-encoding-rejected",
			status: http.StatusPartialContent,
			headers: http.Header{
				"Content-Type":     {"video/mp4"},
				"Content-Encoding": {"gzip"},
				"Content-Range":    {"bytes 0-0/12345"},
			},
			body:     []byte{0x00},
			wantSize: 0,
			wantOK:   false,
		},
		{
			name:   "206-valid-mp4-range-accepted",
			status: http.StatusPartialContent,
			headers: http.Header{
				"Content-Type":  {"video/mp4"},
				"Content-Range": {"bytes 0-0/28523658"},
			},
			body:     []byte{0x00},
			wantSize: 28523658,
			wantOK:   true,
		},
		{
			name:   "200-full-small-mp4-with-ftyp-magic-accepted",
			status: http.StatusOK,
			headers: http.Header{
				"Content-Type":   {"video/mp4"},
				"Content-Length": {"16"},
			},
			body:     []byte("\x00\x00\x00\x10ftypisom\x00\x00\x02\x00"),
			wantSize: 16,
			wantOK:   true,
		},
		{
			name:   "200-truncated-or-non-media-body-rejected",
			status: http.StatusOK,
			headers: http.Header{
				"Content-Type":   {"video/mp4"},
				"Content-Length": {"12345"},
			},
			body:     []byte("\x00\x00\x00\x10ftypisom"),
			wantSize: 0,
			wantOK:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prober := NewSizeProber(fakeProbeExecutor{
				fn: func(_ context.Context, req fetch.Request) (fetch.Response, error) {
					if req.Header.Get("Range") != "bytes=0-0" || req.Header.Get("Accept-Encoding") != "identity" || req.Policy.FollowRedirects {
						t.Fatalf("unexpected probe request policy/headers: %+v", req)
					}
					return fetch.Response{
						StatusCode: tc.status,
						Header:     tc.headers,
						Body:       tc.body,
						URL:        req.URL,
					}, nil
				},
			}, allowed)
			gotSize, gotOK := prober(context.Background(), mediaURL)
			if gotOK != tc.wantOK || gotSize != tc.wantSize {
				t.Fatalf("prober() = (%d, %v), want (%d, %v)", gotSize, gotOK, tc.wantSize, tc.wantOK)
			}
		})
	}
}

func TestPR36ReviewCounterexamples(t *testing.T) {
	const now int64 = 1762300800

	// 1. Same representation renewal keeps stable source_id on googlevideo.com
	w, h, sz := int64(640), int64(360), int64(28523658)
	opts := AdaptOptions{
		PlatformID: "youtube",
		ContentID:  "aqz-KE-bpKQ",
		SourceURL:  "https://www.youtube.com/watch?v=aqz-KE-bpKQ",
		MaxEntries: 1,
	}
	info1 := &Info{
		ID:    "aqz-KE-bpKQ",
		Title: "Big Buck Bunny",
		Formats: []Format{
			{
				FormatID: "18",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&ei=E1&ip=1.1.1.1&id=o-1&itag=18&source=youtube&clen=28523658&dur=634.566&lmt=1700000000000000&sig=SIG_A",
				Ext:      "mp4",
				Protocol: "https",
				Width:    &w,
				Height:   &h,
				VCodec:   "avc1.42001E",
				ACodec:   "mp4a.40.2",
				Filesize: &sz,
			},
		},
	}
	info2 := &Info{
		ID:    "aqz-KE-bpKQ",
		Title: "Big Buck Bunny",
		Formats: []Format{
			{
				FormatID: "18",
				URL:      "https://rr4---sn-ogueln67.googlevideo.com/videoplayback?expire=1762360000&ei=E2&ip=2.2.2.2&id=o-1&itag=18&source=youtube&clen=28523658&dur=634.566&lmt=1700000000000000&sig=SIG_B",
				Ext:      "mp4",
				Protocol: "https",
				Width:    &w,
				Height:   &h,
				VCodec:   "avc1.42001E",
				ACodec:   "mp4a.40.2",
				Filesize: &sz,
			},
		},
	}
	env1 := AdaptInfo(info1, opts, now)
	env2 := AdaptInfo(info2, opts, now+60)
	def1 := unified.Map(unified.Map(unified.Slice(unified.Map(env1["data"])["items"])[0])["default"])
	def2 := unified.Map(unified.Map(unified.Slice(unified.Map(env2["data"])["items"])[0])["default"])
	if unified.Str(def1["source_id"]) != unified.Str(def2["source_id"]) {
		t.Fatalf("R3 counterexample failed: source_id drifted from %v to %v", def1["source_id"], def2["source_id"])
	}

	// 2 & 3. HTML 200 and invalid 206 Content-Range rejected by ValidateProbedMediaResponse
	if sz, ok := ValidateProbedMediaResponse(http.StatusOK, http.Header{
		"Content-Type":   {"text/html"},
		"Content-Length": {"7"},
	}, []byte("<html/>")); ok || sz != 0 {
		t.Fatalf("R4 counterexample 1 failed: accepted 200 text/html as (%d, %v)", sz, ok)
	}
	if sz, ok := ValidateProbedMediaResponse(http.StatusPartialContent, http.Header{
		"Content-Type":  {"video/mp4"},
		"Content-Range": {"not-a-range/12345"},
	}, []byte{0x00}); ok || sz != 0 {
		t.Fatalf("R4 counterexample 2 failed: accepted invalid 206 Content-Range as (%d, %v)", sz, ok)
	}

	// 4. Multi-entry SelectedIndex preserves original entry position when entry #1 has no formats
	multiOpts := AdaptOptions{
		PlatformID:    "x",
		ContentID:     "1577719286659006464",
		SourceURL:     "https://x.com/u/status/1577719286659006464/video/2",
		SelectedIndex: 2,
		MaxEntries:    4,
	}
	multiInfo := &Info{
		ID:   "1577719286659006464",
		Type: "playlist",
		Entries: []Info{
			{ID: "entry1-empty", Formats: nil},
			{
				ID:    "entry2-valid",
				Title: "Entry Two",
				Formats: []Format{
					{
						FormatID: "http-720",
						URL:      "https://video.twimg.com/ext_tw_video/2/pu/vid/1280x720/v2.mp4",
						Ext:      "mp4",
						Protocol: "https",
						Width:    &w,
						Height:   &h,
						VCodec:   "avc1.640020",
						ACodec:   "mp4a.40.2",
					},
				},
			},
			{
				ID:    "entry3-valid",
				Title: "Entry Three",
				Formats: []Format{
					{
						FormatID: "http-720",
						URL:      "https://video.twimg.com/ext_tw_video/3/pu/vid/1280x720/v3.mp4",
						Ext:      "mp4",
						Protocol: "https",
						Width:    &w,
						Height:   &h,
						VCodec:   "avc1.640020",
						ACodec:   "mp4a.40.2",
					},
				},
			},
		},
	}
	multiEnv := AdaptInfo(multiInfo, multiOpts, now)
	multiData := unified.Map(multiEnv["data"])
	multiItems := unified.Slice(multiData["items"])
	firstSurviving := unified.Map(multiItems[0])
	if unified.Str(multiData["default_item_id"]) != unified.Str(firstSurviving["id"]) || !unified.Equal(firstSurviving["index"], 2) {
		t.Fatalf("R1 counterexample failed: expected default_item_id=%v (index 2), got default_item_id=%v (index=%v)", firstSurviving["id"], multiData["default_item_id"], firstSurviving["index"])
	}
}

func TestPR36Review5785629879Counterexamples(t *testing.T) {
	const now int64 = 1762300800

	// Counterexample 1 (R2-A): External proxy must not bypass controlled outbound boundary
	binPath, hashes := writeFakeYTDLP(t, `echo '{"id":"1","formats":[{"format_id":"18","url":"https://video.twimg.com/v.mp4","ext":"mp4"}]}'`)
	privateResolver := stubResolver{
		ips: map[string][]netip.Addr{
			"x.com":         {netip.MustParseAddr("10.0.0.1")},
			"public.x.com":  {netip.MustParseAddr("93.184.216.34")},
			"private.x.com": {netip.MustParseAddr("10.0.0.1")},
		},
	}
	// New with ProxyURL + Resolver/AllowedOutboundHost must fail closed
	if engWithProxy, err := New(Config{
		BinaryPath:          binPath,
		AllowedHashes:       hashes,
		AllowedOutboundHost: func(h string) bool { return h == "x.com" || strings.HasSuffix(h, ".x.com") },
		Resolver:            privateResolver,
		ProxyURL:            "http://127.0.0.1:18080",
	}); err == nil || engWithProxy.Enabled() {
		t.Fatalf("R2-A failed: expected New with ProxyURL and outbound policy to fail closed, got enabled=%v err=%v", engWithProxy.Enabled(), err)
	}
	// Direct verifyPublicTargetHost & Extract with ProxyURL set on Engine struct must still enforce Resolver and block private IP
	directProxyEng := &Engine{
		enabled:             true,
		binaryPath:          binPath,
		allowedOutboundHost: func(h string) bool { return h == "x.com" || strings.HasSuffix(h, ".x.com") },
		resolver:            privateResolver,
		proxyURL:            "http://127.0.0.1:18080",
		sem:                 make(chan struct{}, 1),
		timeout:             5 * time.Second,
	}
	if err := directProxyEng.verifyPublicTargetHost(context.Background(), "https://x.com/i/status/1"); err == nil || err.Code != CodeUpstreamBlocked {
		t.Fatalf("R2-A failed: verifyPublicTargetHost with ProxyURL bypassed private IP check: %v", err)
	}
	if _, err := directProxyEng.Extract(context.Background(), "https://x.com/i/status/1"); err == nil {
		t.Fatalf("R2-A failed: Extract with ProxyURL and private target IP succeeded unexpectedly")
	}
	// NewHTTPSizeProber with external proxy must fail closed without dialing
	proberWithProxy := NewHTTPSizeProber("http://127.0.0.1:18080", func(string) bool { return true })
	if sz, ok := proberWithProxy(context.Background(), "https://video.twimg.com/ext_tw_video/1/pu/vid/720x720/v.mp4"); ok || sz != 0 {
		t.Fatalf("R2-A failed: expected NewHTTPSizeProber with external proxy to fail closed, got (%d, %v)", sz, ok)
	}

	// Counterexamples 2, 3, 4 (R2-B): Gate POST body preservation, HTTP->HTTPS redirect, and CONNECT cancellation cleanup
	var gotPostBody string
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/post":
			b, _ := io.ReadAll(r.Body)
			gotPostBody = string(b)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok:" + gotPostBody))
		case "/step1":
			http.Redirect(w, r, "https://public.x.com/step2", http.StatusFound)
		case "/step2":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("https-step2-ok"))
		case "/step1-private":
			http.Redirect(w, r, "https://private.x.com/step2", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstreamSrv.Close()

	srvURL, _ := url.Parse(upstreamSrv.URL)
	gateEng, err := New(Config{
		BinaryPath:          binPath,
		AllowedHashes:       hashes,
		AllowedOutboundHost: func(h string) bool { return h == "x.com" || strings.HasSuffix(h, ".x.com") },
		Resolver:            privateResolver,
		DialAddr: func(_ netip.Addr, _ string) string {
			return srvURL.Host
		},
	})
	if err != nil {
		t.Fatalf("New gateEng failed: %v", err)
	}

	gateCtx, gateCancel := context.WithCancel(context.Background())
	defer gateCancel()
	gateURL, closeGate, err := gateEng.startOutboundGate(gateCtx)
	if err != nil {
		t.Fatalf("startOutboundGate failed: %v", err)
	}
	defer closeGate()

	parsedGate, _ := url.Parse(gateURL)
	gateClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(parsedGate),
		},
		Timeout: 3 * time.Second,
	}

	// Counterexample 2: POST body "hello=1" preserved through gate
	postResp, err := gateClient.Post("http://public.x.com/post", "application/x-www-form-urlencoded", strings.NewReader("hello=1"))
	if err != nil {
		t.Fatalf("R2-B POST failed: %v", err)
	}
	_ = postResp.Body.Close()
	if postResp.StatusCode != http.StatusOK || gotPostBody != "hello=1" {
		t.Fatalf("R2-B POST body counterexample failed: status=%d gotPostBody=%q, want 'hello=1'", postResp.StatusCode, gotPostBody)
	}

	// Counterexample 3: http://public.x.com/step1 -> 302 https://public.x.com/step2 succeeds, while -> https://private.x.com/step2 is 403
	redirResp, err := gateClient.Get("http://public.x.com/step1")
	if err != nil {
		t.Fatalf("R2-B HTTP->HTTPS redirect request failed: %v", err)
	}
	redirBody, _ := io.ReadAll(redirResp.Body)
	_ = redirResp.Body.Close()
	if redirResp.StatusCode != http.StatusOK || string(redirBody) != "https-step2-ok" {
		t.Fatalf("R2-B HTTP->HTTPS public redirect failed: status=%d body=%q", redirResp.StatusCode, string(redirBody))
	}
	privRedirResp, err := gateClient.Get("http://public.x.com/step1-private")
	if err != nil {
		t.Fatalf("R2-B HTTP->HTTPS private redirect request failed: %v", err)
	}
	_ = privRedirResp.Body.Close()
	if privRedirResp.StatusCode != http.StatusForbidden {
		t.Fatalf("R2-B HTTP->HTTPS private redirect expected 403 Forbidden, got %d", privRedirResp.StatusCode)
	}

	// Counterexample 4: CONNECT tunnel closes within <500ms when cancel() + closeGate() is invoked
	holdLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen holdLn failed: %v", err)
	}
	defer holdLn.Close()
	holdDone := make(chan struct{})
	defer close(holdDone)
	go func() {
		for {
			c, err := holdLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				<-holdDone
			}(c)
		}
	}()

	connEng := *gateEng
	connEng.dialAddr = func(_ netip.Addr, _ string) string {
		return holdLn.Addr().String()
	}
	tunCtx, tunCancel := context.WithCancel(context.Background())
	tunGateURL, closeTunGate, err := connEng.startOutboundGate(tunCtx)
	if err != nil {
		t.Fatalf("startOutboundGate for CONNECT test failed: %v", err)
	}
	tunParsed, _ := url.Parse(tunGateURL)
	tunConn, err := net.DialTimeout("tcp", tunParsed.Host, 2*time.Second)
	if err != nil {
		t.Fatalf("dial tunGate failed: %v", err)
	}
	defer tunConn.Close()
	if _, err := tunConn.Write([]byte("CONNECT public.x.com:443 HTTP/1.1\r\nHost: public.x.com:443\r\nProxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(tunParsed.User.String())) + "\r\n\r\n")); err != nil {
		t.Fatalf("write CONNECT failed: %v", err)
	}
	replyBuf := make([]byte, 128)
	n, err := tunConn.Read(replyBuf)
	if err != nil || !strings.Contains(string(replyBuf[:n]), "200 Connection Established") {
		t.Fatalf("expected 200 Connection Established, got %q (err=%v)", string(replyBuf[:n]), err)
	}
	startCancel := time.Now()
	tunCancel()
	closeTunGate()
	_ = tunConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	eofBuf := make([]byte, 16)
	_, readErr := tunConn.Read(eofBuf)
	elapsed := time.Since(startCancel)
	if readErr == nil {
		t.Fatalf("R2-B CONNECT cleanup failed: expected tunnel read to return error/EOF after cancel+closeGate")
	}
	if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("R2-B CONNECT cleanup failed: tunnel remained open until 500ms deadline (%v)", elapsed)
	}

	// Counterexample 5 (R3): Optional size probe success/failure does not drift source_id, while conflicting size is rejected
	w360, h360, sz360 := int64(640), int64(360), int64(28523658)
	ytOpts := AdaptOptions{
		PlatformID:    "youtube",
		ContentID:     "aqz-KE-bpKQ",
		SourceURL:     "https://www.youtube.com/watch?v=aqz-KE-bpKQ",
		MaxEntries:    1,
		EnableDASHMux: true,
	}
	ytURL := "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=18&source=youtube&clen=28523658&dur=634.566&lmt=1700000000000000&sig=SIG_A"
	envKnownSize := AdaptInfo(&Info{
		ID: "aqz-KE-bpKQ",
		Formats: []Format{{
			FormatID: "18",
			URL:      ytURL,
			Ext:      "mp4",
			Protocol: "https",
			Width:    &w360,
			Height:   &h360,
			VCodec:   "avc1.42001E",
			ACodec:   "mp4a.40.2",
			Filesize: &sz360,
		}},
	}, ytOpts, now)
	envNilSize := AdaptInfo(&Info{
		ID: "aqz-KE-bpKQ",
		Formats: []Format{{
			FormatID: "18",
			URL:      ytURL,
			Ext:      "mp4",
			Protocol: "https",
			Width:    &w360,
			Height:   &h360,
			VCodec:   "avc1.42001E",
			ACodec:   "mp4a.40.2",
			Filesize: nil,
		}},
	}, ytOpts, now)
	defKnown := unified.Map(unified.Map(unified.Slice(unified.Map(envKnownSize["data"])["items"])[0])["default"])
	defNil := unified.Map(unified.Map(unified.Slice(unified.Map(envNilSize["data"])["items"])[0])["default"])
	if unified.Str(defKnown["source_id"]) != unified.Str(defNil["source_id"]) {
		t.Fatalf("R3 counterexample failed: source_id drifted when Filesize toggled nil: %v vs %v", defKnown["source_id"], defNil["source_id"])
	}
	badSz := int64(99999)
	envConflictSize := AdaptInfo(&Info{
		ID: "aqz-KE-bpKQ",
		Formats: []Format{{
			FormatID: "18",
			URL:      ytURL,
			Ext:      "mp4",
			Protocol: "https",
			Width:    &w360,
			Height:   &h360,
			VCodec:   "avc1.42001E",
			ACodec:   "mp4a.40.2",
			Filesize: &badSz,
		}},
	}, ytOpts, now)
	defConflict := unified.Map(unified.Map(unified.Slice(unified.Map(envConflictSize["data"])["items"])[0])["default"])
	if _, hasSize := defConflict["size_bytes"]; hasSize {
		t.Fatalf("R3 counterexample failed: expected conflicting clen vs Filesize to omit size_bytes, got %v", defConflict["size_bytes"])
	}

	// Counterexample 6 (Default selection + 4K metadata): 360p (has_audio=true) + 2160p (3840x2160, has_audio=false)
	w4k, h4k, sz4k := int64(3840), int64(2160), int64(712445280)
	env4K := AdaptInfo(&Info{
		ID:    "aqz-KE-bpKQ",
		Title: "4K Test",
		Formats: []Format{
			{
				FormatID: "18",
				URL:      ytURL,
				Ext:      "mp4",
				Protocol: "https",
				Width:    &w360,
				Height:   &h360,
				VCodec:   "avc1.42001E",
				ACodec:   "mp4a.40.2",
				Filesize: &sz360,
			},
			{
				FormatID: "401",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=401&source=youtube&clen=712445280&dur=634.566&lmt=1700000000000000&sig=SIG_B",
				Ext:      "mp4",
				Protocol: "https",
				Width:    &w4k,
				Height:   &h4k,
				VCodec:   "av01.0.13M.08",
				ACodec:   "none",
				Filesize: &sz4k,
			},
		},
	}, ytOpts, now)
	if err := unified.ValidateEnvelopeAt(env4K, now); err != nil {
		t.Fatalf("4K envelope validation failed: %v", err)
	}
	item4K := unified.Map(unified.Slice(unified.Map(env4K["data"])["items"])[0])
	def4K := unified.Map(item4K["default"])
	if !strings.Contains(unified.Str(def4K["url"]), "itag=18") {
		t.Fatalf("expected 360p audible variant (itag=18) as item.default, got %+v", def4K)
	}
	variants4K := unified.Slice(item4K["variants"])
	meta4K := unified.Map(unified.Map(variants4K[0])["metadata"])
	if !unified.Equal(meta4K["width"], int64(3840)) || !unified.Equal(meta4K["height"], int64(2160)) {
		t.Fatalf("expected 4K silent variant to preserve metadata.width=3840 and metadata.height=2160, got %+v", meta4K)
	}

	// Counterexample 7 (R4): 206 Partial Content with empty body must be rejected
	if sz, ok := ValidateProbedMediaResponse(http.StatusPartialContent, http.Header{
		"Content-Type":  {"video/mp4"},
		"Content-Range": {"bytes 0-0/12345"},
	}, []byte{}); ok || sz != 0 {
		t.Fatalf("R4 206 empty-body counterexample failed: accepted empty body as (%d, %v)", sz, ok)
	}
}

func TestPR36Review5788284772C1AndExport(t *testing.T) {
	const now int64 = 1762300800
	w360, h360, sz360 := int64(640), int64(360), int64(15048844)
	w720, h720, sz720 := int64(1280), int64(720), int64(42000000)
	w1080, h1080, sz1080 := int64(1920), int64(1080), int64(120000000)
	w4k, h4k, sz4k := int64(3840), int64(2160), int64(712445280)
	szAudio := int64(9800000)

	ytOpts := AdaptOptions{
		PlatformID:    "youtube",
		ContentID:     "aqz-KE-bpKQ",
		SourceURL:     "https://www.youtube.com/watch?v=aqz-KE-bpKQ",
		MaxEntries:    1,
		EnableDASHMux: true,
	}

	// 1. Positive: Audible 360p default + 4K/1080p split silent video-only + M4A audio-only (normal & debug)
	ytAudibleAnd4K := AdaptInfo(&Info{
		ID:       "aqz-KE-bpKQ",
		Title:    "Big Buck Bunny 60fps 4K",
		Uploader: "Blender Foundation",
		Formats: []Format{
			{
				FormatID: "18",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=18&source=youtube&clen=15048844&dur=634.566&lmt=1700000000000000&sig=SIG_18",
				Ext:      "mp4",
				Protocol: "https",
				Width:    &w360,
				Height:   &h360,
				VCodec:   "avc1.42001E",
				ACodec:   "mp4a.40.2",
				Filesize: &sz360,
			},
			{
				FormatID: "313",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=313&source=youtube&clen=712445280&dur=634.566&lmt=1700000000000000&sig=SIG_313",
				Ext:      "webm",
				Protocol: "https",
				Width:    &w4k,
				Height:   &h4k,
				VCodec:   "vp9",
				ACodec:   "none",
				Filesize: &sz4k,
			},
			{
				FormatID: "248",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=248&source=youtube&clen=120000000&dur=634.566&lmt=1700000000000000&sig=SIG_248",
				Ext:      "webm",
				Protocol: "https",
				Width:    &w1080,
				Height:   &h1080,
				VCodec:   "vp9",
				ACodec:   "none",
				Filesize: &sz1080,
			},
			{
				FormatID: "140",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=140&source=youtube&clen=9800000&dur=634.566&lmt=1700000000000000&sig=SIG_140",
				Ext:      "m4a",
				Protocol: "https",
				VCodec:   "none",
				ACodec:   "mp4a.40.2",
				Filesize: &szAudio,
			},
		},
	}, ytOpts, now)

	ytNormal := unified.PublicView(ytAudibleAnd4K, false, now)
	ytDebug := unified.PublicView(ytAudibleAnd4K, true, now)
	for viewName, env := range map[string]unified.Object{"normal": ytNormal, "debug": ytDebug} {
		if err := unified.ValidateEnvelopeAt(env, now); err != nil {
			t.Fatalf("YouTube %s envelope failed ValidateEnvelopeAt: %v", viewName, err)
		}
	}

	// Negative 1a: Selecting 4K split silent or M4A audio-only when audible 360p exists must be rejected
	for _, badIdx := range []int{0, 3} {
		mutated := unified.Clone(ytNormal)
		it := unified.Map(unified.Slice(unified.Map(mutated["data"])["items"])[0])
		badVar := unified.Map(unified.Slice(it["variants"])[badIdx])
		badSrc := unified.Map(unified.Slice(unified.Map(badVar["delivery"])["sources"])[0])
		it["default"] = unified.Object{
			"variant_id": badVar["id"],
			"source_id":  badSrc["id"],
			"url":        badSrc["url"],
		}
		if err := unified.ValidateEnvelopeAt(mutated, now); err == nil {
			t.Fatalf("expected ValidateEnvelopeAt to reject non-eligible default variant index %d (%s)", badIdx, unified.Str(badVar["label"]))
		}
	}

	// Negative 1b: Two audible complete files (360p + 720p) alongside 4K silent: selecting 360p must be rejected
	ytTwoAudible := AdaptInfo(&Info{
		ID:    "aqz-KE-bpKQ",
		Title: "Two Audible + 4K Silent",
		Formats: []Format{
			{
				FormatID: "18",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=18&source=youtube&clen=15048844&dur=634.566&lmt=1700000000000000&sig=SIG_18",
				Ext:      "mp4",
				Protocol: "https",
				Width:    &w360,
				Height:   &h360,
				VCodec:   "avc1.42001E",
				ACodec:   "mp4a.40.2",
				Filesize: &sz360,
			},
			{
				FormatID: "22",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=22&source=youtube&clen=42000000&dur=634.566&lmt=1700000000000000&sig=SIG_22",
				Ext:      "mp4",
				Protocol: "https",
				Width:    &w720,
				Height:   &h720,
				VCodec:   "avc1.64001F",
				ACodec:   "mp4a.40.2",
				Filesize: &sz720,
			},
			{
				FormatID: "313",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=313&source=youtube&clen=712445280&dur=634.566&lmt=1700000000000000&sig=SIG_313",
				Ext:      "webm",
				Protocol: "https",
				Width:    &w4k,
				Height:   &h4k,
				VCodec:   "vp9",
				ACodec:   "none",
				Filesize: &sz4k,
			},
		},
	}, ytOpts, now)
	if err := unified.ValidateEnvelopeAt(ytTwoAudible, now); err != nil {
		t.Fatalf("expected 720p audible default to pass ValidateEnvelopeAt: %v", err)
	}
	lowDefault := unified.Clone(ytTwoAudible)
	itLow := unified.Map(unified.Slice(unified.Map(lowDefault["data"])["items"])[0])
	for _, rawV := range unified.Slice(itLow["variants"]) {
		v := unified.Map(rawV)
		if strings.Contains(unified.Str(v["label"]), "360p") {
			src := unified.Map(unified.Slice(unified.Map(v["delivery"])["sources"])[0])
			itLow["default"] = unified.Object{
				"variant_id": v["id"],
				"source_id":  src["id"],
				"url":        src["url"],
			}
		}
	}
	if err := unified.ValidateEnvelopeAt(lowDefault, now); err == nil {
		t.Fatal("expected ValidateEnvelopeAt to reject 360p default when 720p audible complete file is present")
	}

	// 2. Only silent video files: 4K silent selected as default passes; selecting 1080p silent is rejected
	ytOnlySilent := AdaptInfo(&Info{
		ID:    "aqz-KE-bpKQ",
		Title: "Only Silent Video Files",
		Formats: []Format{
			{
				FormatID: "313",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=313&source=youtube&clen=712445280&dur=634.566&lmt=1700000000000000&sig=SIG_313",
				Ext:      "webm",
				Protocol: "https",
				Width:    &w4k,
				Height:   &h4k,
				VCodec:   "vp9",
				ACodec:   "none",
				Filesize: &sz4k,
			},
			{
				FormatID: "248",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=248&source=youtube&clen=120000000&dur=634.566&lmt=1700000000000000&sig=SIG_248",
				Ext:      "webm",
				Protocol: "https",
				Width:    &w1080,
				Height:   &h1080,
				VCodec:   "vp9",
				ACodec:   "none",
				Filesize: &sz1080,
			},
			{
				FormatID: "140",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=140&source=youtube&clen=9800000&dur=634.566&lmt=1700000000000000&sig=SIG_140",
				Ext:      "m4a",
				Protocol: "https",
				VCodec:   "none",
				ACodec:   "mp4a.40.2",
				Filesize: &szAudio,
			},
		},
	}, ytOpts, now)
	ytSilentNormal := unified.PublicView(ytOnlySilent, false, now)
	ytSilentDebug := unified.PublicView(ytOnlySilent, true, now)
	for viewName, env := range map[string]unified.Object{"normal": ytSilentNormal, "debug": ytSilentDebug} {
		if err := unified.ValidateEnvelopeAt(env, now); err != nil {
			t.Fatalf("only-silent %s envelope failed ValidateEnvelopeAt: %v", viewName, err)
		}
	}
	silentLow := unified.Clone(ytSilentNormal)
	itSilentLow := unified.Map(unified.Slice(unified.Map(silentLow["data"])["items"])[0])
	v1080Silent := unified.Map(unified.Slice(itSilentLow["variants"])[1])
	src1080Silent := unified.Map(unified.Slice(unified.Map(v1080Silent["delivery"])["sources"])[0])
	itSilentLow["default"] = unified.Object{
		"variant_id": v1080Silent["id"],
		"source_id":  src1080Silent["id"],
		"url":        src1080Silent["url"],
	}
	if err := unified.ValidateEnvelopeAt(silentLow, now); err == nil {
		t.Fatal("expected ValidateEnvelopeAt to reject 1080p silent default when 4K silent file is present")
	}

	// 3. Unknown audio (omitted has_audio) is not forged as true, and on Instagram is preferred over silent DASH tracks
	igOpts := AdaptOptions{
		PlatformID:                   "instagram",
		ContentID:                    "CTApqC8BGWd",
		SourceURL:                    "https://www.instagram.com/reel/CTApqC8BGWd/",
		MaxEntries:                   1,
		EnableDASHMux:                true,
		AllowCodecLessProgressiveMP4: true,
	}
	igUnknown := AdaptInfo(&Info{
		ID:    "CTApqC8BGWd",
		Title: "Video by mrcigar",
		Formats: []Format{
			{
				FormatID: "dash-720-silent",
				URL:      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t2/f2/m86/dash720.mp4?efg=eyJ2ZW5jb2RlX3RhZyI6ImRhc2hfaGlnaF8xX3YxIn0&_nc_ht=scontent-nrt1-1.cdninstagram.com&oe=690D8A00",
				Ext:      "mp4",
				Protocol: "https",
				Width:    &w720,
				Height:   &h720,
				VCodec:   "avc1.64001F",
				ACodec:   "none",
			},
			{
				FormatID: "1",
				URL:      "https://scontent-nrt1-2.cdninstagram.com/o1/v/t16/f2/m69/10000000_1_n.mp4?efg=eyJ2ZW5jb2RlX3RhZyI6InZ0c192b2RfdXJsZ2VuIn0&_nc_ht=scontent-nrt1-2.cdninstagram.com&oe=690D8A00",
				Ext:      "mp4",
				Protocol: "https",
			},
			{
				FormatID: "dash-audio",
				URL:      "https://scontent-nrt1-1.cdninstagram.com/o1/v/t16/f2/m69/audio.mp4?_nc_ht=scontent-nrt1-1.cdninstagram.com&oe=690D8A00",
				Ext:      "m4a",
				Protocol: "https",
				VCodec:   "none",
				ACodec:   "mp4a.40.5",
			},
		},
	}, igOpts, now)
	igNormal := unified.PublicView(igUnknown, false, now)
	igDebug := unified.PublicView(igUnknown, true, now)
	for viewName, env := range map[string]unified.Object{"normal": igNormal, "debug": igDebug} {
		if err := unified.ValidateEnvelopeAt(env, now); err != nil {
			t.Fatalf("Instagram unknown-audio %s envelope failed ValidateEnvelopeAt: %v", viewName, err)
		}
		item := unified.Map(unified.Slice(unified.Map(env["data"])["items"])[0])
		def := unified.Map(item["default"])
		if def == nil || !strings.Contains(unified.Str(def["url"]), "10000000_1_n.mp4") {
			t.Fatalf("expected Instagram unknown-audio MP4 default in %s view, got %v", viewName, def)
		}
		variants := unified.Slice(item["variants"])
		meta := unified.Map(unified.Map(variants[1])["metadata"])
		if _, exists := meta["has_audio"]; exists {
			t.Fatalf("expected unknown audio to omit has_audio in %s view, got %v", viewName, meta["has_audio"])
		}
	}

	// Unknown audio 360p alongside 4K split silent must NOT be treated as audible complete file
	unknownPlus4KSilent := unified.Clone(ytNormal)
	itUnk := unified.Map(unified.Slice(unified.Map(unknownPlus4KSilent["data"])["items"])[0])
	for _, rawV := range unified.Slice(itUnk["variants"]) {
		v := unified.Map(rawV)
		if strings.Contains(unified.Str(v["label"]), "360p") {
			meta := unified.Map(v["metadata"])
			delete(meta, "has_audio")
			delete(meta, "audio_codec")
		}
	}
	if err := unified.ValidateEnvelopeAt(unknownPlus4KSilent, now); err == nil {
		t.Fatal("expected ValidateEnvelopeAt to reject 360p unknown-audio default when 4K silent file is present")
	}

	if dest := os.Getenv("DOKITOOL_GO_YTDLP_OUTPUT"); dest != "" {
		exports := unified.Array{
			ytNormal,
			ytDebug,
			unified.PublicView(ytTwoAudible, false, now),
			unified.PublicView(ytTwoAudible, true, now),
			ytSilentNormal,
			ytSilentDebug,
			igNormal,
			igDebug,
		}
		raw, err := unified.Encode(exports)
		if err != nil {
			t.Fatalf("encode DOKITOOL_GO_YTDLP_OUTPUT: %v", err)
		}
		if err := os.WriteFile(dest, append(raw, '\n'), 0600); err != nil {
			t.Fatalf("write DOKITOOL_GO_YTDLP_OUTPUT: %v", err)
		}
	}
}

func TestOmniGetPoint2PythonZipappAndStaleTempCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix python3 fixture")
	}

	// 1. CleanupStaleTempDirs only removes stale dokitool-ytdlp-* real directories owned by our UID,
	// never removes _MEI* directories, never follows or removes symlinks, and never removes other-UID dirs.
	tmpRoot := t.TempDir()
	staleMEI := filepath.Join(tmpRoot, "_MEI123456")
	staleDoki := filepath.Join(tmpRoot, "dokitool-ytdlp-old")
	freshDoki := filepath.Join(tmpRoot, "dokitool-ytdlp-fresh")
	unrelatedDir := filepath.Join(tmpRoot, "other-dir")
	symlinkTarget := filepath.Join(tmpRoot, "protected-target")
	symlinkSentinel := filepath.Join(symlinkTarget, "keep.txt")
	dokiSymlink := filepath.Join(tmpRoot, "dokitool-ytdlp-symlink")

	for _, dir := range []string{staleMEI, staleDoki, freshDoki, unrelatedDir, symlinkTarget} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(symlinkSentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(symlinkTarget, dokiSymlink); err != nil {
		t.Fatal(err)
	}

	oldTime := time.Now().Add(-3 * time.Hour)
	for _, dir := range []string{staleMEI, staleDoki, unrelatedDir, symlinkTarget} {
		if err := os.Chtimes(dir, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}

	// Verify that a mismatched UID removes 0 directories even when staleDoki is >2h old.
	if cleanedOtherUID := cleanupStaleTempDirsForUID(tmpRoot, 2*time.Hour, os.Geteuid()+1); cleanedOtherUID != 0 {
		t.Fatalf("expected 0 dirs removed for non-matching UID, got %d", cleanedOtherUID)
	}
	if _, err := os.Stat(staleDoki); err != nil {
		t.Fatalf("expected staleDoki to remain when UID does not match: %v", err)
	}

	// Verify normal CleanupStaleTempDirs removes only staleDoki (1 dir), preserving _MEI*, symlinks, fresh dirs, and unrelated dirs.
	cleaned := CleanupStaleTempDirs(tmpRoot, 2*time.Hour)
	if cleaned != 1 {
		t.Fatalf("expected CleanupStaleTempDirs to remove exactly 1 stale dokitool-ytdlp-* dir, got %d", cleaned)
	}
	if _, err := os.Stat(staleDoki); !os.IsNotExist(err) {
		t.Fatalf("expected stale dir %s to be removed", staleDoki)
	}
	for _, kept := range []string{staleMEI, freshDoki, unrelatedDir, symlinkTarget, symlinkSentinel} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("expected %s to be preserved: %v", kept, err)
		}
	}
	if lstat, err := os.Lstat(dokiSymlink); err != nil || lstat.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink %s to remain untouched, err=%v", dokiSymlink, err)
	}

	// 2. Python zipapp execution (mode 0644, non-executable, invoked via python3)
	pyPath := lookupPython3InPath(os.Getenv("PATH"))
	if pyPath == "" {
		t.Skip("python3 not found in PATH")
	}
	zipappDir := t.TempDir()
	zipappPath := filepath.Join(zipappDir, "yt-dlp")
	pyBody := "#!/usr/bin/env python3\n# PK\x03\x04 simulated zipapp header\nimport json, sys\nif '--version' in sys.argv:\n    print('2026.10.26')\n    sys.exit(0)\nprint(json.dumps({'id':'zipapp-ok','title':'Zipapp Test','formats':[{'format_id':'18','url':'https://rr1---sn-test.googlevideo.com/videoplayback?itag=18','ext':'mp4','protocol':'https','width':1280,'height':720,'vcodec':'avc1.42001E','acodec':'mp4a.40.2'}]}))\n"
	if err := os.WriteFile(zipappPath, []byte(pyBody), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(pyBody))
	digest := hex.EncodeToString(sum[:])

	eng, err := New(Config{
		BinaryPath:    zipappPath,
		AllowedHashes: map[string]string{digest: "yt-dlp"},
		EnvPath:       os.Getenv("PATH"),
	})
	if err != nil || !eng.Enabled() {
		t.Fatalf("expected zipapp engine to be enabled, got err=%v", err)
	}
	if !eng.UsingPythonZipapp() {
		t.Fatal("expected UsingPythonZipapp() == true for python zipapp")
	}

	info, err := eng.Extract(context.Background(), "https://www.youtube.com/watch?v=aqz-KE-bpKQ")
	if err != nil || info == nil || info.ID != "zipapp-ok" {
		t.Fatalf("expected zipapp extraction via python3 to succeed, got info=%+v err=%v", info, err)
	}
}

func TestOmniGetPythonZipappStartupVersionSelfCheckFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix python3 fixture")
	}

	realPython3 := lookupPython3InPath(os.Getenv("PATH"))
	if realPython3 == "" {
		t.Skip("python3 not found in PATH")
	}

	// Create a fake "python3" in a custom PATH directory that simulates an incompatible/old python3 failing self-check.
	badPyDir := t.TempDir()
	badPyPath := filepath.Join(badPyDir, "python3")
	badPyScript := "#!/bin/sh\necho 'SyntaxError: future feature annotations is not defined' >&2\nexit 1\n"
	if err := os.WriteFile(badPyPath, []byte(badPyScript), 0o755); err != nil {
		t.Fatal(err)
	}
	customEnvPath := badPyDir + string(os.PathListSeparator) + os.Getenv("PATH")

	// Case 1: python3 in PATH exists but fails startup --version self-check, AND the verified zipapp is executable (0755)
	// with a working direct interpreter -> falls back cleanly to direct execution (UsingPythonZipapp() == false, Enabled() == true).
	execZipDir := t.TempDir()
	execZipPath := filepath.Join(execZipDir, "yt-dlp")
	execZipBody := fmt.Sprintf("#!%s\n# PK\x03\x04 simulated zipapp header\nimport json, sys\nif '--version' in sys.argv:\n    print('2026.10.26')\n    sys.exit(0)\nprint(json.dumps({'id':'direct-fallback-ok','title':'Direct Fallback','formats':[{'format_id':'18','url':'https://rr1---sn-test.googlevideo.com/videoplayback?itag=18','ext':'mp4','protocol':'https','width':1280,'height':720,'vcodec':'avc1.42001E','acodec':'mp4a.40.2'}]}))\n", realPython3)
	if err := os.WriteFile(execZipPath, []byte(execZipBody), 0o755); err != nil {
		t.Fatal(err)
	}
	execSum := sha256.Sum256([]byte(execZipBody))
	execDigest := hex.EncodeToString(execSum[:])

	fallbackEng, err := New(Config{
		BinaryPath:    execZipPath,
		AllowedHashes: map[string]string{execDigest: "yt-dlp"},
		EnvPath:       customEnvPath,
	})
	if err != nil || fallbackEng == nil || !fallbackEng.Enabled() {
		t.Fatalf("expected zipapp with executable direct fallback to stay enabled when python3 fails self-check, got enabled=%v err=%v", fallbackEng != nil && fallbackEng.Enabled(), err)
	}
	if fallbackEng.UsingPythonZipapp() {
		t.Fatalf("expected UsingPythonZipapp() == false after python3 self-check failure and direct binary fallback")
	}
	info, err := fallbackEng.Extract(context.Background(), "https://www.youtube.com/watch?v=aqz-KE-bpKQ")
	if err != nil || info == nil || info.ID != "direct-fallback-ok" {
		t.Fatalf("expected direct fallback extraction to succeed, got info=%+v err=%v", info, err)
	}

	// Case 2: python3 in PATH exists but fails startup --version self-check, AND direct execution of the zipapp also fails
	// (e.g. non-executable 0644 or shebang uses the broken python3) -> engine is disabled at startup with clear reason.
	nonExecZipDir := t.TempDir()
	nonExecZipPath := filepath.Join(nonExecZipDir, "yt-dlp")
	if err := os.WriteFile(nonExecZipPath, []byte(execZipBody), 0o644); err != nil {
		t.Fatal(err)
	}
	disabledEng, err := New(Config{
		BinaryPath:    nonExecZipPath,
		AllowedHashes: map[string]string{execDigest: "yt-dlp"},
		EnvPath:       customEnvPath,
	})
	if err == nil || disabledEng == nil || disabledEng.Enabled() {
		t.Fatalf("expected engine to be disabled when both python3 self-check and direct execution fail, got enabled=%v err=%v", disabledEng != nil && disabledEng.Enabled(), err)
	}
	if !strings.Contains(disabledEng.DisableReason(), "zipapp startup self-check failed") {
		t.Fatalf("expected DisableReason to mention zipapp startup self-check failure, got %q", disabledEng.DisableReason())
	}

	// Case 3: python3 in PATH outputs wrong yt-dlp version (not PinnedVersion) and direct execution also outputs wrong version -> disabled.
	wrongVerDir := t.TempDir()
	wrongVerPath := filepath.Join(wrongVerDir, "yt-dlp")
	wrongVerBody := fmt.Sprintf("#!%s\n# PK\x03\x04 simulated zipapp header\nimport sys\nif '--version' in sys.argv:\n    print('2024.01.01')\n    sys.exit(0)\n", realPython3)
	if err := os.WriteFile(wrongVerPath, []byte(wrongVerBody), 0o755); err != nil {
		t.Fatal(err)
	}
	wrongSum := sha256.Sum256([]byte(wrongVerBody))
	wrongDigest := hex.EncodeToString(wrongSum[:])

	wrongVerEng, err := New(Config{
		BinaryPath:    wrongVerPath,
		AllowedHashes: map[string]string{wrongDigest: "yt-dlp"},
		EnvPath:       os.Getenv("PATH"),
	})
	if err == nil || wrongVerEng == nil || wrongVerEng.Enabled() {
		t.Fatalf("expected engine to be disabled when --version != %s, got enabled=%v err=%v", PinnedVersion, wrongVerEng != nil && wrongVerEng.Enabled(), err)
	}
}

func TestAdaptInfoDualContainerDASHAudioVariants(t *testing.T) {
	const now int64 = 1762300800
	w360, h360, sz360 := int64(640), int64(360), int64(15048844)
	w1080, h1080, sz1080 := int64(1920), int64(1080), int64(98000000)
	w4k, h4k, sz4k := int64(3840), int64(2160), int64(429584204)
	abr140, abr251, abr171 := 129.4, 135.2, 118.0
	ch2 := int64(2)
	asr44, asr48 := int64(44100), int64(48000)
	sz140, sz251, sz171 := int64(5044939), int64(6407118), int64(5500000)

	ytOpts := AdaptOptions{
		PlatformID:    "youtube",
		ContentID:     "tO01J-M3g0U",
		SourceURL:     "https://www.youtube.com/watch?v=tO01J-M3g0U",
		MaxEntries:    1,
		EnableDASHMux: true,
	}

	env := AdaptInfo(&Info{
		ID:       "tO01J-M3g0U",
		Title:    "Costa Rica in 4K 60fps HDR",
		Uploader: "Jacob + Katie Schwarz",
		Formats: []Format{
			{
				FormatID: "18",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=18&source=youtube&clen=15048844&dur=313.8&sig=SIG_18",
				Ext:      "mp4",
				Protocol: "https",
				Width:    &w360,
				Height:   &h360,
				VCodec:   "avc1.42001E",
				ACodec:   "mp4a.40.2",
				Filesize: &sz360,
			},
			{
				FormatID: "137",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=137&source=youtube&clen=98000000&dur=313.8&sig=SIG_137",
				Ext:      "mp4",
				Protocol: "https",
				Width:    &w1080,
				Height:   &h1080,
				VCodec:   "avc1.640028",
				ACodec:   "none",
				Filesize: &sz1080,
			},
			{
				FormatID: "315",
				URL:      "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=315&source=youtube&clen=429584204&dur=313.8&sig=SIG_315",
				Ext:      "webm",
				Protocol: "https",
				Width:    &w4k,
				Height:   &h4k,
				VCodec:   "vp9",
				ACodec:   "none",
				Filesize: &sz4k,
			},
			{
				FormatID:      "140",
				URL:           "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=140&source=youtube&clen=5044939&dur=313.8&sig=SIG_140",
				Ext:           "m4a",
				Protocol:      "https",
				VCodec:        "none",
				ACodec:        "mp4a.40.2",
				ABR:           &abr140,
				ASR:           &asr44,
				AudioChannels: &ch2,
				Filesize:      &sz140,
			},
			{
				FormatID:      "171",
				URL:           "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=171&source=youtube&clen=5500000&dur=313.8&sig=SIG_171",
				Ext:           "webm",
				Protocol:      "https",
				VCodec:        "none",
				ACodec:        "vorbis",
				ABR:           &abr171,
				ASR:           &asr44,
				AudioChannels: &ch2,
				Filesize:      &sz171,
			},
			{
				FormatID:      "251",
				URL:           "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?expire=1762320000&id=o-1&itag=251&source=youtube&clen=6407118&dur=313.8&sig=SIG_251",
				Ext:           "webm",
				Protocol:      "https",
				VCodec:        "none",
				ACodec:        "opus",
				ABR:           &abr251,
				ASR:           &asr48,
				AudioChannels: &ch2,
				Filesize:      &sz251,
			},
		},
	}, ytOpts, now)

	for viewName, view := range map[string]unified.Object{
		"normal": unified.PublicView(env, false, now),
		"debug":  unified.PublicView(env, true, now),
	} {
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("dual-container audio %s envelope failed ValidateEnvelopeAt: %v", viewName, err)
		}
		item := unified.Map(unified.Slice(unified.Map(view["data"])["items"])[0])
		var sawAAC, sawOpus bool
		var audioVariantCount int
		for _, rawV := range unified.Slice(item["variants"]) {
			v := unified.Map(rawV)
			meta := unified.Map(v["metadata"])
			mime := unified.Str(meta["mime_type"])
			if !strings.HasPrefix(mime, "audio/") {
				continue
			}
			audioVariantCount++
			src := unified.Map(unified.Slice(unified.Map(v["delivery"])["sources"])[0])
			srcURL := unified.Str(src["url"])
			switch unified.Str(meta["container"]) {
			case "mp4":
				sawAAC = true
				if unified.Str(meta["extension"]) != "m4a" || unified.Str(meta["audio_codec"]) != "mp4a.40.2" || !strings.Contains(srcURL, "itag=140") {
					t.Fatalf("unexpected mp4/m4a audio variant in %s: %+v url=%s", viewName, meta, srcURL)
				}
			case "webm":
				sawOpus = true
				if unified.Str(meta["extension"]) != "webm" || unified.Str(meta["audio_codec"]) != "opus" || !strings.Contains(srcURL, "itag=251") {
					t.Fatalf("expected best webm audio variant to prefer opus (itag=251) over vorbis (itag=171) in %s: %+v url=%s", viewName, meta, srcURL)
				}
			}
		}
		if !sawAAC || !sawOpus || audioVariantCount != 2 {
			t.Fatalf("expected exactly 2 deduplicated standalone audio variants (mp4/AAC + webm/Opus) in %s, got count=%d sawAAC=%v sawOpus=%v", viewName, audioVariantCount, sawAAC, sawOpus)
		}
	}
}

func TestWithExtractorsCommandArgsAndSubprocess(t *testing.T) {
	base := &Engine{socketTimeoutSec: 15}
	target := "https://www.facebook.com/watch/?v=647537299265662"

	defaultArgs := base.CommandArgs(target)
	for _, arg := range defaultArgs {
		if arg == "--use-extractors" {
			t.Fatalf("default engine must not emit --use-extractors, got %v", defaultArgs)
		}
	}

	pinned := base.WithExtractors("facebook", "facebook:reel", "facebook")
	args := pinned.CommandArgs(target)
	n := len(args)
	if n < 5 ||
		args[n-5] != "--use-extractors" ||
		args[n-4] != "facebook,facebook:reel" ||
		args[n-3] != "-J" ||
		args[n-2] != "--" ||
		args[n-1] != target {
		t.Fatalf("unexpected --use-extractors CLI tail: %v", args)
	}

	if runtime.GOOS == "windows" {
		return
	}
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "yt-dlp")
	script := "#!/bin/sh\nseen=''\nprev=''\nfor a in \"$@\"; do\n  if [ \"$prev\" = \"--use-extractors\" ]; then seen=\"$a\"; fi\n  prev=\"$a\"\ndone\nif [ \"$seen\" != \"facebook,facebook:reel\" ]; then\n  echo \"ERROR: missing --use-extractors ($seen)\" >&2\n  exit 1\nfi\necho '{\"id\":\"647537299265662\",\"title\":\"FB Test\",\"formats\":[{\"format_id\":\"hd\",\"url\":\"https://video-nrt6-1.xx.fbcdn.net/v/sample.mp4\",\"ext\":\"mp4\",\"protocol\":\"https\"}]}'\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(script))
	digest := hex.EncodeToString(sum[:])

	eng, err := New(Config{
		BinaryPath:    fakeBin,
		AllowedHashes: map[string]string{digest: "yt-dlp-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fbEng := eng.WithExtractors("facebook", "facebook:reel")
	info, err := fbEng.Extract(context.Background(), target)
	if err != nil || info == nil || info.ID != "647537299265662" {
		t.Fatalf("expected subprocess with --use-extractors to succeed, got info=%+v err=%v", info, err)
	}
}

func TestSingleStreamHLSOnlyFiltering(t *testing.T) {
	const now int64 = 1762300800
	w1080, h1080 := int64(1920), int64(1080)
	w720, h720 := int64(1280), int64(720)
	fps60 := float64(60)
	sz := int64(5000000)

	info := &Info{
		ID:       "xbvrdzy",
		Title:    "Single Stream HLS Filter Test",
		Uploader: "Creator",
		Formats: []Format{
			// 1. Progressive MP4 (must be excluded in SingleStreamHLSOnly mode)
			{
				FormatID: "http-720",
				URL:      "https://vod2.cf.dmcdn.net/progressive/720.mp4",
				Ext:      "mp4",
				Protocol: "https",
				Width:    &w720,
				Height:   &h720,
				VCodec:   "avc1.64001f",
				ACodec:   "mp4a.40.2",
				Filesize: &sz,
			},
			// 2. Video-only HLS needing external #EXT-X-MEDIA audio (must be excluded)
			{
				FormatID: "hls-video-only",
				URL:      "https://vod2.cf.dmcdn.net/adaptive/video.m3u8",
				Ext:      "mp4",
				Protocol: "m3u8_native",
				Width:    &w1080,
				Height:   &h1080,
				VCodec:   "avc1.64002a",
				ACodec:   "none",
			},
			// 3. Audio-only HLS (must be excluded)
			{
				FormatID: "hls-audio-only",
				URL:      "https://vod2.cf.dmcdn.net/adaptive/audio.m3u8",
				Ext:      "m4a",
				Protocol: "m3u8_native",
				VCodec:   "none",
				ACodec:   "mp4a.40.2",
			},
			// 4. Unknown-codec HLS (must be excluded)
			{
				FormatID: "hls-unknown",
				URL:      "https://vod2.cf.dmcdn.net/sec2(tok)/cloud/fmp4/1/unknown/manifest.m3u8",
				Ext:      "mp4",
				Protocol: "m3u8_native",
			},
			// 5. Legacy Dailymotion MPEG-TS HLS lacking /fmp4/ (must be excluded even with ext=="mp4" and codecs)
			{
				FormatID: "hls-legacy-ts",
				URL:      "https://vod3.cf.dmcdn.net/sec2(tok)/video/790/233/154332097_mp4_h264_aac_hd.m3u8",
				Ext:      "mp4",
				Protocol: "m3u8_native",
				Width:    &w720,
				Height:   &h720,
				VCodec:   "avc1.64001f",
				ACodec:   "mp4a.40.2",
			},
			// 6. Verified single-stream combined fMP4 HLS (must be kept!)
			{
				FormatID: "hls-1080@60",
				URL:      "https://vod2.cf.dmcdn.net/sec2(tok)/cloud/fmp4/1/h264_aac_fhd_hfr/2/manifest.m3u8",
				Ext:      "mp4",
				Protocol: "m3u8_native",
				Width:    &w1080,
				Height:   &h1080,
				FPS:      &fps60,
				VCodec:   "avc1.64002a",
				ACodec:   "mp4a.40.2",
			},
		},
	}

	opts := AdaptOptions{
		PlatformID:          "dailymotion",
		ContentID:           "xbvrdzy",
		SourceURL:           "https://www.dailymotion.com/video/xbvrdzy",
		SingleStreamHLSOnly: true,
		AllowedMediaURL: func(raw string) bool {
			return strings.Contains(raw, ".dmcdn.net/")
		},
	}
	probed := false
	PopulateMissingMP4Sizes(context.Background(), info, func(context.Context, string) (int64, bool) {
		probed = true
		return 1234, true
	}, opts)
	if probed {
		t.Fatal("PopulateMissingMP4Sizes must not probe when SingleStreamHLSOnly is true")
	}

	env := AdaptInfo(info, opts, now)
	if err := unified.ValidateEnvelopeAt(env, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt failed: %v", err)
	}
	pub := unified.PublicView(env, false, now)
	if err := unified.ValidateEnvelopeAt(pub, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt(PublicView) failed: %v", err)
	}

	item := unified.Map(unified.Slice(unified.Map(pub["data"])["items"])[0])
	if item["default"] != nil || unified.Str(item["default_unavailable_reason"]) != "processing_required" {
		t.Fatalf("expected null default with processing_required, got default=%v reason=%v", item["default"], item["default_unavailable_reason"])
	}
	variants := unified.Slice(item["variants"])
	if len(variants) != 1 {
		t.Fatalf("expected exactly 1 verified single-stream HLS variant, got %d: %+v", len(variants), variants)
	}
	v := unified.Map(variants[0])
	delivery := unified.Map(v["delivery"])
	if unified.Str(delivery["kind"]) != "hls" || unified.Str(delivery["scope"]) != "single" {
		t.Fatalf("expected kind=hls scope=single, got %+v", delivery)
	}
	if _, hasSel := delivery["selection"]; hasSel {
		t.Fatalf("single-stream HLS must not set selection: %+v", delivery)
	}
	if unified.Str(v["label"]) != "1080p60" {
		t.Fatalf("expected label 1080p60, got %v", v["label"])
	}

	// Live streams (is_live == true) must never be emitted as single-stream VOD HLS.
	isLive := true
	liveInfo := *info
	liveInfo.IsLive = &isLive
	liveEnv := AdaptInfo(&liveInfo, opts, now)
	if err := unified.ValidateEnvelopeAt(liveEnv, now); err != nil {
		t.Fatalf("ValidateEnvelopeAt(liveEnv) failed: %v", err)
	}
	if unified.Str(liveEnv["code"]) != "NO_MEDIA" {
		t.Fatalf("expected live stream to return NO_MEDIA in SingleStreamHLSOnly mode, got %+v", liveEnv)
	}
}
