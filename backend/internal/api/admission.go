package api

import (
	"crypto/subtle"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/shresolver"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type route uint8

const (
	unknown route = iota
	parser
	legacy
	telemetry
	media
)

// Match the same exact escaped paths for admission and dispatch.
func classify(path string) route {
	switch path {
	case "/api/parse", "/api/resolve", "/api/platforms":
		return parser
	case "/api/short_videos", "/api/douyin", "/api/ksjx", "/api/bilibili", "/api/xhsjx", "/api/toutiao":
		return legacy
	case tbvideoOutcomePath:
		return telemetry
	case "/api/media/weibo":
		return media
	default:
		return unknown
	}
}

const clientHeader = "X-DokiTool-Client-IP"
const edgeHeader = "X-DokiTool-Edge-Key"

// WithAdmission wraps the production HTTP entry point. All limits are local
// to one serving instance; the pools share neither clients nor leases.
func WithAdmission(next, pullControl http.Handler, mode, edgeKey string) (http.Handler, error) {
	switch mode {
	case "direct":
		if edgeKey != "" {
			return nil, fmt.Errorf("SNAP_INGRESS_KEY requires -client-identity caddy")
		}
	case "caddy":
		if len(edgeKey) < 32 || len(edgeKey) > 512 || strings.IndexFunc(edgeKey, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			return nil, fmt.Errorf("caddy identity requires a 32–512 byte printable SNAP_INGRESS_KEY without spaces")
		}
	default:
		return nil, fmt.Errorf("client-identity must be direct or caddy")
	}
	return &admission{next: next, pullControl: pullControl, edgeKey: edgeKey, now: time.Now,
		public:  newPool(4, 12.0/60, 8, 1, 1),
		reports: newPool(10, 1, 60, 5, 2),
		relay:   newPool(12, 1, 24, 2, 2)}, nil
}

type admission struct {
	next, pullControl      http.Handler
	edgeKey                string
	now                    func() time.Time
	public, relay, reports *pool
}

func (a *admission) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	kind := classify(path)
	client := a.client(r)
	// These headers authenticate only this hop and never belong to an upstream.
	r.Header.Del(edgeHeader)
	r.Header.Del(clientHeader)
	// Long polls and result submissions authenticate the ingress hop as usual.
	// The control handler enforces its own bearer, byte, time and concurrency limits.
	if a.pullControl != nil && (path == shresolver.ClaimPath || path == shresolver.ResultPath) {
		if !client.IsValid() {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		a.pullControl.ServeHTTP(w, r)
		return
	}
	if kind == unknown {
		a.next.ServeHTTP(w, r)
		return
	}
	if !client.IsValid() {
		rejectAdmission(w, r, kind, http.StatusServiceUnavailable, 30)
		return
	}
	budget := a.public
	if kind == media {
		budget = a.relay
	} else if kind == telemetry {
		budget = a.reports
	}
	release, retry := budget.acquire(client, a.now())
	if retry > 0 {
		rejectAdmission(w, r, kind, http.StatusTooManyRequests, retry)
		return
	}
	defer release()
	a.next.ServeHTTP(w, r)
}

func (a *admission) client(r *http.Request) netip.Addr {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	ip := peer.Addr().Unmap()
	if a.edgeKey != "" {
		keys, clients := r.Header.Values(edgeHeader), r.Header.Values(clientHeader)
		if !ip.IsLoopback() || len(keys) != 1 || len(clients) != 1 ||
			subtle.ConstantTimeCompare([]byte(keys[0]), []byte(a.edgeKey)) != 1 {
			return netip.Addr{}
		}
		ip, err = netip.ParseAddr(clients[0])
		if err != nil {
			return netip.Addr{}
		}
		ip = ip.Unmap()
	} else if len(r.Header.Values(edgeHeader)) != 0 || len(r.Header.Values(clientHeader)) != 0 {
		// A proxy accidentally paired with direct mode must not collapse
		// all users into one loopback identity.
		return netip.Addr{}
	}
	if ip.Zone() != "" {
		return netip.Addr{}
	}
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().Addr()
	}
	return ip
}

type bucket struct {
	tokens float64
	at     time.Time
}

func (b bucket) available(now time.Time, burst, rate float64) float64 {
	if b.at.IsZero() {
		return burst
	}
	return min(burst, b.tokens+max(0, now.Sub(b.at).Seconds())*rate)
}

type clientState struct {
	bucket
	active int
}

// Only the short accounting transaction is serialized. No lock is held
// during parsing, upstream reads or writes to the client.
type pool struct {
	mu                                               sync.Mutex
	clientBurst, clientRate, globalBurst, globalRate float64
	clientSlots, active                              int
	global                                           bucket
	clients                                          map[netip.Addr]*clientState
}

func newPool(clientBurst, clientRate, globalBurst, globalRate float64, clientSlots int) *pool {
	return &pool{clientBurst: clientBurst, clientRate: clientRate, globalBurst: globalBurst,
		globalRate: globalRate, clientSlots: clientSlots, clients: make(map[netip.Addr]*clientState)}
}

func (p *pool) acquire(ip netip.Addr, now time.Time) (release func(), retrySeconds int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Concurrent arrivals can obtain the lock in a different order than
	// their timestamps; never credit the same interval twice.
	if now.Before(p.global.at) {
		now = p.global.at
	}
	c := p.clients[ip]
	if c == nil {
		if len(p.clients) >= 1024 {
			for ip, state := range p.clients {
				if state.active == 0 && now.Sub(state.at) >= 10*time.Minute {
					delete(p.clients, ip)
				}
			}
			if len(p.clients) >= 1024 {
				return nil, 60
			}
		}
		c = new(clientState)
	}
	local := c.available(now, p.clientBurst, p.clientRate)
	global := p.global.available(now, p.globalBurst, p.globalRate)
	if local < 1 || global < 1 {
		return nil, int(math.Ceil(max(1, (1-local)/p.clientRate, (1-global)/p.globalRate)))
	}
	if c.active >= p.clientSlots || p.active >= 2 {
		return nil, 1
	}
	c.bucket = bucket{tokens: local - 1, at: now}
	p.global = bucket{tokens: global - 1, at: now}
	c.active++
	p.active++
	p.clients[ip] = c
	return func() {
		p.mu.Lock()
		c.active--
		p.active--
		p.mu.Unlock()
	}, 0
}

func rejectAdmission(w http.ResponseWriter, r *http.Request, kind route, status, retry int) {
	label := "解析"
	if kind == media {
		label = "媒体"
	}
	code, message := "RATE_LIMITED", label+"请求过于频繁，请稍后重试"
	if status == http.StatusServiceUnavailable {
		code, message = "SERVICE_NOT_CONFIGURED", label+"服务暂时不可用，请稍后重试"
	}
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	payload := unified.Object{"code": status, "msg": message, "data": nil}
	if kind == parser {
		payload = unified.ContractError(code, message, status == 429)
		if status == 429 {
			unified.Map(payload["error"])["retry_after_seconds"] = retry
		}
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(status)
		return
	}
	send(w, status, payload)
}
