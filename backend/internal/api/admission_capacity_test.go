package api

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestAdmissionConfigDefaultsAndOverrides(t *testing.T) {
	defaults, err := LoadAdmissionConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Parse.GlobalConcurrency != 16 || defaults.Parse.GlobalRPS != 10 ||
		defaults.Parse.ClientConcurrency != 1 || defaults.Relay.GlobalConcurrency != 12 {
		t.Fatalf("wrong production defaults: %+v", defaults)
	}
	values := map[string]string{
		"DOKITOOL_PARSE_GLOBAL_CONCURRENCY": "32", "DOKITOOL_PARSE_GLOBAL_RPS": "25",
		"DOKITOOL_PARSE_GLOBAL_BURST": "64", "DOKITOOL_PARSE_CLIENT_CONCURRENCY": "2",
		"DOKITOOL_PARSE_CLIENT_RPS": "0.5", "DOKITOOL_CATALOG_GLOBAL_CONCURRENCY": "80",
		"DOKITOOL_WEIBO_GLOBAL_CONCURRENCY": "8", "DOKITOOL_REPORT_GLOBAL_CONCURRENCY": "20",
		"DOKITOOL_ADMISSION_MAX_CLIENTS": "8192",
	}
	got, err := LoadAdmissionConfig(func(k string) string { return values[k] })
	if err != nil {
		t.Fatal(err)
	}
	if got.Parse.GlobalConcurrency != 32 || got.Parse.GlobalRPS != 25 || got.Parse.GlobalBurst != 64 ||
		got.Parse.ClientConcurrency != 2 || got.Parse.ClientRPS != 0.5 || got.Catalog.GlobalConcurrency != 80 ||
		got.Relay.GlobalConcurrency != 8 || got.Reports.GlobalConcurrency != 20 || got.MaxClients != 8192 {
		t.Fatalf("configuration ignored: %+v", got)
	}
	for _, tc := range []struct{ key, value string }{
		{"DOKITOOL_PARSE_GLOBAL_RPS", "NaN"}, {"DOKITOOL_PARSE_CLIENT_RPS", "+Inf"},
		{"DOKITOOL_PARSE_GLOBAL_BURST", "0.5"}, {"DOKITOOL_PARSE_CLIENT_BURST", "0"},
		{"DOKITOOL_PARSE_CLIENT_CONCURRENCY", "17"}, {"DOKITOOL_PARSE_GLOBAL_CONCURRENCY", "0"},
		{"DOKITOOL_PARSE_GLOBAL_CONCURRENCY", "2.5"}, {"DOKITOOL_WEIBO_GLOBAL_CONCURRENCY", "13"},
		{"DOKITOOL_ADMISSION_MAX_CLIENTS", "65537"}, {"DOKITOOL_REPORT_GLOBAL_RPS", "secret-invalid"},
	} {
		_, err := LoadAdmissionConfig(func(k string) string {
			if k == tc.key {
				return tc.value
			}
			return ""
		})
		if err == nil || strings.Contains(err.Error(), "secret-invalid") {
			t.Fatalf("bad configuration accepted or value leaked: %s %v", tc.key, err)
		}
	}
}

func TestAdmissionCatalogIndependentOfBusyParse(t *testing.T) {
	called := 0
	h, err := WithAdmission(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(204) }), nil, "caddy", testEdgeKey)
	if err != nil {
		t.Fatal(err)
	}
	a := h.(*admission)
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for i := 0; i < 16; i++ {
		release, retry := a.public.acquire(netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i+1)), now)
		if retry != 0 {
			t.Fatal(retry)
		}
		releases = append(releases, release)
	}
	if w := call(h, edgeRequest("/api/parse", "198.51.100.1")); w.Code != 429 {
		t.Fatal("global capacity not enforced", w.Code)
	}
	if w := call(h, edgeRequest("/api/platforms?schema_version=1.0", "192.0.2.1")); w.Code != 204 {
		t.Fatal("catalog blocked by parsing", w.Code)
	}
	if w := call(h, edgeRequest("/api/media/weibo", "192.0.2.1")); w.Code != 204 {
		t.Fatal("media pool coupled", w.Code)
	}
	if called != 2 {
		t.Fatal(called)
	}
}

func TestAdmissionGlobalCapacityDoesNotRelaxClientProtection(t *testing.T) {
	h, err := WithAdmission(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), nil, "caddy", testEdgeKey)
	if err != nil {
		t.Fatal(err)
	}
	a := h.(*admission)
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	release, retry := a.public.acquire(netip.MustParseAddr("192.0.2.1"), now)
	if retry != 0 {
		t.Fatal(retry)
	}
	if w := call(h, edgeRequest("/api/parse", "192.0.2.1")); w.Code != 429 {
		t.Fatal("same IP parallel request allowed", w.Code)
	}
	if w := call(h, edgeRequest("/api/parse", "192.0.2.2")); w.Code != 204 {
		t.Fatal("other user rejected", w.Code)
	}
	release()
	release() // A repeated cleanup must not mint a concurrency slot.
	for i := 0; i < 3; i++ {
		if w := call(h, edgeRequest("/api/parse", "192.0.2.1")); w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
	if w := call(h, edgeRequest("/api/resolve", "192.0.2.1")); w.Code != 429 || w.Header().Get("Retry-After") != "5" {
		t.Fatal("client rate limit relaxed", w.Code, w.Header())
	}
	if a.public.active != 0 {
		t.Fatal("lease count leaked", a.public.active)
	}
}

func TestAdmissionConfiguredRateAndConcurrencyIndependent(t *testing.T) {
	p := newConfiguredPool(PoolConfig{10, 1, 6, 2, 2, 4}, 100)
	now := time.Unix(1000, 0)
	var release []func()
	for i := 0; i < 4; i++ {
		f, retry := p.acquire(netip.AddrFrom4([4]byte{192, 0, 2, byte(i + 1)}), now)
		if retry != 0 {
			t.Fatal(retry)
		}
		release = append(release, f)
	}
	fifth := netip.MustParseAddr("192.0.2.9")
	if f, retry := p.acquire(fifth, now); f != nil || retry != 1 {
		t.Fatal("configured global slots not enforced", retry)
	}
	for _, f := range release {
		f()
	}
	for i := 0; i < 2; i++ {
		f, retry := p.acquire(fifth, now)
		if retry != 0 {
			t.Fatal(retry)
		}
		f()
	}
	if f, retry := p.acquire(fifth, now); f != nil || retry != 1 {
		t.Fatal("global tokens not enforced", retry)
	}
	f, retry := p.acquire(fifth, now.Add(500*time.Millisecond))
	if retry != 0 {
		t.Fatal(retry)
	}
	f()
}

func TestAdmissionTableReclaimsOnlyFullIdleBuckets(t *testing.T) {
	p := newConfiguredPool(PoolConfig{1, 1, 100, 100, 1, 8}, 2)
	now := time.Unix(1000, 0)
	first := netip.MustParseAddr("192.0.2.1")
	second := netip.MustParseAddr("192.0.2.2")
	third := netip.MustParseAddr("192.0.2.3")
	release1, _ := p.acquire(first, now)
	defer release1()
	release2, _ := p.acquire(second, now)
	release2()
	if f, retry := p.acquire(third, now); f != nil || retry != 60 {
		t.Fatal("penalty state was evicted", retry)
	}
	f, retry := p.acquire(third, now.Add(time.Second))
	if retry != 0 {
		t.Fatal("full idle state not reclaimed", retry)
	}
	f()
	if p.clients[first] == nil || p.clients[first].active != 1 {
		t.Fatal("live client evicted")
	}
	if p.clients[second] != nil || len(p.clients) != 2 {
		t.Fatal("table reclamation failed")
	}
}
