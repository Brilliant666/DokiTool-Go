package shresolver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
