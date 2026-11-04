package main

import (
	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"net"
	"strings"
	"testing"
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
