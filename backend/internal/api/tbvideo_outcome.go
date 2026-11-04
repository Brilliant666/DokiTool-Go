package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"regexp"
)

const tbvideoOutcomePath = "/api/downloads/tbvideo-result"

var tbvideoLogger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
var tbvideoID = regexp.MustCompile(`^[0-9]{1,32}$`)

// Accept only the small, fixed event vocabulary. Never log request bodies,
// URLs, unknown fields or validation errors. Reports are client observations.
func receiveTBVideoOutcome(w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	if r.URL.RawQuery != "" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var e struct {
		Platform  string `json:"platform"`
		PID       string `json:"pid"`
		VID       string `json:"vid"`
		Candidate *bool  `json:"used_candidate"`
		Result    string `json:"result"`
		Bytes     *int64 `json:"total_bytes"`
		Elapsed   *int64 `json:"elapsed_ms"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 768))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil || d.Decode(new(any)) != io.EOF ||
		(e.Platform != "pipigx" && e.Platform != "zuiyou") || !tbvideoID.MatchString(e.PID) || !tbvideoID.MatchString(e.VID) ||
		e.Candidate == nil || e.Bytes == nil || *e.Bytes < 0 || e.Elapsed == nil || *e.Elapsed < 0 ||
		(e.Result != "saved" && e.Result != "fallback") || (e.Result == "saved" && (!*e.Candidate || *e.Bytes == 0)) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	logger.Info("tbvideo_save", "platform", e.Platform, "pid", e.PID, "vid", e.VID, "used_candidate", *e.Candidate, "result", e.Result, "total_bytes", *e.Bytes, "elapsed_ms", *e.Elapsed)
	w.WriteHeader(http.StatusNoContent)
}
