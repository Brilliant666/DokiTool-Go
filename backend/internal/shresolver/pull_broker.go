package shresolver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/douyin"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const (
	ClaimPath    = "/internal/shanghai/claim"
	ResultPath   = "/internal/shanghai/result"
	PullWait     = 15 * time.Second
	pullCapacity = 4
)

// PullJob is a single-use lease. DeadlineMS is absolute; another poll cannot
// extend it. There is no replay or redelivery on worker disconnect.
// Legacy v1 jobs omit Protocol and AwemeID so v1 workers with
// DisallowUnknownFields accept them byte-for-byte unchanged.
type PullJob struct {
	Protocol   int    `json:"protocol,omitempty"`
	TaskID     string `json:"task_id"`
	AttemptID  string `json:"attempt_id"`
	VideoID    string `json:"video_id,omitempty"`
	AwemeID    string `json:"aweme_id,omitempty"`
	DeadlineMS int64  `json:"deadline_ms"`
}

type PullResult struct {
	PullJob
	Result json.RawMessage `json:"result"` // Response (v1), WorkResponse (v2), or null on failure
}

type pullWaiter struct {
	ctx      context.Context
	protocol int
	job      chan PullJob
}

type pullTask struct {
	job      PullJob
	ctx      context.Context
	result   chan json.RawMessage
	received bool
}

// PullBroker matches requests only to already waiting workers. No queue of
// users accumulates while Shanghai is offline, busy or reconnecting.
type PullBroker struct {
	responseValidator
	token                   string
	claimSlots, resultSlots chan struct{}
	mu                      sync.Mutex
	waiting                 []*pullWaiter
	pending                 map[string]*pullTask
}

func NewPullBroker(token string) (*PullBroker, error) {
	if !ValidToken(token) {
		return nil, errUnavailable
	}
	return &PullBroker{responseValidator: newResponseValidator(), token: token,
		claimSlots: make(chan struct{}, pullCapacity), resultSlots: make(chan struct{}, pullCapacity),
		pending: make(map[string]*pullTask)}, nil
}

func (b *PullBroker) dispatch(ctx context.Context, videoID string) *pullTask {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) >= pullCapacity {
		return nil
	}
	for len(b.waiting) > 0 {
		w := b.waiting[0]
		b.waiting = slices.Delete(b.waiting, 0, 1)
		if w.ctx.Err() != nil {
			continue
		}
		var ids [32]byte
		rand.Read(ids[:])
		deadline, _ := ctx.Deadline()
		job := PullJob{TaskID: hex.EncodeToString(ids[:16]), AttemptID: hex.EncodeToString(ids[16:]), VideoID: videoID, DeadlineMS: deadline.UnixMilli()}
		t := &pullTask{job: job, ctx: ctx, result: make(chan json.RawMessage, 1)}
		b.pending[job.TaskID] = t
		w.job <- job
		return t
	}
	return nil
}

func (b *PullBroker) dispatchWork(ctx context.Context, awemeID string) (*pullTask, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.waiting = slices.DeleteFunc(b.waiting, func(w *pullWaiter) bool { return w.ctx.Err() != nil })
	if len(b.pending) >= pullCapacity {
		return nil, errUnavailable
	}
	for i, w := range b.waiting {
		if w.protocol < ProtocolV2 {
			continue
		}
		b.waiting = slices.Delete(b.waiting, i, i+1)
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
		t := &pullTask{job: job, ctx: ctx, result: make(chan json.RawMessage, 1)}
		b.pending[job.TaskID] = t
		w.job <- job
		return t, nil
	}
	if len(b.waiting) > 0 {
		// Only legacy v1 workers are waiting; preserve their claim so
		// ResolveOriginal can dispatch a v1 video_id lease after detail completes.
		return nil, douyin.ErrProtocolV1
	}
	return nil, errUnavailable
}

func (b *PullBroker) ResolveOriginal(parent context.Context, videoID string) ([]unified.Object, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if !ValidVideoID(videoID) {
		return nil, errUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, clientTimeout)
	defer cancel()
	task := b.dispatch(ctx, videoID)
	if task == nil {
		return nil, errUnavailable
	}
	defer func() {
		b.mu.Lock()
		delete(b.pending, task.job.TaskID)
		b.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case data := <-task.result:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return b.validateResponse(ctx, videoID, data)
	}
}

func (b *PullBroker) ResolveWork(parent context.Context, awemeID string) (*douyin.ShanghaiWork, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if !douyin.ValidAwemeID(awemeID) {
		return nil, errUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, WorkTimeout)
	defer cancel()
	task, err := b.dispatchWork(ctx, awemeID)
	if err != nil {
		return nil, err
	}
	defer func() {
		b.mu.Lock()
		delete(b.pending, task.job.TaskID)
		b.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case data := <-task.result:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return b.validateWorkResponse(ctx, awemeID, data)
	}
}

func (b *PullBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	path := r.URL.EscapedPath()
	if (path != ClaimPath && path != ResultPath) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte("Bearer "+b.token)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// Waiting claims never consume result slots. These concurrency
	// bounds are independent of user parsing and media streaming admission.
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
	if path == ClaimPath {
		if r.ContentLength != 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		b.claim(w, r)
		return
	}
	b.receive(w, r)
}

func (b *PullBroker) claim(w http.ResponseWriter, r *http.Request) {
	if r.Context().Err() != nil {
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), PullWait)
	defer cancel()
	proto := 1
	if r.Header.Get(ProtocolHeader) == "2" {
		proto = ProtocolV2
	}
	waiter := &pullWaiter{ctx: ctx, protocol: proto, job: make(chan PullJob, 1)}
	b.mu.Lock()
	b.waiting = append(b.waiting, waiter)
	b.mu.Unlock()
	select {
	case job := <-waiter.job:
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(job); err != nil {
			b.finish(job, nil)
		}
	case <-ctx.Done():
		b.mu.Lock()
		b.waiting = slices.DeleteFunc(b.waiting, func(v *pullWaiter) bool { return v == waiter })
		b.mu.Unlock()
		select { // a dispatch and the claim timeout can coincide
		case job := <-waiter.job:
			b.finish(job, nil)
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (b *PullBroker) receive(w http.ResponseWriter, r *http.Request) {
	// Limit how long a slow sender can occupy one of the result slots.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(3 * time.Second))
	var result PullResult
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxResponseBytes+512))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || d.Decode(&struct{}{}) != io.EOF {
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = controller.SetReadDeadline(time.Time{})
	if len(result.Result) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	limit := maxResponseBytes
	switch result.Protocol {
	case 0:
		limit = maxV1ResponseBytes
	case ProtocolV2:
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if len(result.Result) > limit {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !bytes.Equal(result.Result, []byte("null")) {
		if result.Protocol == ProtocolV2 {
			var work struct {
				Protocol int    `json:"protocol"`
				AwemeID  string `json:"aweme_id"`
			}
			if json.Unmarshal(result.Result, &work) != nil || work.Protocol != ProtocolV2 || work.AwemeID != result.AwemeID {
				w.WriteHeader(http.StatusConflict)
				return
			}
		} else {
			var media struct {
				VideoID string `json:"video_id"`
			}
			if json.Unmarshal(result.Result, &media) != nil || media.VideoID != result.VideoID {
				w.WriteHeader(http.StatusConflict)
				return
			}
		}
	}
	if !b.finish(result.PullJob, result.Result) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Consume at most one receipt. The original request, not the callback handler,
// owns validation of the candidate with the remainder of its own budget.
func (b *PullBroker) finish(job PullJob, payload json.RawMessage) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	task := b.pending[job.TaskID]
	if task == nil || task.received || job != task.job || task.ctx.Err() != nil || time.Now().UnixMilli() >= task.job.DeadlineMS {
		return false
	}
	task.received = true
	task.result <- payload
	return true
}
