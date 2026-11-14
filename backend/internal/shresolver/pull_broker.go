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
	ClaimPath            = "/internal/shanghai/claim"
	ResultPath           = "/internal/shanghai/result"
	PullWait             = 15 * time.Second
	PullStaleClaimGrace  = 5 * time.Second
	DefaultPullQueueWait = 300 * time.Millisecond
	pullCapacity         = MaxPullWorkers * 2
	pullQueueCapacity    = MaxPullWorkers * 4
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

type pullQueueOutcome struct {
	task *pullTask
	err  error
}

type pullQueueEntry struct {
	ctx      context.Context
	protocol int
	videoID  string
	awemeID  string
	ready    chan pullQueueOutcome
	done     bool
}

// PullBroker matches requests immediately to waiting Shanghai workers, or holds
// requests in a bounded FIFO queue for up to DefaultPullQueueWait when a worker
// has claimed within PullWait+PullStaleClaimGrace so brief worker saturation or
// long-poll reconnects do not lose domestic routes while an offline Shanghai node
// still falls back immediately.
type PullBroker struct {
	responseValidator
	token                   string
	queueWait               time.Duration
	queueCap                int
	claimSlots, resultSlots chan struct{}
	mu                      sync.Mutex
	lastClaimAt             time.Time
	waiting                 []*pullWaiter
	queue                   []*pullQueueEntry
	pending                 map[string]*pullTask
}

func NewPullBroker(token string) (*PullBroker, error) {
	if !ValidToken(token) {
		return nil, errUnavailable
	}
	return &PullBroker{
		responseValidator: newResponseValidator(),
		token:             token,
		queueWait:         DefaultPullQueueWait,
		queueCap:          pullQueueCapacity,
		claimSlots:        make(chan struct{}, pullCapacity),
		resultSlots:       make(chan struct{}, pullCapacity),
		pending:           make(map[string]*pullTask),
	}, nil
}

func (b *PullBroker) currentTime() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *PullBroker) hasRecentClaimLocked() bool {
	if b.lastClaimAt.IsZero() {
		return false
	}
	return b.currentTime().Sub(b.lastClaimAt) <= PullWait+PullStaleClaimGrace
}

func (b *PullBroker) newPullTaskLocked(ctx context.Context, protocol int, videoID, awemeID string) *pullTask {
	var ids [32]byte
	rand.Read(ids[:])
	deadline, _ := ctx.Deadline()
	job := PullJob{
		TaskID:     hex.EncodeToString(ids[:16]),
		AttemptID:  hex.EncodeToString(ids[16:]),
		DeadlineMS: deadline.UnixMilli(),
	}
	if protocol == ProtocolV2 {
		job.Protocol = ProtocolV2
		job.AwemeID = awemeID
	} else {
		job.VideoID = videoID
	}
	t := &pullTask{job: job, ctx: ctx, result: make(chan json.RawMessage, 1)}
	b.pending[job.TaskID] = t
	return t
}

func (b *PullBroker) acquireTask(ctx context.Context, protocol int, videoID, awemeID string) (*pullTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.waiting = slices.DeleteFunc(b.waiting, func(w *pullWaiter) bool { return w.ctx.Err() != nil })
	b.queue = slices.DeleteFunc(b.queue, func(e *pullQueueEntry) bool { return e.done })

	if len(b.pending) < pullCapacity {
		for i, w := range b.waiting {
			if protocol == ProtocolV2 && w.protocol < ProtocolV2 {
				continue
			}
			b.waiting = slices.Delete(b.waiting, i, i+1)
			t := b.newPullTaskLocked(ctx, protocol, videoID, awemeID)
			w.job <- t.job
			b.mu.Unlock()
			return t, nil
		}
		if protocol == ProtocolV2 && len(b.waiting) > 0 {
			// Only legacy v1 workers are waiting; preserve their claim so
			// ResolveOriginal can dispatch a v1 video_id lease after detail completes.
			b.mu.Unlock()
			return nil, douyin.ErrProtocolV1
		}
	} else if len(b.waiting) > 0 {
		b.mu.Unlock()
		return nil, errUnavailable
	}

	wait := b.queueWait
	if wait <= 0 || b.queueCap <= 0 || len(b.queue) >= b.queueCap || !b.hasRecentClaimLocked() {
		b.mu.Unlock()
		return nil, errUnavailable
	}
	entry := &pullQueueEntry{
		ctx:      ctx,
		protocol: protocol,
		videoID:  videoID,
		awemeID:  awemeID,
		ready:    make(chan pullQueueOutcome, 1),
	}
	b.queue = append(b.queue, entry)
	b.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case out := <-entry.ready:
		if err := ctx.Err(); err != nil {
			if out.task != nil {
				b.mu.Lock()
				delete(b.pending, out.task.job.TaskID)
				b.mu.Unlock()
			}
			return nil, err
		}
		return out.task, out.err
	case <-ctx.Done():
		b.mu.Lock()
		if !entry.done {
			entry.done = true
			b.queue = slices.DeleteFunc(b.queue, func(v *pullQueueEntry) bool { return v == entry })
			b.mu.Unlock()
			return nil, ctx.Err()
		}
		b.mu.Unlock()
		out := <-entry.ready
		if out.task != nil {
			b.mu.Lock()
			delete(b.pending, out.task.job.TaskID)
			b.mu.Unlock()
		}
		return nil, ctx.Err()
	case <-timer.C:
		b.mu.Lock()
		if !entry.done {
			entry.done = true
			b.queue = slices.DeleteFunc(b.queue, func(v *pullQueueEntry) bool { return v == entry })
			b.mu.Unlock()
			return nil, errUnavailable
		}
		b.mu.Unlock()
		out := <-entry.ready
		if err := ctx.Err(); err != nil {
			if out.task != nil {
				b.mu.Lock()
				delete(b.pending, out.task.job.TaskID)
				b.mu.Unlock()
			}
			return nil, err
		}
		return out.task, out.err
	}
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
	task, err := b.acquireTask(ctx, 0, videoID, "")
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
	task, err := b.acquireTask(ctx, ProtocolV2, "", awemeID)
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

	b.mu.Lock()
	if ctx.Err() != nil {
		b.mu.Unlock()
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b.lastClaimAt = b.currentTime()
	for i := 0; i < len(b.queue); {
		e := b.queue[i]
		if e.done {
			b.queue = slices.Delete(b.queue, i, i+1)
			continue
		}
		if err := e.ctx.Err(); err != nil {
			e.done = true
			b.queue = slices.Delete(b.queue, i, i+1)
			e.ready <- pullQueueOutcome{err: err}
			continue
		}
		i++
	}
	if len(b.pending) < pullCapacity {
		for i, e := range b.queue {
			if e.protocol == ProtocolV2 && proto < ProtocolV2 {
				continue
			}
			e.done = true
			b.queue = slices.Delete(b.queue, i, i+1)
			t := b.newPullTaskLocked(e.ctx, e.protocol, e.videoID, e.awemeID)
			e.ready <- pullQueueOutcome{task: t}
			b.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(t.job); err != nil {
				b.finish(t.job, nil)
			}
			return
		}
	}
	waiter := &pullWaiter{ctx: ctx, protocol: proto, job: make(chan PullJob, 1)}
	b.waiting = append(b.waiting, waiter)
	if proto < ProtocolV2 && len(b.pending) < pullCapacity {
		for i, e := range b.queue {
			if e.protocol == ProtocolV2 {
				e.done = true
				b.queue = slices.Delete(b.queue, i, i+1)
				e.ready <- pullQueueOutcome{err: douyin.ErrProtocolV1}
				break
			}
		}
	}
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
	delete(b.pending, job.TaskID)
	task.result <- payload
	return true
}
