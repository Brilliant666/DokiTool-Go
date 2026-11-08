package main

import (
	"strings"
	"testing"
)

func TestRejectConflictingModesBeforeConnecting(t *testing.T) {
	for _, arg := range []string{"-listen=127.0.0.1:9083", "-tls-cert=unused", "-tls-key=unused"} {
		if err := run([]string{"-pull-origin=https://japan.example.org", arg}); err == nil || !strings.Contains(err.Error(), "pull mode") {
			t.Fatalf("%s did not reject mixed modes: %v", arg, err)
		}
	}
}

func TestPullRequiresOwnTokenAndOrigin(t *testing.T) {
	t.Setenv("DOKITOOL_SHANGHAI_TOKEN", strings.Repeat("a", 32))
	t.Setenv("DOKITOOL_SHANGHAI_PULL_TOKEN", "")
	if err := run([]string{"-pull-origin=https://japan.example.org"}); err == nil {
		t.Fatal("pull accepted only the old inbound bearer")
	}
	t.Setenv("DOKITOOL_SHANGHAI_PULL_TOKEN", strings.Repeat("b", 32))
	if err := run([]string{"-pull-origin="}); err == nil {
		t.Fatal("explicit empty pull origin accepted")
	}
}
