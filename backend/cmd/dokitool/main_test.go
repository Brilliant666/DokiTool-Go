package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/api"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/x"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/ytengine"
)

func TestXHSPageTransportSelection(t *testing.T) {
	for _, mode := range []string{"standard", "chrome131"} {
		executor, err := newXHSExecutor(mode)
		if err != nil || executor == nil {
			t.Fatalf("valid mode %s failed: %v", mode, err)
		}
	}
	for _, mode := range []string{"", "auto", "chrome150", "https://proxy.example.test", "Chrome131"} {
		if executor, err := newXHSExecutor(mode); err == nil || executor != nil {
			t.Fatalf("invalid mode accepted: %q", mode)
		}
	}
}

func TestUnknownXHSPageTransportFailsBeforeListener(t *testing.T) {
	if err := run([]string{"serve", "-xhs-page-transport", "auto"}); err == nil {
		t.Fatal("unknown transport accepted")
	}
}

func TestAdmissionCaddyRequiresConfiguredKeyBeforeListener(t *testing.T) {
	t.Setenv("SNAP_INGRESS_KEY", "")
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	err = run([]string{"serve", "-listen", occupied.Addr().String(), "-client-identity", "caddy"})
	if err == nil || !strings.Contains(err.Error(), "SNAP_INGRESS_KEY") {
		t.Fatalf("want missing proxy authentication before binding, got %v", err)
	}
}
func TestShanghaiModeSelection(t *testing.T) {
	const (
		mode     = "DOKITOOL_SHANGHAI_MODE"
		endpoint = "DOKITOOL_SHANGHAI_ENDPOINT"
		token    = "DOKITOOL_SHANGHAI_TOKEN"
		pull     = "DOKITOOL_SHANGHAI_PULL_TOKEN"
		address  = "https://shanghai.example.org/v1/douyin/original"
		key      = "development-test-only-32-byte-bearer"
	)
	tests := []struct {
		name                       string
		env                        map[string]string
		resolver, control, invalid bool
	}{
		{"default-off", nil, false, false, false},
		{"explicit-off", map[string]string{mode: "off", endpoint: address, token: key, pull: key}, false, false, false},
		{"legacy-direct", map[string]string{endpoint: address, token: key}, true, false, false},
		{"legacy-invalid-stays-off", map[string]string{endpoint: "invalid"}, false, false, false},
		{"explicit-direct", map[string]string{mode: "direct", endpoint: address, token: key}, true, false, false},
		{"explicit-pull", map[string]string{mode: "pull", pull: key}, true, true, false},
		{"missing-pull-credential", map[string]string{mode: "pull", token: key}, false, false, true},
		{"shared-credential", map[string]string{mode: "pull", pull: key, "SNAP_INGRESS_KEY": key}, false, false, true},
		{"ambiguous", map[string]string{mode: "pull", pull: key, endpoint: address}, false, false, true},
		{"invalid-mode", map[string]string{mode: "auto", pull: key}, false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, h, err := configureShanghai(fetch.New(), func(k string) string { return tc.env[k] })
			if (err != nil) != tc.invalid || (s != nil) != tc.resolver || (h != nil) != tc.control {
				t.Fatalf("resolver=%T control=%T error=%v", s, h, err)
			}
		})
	}
}

func TestYTDLPXConfiguration(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "yt-dlp")
	payload := []byte("#!/bin/sh\necho '{}' \n")
	if err := os.WriteFile(fakeBin, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	origAsset, hadOrig := ytengine.OfficialSHA256Sums20261026[hexSum]
	ytengine.OfficialSHA256Sums20261026[hexSum] = "yt-dlp-cmd-test"
	defer func() {
		if hadOrig {
			ytengine.OfficialSHA256Sums20261026[hexSum] = origAsset
		} else {
			delete(ytengine.OfficialSHA256Sums20261026, hexSum)
		}
	}()

	tests := []struct {
		name           string
		env            map[string]string
		wantRegistered bool
		wantErr        bool
	}{
		{
			name:           "default-off",
			env:            nil,
			wantRegistered: false,
			wantErr:        false,
		},
		{
			name:           "explicit-false",
			env:            map[string]string{"DOKITOOL_YTDLP_X_ENABLED": "false"},
			wantRegistered: false,
			wantErr:        false,
		},
		{
			name:           "invalid-bool",
			env:            map[string]string{"DOKITOOL_YTDLP_X_ENABLED": "auto"},
			wantRegistered: false,
			wantErr:        true,
		},
		{
			name: "enabled-missing-binary-not-registered-in-catalog",
			env: map[string]string{
				"DOKITOOL_YTDLP_X_ENABLED": "true",
				"DOKITOOL_YTDLP_BINARY":    filepath.Join(dir, "missing-yt-dlp"),
			},
			wantRegistered: false,
			wantErr:        false,
		},
		{
			name: "enabled-verified-binary",
			env: map[string]string{
				"DOKITOOL_YTDLP_X_ENABLED": "true",
				"DOKITOOL_YTDLP_BINARY":    fakeBin,
			},
			wantRegistered: true,
			wantErr:        false,
		},
		{
			name: "enabled-verified-binary-via-path",
			env: map[string]string{
				"DOKITOOL_YTDLP_X_ENABLED": "true",
				"PATH":                     dir,
			},
			wantRegistered: true,
			wantErr:        false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := catalog.Load()
			if err != nil {
				t.Fatal(err)
			}
			adapters := map[string]api.Adapter{}
			err = configureYTDLPX(c, adapters, fetch.New(), func(k string) string { return tc.env[k] })
			if (err != nil) != tc.wantErr {
				t.Fatalf("configureYTDLPX err=%v, wantErr=%v", err, tc.wantErr)
			}
			if c.Has("x") != tc.wantRegistered || (adapters["x"] != nil) != tc.wantRegistered {
				t.Fatalf("catalog.Has(x)=%v adapter=%v, wantRegistered=%v", c.Has("x"), adapters["x"] != nil, tc.wantRegistered)
			}
		})
	}

	t.Run("x-engine-pins-twitter-extractors-and-forbids-generic", func(t *testing.T) {
		baseEngine, err := ytengine.NewFromEnv(func(k string) string {
			if k == "DOKITOOL_YTDLP_BINARY" {
				return fakeBin
			}
			return ""
		}, nil)
		if err != nil || !baseEngine.Enabled() {
			t.Fatalf("NewFromEnv failed: %v", err)
		}
		xEngine := newXEngine(baseEngine)
		target := "https://x.com/i/status/2115900212212588814"
		args := xEngine.CommandArgs(target)
		wantExtractors := strings.Join(x.AllowedExtractors(), ",")
		if wantExtractors != "twitter,twitter:amplify,twitter:broadcast,twitter:card,twitter:spaces" {
			t.Fatalf("unexpected x.AllowedExtractors(): %q", wantExtractors)
		}
		if strings.Contains(wantExtractors, "generic") || strings.Contains(wantExtractors, "shortener") {
			t.Fatalf("x.AllowedExtractors() must exclude generic and twitter:shortener, got %q", wantExtractors)
		}
		var sawUseExtractors bool
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--use-extractors" {
				sawUseExtractors = true
				if args[i+1] != wantExtractors {
					t.Fatalf("--use-extractors value = %q, want %q (args=%v)", args[i+1], wantExtractors, args)
				}
			}
		}
		if !sawUseExtractors {
			t.Fatalf("expected --use-extractors in xEngine CommandArgs, got %v", args)
		}
	})
}

func TestYouTubeHelperConfiguration(t *testing.T) {
	denoBin := strings.TrimSpace(os.Getenv("DOKITOOL_DENO_BINARY"))
	if denoBin == "" {
		if runnerTemp := strings.TrimSpace(os.Getenv("RUNNER_TEMP")); runnerTemp != "" {
			candidate := filepath.Join(runnerTemp, "dokitool-validation", "bin", "deno")
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				denoBin = candidate
			}
		}
	}
	if denoBin == "" {
		p, err := exec.LookPath("deno")
		if err != nil || p == "" {
			t.Fatal("deno v2.9.8 binary not found in DOKITOOL_DENO_BINARY or PATH")
		}
		denoBin = p
	}
	ejsDir := filepath.Join("..", "..", "internal", "platform", "youtube", "testdata", "ejs")

	tests := []struct {
		name           string
		env            map[string]string
		wantRegistered bool
		wantErr        bool
	}{
		{
			name:           "default-off",
			env:            nil,
			wantRegistered: false,
			wantErr:        false,
		},
		{
			name:           "explicit-false",
			env:            map[string]string{"DOKITOOL_YOUTUBE_HELPER": "false"},
			wantRegistered: false,
			wantErr:        false,
		},
		{
			name:           "invalid-bool",
			env:            map[string]string{"DOKITOOL_YOUTUBE_HELPER": "auto"},
			wantRegistered: false,
			wantErr:        true,
		},
		{
			name: "enabled-missing-assets-disables-helper-only",
			env: map[string]string{
				"DOKITOOL_YOUTUBE_HELPER": "true",
				"DOKITOOL_DENO_BINARY":    filepath.Join(t.TempDir(), "missing-deno"),
				"DOKITOOL_EJS_DIR":        ejsDir,
			},
			wantRegistered: false,
			wantErr:        false,
		},
		{
			name: "enabled-verified-assets",
			env: map[string]string{
				"DOKITOOL_YOUTUBE_HELPER": "true",
				"DOKITOOL_DENO_BINARY":    denoBin,
				"DOKITOOL_EJS_DIR":        ejsDir,
			},
			wantRegistered: true,
			wantErr:        false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := catalog.Load()
			if err != nil {
				t.Fatal(err)
			}
			h, err := api.New(c, nil, time.Now, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = configureYouTubeHelper(c, h, fetch.New(), func(k string) string { return tc.env[k] })
			if (err != nil) != tc.wantErr {
				t.Fatalf("configureYouTubeHelper err=%v, wantErr=%v", err, tc.wantErr)
			}
			// Check whether /api/youtube/context returns 404 (disabled) vs non-404 (enabled, e.g. 409 without schema_version).
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/youtube/context", nil))
			isEnabled := rec.Code != http.StatusNotFound
			if isEnabled != tc.wantRegistered {
				t.Fatalf("helper enabled=%v (status=%d), want %v", isEnabled, rec.Code, tc.wantRegistered)
			}
		})
	}
}

func TestYTDLPSitesConfiguration(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "yt-dlp")
	payload := []byte("#!/bin/sh\necho '{}' \n")
	if err := os.WriteFile(fakeBin, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	origAsset, hadOrig := ytengine.OfficialSHA256Sums20261026[hexSum]
	ytengine.OfficialSHA256Sums20261026[hexSum] = "yt-dlp-sites-cmd-test"
	defer func() {
		if hadOrig {
			ytengine.OfficialSHA256Sums20261026[hexSum] = origAsset
		} else {
			delete(ytengine.OfficialSHA256Sums20261026, hexSum)
		}
	}()

	tests := []struct {
		name      string
		env       map[string]string
		wantSites []string
		wantErr   bool
	}{
		{
			name:      "default-off",
			env:       nil,
			wantSites: nil,
			wantErr:   false,
		},
		{
			name: "unknown-site-fails-even-when-other-switches-off-and-binary-missing",
			env: map[string]string{
				"DOKITOOL_YTDLP_SITES":  "facebook,vimeo",
				"DOKITOOL_YTDLP_BINARY": filepath.Join(dir, "missing-yt-dlp"),
			},
			wantSites: nil,
			wantErr:   true,
		},
		{
			name: "empty-token-fails",
			env: map[string]string{
				"DOKITOOL_YTDLP_SITES": "facebook,,tumblr",
			},
			wantSites: nil,
			wantErr:   true,
		},
		{
			name: "enabled-missing-binary-not-registered-in-catalog",
			env: map[string]string{
				"DOKITOOL_YTDLP_SITES":  "facebook,tumblr,dailymotion",
				"DOKITOOL_YTDLP_BINARY": filepath.Join(dir, "missing-yt-dlp"),
			},
			wantSites: nil,
			wantErr:   false,
		},
		{
			name: "enabled-verified-binary-registers-selected-sites-only",
			env: map[string]string{
				"DOKITOOL_YTDLP_SITES":  "facebook,tumblr,dailymotion",
				"DOKITOOL_YTDLP_BINARY": fakeBin,
			},
			wantSites: []string{"facebook", "tumblr", "dailymotion"},
			wantErr:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := catalog.Load()
			if err != nil {
				t.Fatal(err)
			}
			adapters := map[string]api.Adapter{}
			err = configureYTDLPX(c, adapters, fetch.New(), func(k string) string { return tc.env[k] })
			if (err != nil) != tc.wantErr {
				t.Fatalf("configureYTDLPX err=%v, wantErr=%v", err, tc.wantErr)
			}
			wantSet := map[string]bool{}
			for _, id := range tc.wantSites {
				wantSet[id] = true
			}
			for _, id := range []string{"facebook", "tumblr", "dailymotion", "x", "youtube", "instagram"} {
				want := wantSet[id]
				if c.Has(id) != want || (adapters[id] != nil) != want {
					t.Fatalf("site %s: catalog.Has=%v adapter=%v, want=%v", id, c.Has(id), adapters[id] != nil, want)
				}
			}
			if len(tc.wantSites) == 3 {
				if got := c.PlatformForURL("https://www.facebook.com/reel/1517545242764683"); got != "facebook" {
					t.Fatalf("PlatformForURL(facebook)=%q, want facebook", got)
				}
				if got := c.PlatformForURL("https://maskofthedragon.tumblr.com/post/706987028632748032"); got != "tumblr" {
					t.Fatalf("PlatformForURL(tumblr)=%q, want tumblr", got)
				}
				if got := c.PlatformForURL("https://www.dailymotion.com/video/x8t9abc"); got != "dailymotion" {
					t.Fatalf("PlatformForURL(dailymotion)=%q, want dailymotion", got)
				}
			}
		})
	}

	t.Run("unknown-site-fails-serve-before-listener", func(t *testing.T) {
		t.Setenv("DOKITOOL_YTDLP_SITES", "facebook,unknownsite")
		occupied, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer occupied.Close()
		err = run([]string{"serve", "-listen", occupied.Addr().String(), "-client-identity", "direct"})
		if err == nil || !strings.Contains(err.Error(), "DOKITOOL_YTDLP_SITES") {
			t.Fatalf("want DOKITOOL_YTDLP_SITES startup error before binding listener, got %v", err)
		}
	})
}

func TestXTombstoneFallbackFlagAndConfiguration(t *testing.T) {
	for _, valid := range []string{x.TombstoneFallbackOff, x.TombstoneFallbackFxTwitter} {
		if err := x.ValidateTombstoneFallback(valid); err != nil {
			t.Fatalf("ValidateTombstoneFallback(%q) failed: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "auto", "true", "false", "vxtwitter", "FXTWITTER", "Off", "https://api.fxtwitter.com"} {
		if err := x.ValidateTombstoneFallback(invalid); err == nil {
			t.Fatalf("ValidateTombstoneFallback(%q) unexpectedly succeeded", invalid)
		}
		occupied, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		runErr := run([]string{"serve", "-listen", occupied.Addr().String(), "-x-tombstone-fallback", invalid})
		occupied.Close()
		if runErr == nil || !strings.Contains(runErr.Error(), "x-tombstone-fallback") {
			t.Fatalf("want startup error before listener for -x-tombstone-fallback=%q, got %v", invalid, runErr)
		}
	}

	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "yt-dlp")
	payload := []byte("#!/bin/sh\necho '{}' \n")
	if err := os.WriteFile(fakeBin, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	origAsset, hadOrig := ytengine.OfficialSHA256Sums20261026[hexSum]
	ytengine.OfficialSHA256Sums20261026[hexSum] = "yt-dlp-tombstone-cmd-test"
	defer func() {
		if hadOrig {
			ytengine.OfficialSHA256Sums20261026[hexSum] = origAsset
		} else {
			delete(ytengine.OfficialSHA256Sums20261026, hexSum)
		}
	}()

	env := map[string]string{
		"DOKITOOL_YTDLP_X_ENABLED": "true",
		"DOKITOOL_YTDLP_BINARY":    fakeBin,
	}

	// Default configureYTDLPX keeps tombstone fallback off.
	cDefault, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	defaultAdapters := map[string]api.Adapter{}
	if err := configureYTDLPX(cDefault, defaultAdapters, fetch.New(), func(k string) string { return env[k] }); err != nil {
		t.Fatalf("configureYTDLPX failed: %v", err)
	}
	defaultX, ok := defaultAdapters["x"].(*x.Parser)
	if !ok || defaultX == nil || defaultX.TombstoneFallback() != x.TombstoneFallbackOff {
		t.Fatalf("default X parser TombstoneFallback = %v, want %q", defaultAdapters["x"], x.TombstoneFallbackOff)
	}

	// Explicit off and fxtwitter modes wire cleanly into the registered X adapter.
	for _, mode := range []string{x.TombstoneFallbackOff, x.TombstoneFallbackFxTwitter} {
		c, err := catalog.Load()
		if err != nil {
			t.Fatal(err)
		}
		adapters := map[string]api.Adapter{}
		if err := configureYTDLPWithOptions(c, adapters, fetch.New(), mode, func(k string) string { return env[k] }); err != nil {
			t.Fatalf("configureYTDLPWithOptions(%q) failed: %v", mode, err)
		}
		xp, ok := adapters["x"].(*x.Parser)
		if !ok || xp == nil || xp.TombstoneFallback() != mode {
			t.Fatalf("mode %q: X parser TombstoneFallback = %v, want %q", mode, adapters["x"], mode)
		}
	}
}

func TestParseCacheStartupSwitch(t *testing.T) {
	for _, invalid := range []string{"true", "false", "1", "0", "ON", "OFF", " on", "off ", "auto"} {
		t.Setenv("DOKITOOL_PARSE_CACHE", invalid)
		occupied, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		runErr := run([]string{"serve", "-listen", occupied.Addr().String(), "-client-identity", "direct"})
		occupied.Close()
		if runErr == nil || !strings.Contains(runErr.Error(), "DOKITOOL_PARSE_CACHE must be on or off") {
			t.Fatalf("want DOKITOOL_PARSE_CACHE startup error before binding listener for %q, got %v", invalid, runErr)
		}
	}

	for _, valid := range []string{"", "on", "off"} {
		t.Setenv("DOKITOOL_PARSE_CACHE", valid)
		occupied, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		runErr := run([]string{"serve", "-listen", occupied.Addr().String(), "-client-identity", "direct"})
		occupied.Close()
		if runErr == nil || strings.Contains(runErr.Error(), "DOKITOOL_PARSE_CACHE") {
			t.Fatalf("valid DOKITOOL_PARSE_CACHE=%q should reach listener bind (expected address-in-use error), got %v", valid, runErr)
		}
	}
}
