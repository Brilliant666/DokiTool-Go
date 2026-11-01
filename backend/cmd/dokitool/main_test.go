package main

import "testing"

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
