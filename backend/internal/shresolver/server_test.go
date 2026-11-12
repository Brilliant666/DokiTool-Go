package shresolver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/douyin"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/probe"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type resolverFunc func(context.Context, string) ([]unified.Object, error)

func (f resolverFunc) Originals(c context.Context, u string) ([]unified.Object, error) {
	return f(c, u)
}
func TestAuthorizedVIDOnlyAndUniqueConfirmedAddresses(t *testing.T) {
	token := strings.Repeat("a", 32)
	vid := "v0200fg10000d9umdsfog65tds1f8o0g"
	cdn := "https://v26-cold.douyinvod.com/00000000000000000000000000000000/abcdef01/video/tos/cn/tos-cn-v/test-object/"
	calls := 0
	handler, err := NewHandler(token, resolverFunc(func(_ context.Context, raw string) ([]unified.Object, error) {
		calls++
		if raw != "https://aweme.snssdk.com/aweme/v1/play/?video_id="+vid+"&ratio=default&line=0" {
			t.Fatal("non-official entry")
		}
		meta := unified.Object{"width": 3840, "height": 2160, "has_audio": true}
		row := unified.Object{"url": cdn, "prefix_sha256": strings.Repeat("f", 64), "metadata": meta}
		return []unified.Object{row, row}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, method, auth, body string
		code                     int
	}{
		{Path, "GET", token, "", 405}, {Path + "?url=a", "POST", token, "", 404}, {Path, "POST", "wrong", "", 401},
		{Path, "POST", token, `{"video_id":"7673404708262453110"}`, 400}, {Path, "POST", token, `{"url":"https://attacker.invalid"}`, 400},
		{Path, "POST", token, `{"video_id":"` + strings.Repeat("v", 600) + `"}`, 400},
		{Path, "POST", token, `{"video_id":"` + vid + `"}`, 200},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.auth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("%s: status %d want %d", tc.body, w.Code, tc.code)
		}
		if w.Code == 200 {
			var result Response
			err = json.Unmarshal(w.Body.Bytes(), &result)
			if err != nil || result.VideoID != vid || len(result.Candidates) != 1 || result.Candidates[0].URL != cdn || result.Candidates[0].ObtainedAt <= 0 || result.Candidates[0].ExpiresAt != 0 {
				t.Fatalf("unexpected candidates: %s", w.Body)
			}
		}
	}
	if calls != 1 {
		t.Fatalf("unauthorized upstream calls: %d", calls)
	}
}

func TestResolveWorkStalledOriginalProbeReturnsRenditionsWithinBudget(t *testing.T) {
	shDetailExec := resolveExecutor(func(_ context.Context, req fetch.Request) (fetch.Response, error) {
		if req.Policy.Purpose != "douyin-detail" {
			return fetch.Response{}, errors.New("unexpected purpose")
		}
		return fetch.Response{StatusCode: 200, Body: interopDetailBody("v26-web.douyinvod.com", "v11-weba.douyinvod.com")}, nil
	})
	jpDetailExec := resolveExecutor(func(_ context.Context, req fetch.Request) (fetch.Response, error) {
		return fetch.Response{StatusCode: 200, Body: interopDetailBody("v3-dy-ov.zjcdn.com", "v3-dy-ov.zjcdn.com")}, nil
	})

	// Real *probe.Probe configured with ProbeTimeout (as in cmd/shanghai-resolver/main.go)
	// whose transport blocks until the probe sub-context expires.
	var probeCancelled atomic.Int32
	stalledProbe := probe.New(resolveExecutor(func(ctx context.Context, _ fetch.Request) (fetch.Response, error) {
		<-ctx.Done()
		probeCancelled.Add(1)
		return fetch.Response{}, ctx.Err()
	}), douyin.AllowedMediaURL).WithTimeout(ProbeTimeout)

	// 1. Tight lease check: resolveWork cuts off stalled original probing via workCtx,
	//    keeps outer lease context alive for callback upload, and returns Renditions with empty VideoID/Candidates.
	leaseCtx, cancelLease := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancelLease()
	start := time.Now()
	got, err := resolveWork(leaseCtx, shDetailExec, stalledProbe, interopAwemeID)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("resolveWork failed on stalled original probe: %v", err)
	}
	if leaseCtx.Err() != nil {
		t.Fatalf("outer lease already expired (%v); no headroom left to upload result", leaseCtx.Err())
	}
	if elapsed > 300*time.Millisecond || probeCancelled.Load() == 0 {
		t.Fatalf("unexpected resolveWork elapsed=%v probeCancelled=%d", elapsed, probeCancelled.Load())
	}
	if got.Protocol != ProtocolV2 || got.AwemeID != interopAwemeID || got.VideoID != "" || len(got.Candidates) != 0 || len(got.Renditions) != 2 {
		t.Fatalf("unexpected degraded WorkResponse: %+v", got)
	}

	// 2. End-to-end PullBroker + PullWorker + douyin.Parser with production WorkResolveTimeout (7s) / WorkTimeout (9s):
	//    Shanghai cuts off stalled original probe at ~7s, delivers domestic Renditions before 9s lease expires,
	//    and Japan falls back to its own original probe, finishing total Parse well under 10s.
	const origin = "https://japan.example.org"
	broker, err := NewPullBroker(wireToken)
	if err != nil {
		t.Fatal(err)
	}
	broker.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	srv := httptest.NewTLSServer(broker)
	defer srv.Close()
	control := controlTransportForTLS(srv, origin)

	worker, err := NewPullWorkerWithDetail(origin, wireToken, control, shDetailExec, stalledProbe)
	if err != nil {
		t.Fatal(err)
	}
	wCtx, wCancel := context.WithCancel(t.Context())
	defer wCancel()
	wDone := make(chan error, 1)
	go func() { wDone <- worker.Run(wCtx) }()
	waitForPullCount(t, broker, pullWorkers)

	var jpProbeCalls atomic.Int32
	jpRow := deliveryRow("japan.zjcdn.com", "objectOrigJP", "jp")
	parser := douyin.NewWithOriginalResolver(jpDetailExec, japanProbeFunc(func(_ context.Context, u string) (unified.Object, error) {
		jpProbeCalls.Add(1)
		if u != douyin.OriginalPlayURL(wireVideo) {
			return nil, errors.New("unexpected Japan original URL")
		}
		return jpRow, nil
	}), broker)

	now := time.Now().Unix()
	e2eStart := time.Now()
	data, err := parser.Parse(t.Context(), interopWorkURL, now)
	e2eElapsed := time.Since(e2eStart)
	if err != nil {
		t.Fatalf("end-to-end Parse failed: %v", err)
	}
	if e2eElapsed < WorkResolveTimeout-250*time.Millisecond || e2eElapsed > 9*time.Second {
		t.Fatalf("expected Parse elapsed near WorkResolveTimeout (%v) and <=9s, got %v", WorkResolveTimeout, e2eElapsed)
	}
	if jpProbeCalls.Load() != 1 {
		t.Fatalf("expected Japan fallback original probe to run once when Shanghai omitted Candidates, got %d", jpProbeCalls.Load())
	}

	view := unified.PublicView(data, false, now)
	if err := unified.ValidateEnvelopeAt(view, now); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	if !bytes.Contains(encoded, []byte("v26-luna.douyinvod.com")) ||
		!bytes.Contains(encoded, []byte("v11-weba.douyinvod.com")) ||
		!bytes.Contains(encoded, []byte("japan.zjcdn.com")) {
		t.Fatalf("expected Shanghai domestic renditions + Japan fallback 4K original, got %s", encoded)
	}

	wCancel()
	<-wDone
}
