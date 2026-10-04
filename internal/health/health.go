// Package health: active health checks (HTTP GET /healthz) plus the
// circuit-breaker state machine per backend (closed → open → half-open).
package health

import (
	"net/http"
	"sync"
	"time"
)

// State of a backend's circuit.
type State int32

const (
	Closed   State = iota // passing traffic
	Open                  // failing: rejecting
	HalfOpen              // probing with a single request
)

func (s State) String() string {
	switch s {
	case Open:
		return "open"
	case HalfOpen:
		return "half-open"
	}
	return "closed"
}

// Config for the checker + breaker.
type Config struct {
	Interval   time.Duration // probe period
	Timeout    time.Duration // probe deadline
	FailAfter  int           // consecutive failures → open the circuit
	ProbeAfter time.Duration // open → half-open after this long
}

type record struct {
	fails     int
	state     State
	openedAt  time.Time
	probing   bool
}

// Checker runs periodic GETs and flips circuit states.
type Checker struct {
	URLs map[string]string // addr -> health URL
	Cfg  Config
	Client *http.Client

	mu      sync.Mutex
	records map[string]*record
}

func New(urls map[string]string, cfg Config) *Checker {
	if cfg.Interval <= 0 {
		cfg.Interval = 2 * time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = time.Second
	}
	if cfg.FailAfter <= 0 {
		cfg.FailAfter = 2
	}
	if cfg.ProbeAfter <= 0 {
		cfg.ProbeAfter = 5 * time.Second
	}
	return &Checker{
		URLs:    urls,
		Cfg:     cfg,
		Client:  &http.Client{Timeout: cfg.Timeout},
		records: map[string]*record{},
	}
}

// Run blocks, probing every Interval until stop is closed.
func (c *Checker) Run(stop <-chan struct{}) {
	t := time.NewTicker(c.Cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			c.ProbeOnce()
		}
	}
}

// ProbeOnce checks every backend once, synchronously.
func (c *Checker) ProbeOnce() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for addr, url := range c.URLs {
		rec := c.records[addr]
		if rec == nil {
			rec = &record{}
			c.records[addr] = rec
		}
		// open circuits stay open until ProbeAfter elapses
		if rec.state == Open && time.Since(rec.openedAt) < c.Cfg.ProbeAfter {
			continue
		}
		if rec.state == Open {
			rec.state = HalfOpen
			rec.probing = true
		}
		ok := probe(c.Client, url)
		switch rec.state {
		case HalfOpen:
			if ok {
				rec.state = Closed
				rec.fails = 0
				rec.probing = false
			} else {
				rec.state = Open
				rec.openedAt = time.Now()
			}
		default:
			if ok {
				rec.fails = 0
			} else {
				rec.fails++
				if rec.fails >= c.Cfg.FailAfter {
					rec.state = Open
					rec.openedAt = time.Now()
				}
			}
		}
	}
}

func probe(client *http.Client, url string) bool {
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// Snapshot returns each addr's circuit state.
func (c *Checker) Snapshot() map[string]State {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]State{}
	for addr, rec := range c.records {
		out[addr] = rec.state
	}
	return out
}

// Healthy lists addrs whose circuit passes traffic.
func (c *Checker) Healthy(all []string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, addr := range all {
		rec := c.records[addr]
		if rec == nil || rec.state == Closed || rec.state == HalfOpen {
			out = append(out, addr)
		}
	}
	return out
}
