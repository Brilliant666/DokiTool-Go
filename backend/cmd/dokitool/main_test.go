package main

import (
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
