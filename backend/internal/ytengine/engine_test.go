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
		stderr   string
		wantCode ErrorCode
	}{
		{"ERROR: [twitter] 123: HTTP Error 429: Too Many Requests", CodeRateLimited},
		{"ERROR: [twitter] 123: No video could be found in this tweet", CodeContentUnavailable},
		{"ERROR: [twitter] 123: Protected tweet; please sign in", CodeUpstreamBlocked},
	}
	for _, tc := range cases {
		errBin, errHashes := writeFakeYTDLP(t, "echo '"+tc.stderr+"' >&2\nexit 1")
		errEng, err := New(Config{BinaryPath: errBin, AllowedHashes: errHashes})
		if err != nil {
			t.Fatal(err)
		}
		_, extractErr := errEng.Extract(context.Background(), "https://x.com/i/status/123")
		if extractErr == nil || !strings.Contains(extractErr.Error(), string(tc.wantCode)) {
			t.Fatalf("stderr %q => %v, want %s", tc.stderr, extractErr, tc.wantCode)
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

	// 1. CleanupStaleTempDirs removes stale _MEI* and dokitool-ytdlp-* dirs older than maxAge
	tmpRoot := t.TempDir()
	staleMEI := filepath.Join(tmpRoot, "_MEI123456")
	staleDoki := filepath.Join(tmpRoot, "dokitool-ytdlp-old")
	freshMEI := filepath.Join(tmpRoot, "_MEI999999")
	unrelatedDir := filepath.Join(tmpRoot, "other-dir")
	for _, dir := range []string{staleMEI, staleDoki, freshMEI, unrelatedDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-3 * time.Hour)
	for _, dir := range []string{staleMEI, staleDoki, unrelatedDir} {
		if err := os.Chtimes(dir, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}

	cleaned := CleanupStaleTempDirs(tmpRoot, 2*time.Hour)
	if cleaned != 2 {
		t.Fatalf("expected CleanupStaleTempDirs to remove 2 stale dirs, got %d", cleaned)
	}
	for _, gone := range []string{staleMEI, staleDoki} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("expected stale dir %s to be removed", gone)
		}
	}
	for _, kept := range []string{freshMEI, unrelatedDir} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("expected dir %s to be preserved: %v", kept, err)
		}
	}

	// 2. Python zipapp execution (mode 0644, non-executable, invoked via python3)
	pyPath := lookupPython3InPath(os.Getenv("PATH"))
	if pyPath == "" {
		t.Skip("python3 not found in PATH")
	}
	zipappDir := t.TempDir()
	zipappPath := filepath.Join(zipappDir, "yt-dlp")
	pyBody := "#!/usr/bin/env python3\n# PK\x03\x04 simulated zipapp header\nimport json, sys\nprint(json.dumps({'id':'zipapp-ok','title':'Zipapp Test','formats':[{'format_id':'18','url':'https://rr1---sn-test.googlevideo.com/videoplayback?itag=18','ext':'mp4','protocol':'https','width':1280,'height':720,'vcodec':'avc1.42001E','acodec':'mp4a.40.2'}]}))\n"
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
	if cap(eng.bootSem) != defaultBootConcurrency {
		t.Fatalf("expected bootSem capacity=%d, got %d", defaultBootConcurrency, cap(eng.bootSem))
	}

	info, err := eng.Extract(context.Background(), "https://www.youtube.com/watch?v=aqz-KE-bpKQ")
	if err != nil || info == nil || info.ID != "zipapp-ok" {
		t.Fatalf("expected zipapp extraction via python3 to succeed, got info=%+v err=%v", info, err)
	}
}

func TestOmniGetPoint3ExtractorArgsMergeAndYouTubeCascade(t *testing.T) {
	// 1. MergeYouTubeExtractorArgs & MergeExtractorArgs single-flag consolidation
	merged := MergeYouTubeExtractorArgs(
		"player_client=default,web",
		"youtube:player_skip=webpage;po_token=web.gvs+tok1",
		"player_client=tv_downgraded,ios,android,web_safari",
	)
	wantMerged := "youtube:player_client=tv_downgraded,ios,android,web_safari;player_skip=webpage;po_token=web.gvs+tok1"
	if merged != wantMerged {
		t.Fatalf("MergeYouTubeExtractorArgs() = %q, want %q", merged, wantMerged)
	}

	groupMerged := MergeExtractorArgs(
		[]string{"youtube:player_client=default", "generic:impersonate=chrome"},
		[]string{"youtube:player_client=ios,mweb"},
	)
	if len(groupMerged) != 2 || groupMerged[0] != "youtube:player_client=ios,mweb" || groupMerged[1] != "generic:impersonate=chrome" {
		t.Fatalf("unexpected MergeExtractorArgs() = %v", groupMerged)
	}

	// 2. Recoverable YouTube SABR/nsig stderr triggers automatic anonymous client cascade
	sabrScript := `
has_fallback=0
for arg in "$@"; do
  case "$arg" in
    *player_client=tv_downgraded,ios,android,web_safari*)
      has_fallback=1
      ;;
  esac
done
if [ "$has_fallback" -eq 0 ]; then
  echo "ERROR: [youtube] aqz-KE-bpKQ: YouTube is forcing SABR streaming for this client; nsig extraction failed" >&2
  exit 1
fi
cat <<'JSON'
{
  "id": "aqz-KE-bpKQ",
  "title": "SABR Recovered Video",
  "formats": [
    {
      "format_id": "18",
      "url": "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?itag=18",
      "ext": "mp4",
      "protocol": "https",
      "width": 640,
      "height": 360,
      "vcodec": "avc1.42001E",
      "acodec": "mp4a.40.2"
    },
    {
      "format_id": "299",
      "url": "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?itag=299",
      "ext": "mp4",
      "protocol": "https",
      "width": 1920,
      "height": 1080,
      "vcodec": "avc1.64002A",
      "acodec": "none"
    }
  ]
}
JSON
`
	sabrBin, sabrHashes := writeFakeYTDLP(t, sabrScript)
	sabrEng, err := New(Config{
		BinaryPath:     sabrBin,
		AllowedHashes:  sabrHashes,
		MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	sabrInfo, err := sabrEng.Extract(context.Background(), "https://www.youtube.com/watch?v=aqz-KE-bpKQ")
	if err != nil || sabrInfo == nil || len(sabrInfo.Formats) != 2 {
		t.Fatalf("expected SABR cascade to recover 1080p+360p formats, got info=%+v err=%v", sabrInfo, err)
	}

	// 3. 360p degradation auto-upgrade on YouTube watch URL
	upgradeScript := `
has_fallback=0
for arg in "$@"; do
  case "$arg" in
    *player_client=tv_downgraded,ios,android,web_safari*)
      has_fallback=1
      ;;
  esac
done
if [ "$has_fallback" -eq 0 ]; then
  cat <<'JSON'
{
  "id": "aqz-KE-bpKQ",
  "title": "Degraded 360p Only",
  "formats": [
    {
      "format_id": "18",
      "url": "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?itag=18",
      "ext": "mp4",
      "protocol": "https",
      "width": 640,
      "height": 360,
      "vcodec": "avc1.42001E",
      "acodec": "mp4a.40.2"
    }
  ]
}
JSON
  exit 0
fi
cat <<'JSON'
{
  "id": "aqz-KE-bpKQ",
  "title": "Upgraded 4K + 360p",
  "formats": [
    {
      "format_id": "18",
      "url": "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?itag=18",
      "ext": "mp4",
      "protocol": "https",
      "width": 640,
      "height": 360,
      "vcodec": "avc1.42001E",
      "acodec": "mp4a.40.2"
    },
    {
      "format_id": "401",
      "url": "https://rr2---sn-a5mekn6s.googlevideo.com/videoplayback?itag=401",
      "ext": "mp4",
      "protocol": "https",
      "width": 3840,
      "height": 2160,
      "vcodec": "av01.0.13M.08",
      "acodec": "none"
    }
  ]
}
JSON
`
	upgBin, upgHashes := writeFakeYTDLP(t, upgradeScript)
	upgEng, err := New(Config{
		BinaryPath:     upgBin,
		AllowedHashes:  upgHashes,
		MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	upgInfo, err := upgEng.Extract(context.Background(), "https://www.youtube.com/watch?v=aqz-KE-bpKQ")
	if err != nil || upgInfo == nil || maxVideoShortEdge(upgInfo) != 2160 {
		t.Fatalf("expected degraded 360p YouTube result to auto-upgrade to 2160p, got info=%+v err=%v", upgInfo, err)
	}
}
