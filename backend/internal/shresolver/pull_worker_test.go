package shresolver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func TestPullWorkerRejectsWrongScopeAndExpiredClaims(t *testing.T) {
	const origin = "https://japan.example.org"
	p := resolverFunc(func(context.Context, string) ([]unified.Object, error) {
		t.Fatal("invalid claim reached original probe")
		return nil, nil
	})
	good := PullJob{TaskID: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32), VideoID: wireVideo, DeadlineMS: time.Now().Add(time.Second).UnixMilli()}
	for _, change := range []func(*PullJob){
		func(j *PullJob) { j.DeadlineMS = time.Now().Add(-time.Second).UnixMilli() },
		func(j *PullJob) { j.VideoID = "https://example.org/" },
		func(j *PullJob) { j.AttemptID = "short" },
		func(j *PullJob) { j.TaskID = strings.Repeat("z", 32) },
	} {
		job := good
		change(&job)
		payload, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		ex := resolveExecutor(func(_ context.Context, r fetch.Request) (fetch.Response, error) {
			if r.URL != origin+ClaimPath || r.Header.Get("Authorization") != "Bearer "+wireToken ||
				r.Policy.FollowRedirects || r.Policy.AllowedURL(origin+ResultPath) || len(r.Body) > 0 {
				t.Fatal("claim did not stay within configured credential scope")
			}
			return fetch.Response{StatusCode: http.StatusOK, Body: payload}, nil
		})
		w, err := NewPullWorker(origin, wireToken, ex, p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.runOnce(t.Context()); err == nil {
			t.Fatal("invalid claim accepted")
		}
	}
}

func TestPullWorkerForbiddenStopsAndIdleCancellationDoesNotSpin(t *testing.T) {
	const origin = "https://japan.example.org"
	p := resolverFunc(func(context.Context, string) ([]unified.Object, error) {
		t.Fatal("no valid claim")
		return nil, nil
	})
	for _, code := range []int{http.StatusNoContent, http.StatusUnauthorized, http.StatusForbidden} {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		calls := 0
		w, err := NewPullWorker(origin, wireToken, resolveExecutor(func(context.Context, fetch.Request) (fetch.Response, error) {
			calls++
			return fetch.Response{StatusCode: code}, nil
		}), p)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		err = w.Run(ctx)
		deadline := ctx.Err()
		cancel()
		if err == nil || calls != 1 || (code != http.StatusNoContent && deadline != nil) {
			t.Fatalf("status %d: calls=%d deadline=%v err=%v", code, calls, deadline, err)
		}
	}
}

func TestPullWorkerRequiresFixedHTTPSOrigin(t *testing.T) {
	ex := resolveExecutor(func(context.Context, fetch.Request) (fetch.Response, error) {
		t.Fatal("configuration contacted network")
		return fetch.Response{}, nil
	})
	p := resolverFunc(func(context.Context, string) ([]unified.Object, error) {
		t.Fatal("configuration probed media")
		return nil, nil
	})
	for _, origin := range []string{"http://japan.example.org", "https://japan.example.org/admin", "https://japan.example.org?query", "https://user@japan.example.org", "https://japan.example.org:8443", "https://127.0.0.1", "https://japan.example.org#"} {
		if _, err := NewPullWorker(origin, wireToken, ex, p); err == nil {
			t.Errorf("accepted invalid origin %q", origin)
		}
	}
}

type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type pooledControlExecutor struct {
	origin        string
	serverURL     string
	client        *http.Client
	dials         atomic.Int32
	closedIdleCnt atomic.Int32
}

func newPooledControlExecutor(srv *httptest.Server, origin string) *pooledControlExecutor {
	base := srv.Client().Transport.(*http.Transport)
	ex := &pooledControlExecutor{origin: origin, serverURL: srv.URL}
	tr := &http.Transport{
		MaxConnsPerHost:     1,
		MaxIdleConnsPerHost: 1,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   false,
		TLSClientConfig:     base.TLSClientConfig.Clone(),
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			ex.dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	ex.client = &http.Client{Transport: tr}
	return ex
}

func (e *pooledControlExecutor) Do(ctx context.Context, req fetch.Request) (fetch.Response, error) {
	path := strings.TrimPrefix(req.URL, e.origin)
	if path != ClaimPath && path != ResultPath {
		return fetch.Response{}, errors.New("unexpected control destination")
	}
	r, err := http.NewRequestWithContext(ctx, req.Method, e.serverURL+path, bytes.NewReader(req.Body))
	if err != nil {
		return fetch.Response{}, err
	}
	r.Header = req.Header.Clone()
	res, err := e.client.Do(r)
	if err != nil {
		return fetch.Response{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, req.Policy.MaxBytes+1))
	return fetch.Response{Body: body, StatusCode: res.StatusCode, Header: res.Header}, err
}

func (e *pooledControlExecutor) CloseIdleConnections() {
	e.closedIdleCnt.Add(1)
	e.client.CloseIdleConnections()
}

func TestPullWorkerRebuildsConnectionAfterFastEmpty204s(t *testing.T) {
	const origin = "https://japan.example.org"
	b := pullTestBroker(t)
	b.now = time.Now

	type connIDKey struct{}
	var serverConns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connID, _ := r.Context().Value(connIDKey{}).(int32)
		if connID == 1 && r.URL.EscapedPath() == ClaimPath {
			// Simulate a poisoned keep-alive connection returning instant empty 204 without Connection: close.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		b.ServeHTTP(w, r)
	}))
	srv.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, connIDKey{}, serverConns.Add(1))
	}
	srv.StartTLS()
	defer srv.Close()

	control := newPooledControlExecutor(srv, origin)
	defer control.client.CloseIdleConnections()

	var logs syncLogBuffer
	prevLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prevLog) })

	w, err := NewPullWorker(origin, wireToken, control, resolverFunc(func(_ context.Context, _ string) ([]unified.Object, error) {
		return []unified.Object{deliveryRow("v11-cold.douyinvod.com", "objectA", "a")}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Wait up to 9s (1s + 2s + 4s backoffs across 3 fast-204 retries -> CloseIdleConnections -> fresh conn #2).
	deadline := time.After(9 * time.Second)
	for {
		b.mu.Lock()
		n := len(b.waiting)
		b.mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("worker never rebuilt connection and registered claim (dials=%d closedIdle=%d logs=%q)",
				control.dials.Load(), control.closedIdleCnt.Load(), logs.String())
		case <-time.After(5 * time.Millisecond):
		}
	}

	rows, err := b.ResolveOriginal(t.Context(), wireVideo)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ResolveOriginal after connection rebuild failed: err=%v rows=%v", err, rows)
	}
	cancel()
	<-done

	if control.closedIdleCnt.Load() < 1 || control.dials.Load() != 2 {
		t.Fatalf("expected CloseIdleConnections>=1 and dials==2, got closedIdle=%d dials=%d",
			control.closedIdleCnt.Load(), control.dials.Load())
	}
	logged := logs.String()
	if logged == "" || strings.Contains(logged, wireToken) {
		t.Fatalf("expected safe diagnostic log on fast-204 streak, got %q", logged)
	}
}

func TestPullWorkerNormalLongPollAndReceiptKeepConnection(t *testing.T) {
	const origin = "https://japan.example.org"
	b := pullTestBroker(t)
	b.now = time.Now

	var slowPolls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == ClaimPath && slowPolls.Add(1) <= 3 {
			// Normal >=1s empty 204 poll must not be treated as an instant-204 anomaly.
			select {
			case <-time.After(1050 * time.Millisecond):
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		b.ServeHTTP(w, r)
	}))
	defer srv.Close()

	control := newPooledControlExecutor(srv, origin)
	defer control.client.CloseIdleConnections()

	var logs syncLogBuffer
	prevLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prevLog) })

	w, err := NewPullWorker(origin, wireToken, control, resolverFunc(func(_ context.Context, _ string) ([]unified.Object, error) {
		return []unified.Object{deliveryRow("v11-cold.douyinvod.com", "objectA", "a")}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Wait for the 3 >=1s empty 204 polls (plus their backoffs) to finish and the 4th claim to register on b,
	// then run 3 fast (<1s) job deliveries in a row on the same connection.
	deadline := time.After(12 * time.Second)
	for {
		b.mu.Lock()
		n := len(b.waiting)
		b.mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("worker did not reach live broker after normal long polls (dials=%d)", control.dials.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}

	for i := range 3 {
		if i > 0 {
			waitForPullWaiter(t, b)
		}
		rows, err := b.ResolveOriginal(t.Context(), wireVideo)
		if err != nil || len(rows) != 1 {
			t.Fatalf("delivery %d failed: err=%v rows=%v", i, err, rows)
		}
	}
	cancel()
	<-done

	if control.closedIdleCnt.Load() != 0 || control.dials.Load() != 1 || logs.String() != "" {
		t.Fatalf("normal long polls / fast receipts disturbed connection: closedIdle=%d dials=%d logs=%q",
			control.closedIdleCnt.Load(), control.dials.Load(), logs.String())
	}
}
