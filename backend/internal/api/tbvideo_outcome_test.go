package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTBVideoOutcomeLogsOnlyAcceptedFields(t *testing.T) {
	const good = `{"platform":"pipigx","pid":"12","vid":"34","used_candidate":true,"result":"saved","total_bytes":1027986,"elapsed_ms":80}`
	for _, c := range []struct {
		body   string
		status int
	}{
		{good, 204},
		{strings.Replace(good, `"saved"`, `"fallback"`, 1), 204},
		{strings.Replace(good, `"34"`, `"https://x/?auth_key=secret"`, 1), 400},
		{strings.Replace(good, `"pid"`, `"url":"https://x/?auth_key=secret","pid"`, 1), 400},
		{strings.Replace(good, "true", "false", 1), 400},
		{good + good, 400},
		{strings.Repeat(" ", 768) + good, 400},
	} {
		var logs bytes.Buffer
		req := httptest.NewRequest("POST", tbvideoOutcomePath, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		receiveTBVideoOutcome(w, req, slog.New(slog.NewJSONHandler(&logs, nil)))
		if w.Code != c.status {
			t.Fatalf("status %d want %d", w.Code, c.status)
		}
		if w.Code == 204 {
			var row map[string]any
			if json.Unmarshal(logs.Bytes(), &row) != nil || row["vid"] != "34" || row["total_bytes"] != float64(1027986) {
				t.Fatal("invalid structured event")
			}
		} else if logs.Len() != 0 {
			t.Fatal("rejected input logged")
		}
	}
}

// Use a real listener: ResponseRecorder cannot enforce socket read deadlines.
func TestAdmissionSlowTelemetryReleasesLease(t *testing.T) {
	config := DefaultAdmissionConfig()
	config.Reports.ClientConcurrency, config.Reports.GlobalConcurrency = 1, 1
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := WithAdmissionConfig(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receiveTBVideoOutcome(w, r, logger)
	}), nil, "direct", "", config)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		finished <- struct{}{}
	}))
	defer server.Close()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(6 * time.Second))
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: test\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{", tbvideoOutcomePath)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal("slow body did not terminate", err)
	}
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatal(response.StatusCode)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("report lease not released")
	}
	body := `{"platform":"pipigx","pid":"12","vid":"34","used_candidate":true,"result":"saved","total_bytes":10,"elapsed_ms":1}`
	client := server.Client()
	client.Timeout = 2 * time.Second
	response, err = client.Post(server.URL+tbvideoOutcomePath, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal("valid report blocked after slow-body timeout", response.StatusCode)
	}
}
