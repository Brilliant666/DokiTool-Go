// Adapted from wader/goutubedl and lrstanley/go-ytdlp (MIT License).
package ytengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
