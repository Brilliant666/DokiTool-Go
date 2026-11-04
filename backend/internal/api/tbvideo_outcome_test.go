package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
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
