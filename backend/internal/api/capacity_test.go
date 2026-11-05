package api

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Distinct legitimate visitors must not share the former two-request ceiling.
func TestAdmissionProductionAccepts16DifferentClients(t *testing.T) {
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	results := make(chan int, 16)
	var wg sync.WaitGroup
	h, err := WithAdmission(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}), nil, "caddy", testEdgeKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(release); wg.Wait() }()
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			results <- call(h, edgeRequest("/api/parse", fmt.Sprintf("192.0.2.%d", n+1))).Code
		}(i)
		select {
		case <-entered:
		case code := <-results:
			t.Fatalf("visitor %d rejected before 16 concurrent parses: HTTP %d", i+1, code)
		case <-time.After(2 * time.Second):
			t.Fatal("request did not enter handler")
		}
	}
}
