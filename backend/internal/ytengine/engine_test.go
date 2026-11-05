// Adapted from wader/goutubedl and lrstanley/go-ytdlp (MIT License).
package ytengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	script := `
sleep 0.25
echo '{"id":"ok","formats":[{"format_id":"18","url":"https://video.twimg.com/ext_tw_video/1/pu/vid/640x360/a.mp4","ext":"mp4","protocol":"https","width":640,"height":360}]}'
`
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

	var peakInUse int32
	stopMonitor := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopMonitor:
				return
			default:
				cur := int32(len(baseEng.sem))
				for {
					prev := atomic.LoadInt32(&peakInUse)
					if cur <= prev || atomic.CompareAndSwapInt32(&peakInUse, prev, cur) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()

	var wg sync.WaitGroup
	errs := make([]error, 3)
	calls := []struct {
		eng *Engine
		url string
	}{
		{xEng, "https://x.com/i/status/1"},
		{ytEng, "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		{igEng, "https://www.instagram.com/reel/C123456/"},
	}
	for i, c := range calls {
		wg.Add(1)
		go func(idx int, eng *Engine, target string) {
			defer wg.Done()
			_, errs[idx] = eng.Extract(context.Background(), target)
		}(i, c.eng, c.url)
	}
	wg.Wait()
	close(stopMonitor)

	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent call %d failed: %v", i, e)
		}
	}
	if peak := atomic.LoadInt32(&peakInUse); peak > 2 {
		t.Fatalf("expected shared concurrency peak <= 2 across X/YouTube/Instagram, got %d", peak)
	}
	if len(baseEng.sem) != 0 {
		t.Fatalf("expected semaphore to be empty after completion, got %d", len(baseEng.sem))
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
