package ytengine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func gateLeases(e *Engine) int {
	e.gatePorts.mu.Lock()
	defer e.gatePorts.mu.Unlock()
	return len(e.gatePorts.used)
}

func TestGateExtractSharedConcurrencyAndCancellation(t *testing.T) {
	bin, hashes := writeFakeYTDLP(t, "sleep 30")
	e, err := New(Config{BinaryPath: bin, AllowedHashes: hashes, Resolver: &countingGateResolver{}, GatePorts: testGatePorts(t, 2)})
	if err != nil {
		t.Fatal(err)
	}
	x := e.WithTargetPolicy(nil, func(h string) bool { return h == "x.example" })
	y := e.WithTargetPolicy(nil, func(h string) bool { return h == "y.example" })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	for _, task := range []struct {
		e   *Engine
		url string
	}{{x, "https://x.example/1"}, {y, "https://y.example/2"}} {
		go func(e *Engine, u string) { defer wg.Done(); _, _ = e.Extract(ctx, u) }(task.e, task.url)
	}
	deadline := time.Now().Add(3 * time.Second)
	for gateLeases(e) != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if gateLeases(e) != 2 {
		t.Fatal("both tasks did not acquire separate fixed ports")
	}
	start := time.Now()
	_, err = x.Extract(context.Background(), "https://x.example/3")
	var busy *Error
	if !errors.As(err, &busy) || !busy.LocalBusy || busy.RetryAfter != 1 || time.Since(start) > time.Second {
		t.Fatalf("third request did not fail promptly with T1 busy: %v", err)
	}
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not reap subprocesses")
	}
	if gateLeases(e) != 0 || len(e.sem) != 0 {
		t.Fatal("cancel leaked shared ports or semaphore")
	}
}

func TestGateExtractCompletionReleasesPorts(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		timeout      bool
		success      bool
	}{
		{"success", `echo '{"id":"controlled"}'`, false, true},
		{"failure", "exit 1", false, false},
		{"timeout", "sleep 30", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, hashes := writeFakeYTDLP(t, tc.script)
			e, err := New(Config{BinaryPath: bin, AllowedHashes: hashes, Resolver: &countingGateResolver{}, MaxConcurrency: 1, GatePorts: testGatePorts(t, 1), Timeout: 2 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.Extract(context.Background(), "https://public.example/1")
			if (err == nil) != tc.success {
				t.Fatalf("unexpected extraction outcome: %v", err)
			}
			if tc.timeout {
				var typed *Error
				if !errors.As(err, &typed) || typed.Code != CodeUpstreamTimeout {
					t.Fatalf("wrong timeout result: %v", err)
				}
			}
			if gateLeases(e) != 0 || len(e.sem) != 0 {
				t.Fatal("completion leaked port or execution slot")
			}
			_, closeGate, err := e.startOutboundGate(context.Background())
			if err != nil {
				t.Fatal("completed task port not reusable:", err)
			}
			closeGate()
		})
	}
}
