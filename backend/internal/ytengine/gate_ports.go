package ytengine

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
)

// A pool belongs to the base engine; all platform policy copies share it.
// An empty configuration retains dynamic ports only for environments that allow
// dynamic loopback. Restricted deployments must explicitly configure fixed ports.
type gatePortPool struct {
	mu    sync.Mutex
	ports []int
	used  map[int]bool
}

func parseGatePorts(raw string) ([]int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ports []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" || strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return nil, errors.New("DOKITOOL_YTDLP_GATE_PORTS must be comma-separated decimal ports")
		}
		port, err := strconv.Atoi(part)
		if err != nil {
			return nil, errors.New("DOKITOOL_YTDLP_GATE_PORTS contains an invalid port")
		}
		ports = append(ports, port)
	}
	return ports, nil
}

func newGatePortPool(ports []int, concurrency int) (*gatePortPool, error) {
	if len(ports) == 0 {
		return nil, nil
	}
	if len(ports) > maxAllowedConcurrency || len(ports) != concurrency {
		return nil, errors.New("DOKITOOL_YTDLP_GATE_PORTS count must equal shared concurrency (1-8)")
	}
	p := &gatePortPool{ports: append([]int(nil), ports...), used: make(map[int]bool)}
	seen := make(map[int]bool)
	for _, port := range ports {
		if port < 1024 || port > 65535 || seen[port] {
			return nil, errors.New("DOKITOOL_YTDLP_GATE_PORTS must contain unique ports between 1024 and 65535")
		}
		seen[port] = true
	}
	return p, nil
}

func (p *gatePortPool) listen() (net.Listener, func(), error) {
	if p == nil {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		return ln, func() {}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, port := range p.ports {
		if p.used[port] {
			continue
		}
		ln, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			continue
		} // Never connect to an existing service or fall back to :0.
		p.used[port] = true
		var once sync.Once
		return ln, func() { once.Do(func() { p.mu.Lock(); delete(p.used, port); p.mu.Unlock() }) }, nil
	}
	return nil, nil, fmt.Errorf("configured loopback gateway ports are busy or unavailable")
}

type gateCredential struct{ password, authorization string }

func newGateCredential() (gateCredential, error) {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return gateCredential{}, errors.New("generate proxy task credential failed")
	}
	password := base64.RawURLEncoding.EncodeToString(entropy[:])
	return gateCredential{password: password, authorization: "Basic " + base64.StdEncoding.EncodeToString([]byte("task:"+password))}, nil
}
