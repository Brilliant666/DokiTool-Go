package shresolver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/douyin"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const deliveryWork = "https://www.douyin.com/video/7673404708262453110"

func deliveryParser(b *PullBroker, jp douyin.Prober) *douyin.Parser {
	detail := resolveExecutor(func(_ context.Context, req fetch.Request) (fetch.Response, error) {
		if req.Policy.Purpose != "douyin-detail" {
			return fetch.Response{}, fmt.Errorf("unexpected platform request")
		}
		return fetch.Response{StatusCode: 200, Body: []byte(`{"status_code":0,"aweme_detail":{"aweme_id":"7673404708262453110","video":{"duration":13667,"play_addr":{"uri":"` + wireVideo + `"},"bit_rate":[{"play_addr":{"width":2160,"height":3840,"data_size":8662403,"url_list":["https://example.net/transcode.mp4"]},"bit_rate":5000000}]}}}`)}, nil
	})
	return douyin.NewWithOriginalResolver(detail, jp, b)
}

// Use the normal Run loop and broker over real local TLS. Only the platform
// bytes, public DNS answer and trusted fixture transport are substituted.
func deliveryWorker(t *testing.T, p originalProbe) (*PullBroker, *httptest.Server, func()) {
	t.Helper()
	b, err := NewPullBroker(wireToken)
	if err != nil {
		t.Fatal(err)
	}
	b.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	srv := httptest.NewTLSServer(b)
	t.Cleanup(srv.Close)
	const origin = "https://japan.example.org"
	control := resolveExecutor(func(ctx context.Context, req fetch.Request) (fetch.Response, error) {
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
	w, err := NewPullWorker(origin, wireToken, control, p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var workerErr error
	go func() { defer close(done); workerErr = w.Run(ctx) }()
	stop := func() {
		t.Helper()
		cancel()
		select {
		case <-done:
			if workerErr != nil && !errors.Is(workerErr, context.Canceled) {
				t.Errorf("worker exit: %v", workerErr)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not stop")
		}
	}
	t.Cleanup(stop)
	return b, srv, stop
}

func deliveryRow(host, object, sig string) unified.Object {
	return unified.Object{"url": "https://" + host + strings.Replace(mediaPath, "objectA", object, 1) + "?token=" + sig, "prefix_sha256": strings.Repeat("a", 64), "metadata": goodResponse().Candidates[0].Metadata}
}

func TestPullDeliveryFrontendExamples(t *testing.T) {
	observations := make(chan []unified.Object, 1)
	b, _, stop := deliveryWorker(t, resolverFunc(func(ctx context.Context, u string) ([]unified.Object, error) {
		if u != douyin.OriginalPlayURL(wireVideo) {
			return nil, errors.New("wrong official video ID")
		}
		select {
		case rows := <-observations:
			return rows, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	parser := deliveryParser(b, nil)
	var views []unified.Object
	for i, rows := range [][]unified.Object{
		{deliveryRow("v3-dy-o.zjcdn.com", "objectA", "a"), deliveryRow("v11-cold.douyinvod.com", "objectA", "b")},
		{deliveryRow("v11-cold.douyinvod.com", "objectA", "c")},
		{deliveryRow("v11-cold.douyinvod.com", "objectB", "d")},
	} {
		waitForPullWaiter(t, b)
		observations <- rows
		now := time.Now().Unix()
		data, err := parser.Parse(t.Context(), deliveryWork, now)
		if err != nil {
			t.Fatal(err)
		}
		view := unified.PublicView(data, false, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(view)
		if !bytes.Contains(encoded, []byte("original_endpoint")) || bytes.Contains(encoded, []byte(wireToken)) {
			t.Fatalf("exchange %d missing original or leaked control credential", i)
		}
		views = append(views, view)
	}
	waitForPullWaiter(t, b)
	stop()
	waitForPullCount(t, b, 0)
	if path := os.Getenv("DOKITOOL_PULL_EXAMPLES"); path != "" {
		body, err := json.Marshal(views)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPullWorkerCancelDuringActiveProbe(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan error, 1)
	b, _, stop := deliveryWorker(t, resolverFunc(func(ctx context.Context, _ string) ([]unified.Object, error) {
		close(started)
		<-ctx.Done()
		stopped <- ctx.Err()
		return nil, ctx.Err()
	}))
	waitForPullWaiter(t, b)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := b.ResolveOriginal(ctx, wireVideo); result <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("did not reach active Shanghai probe")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Japanese request still waiting after cancellation")
	}
	stop()
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker cancellation did not stop probe")
	}
}

type japanProbeFunc func(context.Context, string) (unified.Object, error)

func (f japanProbeFunc) Probe(ctx context.Context, u string) (unified.Object, error) {
	return f(ctx, u)
}

func TestPullDisconnectedWorkerFallsBackToJapan(t *testing.T) {
	b, srv, _ := deliveryWorker(t, resolverFunc(func(context.Context, string) ([]unified.Object, error) {
		return nil, errors.New("offline worker must not receive the work")
	}))
	waitForPullWaiter(t, b)
	// Remove the listener and break the active long poll while Run is still
	// live. No replacement poll can arrive, even if Run starts reconnecting.
	srv.Listener.Close()
	srv.CloseClientConnections()
	waitForPullCount(t, b, 0)
	row := deliveryRow("japan.zjcdn.com", "objectA", "j")
	calls := 0
	parser := deliveryParser(b, japanProbeFunc(func(_ context.Context, u string) (unified.Object, error) {
		calls++
		if u != douyin.OriginalPlayURL(wireVideo) {
			return nil, errors.New("invalid Japan dispatcher")
		}
		return row, nil
	}))
	now := time.Now().Unix()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	data, err := parser.Parse(ctx, deliveryWork, now)
	if err != nil {
		t.Fatal(err)
	}
	view := unified.PublicView(data, false, now)
	if err := unified.ValidateEnvelopeAt(view, now); err != nil {
		t.Fatal(err)
	}
	items := unified.Slice(unified.Map(view["data"])["items"])
	if len(items) != 1 || calls != 1 {
		t.Fatalf("items=%d Japan probes=%d", len(items), calls)
	}
	def := unified.Map(unified.Map(items[0])["default"])
	if def["url"] != row["url"] {
		t.Fatalf("disconnected default was not the Japan original: %v", def)
	}
}
