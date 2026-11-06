package ytengine

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// This opt-in check uses the official hash-locked release, never a shell shim.
// All extractor requests terminate at tiny local fixtures. The custom CA is a
// test trust anchor, not --no-check-certificates or a production TLS override.
func TestPinnedYTDLPGateHandshake(t *testing.T) {
	bin := os.Getenv("YTDLP_TEST_BINARY")
	if bin == "" {
		t.Skip("set YTDLP_TEST_BINARY to the pinned release for controlled handshake")
	}
	ports, err := parseGatePorts(os.Getenv("YTDLP_TEST_GATE_PORTS"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) == 0 {
		ports = testGatePorts(t, 2)
	}
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			var hits atomic.Int64
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Header.Get("Proxy-Authorization") != "" {
					t.Error("proxy credentials reached upstream")
				}
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("Content-Length", "16")
				if r.Method != "HEAD" {
					_, _ = w.Write([]byte("fixture metadata"))
				}
			})
			var up *httptest.Server
			if scheme == "https" {
				up = httptest.NewTLSServer(handler)
			} else {
				up = httptest.NewServer(handler)
			}
			defer up.Close()
			upURL, _ := url.Parse(up.URL)
			e, err := New(Config{BinaryPath: bin, GatePorts: ports, AllowedOutboundHost: func(h string) bool { return h == "example.com" }, Resolver: &countingGateResolver{}, DialAddr: func(netip.Addr, string) string { return upURL.Host }})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			gate, closeGate, err := e.startOutboundGate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer closeGate()
			args := e.commandArgsWithProxy(scheme+"://example.com/controlled.mp4", gate)
			// Insert controlled extractor/trust options before the terminal -- URL.
			tail := append([]string(nil), args[len(args)-2:]...)
			args = append(args[:len(args)-2], "--force-generic-extractor", "--skip-download")
			if scheme == "https" {
				ca := filepath.Join(t.TempDir(), "fixture-ca.pem")
				if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: up.Certificate().Raw}), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--cacert", ca)
			}
			args = append(args, tail...)
			cmd := exec.CommandContext(ctx, e.binaryPath, args...)
			tmp := t.TempDir()
			cmd.Dir = tmp
			cmd.Env = buildIsolatedEnv(tmp, e.envPath)
			stdout := &limitedBuffer{max: defaultMaxStdoutBytes}
			stderr := &limitedBuffer{max: defaultMaxStderrBytes}
			cmd.Stdout = stdout
			cmd.Stderr = stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("pinned yt-dlp %s handshake failed: exit=%v upstream_requests=%d (proxy credentials and stderr intentionally omitted)", scheme, err, hits.Load())
			}
			var info Info
			if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
				t.Fatal("pinned extractor did not return metadata JSON")
			}
			if info.ID != "controlled" || hits.Load() == 0 {
				t.Fatalf("unexpected controlled result id=%q requests=%d", info.ID, hits.Load())
			}
			t.Logf("pinned %s %s authenticated handshake: upstream_requests=%d", PinnedVersion, scheme, hits.Load())
		})
	}
}
