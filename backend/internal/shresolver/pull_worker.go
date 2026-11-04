package shresolver

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
)

var errPullAuth = errors.New("Shanghai pull credential rejected")

// PullWorker handles one lease at a time. Only the pinned Japanese control
// endpoints receive its bearer. The media probe has its own transport.
type PullWorker struct {
	origin  string
	token   string
	control fetch.Executor
	probe   originalProbe
}

func NewPullWorker(origin, token string, control fetch.Executor, p originalProbe) (*PullWorker, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.EscapedPath() != "" && u.EscapedPath() != "/") || u.RawQuery != "" || u.ForceQuery || strings.Contains(origin, "#") ||
		!fetch.AllowHosts(u.Hostname())(origin) || !ValidToken(token) || control == nil || p == nil {
		return nil, errors.New("invalid Shanghai pull configuration")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !fetch.PublicIP(ip) {
		return nil, errors.New("pull origin requires a public address")
	}
	return &PullWorker{origin: "https://" + u.Host, token: token, control: control, probe: p}, nil
}

func (w *PullWorker) post(ctx context.Context, path string, body []byte, timeout time.Duration) (fetch.Response, error) {
	target := w.origin + path
	h := http.Header{"Authorization": {"Bearer " + w.token}, "Content-Type": {"application/json"}}
	resp, err := w.control.Do(ctx, fetch.Request{URL: target, Method: http.MethodPost, Body: body, Header: h,
		Policy: fetch.Policy{Purpose: "douyin-original-pull", Timeout: timeout, MaxBytes: 1024, MaxHeaderBytes: 8192,
			AllowedURL: func(raw string) bool { return raw == target }}})
	if err == nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
		return resp, errPullAuth
	}
	return resp, err
}

func validLeaseID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// runOnce never retries a claimed task or replays its result on another lease.
func (w *PullWorker) runOnce(ctx context.Context) (bool, error) {
	res, err := w.post(ctx, ClaimPath, nil, PullWait+10*time.Second)
	if err != nil {
		return false, err
	}
	if res.StatusCode == http.StatusNoContent {
		return false, nil
	}
	if res.StatusCode != http.StatusOK {
		return false, errUnavailable
	}
	var job PullJob
	d := json.NewDecoder(bytes.NewReader(res.Body))
	d.DisallowUnknownFields()
	if d.Decode(&job) != nil || d.Decode(&struct{}{}) != io.EOF || !validLeaseID(job.TaskID) || !validLeaseID(job.AttemptID) || !ValidVideoID(job.VideoID) {
		return false, errUnavailable
	}
	deadline := time.UnixMilli(job.DeadlineMS)
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false, errUnavailable
	}
	lease, cancel := context.WithTimeout(ctx, min(remaining, clientTimeout))
	defer cancel()
	out, probeErr := probeOriginal(lease, w.probe, job.VideoID)
	if err := lease.Err(); err != nil {
		return false, err
	}
	msg := PullResult{PullJob: job, Result: json.RawMessage("null")}
	if probeErr == nil {
		raw, err := json.Marshal(out)
		if err == nil && len(raw) <= maxResponseBytes {
			msg.Result = raw
		}
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return false, err
	}
	ack, err := w.post(lease, ResultPath, body, clientTimeout)
	if err != nil {
		return false, err
	}
	if ack.StatusCode != http.StatusNoContent {
		return false, errUnavailable
	}
	return true, nil
}

func (w *PullWorker) Run(ctx context.Context) error {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		delivered, err := w.runOnce(ctx)
		if ctx.Err() != nil {
			break
		}
		if errors.Is(err, errPullAuth) {
			return err
		}
		if err == nil && (delivered || time.Since(start) >= PullWait/2) {
			backoff = time.Second
			continue
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(2*backoff, 10*time.Second)
	}
	return ctx.Err()
}
