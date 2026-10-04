// Package server: the proxy itself — one port in, backends out, with
// per-backend accounting, X-Forwarded-For, retry-on-next and the
// live status endpoint.
package server

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"lbforge/internal/balancer"
	"lbforge/internal/health"
)

type BackendView struct {
	Addr    string `json:"addr"`
	Healthy bool   `json:"healthy"`
	State   string `json:"state"`
	Conns   int64  `json:"conns"`
	Reqs    int64  `json:"reqs"`
	Fails   int64  `json:"fails"`
}

type Stats struct {
	TotalReqs  int64         `json:"total_requests"`
	TotalFails int64         `json:"backend_failures"`
	Retries    int64         `json:"retries"`
	Backends   []BackendView `json:"backends"`
	Strategy   string        `json:"strategy"`
	UptimeSec  int64         `json:"uptime_sec"`
}

type Proxy struct {
	Strategy balancer.Strategy
	Backends []balancer.Backend
	Checker  *health.Checker
	LC       *balancer.LeastConnections

	conns   map[string]*atomic.Int64
	reqs    map[string]*atomic.Int64
	fails   map[string]*atomic.Int64
	started time.Time
}

func New(strategy balancer.Strategy, backends []balancer.Backend, checker *health.Checker) *Proxy {
	p := &Proxy{
		Strategy: strategy,
		Backends: backends,
		Checker:  checker,
		conns:    map[string]*atomic.Int64{},
		reqs:     map[string]*atomic.Int64{},
		fails:    map[string]*atomic.Int64{},
		started:  time.Now(),
	}
	for _, b := range backends {
		p.conns[b.Addr] = &atomic.Int64{}
		p.reqs[b.Addr] = &atomic.Int64{}
		p.fails[b.Addr] = &atomic.Int64{}
	}
	if lc, ok := strategy.(*balancer.LeastConnections); ok {
		for _, b := range backends {
			p.conns[b.Addr] = lc.Track(b.Addr)
		}
		p.LC = lc
	}
	return p
}

func (p *Proxy) Handler() http.Handler {
	return http.HandlerFunc(p.serve)
}

var (
	totalReqs    atomic.Int64
	totalFails   atomic.Int64
	totalRetries atomic.Int64
)

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/lb/status" {
		p.statusHandler(w, r)
		return
	}
	totalReqs.Add(1)

	for attempt := 0; attempt < 2; attempt++ {
		healthy := p.healthy()
		if len(healthy) == 0 {
			http.Error(w, "no healthy backends", http.StatusServiceUnavailable)
			return
		}
		picked := p.Strategy.Pick(healthy)
		target := &url.URL{Scheme: "http", Host: picked.Addr}
		if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			prior := r.Header.Get("X-Forwarded-For")
			if prior == "" {
				r.Header.Set("X-Forwarded-For", clientIP)
			} else {
				r.Header.Set("X-Forwarded-For", prior+", "+clientIP)
			}
		}
		p.reqs[picked.Addr].Add(1)
		if attempt > 0 {
			totalRetries.Add(1)
		}

		rp := httputil.NewSingleHostReverseProxy(target)
		dialErr := p.doReverse(rp, w, r)
		if dialErr == nil {
			return
		}
		log.Printf("backend %s failed: %v", picked.Addr, dialErr)
		p.fails[picked.Addr].Add(1)
		totalFails.Add(1)
		if !isConnectFailure(dialErr) {
			return
		}
		// next attempt picks again (the failed backend stays out of the
		// healthy set once the checker opens its circuit)
	}
	w.WriteHeader(http.StatusBadGateway)
}

// doReverse runs the reverse proxy and captures a dial-phase failure
// through the proxy's ErrorHandler without writing a response.
func (p *Proxy) doReverse(rp *httputil.ReverseProxy, w http.ResponseWriter, r *http.Request) error {
	var dialErr error
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		dialErr = err
		w.WriteHeader(http.StatusBadGateway)
	}
	rp.ServeHTTP(w, r)
	return dialErr
}

func isConnectFailure(err error) bool {
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "no such host") ||
		strings.Contains(s, "i/o timeout") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "broken pipe")
}

func (p *Proxy) healthy() []balancer.Backend {
	addrs := make([]string, 0, len(p.Backends))
	for _, b := range p.Backends {
		addrs = append(addrs, b.Addr)
	}
	if p.Checker == nil {
		return p.Backends
	}
	live := map[string]bool{}
	for _, a := range p.Checker.Healthy(addrs) {
		live[a] = true
	}
	var out []balancer.Backend
	for _, b := range p.Backends {
		if live[b.Addr] {
			out = append(out, b)
		}
	}
	return out
}

func (p *Proxy) statusHandler(w http.ResponseWriter, r *http.Request) {
	states := map[string]health.State{}
	if p.Checker != nil {
		states = p.Checker.Snapshot()
	}
	view := Stats{Strategy: p.Strategy.Name(), UptimeSec: int64(time.Since(p.started).Seconds())}
	view.TotalReqs = totalReqs.Load()
	view.TotalFails = totalFails.Load()
	view.Retries = totalRetries.Load()
	for _, b := range p.Backends {
		bv := BackendView{Addr: b.Addr, Conns: p.conns[b.Addr].Load(), Reqs: p.reqs[b.Addr].Load(), Fails: p.fails[b.Addr].Load()}
		if st, ok := states[b.Addr]; ok {
			bv.State = st.String()
			bv.Healthy = st != health.Open
		} else {
			bv.Healthy = true
			bv.State = "closed"
		}
		view.Backends = append(view.Backends, bv)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(view)
}
