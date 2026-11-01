package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/catalog"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/platform/xiaohongshu"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type xhsHTTPOracleTape struct {
	t                   *testing.T
	responses, requests unified.Array
	next                int
}

func (f *xhsHTTPOracleTape) Do(ctx context.Context, request fetch.Request) (fetch.Response, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return fetch.Response{}, err
	}
	if _, ok := ctx.Deadline(); !ok || request.Policy.AllowedURL == nil || !request.Policy.AllowedURL(request.URL) || request.Policy.FollowRedirects {
		f.t.Fatal("missing XHS policy/deadline")
	}
	if f.next >= len(f.responses) || f.next >= len(f.requests) {
		f.t.Fatalf("unexpected XHS HTTP request %s", request.URL)
	}
	wanted := unified.Clone(unified.Map(f.requests[f.next]))
	step := unified.Map(f.responses[f.next])
	headers := unified.Object{}
	for key, values := range request.Header {
		headers[strings.ToLower(key)] = strings.Join(values, ", ")
	}
	byteRange := strings.TrimPrefix(request.Header.Get("Range"), "bytes=")
	observed := unified.Object{"url": request.URL, "method": request.Method, "headers": headers, "body": string(request.Body),
		"follow_redirects": request.Policy.FollowRedirects, "timeout_ms": int64(request.Policy.Timeout / time.Millisecond), "range": byteRange}
	if byteRange == "" {
		if request.Policy.Purpose != "xiaohongshu-page" || request.Policy.MaxBytes != 8<<20 || request.Policy.Timeout <= 0 || request.Policy.Timeout > 10*time.Second {
			f.t.Fatal("invalid page request budget")
		}
		unified.Map(wanted["headers"])["accept-encoding"] = "gzip,deflate"
	} else {
		if request.Policy.Purpose != "media-probe" || request.Policy.MaxBytes != 65536 || !request.Policy.StrictMediaHeaders || request.Policy.Timeout <= 0 || request.Policy.Timeout > 6*time.Second {
			f.t.Fatal("invalid container probe request budget")
		}
		delete(observed, "timeout_ms")
		delete(wanted, "timeout_ms")
	}
	xhsHTTPJSON(f.t, observed, wanted)
	if request.URL != step["url"] || byteRange != step["range"] {
		f.t.Fatal("PHP request observation differs from tape")
	}
	f.next++
	if code, _ := unified.Numeric(step["error"]); code != 0 {
		return fetch.Response{}, errors.New("synthetic transport failure")
	}
	body, err := base64.StdEncoding.DecodeString(unified.Str(step["body_b64"]))
	if err != nil {
		f.t.Fatal(err)
	}
	status, _ := unified.Numeric(step["status"])
	reply := fetch.Response{StatusCode: int(status), Header: http.Header{}, Body: body, URL: request.URL}
	for key, value := range unified.Map(step["headers"]) {
		if values := unified.Slice(value); values != nil {
			for _, value := range values {
				reply.Header.Add(key, unified.Str(value))
			}
		} else {
			reply.Header.Set(key, unified.Str(value))
		}
	}
	return reply, nil
}

func xhsHTTPJSON(t *testing.T, got, want any) {
	t.Helper()
	if !unified.Equal(got, want) {
		a, _ := unified.Encode(got)
		b, _ := unified.Encode(want)
		t.Fatalf("XHS HTTP differs from frozen PHP: got=%s want=%s", a, b)
	}
}

func TestXiaohongshuHTTPFrozenPHP(t *testing.T) {
	body, err := os.ReadFile("../platform/xiaohongshu/testdata/php-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := unified.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range unified.Slice(document["cases"]) {
		fixture := unified.Map(raw)
		input, expected := unified.Map(fixture["input"]), unified.Map(fixture["expected"])
		// The parser historically accepts xhs.com aliases, but the frozen
		// public catalog does not. Preserve that gateway preflight boundary.
		gatewayRejects := strings.Contains(unified.Str(fixture["id"]), "/entry/reject-") || fixture["id"] == "xiaohongshu/entry/xhs-com-alias"
		for _, view := range []string{"normal", "debug"} {
			base := unified.Map(expected[view])
			choices := []string{""}
			if base["code"] == "OK" && !gatewayRejects {
				for _, item := range unified.Slice(unified.Map(base["data"])["items"]) {
					if unified.Map(item)["state"] == nil || unified.Map(item)["state"] == "ready" {
						choices = append(choices, unified.Str(unified.Map(item)["id"]))
					}
				}
				choices = append(choices, "missing-item")
			}
			for _, itemID := range choices {
				t.Run(unified.Str(fixture["id"])+"/"+view+"/"+itemID, func(t *testing.T) {
					tape := &xhsHTTPOracleTape{t: t, responses: unified.Slice(input["http_responses"]), requests: unified.Slice(expected["http_requests"])}
					if gatewayRejects {
						tape.responses, tape.requests = nil, nil
					}
					now, _ := unified.PHPInt(input["now"])
					parser := xiaohongshu.NewWithClock(tape, func() time.Time { return time.Unix(100, 0) })
					handler, err := New(registry, map[string]Adapter{"xiaohongshu": parser}, func() time.Time { return time.Unix(now, 0) }, nil)
					if err != nil {
						t.Fatal(err)
					}
					query := url.Values{"schema_version": {"1.0"}, "url": {unified.Str(input["url"])}}
					if view == "debug" {
						query.Set("debug", "1")
					}
					route := "parse"
					if itemID != "" {
						route = "resolve"
						query.Set("item_id", itemID)
					}
					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/"+route+"?"+query.Encode(), nil))
					actual, err := unified.Decode(recorder.Body.Bytes())
					if err != nil {
						t.Fatal(err)
					}
					want := unified.Clone(base)
					if gatewayRejects {
						want = unified.Object{"schema_version": "1.0", "code": "INVALID_INPUT", "error": unified.Object{"code": "INVALID_INPUT", "message": "请输入对应平台的有效分享链接"}}
					} else if itemID == "missing-item" {
						want = unified.Object{"schema_version": "1.0", "code": "CONTENT_UNAVAILABLE", "error": unified.Object{"code": "CONTENT_UNAVAILABLE", "message": "请求的媒体条目暂时不可用"}}
					} else if itemID != "" {
						unified.Map(want["data"])["default_item_id"] = itemID
					}
					xhsHTTPJSON(t, actual, want)
					status := map[string]int{"OK": 200, "INVALID_INPUT": 400, "UPSTREAM_BLOCKED": 403, "RATE_LIMITED": 429, "CONTENT_UNAVAILABLE": 404, "NO_MEDIA": 404, "UPSTREAM_INVALID_RESPONSE": 502, "UPSTREAM_UNAVAILABLE": 502}[unified.Str(want["code"])]
					if status == 0 || recorder.Code != status || tape.next != len(tape.responses) {
						t.Fatalf("status/tape mismatch: status %d want %d, consumed %d/%d", recorder.Code, status, tape.next, len(tape.responses))
					}
					if recorder.Header().Get("Content-Type") != "application/json; charset=utf-8" || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" || recorder.Header().Get("Access-Control-Allow-Origin") != "" {
						t.Fatal("public response headers changed")
					}
				})
			}
		}
	}
}
