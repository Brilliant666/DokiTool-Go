package shresolver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/douyin"
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
		var calls atomic.Int32
		w, err := NewPullWorker(origin, wireToken, resolveExecutor(func(context.Context, fetch.Request) (fetch.Response, error) {
			calls.Add(1)
			return fetch.Response{StatusCode: code}, nil
		}), p)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		err = w.Run(ctx)
		deadline := ctx.Err()
		cancel()
		gotCalls := int(calls.Load())
		if code == http.StatusNoContent {
			if err == nil || gotCalls != pullWorkers || deadline == nil {
				t.Fatalf("status %d: calls=%d deadline=%v err=%v", code, gotCalls, deadline, err)
			}
		} else if !errors.Is(err, errPullAuth) || gotCalls < 1 || gotCalls > pullWorkers || deadline != nil {
			t.Fatalf("status %d: calls=%d deadline=%v err=%v", code, gotCalls, deadline, err)
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
		MaxConnsPerHost:     pullWorkers,
		MaxIdleConnsPerHost: pullWorkers,
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
		if connID <= pullWorkers && r.URL.EscapedPath() == ClaimPath {
			// Simulate poisoned keep-alive connections returning instant empty 204 without Connection: close.
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

	// Wait up to 9s (1s + 2s + 4s backoffs across 3 fast-204 retries -> CloseIdleConnections -> fresh conns).
	deadline := time.After(9 * time.Second)
	for {
		b.mu.Lock()
		n := len(b.waiting)
		b.mu.Unlock()
		if n == pullWorkers {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("worker never rebuilt connection and registered claims (dials=%d closedIdle=%d logs=%q)",
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

	if control.closedIdleCnt.Load() < 1 || control.dials.Load() <= pullWorkers {
		t.Fatalf("expected CloseIdleConnections>=1 and dials>%d, got closedIdle=%d dials=%d",
			pullWorkers, control.closedIdleCnt.Load(), control.dials.Load())
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
		if r.URL.EscapedPath() == ClaimPath && slowPolls.Add(1) <= 3*pullWorkers {
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

	// Wait for the >=1s empty 204 polls (plus their backoffs) to finish and all pullWorkers claims to register on b,
	// then run 3 fast (<1s) job deliveries in a row reusing the existing pooled connections.
	deadline := time.After(14 * time.Second)
	for {
		b.mu.Lock()
		n := len(b.waiting)
		b.mu.Unlock()
		if n == pullWorkers {
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

	if control.closedIdleCnt.Load() != 0 || control.dials.Load() != pullWorkers || logs.String() != "" {
		t.Fatalf("normal long polls / fast receipts disturbed connection: closedIdle=%d dials=%d logs=%q",
			control.closedIdleCnt.Load(), control.dials.Load(), logs.String())
	}
}

func TestPullWorkerNoBackoffOnLeaseTimeoutOrResultError(t *testing.T) {
	const origin = "https://japan.example.org"

	t.Run("post_claim_timeout_and_result_failures_reclaim_within_1s", func(t *testing.T) {
		var (
			mu          sync.Mutex
			claimTimes  []time.Time
			claimStep   int
			leaseDoneAt time.Time
		)
		allDone := make(chan struct{})

		detailExec := resolveExecutor(func(ctx context.Context, _ fetch.Request) (fetch.Response, error) {
			mu.Lock()
			step := claimStep
			mu.Unlock()
			if step == 1 {
				// Step 1: simulate a task that exceeds its 150ms lease deadline.
				<-ctx.Done()
				mu.Lock()
				leaseDoneAt = time.Now()
				mu.Unlock()
				return fetch.Response{}, ctx.Err()
			}
			return fetch.Response{StatusCode: 200, Body: interopDetailBody("v26-web.douyinvod.com", "v11-weba.douyinvod.com")}, nil
		})
		probeFn := resolverFunc(func(_ context.Context, _ string) ([]unified.Object, error) {
			return []unified.Object{deliveryRow("v26-cold.douyinvod.com", "objectA", "a")}, nil
		})
		control := resolveExecutor(func(ctx context.Context, req fetch.Request) (fetch.Response, error) {
			switch req.URL {
			case origin + ClaimPath:
				now := time.Now()
				mu.Lock()
				claimStep++
				step := claimStep
				claimTimes = append(claimTimes, now)
				mu.Unlock()
				switch step {
				case 1:
					// Job 1: short 150ms lease that will time out during detail lookup.
					job := PullJob{
						Protocol:   ProtocolV2,
						TaskID:     strings.Repeat("1", 32),
						AttemptID:  strings.Repeat("a", 32),
						AwemeID:    interopAwemeID,
						DeadlineMS: now.Add(150 * time.Millisecond).UnixMilli(),
					}
					raw, _ := json.Marshal(job)
					return fetch.Response{StatusCode: http.StatusOK, Body: raw}, nil
				case 2, 3:
					// Job 2 & 3: valid leases whose ResultPath will fail (HTTP 502, then network error).
					ch := string('0' + byte(step))
					job := PullJob{
						Protocol:   ProtocolV2,
						TaskID:     strings.Repeat(ch, 32),
						AttemptID:  strings.Repeat("b", 32),
						AwemeID:    interopAwemeID,
						DeadlineMS: now.Add(2 * time.Second).UnixMilli(),
					}
					raw, _ := json.Marshal(job)
					return fetch.Response{StatusCode: http.StatusOK, Body: raw}, nil
				default:
					close(allDone)
					<-ctx.Done()
					return fetch.Response{}, ctx.Err()
				}
			case origin + ResultPath:
				mu.Lock()
				step := claimStep
				mu.Unlock()
				if step == 2 {
					return fetch.Response{StatusCode: http.StatusBadGateway}, nil
				}
				return fetch.Response{}, errors.New("result connection reset")
			default:
				return fetch.Response{}, errors.New("unexpected URL")
			}
		})

		w, err := NewPullWorkerWithDetail(origin, wireToken, control, detailExec, probeFn)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		loopDone := make(chan error, 1)
		go func() { loopDone <- w.runLoop(ctx) }()

		select {
		case <-allDone:
		case <-time.After(2 * time.Second):
			t.Fatal("worker backed off instead of immediately claiming next tasks")
		}
		cancel()
		<-loopDone

		mu.Lock()
		defer mu.Unlock()
		if len(claimTimes) < 4 {
			t.Fatalf("expected 4 claims, got %d", len(claimTimes))
		}
		if gapAfterLeaseTimeout := claimTimes[1].Sub(leaseDoneAt); gapAfterLeaseTimeout > 250*time.Millisecond {
			t.Fatalf("claim after lease timeout took %v (want <1s without backoff)", gapAfterLeaseTimeout)
		}
		if gapAfterResult502 := claimTimes[2].Sub(claimTimes[1]); gapAfterResult502 > 500*time.Millisecond {
			t.Fatalf("claim after result HTTP 502 took %v (want <1s without backoff)", gapAfterResult502)
		}
		if gapAfterResultNetErr := claimTimes[3].Sub(claimTimes[2]); gapAfterResultNetErr > 500*time.Millisecond {
			t.Fatalf("claim after result network error took %v (want <1s without backoff)", gapAfterResultNetErr)
		}
	})

	t.Run("claim_failures_still_backoff", func(t *testing.T) {
		var (
			mu         sync.Mutex
			claimTimes []time.Time
		)
		thirdClaim := make(chan struct{})
		control := resolveExecutor(func(ctx context.Context, req fetch.Request) (fetch.Response, error) {
			mu.Lock()
			claimTimes = append(claimTimes, time.Now())
			n := len(claimTimes)
			mu.Unlock()
			switch n {
			case 1:
				return fetch.Response{}, errors.New("claim dial error")
			case 2:
				return fetch.Response{StatusCode: http.StatusBadGateway}, nil
			default:
				close(thirdClaim)
				<-ctx.Done()
				return fetch.Response{}, ctx.Err()
			}
		})
		w, err := NewPullWorker(origin, wireToken, control, resolverFunc(func(context.Context, string) ([]unified.Object, error) {
			return nil, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		loopDone := make(chan error, 1)
		go func() { loopDone <- w.runLoop(ctx) }()

		select {
		case <-thirdClaim:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for backed-off claim retries")
		}
		cancel()
		<-loopDone

		mu.Lock()
		defer mu.Unlock()
		if firstBackoff := claimTimes[1].Sub(claimTimes[0]); firstBackoff < 900*time.Millisecond {
			t.Fatalf("claim network error did not back off ~1s: got %v", firstBackoff)
		}
		if secondBackoff := claimTimes[2].Sub(claimTimes[1]); secondBackoff < 1800*time.Millisecond {
			t.Fatalf("claim HTTP 502 did not double backoff ~2s: got %v", secondBackoff)
		}
	})
}

func TestPullWorkerThreeConcurrentJobsAndFourthImmediateFallback(t *testing.T) {
	const origin = "https://japan.example.org"
	awemeIDs := []string{
		"7704056737233449611",
		"7704056737233449612",
		"7704056737233449613",
		"7704056737233449614",
		"7704056737233449615",
	}

	detailFor := func(awemeID, host1080 string) []byte {
		objKey := "/video/tos/cn/tos-cn-v-0015c001-alinc2/oConcurrent" + awemeID[len(awemeID)-2:]
		u1080 := "https://" + host1080 + "/" + strings.Repeat("1", 32) + "/6a000001" + objKey + "/?a=6383&mime_type=video_mp4"
		www := "https://www.douyin.com/aweme/v1/play/?video_id=" + wireVideo
		return []byte(`{"status_code":0,"aweme_detail":{"aweme_id":"` + awemeID + `","video":{"duration":13667,"play_addr":{"uri":"` + wireVideo + `"},"bit_rate":[` +
			`{"gear_name":"normal_1080_0","format":"mp4","bit_rate":2100000,"play_addr":{"uri":"` + wireVideo + `","file_hash":"h` + awemeID + `","width":1920,"height":1080,"data_size":3500000,"url_list":["` + u1080 + `","` + www + `"]}}` +
			`]}}}`)
	}

	var active atomic.Int32
	allThreeActive := make(chan struct{})
	releaseFastTwo := make(chan struct{})
	releaseSlowThird := make(chan struct{})

	shDetailExec := resolveExecutor(func(ctx context.Context, req fetch.Request) (fetch.Response, error) {
		for idx, id := range awemeIDs {
			if strings.Contains(req.URL, "aweme_id="+id) {
				if idx < 3 {
					if active.Add(1) == pullWorkers {
						close(allThreeActive)
					}
					gate := releaseFastTwo
					if idx == 2 {
						gate = releaseSlowThird
					}
					select {
					case <-gate:
					case <-ctx.Done():
						return fetch.Response{}, ctx.Err()
					}
				}
				shHost := fmt.Sprintf("v%s-weba.douyinvod.com", id[len(id)-2:])
				return fetch.Response{StatusCode: 200, Body: detailFor(id, shHost)}, nil
			}
		}
		return fetch.Response{}, fmt.Errorf("unexpected Shanghai detail request %q", req.URL)
	})
	shProbe := resolverFunc(func(_ context.Context, _ string) ([]unified.Object, error) {
		return []unified.Object{deliveryRow("v26-cold.douyinvod.com", "objectA", "a")}, nil
	})
	jpDetailExec := resolveExecutor(func(_ context.Context, req fetch.Request) (fetch.Response, error) {
		for _, id := range awemeIDs {
			if strings.Contains(req.URL, "aweme_id="+id) {
				return fetch.Response{StatusCode: 200, Body: detailFor(id, "v3-dy-ov.zjcdn.com")}, nil
			}
		}
		return fetch.Response{}, fmt.Errorf("unexpected Japan detail request %q", req.URL)
	})

	broker, err := NewPullBroker(wireToken)
	if err != nil {
		t.Fatal(err)
	}
	broker.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	srv := httptest.NewTLSServer(broker)
	defer srv.Close()

	worker, err := NewPullWorkerWithDetail(origin, wireToken, controlTransportForTLS(srv, origin), shDetailExec, shProbe)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx) }()
	waitForPullCount(t, broker, pullWorkers)

	var jpFallbackProbes atomic.Int32
	jpRow := deliveryRow("japan-fallback.zjcdn.com", "objectJP4", "jp4")
	parser := douyin.NewWithOriginalResolver(jpDetailExec, japanProbeFunc(func(_ context.Context, _ string) (unified.Object, error) {
		jpFallbackProbes.Add(1)
		return jpRow, nil
	}), broker)

	now := time.Now().Unix()
	type parseRes struct {
		encoded []byte
		err     error
	}
	results := make([]chan parseRes, 3)
	for i := range 3 {
		ch := make(chan parseRes, 1)
		results[i] = ch
		id := awemeIDs[i]
		go func() {
			data, pErr := parser.Parse(t.Context(), "https://www.douyin.com/video/"+id, now)
			if pErr != nil {
				ch <- parseRes{err: pErr}
				return
			}
			view := unified.PublicView(data, false, now)
			if vErr := unified.ValidateEnvelopeAt(view, now); vErr != nil {
				ch <- parseRes{err: vErr}
				return
			}
			raw, _ := json.Marshal(view)
			ch <- parseRes{encoded: raw}
		}()
	}

	select {
	case <-allThreeActive:
	case <-time.After(3 * time.Second):
		t.Fatalf("did not dispatch 3 concurrent jobs simultaneously (active=%d)", active.Load())
	}

	// 1. While all 3 workers are busy, the 4th parse must immediately fall back to Japan without waiting.
	fourthStart := time.Now()
	fourthData, err := parser.Parse(t.Context(), "https://www.douyin.com/video/"+awemeIDs[3], now)
	fourthElapsed := time.Since(fourthStart)
	if err != nil {
		t.Fatalf("4th concurrent parse failed: %v", err)
	}
	if fourthElapsed > 500*time.Millisecond {
		t.Fatalf("4th parse blocked while all 3 workers were busy: elapsed=%v", fourthElapsed)
	}
	fourthView := unified.PublicView(fourthData, false, now)
	if err := unified.ValidateEnvelopeAt(fourthView, now); err != nil {
		t.Fatal(err)
	}
	fourthRaw, _ := json.Marshal(fourthView)
	if !bytes.Contains(fourthRaw, []byte("japan-fallback.zjcdn.com")) || bytes.Contains(fourthRaw, []byte("douyinvod.com")) {
		t.Fatalf("4th parse did not take pure Japan fallback: %s", fourthRaw)
	}
	if jpFallbackProbes.Load() != 1 {
		t.Fatalf("expected exactly 1 Japan fallback probe (for the 4th request), got %d", jpFallbackProbes.Load())
	}

	// 2. Release 2 of the 3 jobs while job #2 remains slow/stalled; verify the 2 freed workers
	//    immediately re-register and serve a 5th job on Shanghai without waiting for job #2.
	close(releaseFastTwo)
	for i := range 2 {
		res := <-results[i]
		if res.err != nil {
			t.Fatalf("concurrent parse %d failed: %v", i, res.err)
		}
		wantHost := fmt.Sprintf("v%s-weba.douyinvod.com", awemeIDs[i][len(awemeIDs[i])-2:])
		if !bytes.Contains(res.encoded, []byte(wantHost)) {
			t.Fatalf("concurrent parse %d missing its own Shanghai rendition (%s): %s", i, wantHost, res.encoded)
		}
	}
	waitForPullCount(t, broker, 2)

	fifthData, err := parser.Parse(t.Context(), "https://www.douyin.com/video/"+awemeIDs[4], now)
	if err != nil {
		t.Fatalf("5th parse while 1 worker remained stalled failed: %v", err)
	}
	fifthRaw, _ := json.Marshal(unified.PublicView(fifthData, false, now))
	if !bytes.Contains(fifthRaw, []byte("v15-weba.douyinvod.com")) {
		t.Fatalf("5th parse was blocked by 1 slow worker instead of hitting idle workers: %s", fifthRaw)
	}

	// 3. Release the 3rd stalled worker and verify its matched result.
	close(releaseSlowThird)
	res2 := <-results[2]
	if res2.err != nil {
		t.Fatalf("concurrent parse 2 failed: %v", res2.err)
	}
	if !bytes.Contains(res2.encoded, []byte("v13-weba.douyinvod.com")) || !bytes.Contains(res2.encoded, []byte("v26-cold.douyinvod.com")) {
		t.Fatalf("concurrent parse 2 missing its own matched Shanghai rendition/original: %s", res2.encoded)
	}

	cancel()
	<-workerDone
}
