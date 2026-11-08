package shresolver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func waitForPullWaiter(t *testing.T, b *PullBroker) {
	t.Helper()
	waitForPullCount(t, b, 1)
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
