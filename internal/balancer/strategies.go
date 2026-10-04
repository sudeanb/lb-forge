// Package balancer holds the balancing strategies. A Strategy picks the
// next backend from the healthy set.
package balancer

import (
	"sync"
	"sync/atomic"
)

// Backend is one upstream.
type Backend struct {
	Name string
	Addr string // host:port
}

// Strategy picks among healthy backends.
type Strategy interface {
	Pick(healthy []Backend) Backend
	Name() string
}

// RoundRobin walks the full list with a counter; unhealthy entries
// still advance the counter so rejoin traffic stays spread.
type RoundRobin struct {
	n atomic.Uint64
}

func NewRoundRobin() *RoundRobin { return &RoundRobin{} }

func (rr *RoundRobin) Name() string        { return "round-robin" }
func (rr *RoundRobin) Pick(b []Backend) Backend {
	if len(b) == 0 {
		return Backend{}
	}
	return b[(rr.n.Add(1)-1)%uint64(len(b))]
}

// LeastConnections asks each backend's live counter.
type LeastConnections struct {
	conns map[string]*atomic.Int64
	mu    sync.Mutex
}

func NewLeastConnections() *LeastConnections {
	return &LeastConnections{conns: map[string]*atomic.Int64{}}
}

func (lc *LeastConnections) Name() string { return "least-connections" }

// Track registers a backend's connection counter.
func (lc *LeastConnections) Track(addr string) *atomic.Int64 {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.conns == nil {
		lc.conns = map[string]*atomic.Int64{}
	}
	c, ok := lc.conns[addr]
	if !ok {
		c = &atomic.Int64{}
		lc.conns[addr] = c
	}
	return c
}

func (lc *LeastConnections) Pick(b []Backend) Backend {
	if len(b) == 0 {
		return Backend{}
	}
	var best Backend
	var bestN int64 = 1<<62
	for _, bk := range b {
		var n int64
		if c, ok := lc.conns[bk.Addr]; ok {
			n = c.Load()
		}
		if n < bestN {
			bestN, best = n, bk
		}
	}
	return best
}

// Weighted picks by weight proportional scheduling (smooth weighted
// round robin, nginx style).
type Weighted struct {
	Items []WeightedBackend
	mu    sync.Mutex
	cur   map[string]int
	eff   map[string]int
}

type WeightedBackend struct {
	Backend
	Weight int
}

func NewWeighted(items []WeightedBackend) *Weighted {
	return &Weighted{Items: items, cur: map[string]int{}, eff: map[string]int{}}
}

func (w *Weighted) Name() string { return "weighted" }

func (w *Weighted) Pick(healthy []Backend) Backend {
	w.mu.Lock()
	defer w.mu.Unlock()
	healthySet := map[string]bool{}
	for _, b := range healthy {
		healthySet[b.Addr] = true
	}
	if len(healthySet) == 0 {
		for _, it := range w.Items {
			healthySet[it.Backend.Addr] = true
		}
	}
	total := 0
	for _, it := range w.Items {
		if !healthySet[it.Backend.Addr] {
			continue
		}
		w.eff[it.Addr] += it.Weight
		total += it.Weight
	}
	if total == 0 {
		return Backend{}
	}
	var picked string
	maxEff := -1
	for _, it := range w.Items {
		if !healthySet[it.Backend.Addr] {
			continue
		}
		if e := w.eff[it.Addr]; e > maxEff {
			maxEff, picked = e, it.Addr
		}
	}
	// smooth WRR: only the picked entry hands its total back
	w.eff[picked] -= total
	for _, it := range w.Items {
		if it.Addr == picked {
			return it.Backend
		}
	}
	return Backend{}
}
