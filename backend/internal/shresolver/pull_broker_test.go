package shresolver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/douyin"
)

func waitForPullWaiter(t *testing.T, b *PullBroker) {
	t.Helper()
	stop := time.After(time.Second * 3)
	for {
		b.mu.Lock()
		ready := len(b.waiting) >= 1
		b.mu.Unlock()
		if ready {
			return
		}
		select {
		case <-stop:
			t.Fatal("did not observe a registered claim")
		case <-time.After(time.Millisecond):
		}
	}
}

func waitForPullCount(t *testing.T, b *PullBroker, n int) {
	t.Helper()
	stop := time.After(time.Second * 3)
	for {
		b.mu.Lock()
		matched := len(b.waiting) == n
		b.mu.Unlock()
		if matched {
			return
		}
		select {
		case <-stop:
			t.Fatalf("did not observe %d registered claims", n)
		case <-time.After(time.Millisecond):
		}
	}
}

func pullTestRequest(b *PullBroker, ctx context.Context, path, token string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r)
	return w
}

func pullTestBroker(t *testing.T) *PullBroker {
	t.Helper()
	b, err := NewPullBroker(wireToken)
	if err != nil {
		t.Fatal(err)
	}
	b.now = func() time.Time { return time.Unix(wireNow, 0) }
	b.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	return b
}

func beginPullTest(t *testing.T, b *PullBroker, ctx context.Context) (PullJob, <-chan error) {
	t.Helper()
	claim := make(chan *httptest.ResponseRecorder, 1)
	go func() { claim <- pullTestRequest(b, t.Context(), ClaimPath, wireToken, nil) }()
	waitForPullWaiter(t, b)
	done := make(chan error, 1)
	go func() {
		rows, err := b.ResolveOriginal(ctx, wireVideo)
		if err == nil && (len(rows) != 1 || rows[0]["source_key"] != "v3-dy-o.zjcdn.com") {
			err = errors.New("selected candidate missing")
		}
		done <- err
	}()
	select {
	case w := <-claim:
		var j PullJob
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &j) != nil || j.VideoID != wireVideo || len(j.TaskID) != 32 || len(j.AttemptID) != 32 {
			t.Fatalf("bad lease: %v", w)
		}
		return j, done
	case <-time.After(3 * time.Second):
		t.Fatal("worker never got its job")
	}
	panic("unreachable")
}

func postPullTest(b *PullBroker, j PullJob, media Response) int {
	payload, _ := json.Marshal(media)
	body, _ := json.Marshal(PullResult{PullJob: j, Result: payload})
	return pullTestRequest(b, context.Background(), ResultPath, wireToken, body).Code
}

func TestPullMatchingIsSingleUseAndBoundToLeaseAndVideo(t *testing.T) {
	b := pullTestBroker(t)
	if _, err := b.ResolveOriginal(t.Context(), wireVideo); !errors.Is(err, errUnavailable) {
		t.Fatal("offline worker must fall back immediately", err)
	}
	j, done := beginPullTest(t, b, t.Context())
	for _, change := range []func(*PullJob){func(j *PullJob) { j.TaskID = "another" }, func(j *PullJob) { j.AttemptID = "another" }, func(j *PullJob) { j.VideoID = "v0000000000999" }, func(j *PullJob) { j.DeadlineMS++ }} {
		wrong := j
		change(&wrong)
		if got := postPullTest(b, wrong, goodResponse()); got != 409 {
			t.Fatalf("mismatched lease accepted: %d", got)
		}
	}
	wrong := goodResponse()
	wrong.VideoID = "v0000000000999"
	if got := postPullTest(b, j, wrong); got != 409 {
		t.Fatalf("unrelated media receipt accepted: %d", got)
	}
	if got := postPullTest(b, j, goodResponse()); got != 204 {
		t.Fatalf("good result rejected: %d", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := postPullTest(b, j, goodResponse()); got != 409 {
		t.Fatal("duplicate receipt accepted")
	}
}

func TestPullLateOrCancelledResultsCannotSatisfyNewRequest(t *testing.T) {
	b := pullTestBroker(t)
	c, cancel := context.WithCancel(t.Context())
	j, previous := beginPullTest(t, b, c)
	cancel()
	if err := <-previous; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	c2, stop := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer stop()
	j2, expired := beginPullTest(t, b, c2)
	deadline, _ := c2.Deadline()
	if j2.DeadlineMS != deadline.UnixMilli() || j.TaskID == j2.TaskID || j.AttemptID == j2.AttemptID {
		t.Fatal("old identity reused or parent's budget reset")
	}
	if postPullTest(b, j, goodResponse()) != 409 {
		t.Fatal("cancelled lease replaced active request")
	}
	if err := <-expired; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	if postPullTest(b, j2, goodResponse()) != 409 {
		t.Fatal("late receipt accepted")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) != 0 {
		t.Fatalf("%d abandoned leases", len(b.pending))
	}
}

func TestPullCapacityIsIndependentForWaitingAndClaimedJobs(t *testing.T) {
	b := pullTestBroker(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	claims := make(chan *httptest.ResponseRecorder, pullCapacity)
	for range pullCapacity {
		go func() { claims <- pullTestRequest(b, ctx, ClaimPath, wireToken, nil) }()
	}
	waitForPullCount(t, b, pullCapacity)
	if w := pullTestRequest(b, ctx, ClaimPath, wireToken, nil); w.Code != 429 {
		t.Fatal("poll capacity not enforced", w.Code)
	}
	if w := pullTestRequest(b, ctx, ResultPath, wireToken, []byte("{}")); w.Code != 400 {
		t.Fatal("waiting polls exhausted result capacity", w.Code)
	}
	resolved := make(chan error, pullCapacity)
	for range pullCapacity {
		go func() { _, err := b.ResolveOriginal(ctx, wireVideo); resolved <- err }()
	}
	for range pullCapacity {
		if w := <-claims; w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	rest := make(chan *httptest.ResponseRecorder, 1)
	go func() { rest <- pullTestRequest(b, ctx, ClaimPath, wireToken, nil) }()
	waitForPullWaiter(t, b)
	if _, err := b.ResolveOriginal(ctx, wireVideo); !errors.Is(err, errUnavailable) {
		t.Fatal("outstanding job capacity not enforced", err)
	}
	cancel()
	for range pullCapacity {
		if err := <-resolved; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if (<-rest).Code != 204 {
		t.Fatal("waiting poll did not end")
	}
}

func TestPullControlRejectsUnauthorizedAndInvalidBodies(t *testing.T) {
	if _, err := NewPullBroker("short"); err == nil {
		t.Fatal("short token accepted")
	}
	b := pullTestBroker(t)
	for _, target := range []string{ClaimPath, ResultPath} {
		for _, token := range []string{"", "wrong"} {
			if r := pullTestRequest(b, t.Context(), target, token, nil); r.Code != 401 {
				t.Fatal(r.Code)
			}
		}
		r := httptest.NewRequest("GET", target, nil)
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		if w.Code != 405 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code)
		}
		if r := pullTestRequest(b, t.Context(), target+"?token=no", wireToken, nil); r.Code != 404 {
			t.Fatal("permitted query")
		}
	}
	if w := pullTestRequest(b, t.Context(), ClaimPath, wireToken, []byte("{}")); w.Code != 400 {
		t.Fatal("claim accepted body")
	}
	if w := pullTestRequest(b, t.Context(), ResultPath, wireToken, []byte(strings.Repeat(" ", maxResponseBytes+513)+"{}")); w.Code != 400 {
		t.Fatal("unbounded result")
	}
	j, done := beginPullTest(t, b, t.Context())
	// The correct identifiers cannot grant access to a fabricated/private origin.
	private := goodResponse()
	private.Candidates[0].URL = "https://127.0.0.1" + mediaPath
	if got := postPullTest(b, j, private); got != 204 {
		t.Fatalf("receipt not correlated: %d", got)
	}
	if err := <-done; err == nil {
		t.Fatal("unsafe candidate passed response validation")
	}
}

type delayedReader struct {
	first []byte
	rest  []byte
	delay time.Duration
	step  int
}

func (r *delayedReader) Read(p []byte) (int, error) {
	switch r.step {
	case 0:
		r.step = 1
		return copy(p, r.first), nil
	case 1:
		r.step = 2
		time.Sleep(r.delay)
		return copy(p, r.rest), nil
	default:
		return 0, io.EOF
	}
}

func TestSlowResultTimeoutDoesNotPoisonKeepAliveClaim(t *testing.T) {
	b := pullTestBroker(t)
	srv := httptest.NewUnstartedServer(b)
	srv.Config.ReadHeaderTimeout = 5 * time.Second
	srv.Config.WriteTimeout = 50 * time.Second
	srv.Config.IdleTimeout = 60 * time.Second
	srv.Start()
	defer srv.Close()

	var dials atomic.Int32
	tr := &http.Transport{
		MaxConnsPerHost:     1,
		MaxIdleConnsPerHost: 1,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   false,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}

	// 1. Send a result request whose body read exceeds the 3s read deadline,
	//    then finishes sending the remaining bytes on the same connection.
	first := []byte(`{"task_id":"`)
	rest := []byte(`00000000000000000000000000000000","attempt_id":"00000000000000000000000000000000","result":null}`)
	req1, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+ResultPath, &delayedReader{
		first: first,
		rest:  rest,
		delay: 3200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	req1.ContentLength = int64(len(first) + len(rest))
	req1.Header.Set("Authorization", "Bearer "+wireToken)
	req1.Header.Set("Content-Type", "application/json")
	res1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("result request error: %v", err)
	}
	_, _ = io.Copy(io.Discard, res1.Body)
	res1.Body.Close()
	if res1.StatusCode != http.StatusBadRequest {
		t.Fatalf("slow result status = %d, want 400", res1.StatusCode)
	}

	// 2. Send claim through the same single-connection keep-alive client.
	//    Without the fix, the poisoned connection is reused and claim returns 204 immediately.
	type claimOutcome struct {
		res     *http.Response
		body    []byte
		err     error
		elapsed time.Duration
	}
	claimCtx, cancelClaim := context.WithCancel(t.Context())
	defer cancelClaim()
	claimCh := make(chan claimOutcome, 1)
	go func() {
		req2, reqErr := http.NewRequestWithContext(claimCtx, http.MethodPost, srv.URL+ClaimPath, nil)
		if reqErr != nil {
			claimCh <- claimOutcome{err: reqErr}
			return
		}
		req2.Header.Set("Authorization", "Bearer "+wireToken)
		start := time.Now()
		res2, doErr := client.Do(req2)
		elapsed := time.Since(start)
		var body []byte
		if doErr == nil {
			body, doErr = io.ReadAll(res2.Body)
			res2.Body.Close()
		}
		claimCh <- claimOutcome{res: res2, body: body, err: doErr, elapsed: elapsed}
	}()

	select {
	case early := <-claimCh:
		if early.err != nil {
			t.Fatalf("claim failed early (%v) after %v", early.err, early.elapsed)
		}
		t.Fatalf("claim returned HTTP %d immediately in %v (dials=%d) instead of waiting for work",
			early.res.StatusCode, early.elapsed, dials.Load())
	case <-time.After(250 * time.Millisecond):
	}

	// net/http ReadResponse strips Header["Connection"] and sets Response.Close = true when "Connection: close" is on the wire.
	if !res1.Close {
		t.Fatalf("slow result did not send Connection: close (res1.Close=%v)", res1.Close)
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("expected second dial after slow result closed conn #1, got dials=%d", got)
	}
	waitForPullWaiter(t, b)

	// 3. Dispatch a real job on connection #2, post a valid receipt on connection #2,
	//    and verify a subsequent >3s claim on connection #2 is unaffected by receive's deadline.
	resolveDone := make(chan error, 1)
	go func() {
		_, rErr := b.ResolveOriginal(t.Context(), wireVideo)
		resolveDone <- rErr
	}()
	claimed := <-claimCh
	if claimed.err != nil || claimed.res.StatusCode != http.StatusOK {
		t.Fatalf("claim outcome: err=%v res=%v", claimed.err, claimed.res)
	}
	var job PullJob
	if err := json.Unmarshal(claimed.body, &job); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(goodResponse())
	receiptBody, _ := json.Marshal(PullResult{PullJob: job, Result: payload})
	req3, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+ResultPath, bytes.NewReader(receiptBody))
	if err != nil {
		t.Fatal(err)
	}
	req3.Header.Set("Authorization", "Bearer "+wireToken)
	req3.Header.Set("Content-Type", "application/json")
	res3, err := client.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res3.Body)
	res3.Body.Close()
	if res3.StatusCode != http.StatusNoContent || res3.Close {
		t.Fatalf("normal receipt status=%d close=%v", res3.StatusCode, res3.Close)
	}
	if err := <-resolveDone; err != nil {
		t.Fatal(err)
	}

	// Connection #2 must remain reused (dials==2) and have no leftover 3s read deadline.
	holdCtx, cancelHold := context.WithTimeout(t.Context(), 3400*time.Millisecond)
	defer cancelHold()
	req4, err := http.NewRequestWithContext(holdCtx, http.MethodPost, srv.URL+ClaimPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req4.Header.Set("Authorization", "Bearer "+wireToken)
	res4, err := client.Do(req4)
	if err == nil {
		res4.Body.Close()
		t.Fatalf("follow-up claim ended early with HTTP %d (leftover read deadline?)", res4.StatusCode)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("follow-up claim error = %v, want context.DeadlineExceeded", err)
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("healthy keep-alive connection was not reused across receipt+claim: dials=%d, want 2", got)
	}
}

func TestPullClaimCanceledContextClosesConnection(t *testing.T) {
	b := pullTestBroker(t)
	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	rec := pullTestRequest(b, canceledCtx, ClaimPath, wireToken, nil)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Connection") != "close" {
		t.Fatalf("canceled claim: code=%d Connection=%q, want 204 with Connection: close", rec.Code, rec.Header().Get("Connection"))
	}

	// End-to-end HTTP/1.1 keep-alive recovery when the first connection's context is already canceled.
	var connSeq atomic.Int32
	srv := httptest.NewUnstartedServer(b)
	srv.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		if connSeq.Add(1) == 1 {
			c, stop := context.WithCancel(ctx)
			stop()
			return c
		}
		return ctx
	}
	srv.Start()
	defer srv.Close()

	var dials atomic.Int32
	tr := &http.Transport{
		MaxConnsPerHost:     1,
		MaxIdleConnsPerHost: 1,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   false,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}

	req1, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+ClaimPath, nil)
	req1.Header.Set("Authorization", "Bearer "+wireToken)
	res1, err := client.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	res1.Body.Close()
	if res1.StatusCode != http.StatusNoContent || !res1.Close {
		t.Fatalf("claim on poisoned conn #1: status=%d Close=%v", res1.StatusCode, res1.Close)
	}

	// Next claim on the same single-connection client must dial conn #2, wait the normal 15s PullWait,
	// return 204 without Connection: close, and keep conn #2 reusable.
	start := time.Now()
	req2, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+ClaimPath, nil)
	req2.Header.Set("Authorization", "Bearer "+wireToken)
	res2, err := client.Do(req2)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusNoContent || res2.Close || elapsed < PullWait-500*time.Millisecond {
		t.Fatalf("normal 15s claim timeout: status=%d Close=%v elapsed=%v", res2.StatusCode, res2.Close, elapsed)
	}

	holdCtx, cancelHold := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancelHold()
	req3, _ := http.NewRequestWithContext(holdCtx, http.MethodPost, srv.URL+ClaimPath, nil)
	req3.Header.Set("Authorization", "Bearer "+wireToken)
	res3, err := client.Do(req3)
	if err == nil {
		res3.Body.Close()
		t.Fatalf("follow-up claim on conn #2 returned HTTP %d instead of waiting", res3.StatusCode)
	}
	if !errors.Is(err, context.DeadlineExceeded) || dials.Load() != 2 {
		t.Fatalf("conn #2 reuse after normal 15s timeout: err=%v dials=%d, want DeadlineExceeded and dials=2", err, dials.Load())
	}
}

func randomBrokerToken(t *testing.T) string {
	t.Helper()
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(buf[:])
}

func pullV2ClaimRequest(b *PullBroker, ctx context.Context, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, ClaimPath, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set(ProtocolHeader, "2")
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r)
	return w
}

func waitForQueueCount(t *testing.T, b *PullBroker, n int) {
	t.Helper()
	stop := time.After(2 * time.Second)
	for {
		b.mu.Lock()
		got := len(b.queue)
		b.mu.Unlock()
		if got == n {
			return
		}
		select {
		case <-stop:
			t.Fatalf("did not observe %d queued requests (got %d)", n, got)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestPullShortWaitQueueHandsOffWithin300msAndRejectsLateWorker(t *testing.T) {
	token := randomBrokerToken(t)
	b, err := NewPullBroker(token)
	if err != nil {
		t.Fatal(err)
	}
	b.now = func() time.Time { return time.Unix(wireNow, 0) }
	b.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}

	// 1. Queued v2 ResolveWork (no worker initially idle) is claimed when a worker arrives within 300ms,
	//    preserving FIFO order across queued requests, while an unmatched request falls back after ~300ms.
	awemeIDs := []string{"7704056737233449701", "7704056737233449702", "7704056737233449703"}
	outcomes := make([]chan error, 3)
	for i, id := range awemeIDs {
		ch := make(chan error, 1)
		outcomes[i] = ch
		aweme := id
		go func() {
			work, rErr := b.ResolveWork(t.Context(), aweme)
			if rErr == nil && (work == nil || work.VideoID != wireVideo) {
				rErr = errors.New("missing validated work")
			}
			ch <- rErr
		}()
		waitForQueueCount(t, b, i+1)
	}

	time.Sleep(60 * time.Millisecond)
	for idx := range 2 {
		rec := pullV2ClaimRequest(b, t.Context(), token)
		if rec.Code != http.StatusOK {
			t.Fatalf("claim %d status=%d, want 200", idx, rec.Code)
		}
		var job PullJob
		if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if job.Protocol != ProtocolV2 || job.AwemeID != awemeIDs[idx] {
			t.Fatalf("FIFO violation on claim %d: got aweme=%q want %q", idx, job.AwemeID, awemeIDs[idx])
		}
		workResp := WorkResponse{
			Protocol:   ProtocolV2,
			AwemeID:    job.AwemeID,
			VideoID:    wireVideo,
			Candidates: goodResponse().Candidates,
		}
		rawWork, _ := json.Marshal(workResp)
		rawReceipt, _ := json.Marshal(PullResult{PullJob: job, Result: rawWork})
		if ack := pullTestRequest(b, t.Context(), ResultPath, token, rawReceipt); ack.Code != http.StatusNoContent {
			t.Fatalf("result %d status=%d, want 204", idx, ack.Code)
		}
		if rErr := <-outcomes[idx]; rErr != nil {
			t.Fatalf("queued ResolveWork %d failed: %v", idx, rErr)
		}
	}

	// Request #3 receives no worker within 300ms and must time out cleanly with errUnavailable.
	select {
	case err3 := <-outcomes[2]:
		if !errors.Is(err3, errUnavailable) {
			t.Fatalf("3rd queued request error = %v, want errUnavailable", err3)
		}
	case <-time.After(time.Second):
		t.Fatal("3rd queued request did not fall back after 300ms queue window")
	}

	b.mu.Lock()
	qLen, pLen := len(b.queue), len(b.pending)
	b.mu.Unlock()
	if qLen != 0 || pLen != 0 {
		t.Fatalf("stale state after queue timeout: queue=%d pending=%d", qLen, pLen)
	}

	// A late worker arriving AFTER 300ms must NOT claim the already-abandoned 3rd request;
	// instead it must park in b.waiting until cancelled.
	lateCtx, cancelLate := context.WithCancel(t.Context())
	lateRec := make(chan *httptest.ResponseRecorder, 1)
	go func() { lateRec <- pullV2ClaimRequest(b, lateCtx, token) }()
	waitForPullCount(t, b, 1)
	cancelLate()
	if rec := <-lateRec; rec.Code != http.StatusNoContent {
		t.Fatalf("late worker unexpectedly claimed abandoned task: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 2. Legacy v1 worker arriving while ResolveWork is queued immediately wakes ResolveWork with ErrProtocolV1
	//    and stays parked for the follow-up ResolveOriginal call.
	v2ErrCh := make(chan error, 1)
	startV1 := time.Now()
	go func() {
		_, rErr := b.ResolveWork(t.Context(), awemeIDs[0])
		v2ErrCh <- rErr
	}()
	waitForQueueCount(t, b, 1)

	v1ClaimCtx, cancelV1Claim := context.WithCancel(t.Context())
	defer cancelV1Claim()
	v1ClaimCh := make(chan *httptest.ResponseRecorder, 1)
	go func() { v1ClaimCh <- pullTestRequest(b, v1ClaimCtx, ClaimPath, token, nil) }()

	if rErr := <-v2ErrCh; !errors.Is(rErr, douyin.ErrProtocolV1) || time.Since(startV1) > 150*time.Millisecond {
		t.Fatalf("v1 worker arrival did not immediately wake ResolveWork with ErrProtocolV1: err=%v elapsed=%v", rErr, time.Since(startV1))
	}
	origDone := make(chan error, 1)
	go func() {
		_, oErr := b.ResolveOriginal(t.Context(), wireVideo)
		origDone <- oErr
	}()
	v1Rec := <-v1ClaimCh
	if v1Rec.Code != http.StatusOK {
		t.Fatalf("preserved v1 worker did not receive ResolveOriginal job: %d", v1Rec.Code)
	}
	var v1Job PullJob
	if err := json.Unmarshal(v1Rec.Body.Bytes(), &v1Job); err != nil {
		t.Fatal(err)
	}
	rawMedia, _ := json.Marshal(goodResponse())
	rawV1Receipt, _ := json.Marshal(PullResult{PullJob: v1Job, Result: rawMedia})
	if ack := pullTestRequest(b, t.Context(), ResultPath, token, rawV1Receipt); ack.Code != http.StatusNoContent {
		t.Fatalf("v1 receipt status=%d", ack.Code)
	}
	if oErr := <-origDone; oErr != nil {
		t.Fatalf("ResolveOriginal after ErrProtocolV1 wakeup failed: %v", oErr)
	}
}

func TestPullShortWaitQueueCancellationFreesSlotAndSurvivesRace(t *testing.T) {
	token := randomBrokerToken(t)
	b, err := NewPullBroker(token)
	if err != nil {
		t.Fatal(err)
	}
	b.now = func() time.Time { return time.Unix(wireNow, 0) }
	b.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}

	// 1. Fill all pullQueueCapacity slots; verify (pullQueueCapacity+1)th fails fast without waiting.
	cancels := make([]context.CancelFunc, pullQueueCapacity)
	errs := make([]chan error, pullQueueCapacity)
	for i := range pullQueueCapacity {
		cCtx, cancel := context.WithCancel(t.Context())
		cancels[i] = cancel
		ch := make(chan error, 1)
		errs[i] = ch
		id := fmt.Sprintf("77040567372334%05d", 10000+i)
		go func() {
			_, rErr := b.ResolveWork(cCtx, id)
			ch <- rErr
		}()
	}
	waitForQueueCount(t, b, pullQueueCapacity)

	overflowStart := time.Now()
	_, overflowErr := b.ResolveWork(t.Context(), "7704056737233499999")
	if !errors.Is(overflowErr, errUnavailable) || time.Since(overflowStart) > 50*time.Millisecond {
		t.Fatalf("overflow request when queue full: err=%v elapsed=%v", overflowErr, time.Since(overflowStart))
	}

	// 2. Cancelling slot #0 must exit immediately with context.Canceled and free its slot right away.
	cancelStart := time.Now()
	cancels[0]()
	if cErr := <-errs[0]; !errors.Is(cErr, context.Canceled) || time.Since(cancelStart) > 100*time.Millisecond {
		t.Fatalf("cancelled queued request did not exit promptly: err=%v elapsed=%v", cErr, time.Since(cancelStart))
	}
	waitForQueueCount(t, b, pullQueueCapacity-1)

	// Cancel all remaining queued requests and confirm a subsequent worker never sees a cancelled job.
	for i := 1; i < pullQueueCapacity; i++ {
		cancels[i]()
	}
	for i := 1; i < pullQueueCapacity; i++ {
		if cErr := <-errs[i]; !errors.Is(cErr, context.Canceled) {
			t.Fatalf("slot %d cancel err=%v", i, cErr)
		}
	}
	waitForQueueCount(t, b, 0)

	checkCtx, cancelCheck := context.WithCancel(t.Context())
	checkCh := make(chan *httptest.ResponseRecorder, 1)
	go func() { checkCh <- pullV2ClaimRequest(b, checkCtx, token) }()
	waitForPullCount(t, b, 1)
	cancelCheck()
	if rec := <-checkCh; rec.Code != http.StatusNoContent {
		t.Fatalf("worker received cancelled task: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 3. Concurrent race stress: overlapping cancellations, short queue timeouts, and live worker handoffs.
	b.queueWait = 40 * time.Millisecond
	var wg sync.WaitGroup
	for i := range 48 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cCtx, cancel := context.WithTimeout(t.Context(), time.Duration(10+(idx%50))*time.Millisecond)
			defer cancel()
			if idx%3 == 0 {
				go func() {
					time.Sleep(time.Duration(idx%25) * time.Millisecond)
					cancel()
				}()
			}
			_, _ = b.ResolveWork(cCtx, fmt.Sprintf("77040567372335%05d", 10000+idx))
		}(i)
		if i%2 == 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				claimCtx, stopClaim := context.WithTimeout(t.Context(), 60*time.Millisecond)
				defer stopClaim()
				rec := pullV2ClaimRequest(b, claimCtx, token)
				if rec.Code == http.StatusOK {
					var job PullJob
					if json.Unmarshal(rec.Body.Bytes(), &job) == nil {
						workResp := WorkResponse{Protocol: ProtocolV2, AwemeID: job.AwemeID, VideoID: wireVideo, Candidates: goodResponse().Candidates}
						rawWork, _ := json.Marshal(workResp)
						rawReceipt, _ := json.Marshal(PullResult{PullJob: job, Result: rawWork})
						_ = pullTestRequest(b, t.Context(), ResultPath, token, rawReceipt)
					}
				}
			}()
		}
	}
	wg.Wait()

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) != 0 || len(b.pending) != 0 {
		t.Fatalf("leaked broker entries after race stress: queue=%d pending=%d", len(b.queue), len(b.pending))
	}
}
