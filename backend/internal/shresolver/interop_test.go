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
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/douyin"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const (
	interopAwemeID = "7704056737233449610"
	interopWorkURL = "https://www.douyin.com/jingxuan?modal_id=" + interopAwemeID
	interop1080Key = "/video/tos/cn/tos-cn-v-0015c001-alinc2/oInterop1080"
	interop720Key  = "/video/tos/cn/tos-cn-v-0015c001-alinc2/oInterop720"
)

// Frozen pre-Phase-2 v1 wire contracts with DisallowUnknownFields() and 32 KiB limit.
type frozenV1PullJob struct {
	TaskID     string `json:"task_id"`
	AttemptID  string `json:"attempt_id"`
	VideoID    string `json:"video_id"`
	DeadlineMS int64  `json:"deadline_ms"`
}

type frozenV1PullResult struct {
	frozenV1PullJob
	Result json.RawMessage `json:"result"`
}

type frozenV1Response struct {
	VideoID    string      `json:"video_id"`
	Candidates []Candidate `json:"candidates"`
}

type frozenV1PullBroker struct {
	token   string
	mu      sync.Mutex
	waiting []chan frozenV1PullJob
	pending map[string]chan frozenV1Response
}

func newFrozenV1PullBroker(token string) *frozenV1PullBroker {
	return &frozenV1PullBroker{token: token, pending: map[string]chan frozenV1Response{}}
}

func (b *frozenV1PullBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+b.token || r.Method != http.MethodPost {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.URL.EscapedPath() {
	case ClaimPath:
		if r.ContentLength != 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ch := make(chan frozenV1PullJob, 1)
		b.mu.Lock()
		b.waiting = append(b.waiting, ch)
		b.mu.Unlock()
		select {
		case job := <-ch:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(job)
		case <-r.Context().Done():
			w.WriteHeader(http.StatusNoContent)
		}
	case ResultPath:
		var res frozenV1PullResult
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, (32<<10)+512))
		dec.DisallowUnknownFields()
		if dec.Decode(&res) != nil || dec.Decode(&struct{}{}) != io.EOF || len(res.Result) == 0 || len(res.Result) > (32<<10) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var parsed frozenV1Response
		inner := json.NewDecoder(bytes.NewReader(res.Result))
		inner.UseNumber()
		inner.DisallowUnknownFields()
		if inner.Decode(&parsed) != nil || inner.Decode(&struct{}{}) != io.EOF || parsed.VideoID != res.VideoID {
			w.WriteHeader(http.StatusConflict)
			return
		}
		b.mu.Lock()
		out := b.pending[res.TaskID]
		delete(b.pending, res.TaskID)
		b.mu.Unlock()
		if out == nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		out <- parsed
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (b *frozenV1PullBroker) waitClaim(t *testing.T) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		b.mu.Lock()
		n := len(b.waiting)
		b.mu.Unlock()
		if n >= 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for v1 broker claim")
		case <-time.After(time.Millisecond):
		}
	}
}

func (b *frozenV1PullBroker) resolveV1(ctx context.Context, videoID string) (frozenV1Response, error) {
	b.mu.Lock()
	if len(b.waiting) == 0 {
		b.mu.Unlock()
		return frozenV1Response{}, errUnavailable
	}
	ch := b.waiting[0]
	b.waiting = b.waiting[1:]
	var ids [32]byte
	rand.Read(ids[:])
	job := frozenV1PullJob{
		TaskID:     hex.EncodeToString(ids[:16]),
		AttemptID:  hex.EncodeToString(ids[16:]),
		VideoID:    videoID,
		DeadlineMS: time.Now().Add(8 * time.Second).UnixMilli(),
	}
	out := make(chan frozenV1Response, 1)
	b.pending[job.TaskID] = out
	b.mu.Unlock()
	ch <- job
	select {
	case <-ctx.Done():
		return frozenV1Response{}, ctx.Err()
	case r := <-out:
		return r, nil
	}
}

func runFrozenV1WorkerOnce(ctx context.Context, control fetch.Executor, origin, token string, p originalProbe) error {
	post := func(path string, body []byte) (fetch.Response, error) {
		target := origin + path
		return control.Do(ctx, fetch.Request{
			URL:    target,
			Method: http.MethodPost,
			Body:   body,
			Header: http.Header{"Authorization": {"Bearer " + token}, "Content-Type": {"application/json"}},
			Policy: fetch.Policy{Purpose: "douyin-original-pull", Timeout: 10 * time.Second, MaxBytes: 1024, MaxHeaderBytes: 8192, AllowedURL: func(raw string) bool { return raw == target }},
		})
	}
	res, err := post(ClaimPath, nil)
	if err != nil {
		return err
	}
	if res.StatusCode == http.StatusNoContent {
		return nil
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected claim status %d", res.StatusCode)
	}
	var job frozenV1PullJob
	d := json.NewDecoder(bytes.NewReader(res.Body))
	d.DisallowUnknownFields()
	if d.Decode(&job) != nil || d.Decode(&struct{}{}) != io.EOF || !ValidVideoID(job.VideoID) {
		return errors.New("v1 worker rejected job JSON")
	}
	out, err := probeOriginal(ctx, p, job.VideoID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(frozenV1Response{VideoID: out.VideoID, Candidates: out.Candidates})
	if err != nil || len(raw) > (32<<10) {
		return errors.New("v1 response marshal failed")
	}
	body, _ := json.Marshal(frozenV1PullResult{frozenV1PullJob: job, Result: raw})
	ack, err := post(ResultPath, body)
	if err != nil {
		return err
	}
	if ack.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected result ack %d", ack.StatusCode)
	}
	return nil
}

func interopDetailBody(host1080, host720 string) []byte {
	u1080 := "https://" + host1080 + "/" + strings.Repeat("1", 32) + "/6a000001" + interop1080Key + "/?a=6383&mime_type=video_mp4"
	u720 := "https://" + host720 + "/" + strings.Repeat("2", 32) + "/6a000001" + interop720Key + "/?a=6383&mime_type=video_mp4"
	www := "https://www.douyin.com/aweme/v1/play/?video_id=" + wireVideo
	return []byte(`{"status_code":0,"aweme_detail":{"aweme_id":"` + interopAwemeID + `","video":{"duration":13667,"play_addr":{"uri":"` + wireVideo + `"},"bit_rate":[` +
		`{"gear_name":"normal_1080_0","format":"mp4","bit_rate":2100000,"play_addr":{"uri":"` + wireVideo + `","file_hash":"h1080","width":1920,"height":1080,"data_size":3500000,"url_list":["` + u1080 + `","` + www + `"]}},` +
		`{"gear_name":"normal_720_0","format":"mp4","bit_rate":1500000,"play_addr":{"uri":"` + wireVideo + `","file_hash":"h720","width":1280,"height":720,"data_size":2400000,"url_list":["` + u720 + `","` + www + `"]}}` +
		`]}}}`)
}

func controlTransportForTLS(srv *httptest.Server, origin string) fetch.Executor {
	return resolveExecutor(func(ctx context.Context, req fetch.Request) (fetch.Response, error) {
		path := strings.TrimPrefix(req.URL, origin)
		if path != ClaimPath && path != ResultPath {
			return fetch.Response{}, errors.New("unexpected control destination")
		}
		r, err := http.NewRequestWithContext(ctx, req.Method, srv.URL+path, bytes.NewReader(req.Body))
		if err != nil {
			return fetch.Response{}, err
		}
		r.Header = req.Header.Clone()
		res, err := srv.Client().Do(r)
		if err != nil {
			return fetch.Response{}, err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, req.Policy.MaxBytes+1))
		return fetch.Response{Body: body, StatusCode: res.StatusCode, Header: res.Header}, err
	})
}

func TestPullProtocolFourWayInterop(t *testing.T) {
	const origin = "https://japan.example.org"
	shOriginalProbe := resolverFunc(func(_ context.Context, u string) ([]unified.Object, error) {
		if u != douyin.OriginalPlayURL(wireVideo) {
			return nil, fmt.Errorf("unexpected original dispatcher %q", u)
		}
		return []unified.Object{deliveryRow("v26-cold.douyinvod.com", "objectA", "a")}, nil
	})
	shDetailExec := resolveExecutor(func(_ context.Context, req fetch.Request) (fetch.Response, error) {
		if req.Policy.Purpose != "douyin-detail" {
			return fetch.Response{}, fmt.Errorf("unexpected Shanghai request purpose %q", req.Policy.Purpose)
		}
		return fetch.Response{StatusCode: 200, Body: interopDetailBody("v26-web.douyinvod.com", "v11-weba.douyinvod.com")}, nil
	})
	jpDetailExec := resolveExecutor(func(_ context.Context, req fetch.Request) (fetch.Response, error) {
		if req.Policy.Purpose != "douyin-detail" {
			return fetch.Response{}, fmt.Errorf("unexpected Japan request purpose %q", req.Policy.Purpose)
		}
		return fetch.Response{StatusCode: 200, Body: interopDetailBody("v3-dy-ov.zjcdn.com", "v3-dy-ov.zjcdn.com")}, nil
	})

	// 1. Old Japan + Old Shanghai
	t.Run("old_japan_old_shanghai", func(t *testing.T) {
		oldBroker := newFrozenV1PullBroker(wireToken)
		srv := httptest.NewTLSServer(oldBroker)
		defer srv.Close()
		control := controlTransportForTLS(srv, origin)
		workerErr := make(chan error, 1)
		go func() { workerErr <- runFrozenV1WorkerOnce(t.Context(), control, origin, wireToken, shOriginalProbe) }()
		oldBroker.waitClaim(t)
		got, err := oldBroker.resolveV1(t.Context(), wireVideo)
		if err != nil || len(got.Candidates) != 1 || got.VideoID != wireVideo {
			t.Fatalf("old+old failed: %v %+v", err, got)
		}
		if err := <-workerErr; err != nil {
			t.Fatal(err)
		}
	})

	// 2. Old Japan + New Shanghai (rollout step 1: Shanghai upgraded first!)
	t.Run("old_japan_new_shanghai", func(t *testing.T) {
		oldBroker := newFrozenV1PullBroker(wireToken)
		srv := httptest.NewTLSServer(oldBroker)
		defer srv.Close()
		control := controlTransportForTLS(srv, origin)
		newWorker, err := NewPullWorkerWithDetail(origin, wireToken, control, shDetailExec, shOriginalProbe)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- newWorker.Run(ctx) }()
		oldBroker.waitClaim(t)
		got, err := oldBroker.resolveV1(t.Context(), wireVideo)
		if err != nil || len(got.Candidates) != 1 || got.VideoID != wireVideo {
			t.Fatalf("old Japan rejected new Shanghai worker response: %v %+v", err, got)
		}
		cancel()
		<-done
	})

	// 3. New Japan + Old Shanghai
	t.Run("new_japan_old_shanghai", func(t *testing.T) {
		newBroker, err := NewPullBroker(wireToken)
		if err != nil {
			t.Fatal(err)
		}
		newBroker.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
		}
		srv := httptest.NewTLSServer(newBroker)
		defer srv.Close()
		control := controlTransportForTLS(srv, origin)
		workerDone := make(chan error, 1)
		go func() { workerDone <- runFrozenV1WorkerOnce(t.Context(), control, origin, wireToken, shOriginalProbe) }()
		waitForPullWaiter(t, newBroker)

		parser := douyin.NewWithOriginalResolver(jpDetailExec, nil, newBroker)
		now := time.Now().Unix()
		data, err := parser.Parse(t.Context(), interopWorkURL, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := <-workerDone; err != nil {
			t.Fatalf("old Shanghai worker failed under new Japan broker: %v", err)
		}
		view := unified.PublicView(data, false, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(view)
		if !bytes.Contains(encoded, []byte("original_endpoint")) {
			t.Fatalf("new Japan + old Shanghai lost v1 4K original: %s", encoded)
		}
	})

	// 4. New Japan + New Shanghai (rollout step 2: full parallel domestic renditions + 4K original!)
	t.Run("new_japan_new_shanghai", func(t *testing.T) {
		newBroker, err := NewPullBroker(wireToken)
		if err != nil {
			t.Fatal(err)
		}
		newBroker.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
		}
		srv := httptest.NewTLSServer(newBroker)
		defer srv.Close()
		control := controlTransportForTLS(srv, origin)
		newWorker, err := NewPullWorkerWithDetail(origin, wireToken, control, shDetailExec, shOriginalProbe)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- newWorker.Run(ctx) }()
		waitForPullWaiter(t, newBroker)

		parser := douyin.NewWithOriginalResolver(jpDetailExec, nil, newBroker)
		now := time.Now().Unix()
		data, err := parser.Parse(t.Context(), interopWorkURL, now)
		if err != nil {
			t.Fatal(err)
		}
		view := unified.PublicView(data, false, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatal(err)
		}
		variants := unified.Slice(unified.Map(unified.Slice(unified.Map(view["data"])["items"])[0])["variants"])
		if len(variants) != 3 {
			t.Fatalf("expected 3 variants (4K original + 1080p + 720p), got %d", len(variants))
		}
		// 1080p must have rewritten v26-luna.douyinvod.com at sources[0] and overseas zjcdn.com behind it.
		s1080 := unified.Slice(unified.Map(unified.Map(variants[1])["delivery"])["sources"])
		if !strings.HasPrefix(unified.Str(unified.Map(s1080[0])["url"]), "https://v26-luna.douyinvod.com/") ||
			!strings.Contains(unified.Str(unified.Map(s1080[1])["url"]), "v3-dy-ov.zjcdn.com") {
			t.Fatalf("unexpected 1080p sources: %#v", s1080)
		}
		// 720p must have domestic v11-weba.douyinvod.com at sources[0] and overseas zjcdn.com behind it.
		s720 := unified.Slice(unified.Map(unified.Map(variants[2])["delivery"])["sources"])
		if !strings.HasPrefix(unified.Str(unified.Map(s720[0])["url"]), "https://v11-weba.douyinvod.com/") ||
			!strings.Contains(unified.Str(unified.Map(s720[1])["url"]), "v3-dy-ov.zjcdn.com") {
			t.Fatalf("unexpected 720p sources: %#v", s720)
		}
		cancel()
		<-done
	})
}

func TestDirectHTTPProtocolFourWayInterop(t *testing.T) {
	shOriginalProbe := resolverFunc(func(_ context.Context, _ string) ([]unified.Object, error) {
		return []unified.Object{deliveryRow("v26-cold.douyinvod.com", "objectA", "a")}, nil
	})
	shDetailExec := resolveExecutor(func(context.Context, fetch.Request) (fetch.Response, error) {
		return fetch.Response{StatusCode: 200, Body: interopDetailBody("v26-web.douyinvod.com", "v11-weba.douyinvod.com")}, nil
	})
	jpDetailExec := resolveExecutor(func(context.Context, fetch.Request) (fetch.Response, error) {
		return fetch.Response{StatusCode: 200, Body: interopDetailBody("v3-dy-ov.zjcdn.com", "v3-dy-ov.zjcdn.com")}, nil
	})

	handlerClient := func(t *testing.T, h http.Handler) *Client {
		t.Helper()
		c, err := NewClient(testEndpoint, wireToken, resolveExecutor(func(ctx context.Context, req fetch.Request) (fetch.Response, error) {
			rec := httptest.NewRecorder()
			httpReq := httptest.NewRequestWithContext(ctx, req.Method, Path, bytes.NewReader(req.Body))
			httpReq.Header = req.Header.Clone()
			h.ServeHTTP(rec, httpReq)
			return fetch.Response{StatusCode: rec.Code, Body: rec.Body.Bytes(), Header: rec.Header()}, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		c.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
		}
		return c
	}

	oldServer, err := NewHandler(wireToken, shOriginalProbe)
	if err != nil {
		t.Fatal(err)
	}
	newServer, err := NewHandlerWithDetail(wireToken, shDetailExec, shOriginalProbe)
	if err != nil {
		t.Fatal(err)
	}

	// 1 & 2: Old Japan client (ResolveOriginal) against Old Server and New Server.
	for name, srv := range map[string]http.Handler{"old_server": oldServer, "new_server": newServer} {
		rows, err := handlerClient(t, srv).ResolveOriginal(t.Context(), wireVideo)
		if err != nil || len(rows) != 1 {
			t.Fatalf("old Japan ResolveOriginal -> %s failed: %v %v", name, err, rows)
		}
	}

	// 3: New Japan parser against Old Server (HTTP 400 on v2 -> ErrProtocolV1 -> ResolveOriginal fallback).
	now := time.Now().Unix()
	dataOld, err := douyin.NewWithOriginalResolver(jpDetailExec, nil, handlerClient(t, oldServer)).Parse(t.Context(), interopWorkURL, now)
	if err != nil {
		t.Fatal(err)
	}
	encOld, _ := json.Marshal(unified.PublicView(dataOld, false, now))
	if !bytes.Contains(encOld, []byte("original_endpoint")) {
		t.Fatalf("new Japan + old direct server failed to fall back to v1 original: %s", encOld)
	}

	// 4: New Japan parser against New Server (v2 ResolveWork).
	dataNew, err := douyin.NewWithOriginalResolver(jpDetailExec, nil, handlerClient(t, newServer)).Parse(t.Context(), interopWorkURL, now)
	if err != nil {
		t.Fatal(err)
	}
	encNew, _ := json.Marshal(unified.PublicView(dataNew, false, now))
	if !bytes.Contains(encNew, []byte("v26-luna.douyinvod.com")) || !bytes.Contains(encNew, []byte("v11-weba.douyinvod.com")) {
		t.Fatalf("new Japan + new direct server missing domestic rendition routes: %s", encNew)
	}
}

func TestWorkResponse128KiBLimitAndHostFiltering(t *testing.T) {
	v := newResponseValidator()
	v.now = func() time.Time { return time.Unix(wireNow, 0) }
	v.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}

	// Build a ~50 KiB valid v2 WorkResponse (> old 32 KiB cap, <= new 128 KiB cap).
	renditions := map[string][]string{}
	paddingQuery := strings.Repeat("x", 180)
	for i := 0; i < 40; i++ {
		objKey := "/video/tos/cn/tos-cn-v-0015c001-alinc2/objectKeyNumber" + strconv.Itoa(i)
		var urls []string
		for j := 0; j < 5; j++ {
			urls = append(urls, fmt.Sprintf("https://v26-web.douyinvod.com/%032d/6a000001%s/?a=6383&p=%s&idx=%d", j, objKey, paddingQuery, i))
		}
		renditions[objKey] = urls
	}
	// Inject a zjcdn.com URL and a mismatched storage path URL on objectKeyNumber0; both must be dropped.
	key0 := "/video/tos/cn/tos-cn-v-0015c001-alinc2/objectKeyNumber0"
	renditions[key0] = append(renditions[key0],
		"https://v3-dy-o.zjcdn.com/"+strings.Repeat("9", 32)+"/6a000001"+key0+"/?a=6383",
		"https://v11-weba.douyinvod.com/"+strings.Repeat("8", 32)+"/6a000001/video/tos/cn/tos-cn-v-0015/wrongKey/?a=6383",
	)

	payload, err := json.Marshal(WorkResponse{
		Protocol:   ProtocolV2,
		AwemeID:    interopAwemeID,
		VideoID:    wireVideo,
		Renditions: renditions,
		Candidates: []Candidate{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) <= maxV1ResponseBytes || len(payload) > maxResponseBytes {
		t.Fatalf("expected payload between 32 KiB and 128 KiB, got %d bytes", len(payload))
	}

	work, err := v.validateWorkResponse(t.Context(), interopAwemeID, payload)
	if err != nil {
		t.Fatalf("valid >32KiB v2 payload rejected: %v", err)
	}
	if len(work.Renditions) != 40 || len(work.Renditions[key0]) != 5 {
		t.Fatalf("expected 40 objects and 5 kept douyinvod.com URLs on key0 (zjcdn + wrongKey dropped), got objects=%d key0=%d", len(work.Renditions), len(work.Renditions[key0]))
	}
	for _, entry := range work.Renditions[key0] {
		u := unified.Str(unified.Map(entry)["url"])
		if !strings.HasPrefix(u, "https://v26-luna.douyinvod.com/") {
			t.Fatalf("expected v26-web rewritten to v26-luna, got %q", u)
		}
	}

	// Over 128 KiB must be rejected.
	overLimit := append([]byte(strings.Repeat(" ", maxResponseBytes)), payload...)
	if _, err := v.validateWorkResponse(t.Context(), interopAwemeID, overLimit); err == nil {
		t.Fatal("over-128KiB payload accepted")
	}

	// Legacy v1 receipt JSON passed to validateWorkResponse is accepted under v1 rules.
	v1Raw, _ := json.Marshal(goodResponse())
	legacyWork, err := v.validateWorkResponse(t.Context(), interopAwemeID, v1Raw)
	if err != nil || legacyWork.VideoID != wireVideo || len(legacyWork.Candidates) != 1 || len(legacyWork.Renditions) != 0 {
		t.Fatalf("legacy v1 format handling failed: err=%v work=%+v", err, legacyWork)
	}
}
