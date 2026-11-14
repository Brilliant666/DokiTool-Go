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

// frozenV2OldBroker models commit 213fd13's Japan PullBroker (pullCapacity = 4,
// WorkTimeout = 14s, strict DisallowUnknownFields on PullResult and WorkResponse).
type frozenV2OldBroker struct {
	responseValidator
	token                   string
	claimSlots, resultSlots chan struct{}
	mu                      sync.Mutex
	waiting                 []chan PullJob
	pending                 map[string]chan json.RawMessage
}

func newFrozenV2OldBroker(token string) *frozenV2OldBroker {
	v := newResponseValidator()
	v.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	return &frozenV2OldBroker{
		responseValidator: v,
		token:             token,
		claimSlots:        make(chan struct{}, 4), // 213fd13 pullCapacity = 4
		resultSlots:       make(chan struct{}, 4),
		pending:           map[string]chan json.RawMessage{},
	}
}

func (b *frozenV2OldBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+b.token || r.Method != http.MethodPost {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := r.URL.EscapedPath()
	slots := b.resultSlots
	if path == ClaimPath {
		slots = b.claimSlots
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	switch path {
	case ClaimPath:
		if r.ContentLength != 0 || r.Header.Get(ProtocolHeader) != "2" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ch := make(chan PullJob, 1)
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
		var res PullResult
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxResponseBytes+512))
		dec.DisallowUnknownFields()
		if dec.Decode(&res) != nil || dec.Decode(&struct{}{}) != io.EOF || res.Protocol != ProtocolV2 || len(res.Result) == 0 || len(res.Result) > maxResponseBytes {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		b.mu.Lock()
		ch := b.pending[res.TaskID]
		delete(b.pending, res.TaskID)
		b.mu.Unlock()
		if ch == nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		ch <- res.Result
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (b *frozenV2OldBroker) ResolveOriginal(context.Context, string) ([]unified.Object, error) {
	return nil, errUnavailable
}

func (b *frozenV2OldBroker) ResolveWork(parent context.Context, awemeID string) (*douyin.ShanghaiWork, error) {
	ctx, cancel := context.WithTimeout(parent, 14*time.Second) // 213fd13 WorkTimeout = 14s
	defer cancel()
	b.mu.Lock()
	if len(b.waiting) == 0 {
		b.mu.Unlock()
		return nil, errUnavailable
	}
	w := b.waiting[0]
	b.waiting = b.waiting[1:]
	var ids [32]byte
	rand.Read(ids[:])
	deadline, _ := ctx.Deadline()
	job := PullJob{
		Protocol:   ProtocolV2,
		TaskID:     hex.EncodeToString(ids[:16]),
		AttemptID:  hex.EncodeToString(ids[16:]),
		AwemeID:    awemeID,
		DeadlineMS: deadline.UnixMilli(),
	}
	resCh := make(chan json.RawMessage, 1)
	b.pending[job.TaskID] = resCh
	b.mu.Unlock()
	w <- job
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case raw := <-resCh:
		return b.validateWorkResponse(ctx, awemeID, raw)
	}
}

// runFrozenV2OldWorkerOnce models commit 213fd13's v2 PullWorker.runOnce
// (single loop, leaseBudget = min(remaining, 14s), strict DisallowUnknownFields).
func runFrozenV2OldWorkerOnce(ctx context.Context, control, detailExec fetch.Executor, origin, token string, p originalProbe, seenDeadline *int64) error {
	post := func(c context.Context, path string, body []byte) (fetch.Response, error) {
		target := origin + path
		h := http.Header{"Authorization": {"Bearer " + token}, "Content-Type": {"application/json"}}
		if path == ClaimPath {
			h.Set(ProtocolHeader, "2")
		}
		return control.Do(c, fetch.Request{
			URL: target, Method: http.MethodPost, Body: body, Header: h,
			Policy: fetch.Policy{Purpose: "douyin-original-pull", Timeout: 25 * time.Second, MaxBytes: 1024, MaxHeaderBytes: 8192, AllowedURL: func(raw string) bool { return raw == target }},
		})
	}
	res, err := post(ctx, ClaimPath, nil)
	if err != nil || res.StatusCode != http.StatusOK {
		return fmt.Errorf("claim failed: err=%v status=%d", err, res.StatusCode)
	}
	var job PullJob
	d := json.NewDecoder(bytes.NewReader(res.Body))
	d.DisallowUnknownFields()
	if d.Decode(&job) != nil || d.Decode(&struct{}{}) != io.EOF || job.Protocol != ProtocolV2 {
		return errors.New("old v2 worker rejected PullJob JSON")
	}
	if seenDeadline != nil {
		*seenDeadline = job.DeadlineMS
	}
	remaining := time.Until(time.UnixMilli(job.DeadlineMS))
	lease, cancel := context.WithTimeout(ctx, min(remaining, 14*time.Second))
	defer cancel()
	detail, err := douyin.FetchShanghaiDetail(lease, detailExec, job.AwemeID)
	if err != nil {
		return err
	}
	out := WorkResponse{Protocol: ProtocolV2, AwemeID: job.AwemeID, Renditions: detail.Renditions, Candidates: []Candidate{}}
	if orig, origErr := probeOriginal(lease, p, detail.VideoID); origErr == nil {
		out.VideoID = detail.VideoID
		out.Candidates = orig.Candidates
	}
	if err := lease.Err(); err != nil {
		return err
	}
	raw, _ := json.Marshal(out)
	body, _ := json.Marshal(PullResult{PullJob: job, Result: raw})
	ack, err := post(lease, ResultPath, body)
	if err != nil || ack.StatusCode != http.StatusNoContent {
		return fmt.Errorf("result ack failed: err=%v status=%d", err, ack.StatusCode)
	}
	return nil
}

func TestPullProtocolV2OldNewInterop(t *testing.T) {
	const origin = "https://japan.example.org"
	shDetailExec := resolveExecutor(func(_ context.Context, _ fetch.Request) (fetch.Response, error) {
		return fetch.Response{StatusCode: 200, Body: interopDetailBody("v26-web.douyinvod.com", "v11-weba.douyinvod.com")}, nil
	})
	jpDetailExec := resolveExecutor(func(_ context.Context, _ fetch.Request) (fetch.Response, error) {
		return fetch.Response{StatusCode: 200, Body: interopDetailBody("v3-dy-ov.zjcdn.com", "v3-dy-ov.zjcdn.com")}, nil
	})

	// Rollout Step 1 (Shanghai upgraded first): Old Japan (213fd13, pullCapacity=4) + New Shanghai (3 workers, probe sub-budget).
	t.Run("old_v2_japan_new_shanghai", func(t *testing.T) {
		oldBroker := newFrozenV2OldBroker(wireToken)
		srv := httptest.NewTLSServer(oldBroker)
		defer srv.Close()

		// Simulate stalled original probe on Shanghai; New Shanghai cuts off probe, returns Renditions,
		// and Old Japan accepts degraded v2 WorkResponse and runs its own fallback original probe.
		stalledProbe := resolverFunc(func(ctx context.Context, _ string) ([]unified.Object, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
		newWorker, err := NewPullWorkerWithDetail(origin, wireToken, controlTransportForTLS(srv, origin), shDetailExec, stalledProbe)
		if err != nil {
			t.Fatal(err)
		}
		wCtx, wCancel := context.WithCancel(t.Context())
		defer wCancel()
		wDone := make(chan error, 1)
		go func() { wDone <- newWorker.Run(wCtx) }()

		// All 3 worker loops must fit cleanly inside Old Japan's pullCapacity=4 without hitting 429.
		deadline := time.After(3 * time.Second)
		for {
			oldBroker.mu.Lock()
			n := len(oldBroker.waiting)
			oldBroker.mu.Unlock()
			if n == pullWorkers {
				break
			}
			select {
			case <-deadline:
				t.Fatalf("expected all %d new worker loops registered on old v2 broker", pullWorkers)
			case <-time.After(time.Millisecond):
			}
		}

		jpProbes := 0
		jpRow := deliveryRow("old-jp-fallback.zjcdn.com", "objectOldJP", "ojp")
		parser := douyin.NewWithOriginalResolver(jpDetailExec, japanProbeFunc(func(_ context.Context, _ string) (unified.Object, error) {
			jpProbes++
			return jpRow, nil
		}), oldBroker)

		// Tight 400ms parent deadline exercises New Shanghai's workBudget = remaining/2 = 200ms cutoff.
		parseCtx, cancelParse := context.WithTimeout(t.Context(), 400*time.Millisecond)
		defer cancelParse()
		now := time.Now().Unix()
		data, err := parser.Parse(parseCtx, interopWorkURL, now)
		if err != nil {
			t.Fatalf("old v2 Japan + new Shanghai degraded parse failed: %v", err)
		}
		if jpProbes != 1 {
			t.Fatalf("expected old v2 Japan to run 1 local fallback probe, got %d", jpProbes)
		}
		raw, _ := json.Marshal(unified.PublicView(data, false, now))
		if !bytes.Contains(raw, []byte("v26-luna.douyinvod.com")) || !bytes.Contains(raw, []byte("old-jp-fallback.zjcdn.com")) {
			t.Fatalf("missing Shanghai renditions or Old Japan fallback original: %s", raw)
		}

		wCancel()
		<-wDone
	})

	// Reverse interop: New Japan (WorkTimeout=9s) + Old v2 Shanghai (213fd13).
	t.Run("new_japan_old_v2_shanghai", func(t *testing.T) {
		newBroker, err := NewPullBroker(wireToken)
		if err != nil {
			t.Fatal(err)
		}
		newBroker.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
		}
		srv := httptest.NewTLSServer(newBroker)
		defer srv.Close()

		var seenDeadline int64
		workerDone := make(chan error, 1)
		go func() {
			workerDone <- runFrozenV2OldWorkerOnce(t.Context(), controlTransportForTLS(srv, origin), shDetailExec, origin, wireToken,
				resolverFunc(func(_ context.Context, _ string) ([]unified.Object, error) {
					return []unified.Object{deliveryRow("v26-cold.douyinvod.com", "objectA", "a")}, nil
				}), &seenDeadline)
		}()
		waitForPullWaiter(t, newBroker)

		before := time.Now()
		now := before.Unix()
		parser := douyin.NewWithOriginalResolver(jpDetailExec, nil, newBroker)
		data, err := parser.Parse(t.Context(), interopWorkURL, now)
		if err != nil {
			t.Fatalf("new Japan + old v2 Shanghai parse failed: %v", err)
		}
		if err := <-workerDone; err != nil {
			t.Fatalf("old v2 worker error: %v", err)
		}
		gotBudget := time.UnixMilli(seenDeadline).Sub(before)
		if gotBudget <= 0 || gotBudget > WorkTimeout+500*time.Millisecond {
			t.Fatalf("expected New Japan DeadlineMS bounded by WorkTimeout (%v), got %v", WorkTimeout, gotBudget)
		}
		raw, _ := json.Marshal(unified.PublicView(data, false, now))
		if !bytes.Contains(raw, []byte("v26-luna.douyinvod.com")) || !bytes.Contains(raw, []byte("v26-cold.douyinvod.com")) {
			t.Fatalf("new Japan + old v2 Shanghai missing domestic routes: %s", raw)
		}
	})
}

type interopStatusWriter struct {
	http.ResponseWriter
	code int
}

func (w *interopStatusWriter) WriteHeader(statusCode int) {
	w.code = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *interopStatusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// frozenMain7ed670fBroker models main (7ed670f) Japan PullBroker:
// pullCapacity = 8, 0ms queue wait, WorkTimeout = 9s, DisallowUnknownFields().
func newFrozenMain7ed670fBroker(t *testing.T, token string) *PullBroker {
	t.Helper()
	b, err := NewPullBroker(token)
	if err != nil {
		t.Fatal(err)
	}
	b.queueWait = 0
	b.queueCap = 0
	b.claimSlots = make(chan struct{}, 8)
	b.resultSlots = make(chan struct{}, 8)
	b.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	return b
}

func TestPullProtocolPR3RollingRestartAndMutualInterop(t *testing.T) {
	const origin = "https://japan.example.org"

	detailFor := func(awemeID, host1080 string) []byte {
		objKey := "/video/tos/cn/tos-cn-v-0015c001-alinc2/oPR3Interop" + awemeID[len(awemeID)-2:]
		u1080 := "https://" + host1080 + "/" + strings.Repeat("1", 32) + "/6a000001" + objKey + "/?a=6383&mime_type=video_mp4"
		www := "https://www.douyin.com/aweme/v1/play/?video_id=" + wireVideo
		return []byte(`{"status_code":0,"aweme_detail":{"aweme_id":"` + awemeID + `","video":{"duration":13667,"play_addr":{"uri":"` + wireVideo + `"},"bit_rate":[` +
			`{"gear_name":"normal_1080_0","format":"mp4","bit_rate":2100000,"play_addr":{"uri":"` + wireVideo + `","file_hash":"h` + awemeID + `","width":1920,"height":1080,"data_size":3500000,"url_list":["` + u1080 + `","` + www + `"]}}` +
			`]}}}`)
	}

	jpDetailExec := resolveExecutor(func(_ context.Context, req fetch.Request) (fetch.Response, error) {
		_, id, _ := strings.Cut(req.URL, "aweme_id=")
		if idx := strings.IndexByte(id, '&'); idx >= 0 {
			id = id[:idx]
		}
		return fetch.Response{StatusCode: 200, Body: detailFor(id, "v3-dy-ov.zjcdn.com")}, nil
	})
	shProbe := resolverFunc(func(_ context.Context, _ string) ([]unified.Object, error) {
		return []unified.Object{deliveryRow("v26-cold.douyinvod.com", "objectA", "a")}, nil
	})

	// 1. Old Shanghai (fixed 3 v2 workers) <-> New Japan (pullCapacity=16 + 300ms short queue):
	//    6 concurrent Douyin parses where Shanghai work takes ~70ms all succeed via Shanghai (0 fallbacks).
	t.Run("old_shanghai_3_workers_new_japan_short_queue", func(t *testing.T) {
		token := randomBrokerToken(t)
		newBroker, err := NewPullBroker(token)
		if err != nil {
			t.Fatal(err)
		}
		newBroker.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
		}
		srv := httptest.NewTLSServer(newBroker)
		defer srv.Close()

		shDetailSlow := resolveExecutor(func(ctx context.Context, req fetch.Request) (fetch.Response, error) {
			_, id, _ := strings.Cut(req.URL, "aweme_id=")
			if idx := strings.IndexByte(id, '&'); idx >= 0 {
				id = id[:idx]
			}
			select {
			case <-time.After(70 * time.Millisecond):
			case <-ctx.Done():
				return fetch.Response{}, ctx.Err()
			}
			return fetch.Response{StatusCode: 200, Body: detailFor(id, "v26-web.douyinvod.com")}, nil
		})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var wgOld sync.WaitGroup
		for range 3 {
			wgOld.Add(1)
			go func() {
				defer wgOld.Done()
				for ctx.Err() == nil {
					if wErr := runFrozenV2OldWorkerOnce(ctx, controlTransportForTLS(srv, origin), shDetailSlow, origin, token, shProbe, nil); wErr != nil && ctx.Err() != nil {
						return
					}
				}
			}()
		}
		waitForPullCount(t, newBroker, 3)

		parser := douyin.NewWithOriginalResolver(jpDetailExec, japanProbeFunc(func(context.Context, string) (unified.Object, error) {
			return nil, errors.New("unexpected Japan fallback when old workers finish within 70ms")
		}), newBroker)

		now := time.Now().Unix()
		errs := make(chan error, 6)
		for i := range 6 {
			id := fmt.Sprintf("77040567372337%05d", 10010+i)
			go func() {
				data, pErr := parser.Parse(t.Context(), "https://www.douyin.com/video/"+id, now)
				if pErr != nil {
					errs <- pErr
					return
				}
				raw, _ := json.Marshal(unified.PublicView(data, false, now))
				if !bytes.Contains(raw, []byte("v26-luna.douyinvod.com")) {
					errs <- fmt.Errorf("missing Shanghai domestic rendition for %s: %s", id, raw)
					return
				}
				errs <- nil
			}()
		}
		for range 6 {
			if rErr := <-errs; rErr != nil {
				t.Fatal(rErr)
			}
		}
		cancel()
		wgOld.Wait()
	})

	// 2. Rolling restart overlap: Old Shanghai (-workers 8) + New Shanghai (-workers 8) = 16 concurrent
	//    claims and 16 concurrent active leases against New Japan (pullCapacity=16) with 0 HTTP 429s!
	t.Run("rolling_restart_overlap_8_old_plus_8_new_workers", func(t *testing.T) {
		token := randomBrokerToken(t)
		newBroker, err := NewPullBroker(token)
		if err != nil {
			t.Fatal(err)
		}
		newBroker.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
		}

		var got429 int32
		var statusMu sync.Mutex
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &interopStatusWriter{ResponseWriter: w, code: http.StatusOK}
			newBroker.ServeHTTP(sw, r)
			if sw.code == http.StatusTooManyRequests {
				statusMu.Lock()
				got429++
				statusMu.Unlock()
			}
		}))
		defer srv.Close()

		allSixteenActive := make(chan struct{})
		releaseSixteen := make(chan struct{})
		var active int32

		shDetailGate := resolveExecutor(func(ctx context.Context, req fetch.Request) (fetch.Response, error) {
			_, id, _ := strings.Cut(req.URL, "aweme_id=")
			if idx := strings.IndexByte(id, '&'); idx >= 0 {
				id = id[:idx]
			}
			statusMu.Lock()
			active++
			if active == MaxPullWorkers*2 {
				close(allSixteenActive)
			}
			statusMu.Unlock()
			select {
			case <-releaseSixteen:
			case <-ctx.Done():
				return fetch.Response{}, ctx.Err()
			}
			return fetch.Response{StatusCode: 200, Body: detailFor(id, "v26-web.douyinvod.com")}, nil
		})

		oldWorker, err := NewPullWorkerWithDetail(origin, token, controlTransportForTLS(srv, origin), shDetailGate, shProbe)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := oldWorker.WithWorkers(MaxPullWorkers); err != nil {
			t.Fatal(err)
		}
		newWorker, err := NewPullWorkerWithDetail(origin, token, controlTransportForTLS(srv, origin), shDetailGate, shProbe)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := newWorker.WithWorkers(MaxPullWorkers); err != nil {
			t.Fatal(err)
		}

		oldCtx, cancelOld := context.WithCancel(t.Context())
		defer cancelOld()
		newCtx, cancelNew := context.WithCancel(t.Context())
		defer cancelNew()

		oldDone := make(chan error, 1)
		newDone := make(chan error, 1)
		go func() { oldDone <- oldWorker.Run(oldCtx) }()
		go func() { newDone <- newWorker.Run(newCtx) }()

		// All 16 workers (8 old + 8 new) must park simultaneously without hitting 429.
		waitForPullCount(t, newBroker, MaxPullWorkers*2)

		parser := douyin.NewWithOriginalResolver(jpDetailExec, nil, newBroker)
		now := time.Now().Unix()
		errs := make(chan error, MaxPullWorkers*2)
		for i := range MaxPullWorkers * 2 {
			id := fmt.Sprintf("77040567372338%05d", 10010+i)
			go func() {
				data, pErr := parser.Parse(t.Context(), "https://www.douyin.com/video/"+id, now)
				if pErr != nil {
					errs <- pErr
					return
				}
				raw, _ := json.Marshal(unified.PublicView(data, false, now))
				if !bytes.Contains(raw, []byte("v26-luna.douyinvod.com")) {
					errs <- fmt.Errorf("overlap parse %s missed Shanghai domestic route: %s", id, raw)
					return
				}
				errs <- nil
			}()
		}

		select {
		case <-allSixteenActive:
		case <-time.After(3 * time.Second):
			t.Fatal("did not lease all 16 overlapping tasks simultaneously")
		}
		close(releaseSixteen)

		for range MaxPullWorkers * 2 {
			if rErr := <-errs; rErr != nil {
				t.Fatal(rErr)
			}
		}

		// Drain old Shanghai process and verify new Shanghai process continues serving cleanly.
		waitForPullCount(t, newBroker, MaxPullWorkers*2)
		cancelOld()
		<-oldDone
		waitForPullCount(t, newBroker, MaxPullWorkers)

		postDrainData, err := parser.Parse(t.Context(), "https://www.douyin.com/video/7704056737233899999", now)
		if err != nil {
			t.Fatal(err)
		}
		postDrainRaw, _ := json.Marshal(unified.PublicView(postDrainData, false, now))
		if !bytes.Contains(postDrainRaw, []byte("v26-luna.douyinvod.com")) {
			t.Fatalf("new worker did not continue serving after old process drained: %s", postDrainRaw)
		}

		statusMu.Lock()
		rejects := got429
		statusMu.Unlock()
		if rejects != 0 {
			t.Fatalf("rolling restart 8+8 overlap suffered %d HTTP 429 rejections, want 0", rejects)
		}

		cancelNew()
		<-newDone
	})

	// 3. New Shanghai (-workers 1, 3, 8) <-> Frozen main (7ed670f, pullCapacity=8, 0ms queue):
	//    Wire protocol v2 remains 100% compatible across all worker counts.
	t.Run("new_shanghai_configurable_workers_with_frozen_main_broker", func(t *testing.T) {
		shDetailFast := resolveExecutor(func(_ context.Context, req fetch.Request) (fetch.Response, error) {
			_, id, _ := strings.Cut(req.URL, "aweme_id=")
			if idx := strings.IndexByte(id, '&'); idx >= 0 {
				id = id[:idx]
			}
			return fetch.Response{StatusCode: 200, Body: detailFor(id, "v26-web.douyinvod.com")}, nil
		})

		for _, nWorkers := range []int{1, 3, 8} {
			token := randomBrokerToken(t)
			oldMainBroker := newFrozenMain7ed670fBroker(t, token)
			srv := httptest.NewTLSServer(oldMainBroker)

			w, err := NewPullWorkerWithDetail(origin, token, controlTransportForTLS(srv, origin), shDetailFast, shProbe)
			if err != nil {
				srv.Close()
				t.Fatal(err)
			}
			if _, err := w.WithWorkers(nWorkers); err != nil {
				srv.Close()
				t.Fatal(err)
			}
			wCtx, cancelW := context.WithCancel(t.Context())
			wDone := make(chan error, 1)
			go func() { wDone <- w.Run(wCtx) }()
			waitForPullCount(t, oldMainBroker, nWorkers)

			parser := douyin.NewWithOriginalResolver(jpDetailExec, nil, oldMainBroker)
			now := time.Now().Unix()
			data, err := parser.Parse(t.Context(), "https://www.douyin.com/video/7704056737233910001", now)
			if err != nil {
				cancelW()
				srv.Close()
				t.Fatalf("workers=%d against frozen main broker failed: %v", nWorkers, err)
			}
			raw, _ := json.Marshal(unified.PublicView(data, false, now))
			if !bytes.Contains(raw, []byte("v26-luna.douyinvod.com")) || !bytes.Contains(raw, []byte("v26-cold.douyinvod.com")) {
				cancelW()
				srv.Close()
				t.Fatalf("workers=%d missing domestic routes on frozen main broker: %s", nWorkers, raw)
			}

			cancelW()
			<-wDone
			srv.Close()
		}
	})
}
