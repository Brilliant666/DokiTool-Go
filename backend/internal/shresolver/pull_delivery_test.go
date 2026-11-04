package shresolver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

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

// Exercise the Japanese broker and unchanged parser/adapter. The remote peer
// supplies controlled receipts over test TLS; it is not a Shanghai process.
func TestPullDeliveryFrontendExamples(t *testing.T) {
	b, err := NewPullBroker(wireToken)
	if err != nil {
		t.Fatal(err)
	}
	b.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	srv := httptest.NewTLSServer(b)
	defer srv.Close()
	ctx := t.Context()
	post := func(path string, body []byte) (*http.Response, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer "+wireToken)
		return srv.Client().Do(r)
	}
	const id = "7673404708262453110"
	detail := resolveExecutor(func(_ context.Context, req fetch.Request) (fetch.Response, error) {
		if req.Policy.Purpose != "douyin-detail" {
			return fetch.Response{}, fmt.Errorf("unexpected platform request")
		}
		return fetch.Response{StatusCode: 200, Body: []byte(`{"status_code":0,"aweme_detail":{"aweme_id":"` + id + `","video":{"duration":13667,"play_addr":{"uri":"` + wireVideo + `"},"bit_rate":[{"play_addr":{"width":2160,"height":3840,"data_size":8662403,"url_list":["https://example.net/transcode.mp4"]},"bit_rate":5000000}]}}}`)}, nil
	})
	parser := douyin.NewWithOriginalResolver(detail, nil, b)
	row := func(host, object, sig string) unified.Object {
		return unified.Object{"url": "https://" + host + strings.Replace(mediaPath, "objectA", object, 1) + "?token=" + sig, "prefix_sha256": strings.Repeat("a", 64), "metadata": goodResponse().Candidates[0].Metadata}
	}
	var views []unified.Object
	for i, rows := range [][]unified.Object{
		{row("v3-dy-o.zjcdn.com", "objectA", "a"), row("v11-cold.douyinvod.com", "objectA", "b")},
		{row("v11-cold.douyinvod.com", "objectA", "c")},
		{row("v11-cold.douyinvod.com", "objectB", "d")},
	} {
		// Exactly one scripted exchange; the fixture never contacts an upstream.
		receipt := make(chan error, 1)
		go func() {
			res, err := post(ClaimPath, nil)
			if err != nil {
				receipt <- err
				return
			}
			defer res.Body.Close()
			var job PullJob
			if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&job) != nil || job.VideoID != wireVideo {
				receipt <- fmt.Errorf("unexpected claim HTTP %d", res.StatusCode)
				return
			}
			payload, err := probeOriginal(ctx, resolverFunc(func(context.Context, string) ([]unified.Object, error) { return rows, nil }), job.VideoID)
			if err != nil {
				receipt <- err
				return
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				receipt <- err
				return
			}
			body, err := json.Marshal(PullResult{PullJob: job, Result: raw})
			if err != nil {
				receipt <- err
				return
			}
			ack, err := post(ResultPath, body)
			if err == nil {
				ack.Body.Close()
				if ack.StatusCode != 204 {
					err = fmt.Errorf("receipt HTTP %d", ack.StatusCode)
				}
			}
			receipt <- err
		}()
		waitForPullWaiter(t, b)
		now := time.Now().Unix()
		data, err := parser.Parse(ctx, "https://www.douyin.com/video/"+id, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := <-receipt; err != nil {
			t.Fatal(err)
		}
		view := unified.PublicView(data, false, now)
		if err := unified.ValidateEnvelopeAt(view, now); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(view)
		if !bytes.Contains(encoded, []byte("original_endpoint")) || bytes.Contains(encoded, []byte(wireToken)) {
			t.Fatalf("exchange %d lost original or leaked control credential", i)
		}
		views = append(views, view)
	}
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
