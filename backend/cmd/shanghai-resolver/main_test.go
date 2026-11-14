package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func randomTestToken(t *testing.T) string {
	t.Helper()
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(buf[:])
}

func TestRejectConflictingModesBeforeConnecting(t *testing.T) {
	for _, arg := range []string{"-listen=127.0.0.1:9083", "-tls-cert=unused", "-tls-key=unused"} {
		if err := run([]string{"-pull-origin=https://japan.example.org", arg}); err == nil || !strings.Contains(err.Error(), "pull mode") {
			t.Fatalf("%s did not reject mixed modes: %v", arg, err)
		}
		if err := run([]string{"-pull-origin=https://japan.example.org", "-workers=4", arg}); err == nil || !strings.Contains(err.Error(), "pull mode") {
			t.Fatalf("%s with -workers did not reject mixed modes: %v", arg, err)
		}
	}
	if err := run([]string{"-workers=4"}); err == nil || !strings.Contains(err.Error(), "requires -pull-origin") {
		t.Fatalf("-workers without -pull-origin was not rejected: %v", err)
	}
}

func TestPullRequiresOwnTokenAndOrigin(t *testing.T) {
	t.Setenv("DOKITOOL_SHANGHAI_TOKEN", randomTestToken(t))
	t.Setenv("DOKITOOL_SHANGHAI_PULL_TOKEN", "")
	if err := run([]string{"-pull-origin=https://japan.example.org"}); err == nil {
		t.Fatal("pull accepted only the old inbound bearer")
	}
	t.Setenv("DOKITOOL_SHANGHAI_PULL_TOKEN", randomTestToken(t))
	if err := run([]string{"-pull-origin="}); err == nil {
		t.Fatal("explicit empty pull origin accepted")
	}
}

func TestWorkersSubprocessHelper(t *testing.T) {
	raw := os.Getenv("SHANGHAI_RESOLVER_TEST_MAIN_ARGS")
	if raw == "" {
		return
	}
	os.Args = append([]string{"shanghai-resolver"}, strings.Split(raw, "\n")...)
	main()
}

func TestWorkersFlagBoundsAndStartupExitCode(t *testing.T) {
	t.Setenv("DOKITOOL_SHANGHAI_PULL_TOKEN", randomTestToken(t))

	// Invalid values (< 1, > 8, non-numeric) must fail immediately at startup.
	for _, bad := range []string{"0", "-1", "-2", "9", "100", "invalid"} {
		for _, args := range [][]string{
			{"-pull-origin=https://japan.example.org", "-workers=" + bad},
			{"-workers=" + bad},
		} {
			err := run(args)
			if err == nil {
				t.Fatalf("run(%v) succeeded for invalid -workers=%s", args, bad)
			}
			if bad != "invalid" && !strings.Contains(err.Error(), "workers must be between 1 and 8") {
				t.Fatalf("run(%v) unexpected error: %v", args, err)
			}
		}

		cmd := exec.Command(os.Args[0], "-test.run=^TestWorkersSubprocessHelper$")
		cmd.Env = append(os.Environ(), "SHANGHAI_RESOLVER_TEST_MAIN_ARGS=-pull-origin=https://japan.example.org\n-workers="+bad)
		out, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
			t.Fatalf("main() with -workers=%s did not exit non-zero: err=%v out=%s", bad, err, out)
		}
	}

	// Valid boundary values (omitted default 3, plus 1, 3, 8) pass -workers validation
	// and proceed to pull worker construction (verified without network via loopback origin check).
	for _, args := range [][]string{
		{"-pull-origin=https://127.0.0.1"},
		{"-pull-origin=https://127.0.0.1", "-workers=1"},
		{"-pull-origin=https://127.0.0.1", "-workers=3"},
		{"-pull-origin=https://127.0.0.1", "-workers=8"},
	} {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "public address") {
			t.Fatalf("run(%v) did not pass -workers validation into worker setup: %v", args, err)
		}
	}
}
