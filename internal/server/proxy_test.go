package server

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lbforge/internal/balancer"
	"lbforge/internal/health"
)

// spawnBackend starts a tiny HTTP server that answers /healthz and
// everything else with its own name.
func spawnBackend(t *testing.T, name string, healthy *atomic.Bool) (addr string, hits *atomic.Int64) {
	t.Helper()
	hits = &atomic.Int64{}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if healthy.Load() {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(500)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !healthy.Load() {
			w.WriteHeader(500)
			fmt.Fprint(w, name+":unhealthy")
			return
		}
		fmt.Fprint(w, name)
	})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return l.Addr().String(), hits
}

func TestProxy_RoundRobinSpread(t *testing.T) {
	h1, h2 := &atomic.Bool{}, &atomic.Bool{}
	h1.Store(true)
	h2.Store(true)
	a1, h1hits := spawnBackend(t, "one", h1)
	a2, h2hits := spawnBackend(t, "two", h2)

	urls := map[string]string{a1: "http://" + a1 + "/healthz", a2: "http://" + a2 + "/healthz"}
	ck := health.New(urls, health.Config{Interval: 50 * time.Millisecond, Timeout: 300 * time.Millisecond, FailAfter: 1})
	stop := make(chan struct{})
	go ck.Run(stop)
	t.Cleanup(func() { close(stop) })
	ck.ProbeOnce() // populate states

	p := New(balancer.NewRoundRobin(), []balancer.Backend{
		{Name: "one", Addr: a1}, {Name: "two", Addr: a2},
	}, ck)

	h := p.Handler()
	req, _ := http.NewRequest("GET", "http://lb/x", nil)
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
	}
	if h1hits.Load() == 0 || h2hits.Load() == 0 {
		t.Fatalf("spread broken: %d/%d", h1hits.Load(), h2hits.Load())
	}
}

func TestProxy_FlipsAwayFromSickBackend(t *testing.T) {
	h1, h2 := &atomic.Bool{}, &atomic.Bool{}
	h1.Store(false) // backend one starts sick
	h2.Store(true)
	a1, _ := spawnBackend(t, "one", h1)
	a2, h2hits := spawnBackend(t, "two", h2)

	urls := map[string]string{a1: "http://" + a1 + "/healthz", a2: "http://" + a2 + "/healthz"}
	ck := health.New(urls, health.Config{Interval: 30 * time.Millisecond, Timeout: 300 * time.Millisecond, FailAfter: 1})
	stop := make(chan struct{})
	go ck.Run(stop)
	t.Cleanup(func() { close(stop) })
	ck.ProbeOnce()
	ck.ProbeOnce()

	p := New(balancer.NewRoundRobin(), []balancer.Backend{
		{Name: "one", Addr: a1}, {Name: "two", Addr: a2},
	}, ck)

	h := p.Handler()
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "http://lb/x", nil)
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("request %d: %d", i, w.Code)
		}
		if strings.Contains(w.Body.String(), "unhealthy") {
			t.Fatalf("sick backend served: %s", w.Body.String())
		}
	}
	if h2hits.Load() == 0 {
		t.Fatal("healthy backend never hit")
	}
}

func TestCircuitBreaker_OpensAndHalfOpens(t *testing.T) {
	h := &atomic.Bool{}
	h.Store(false) // failing
	a, hits := spawnBackend(t, "sick", h)
	urls := map[string]string{a: "http://" + a + "/healthz"}
	ck := health.New(urls, health.Config{Interval: 20 * time.Millisecond, Timeout: 200 * time.Millisecond, FailAfter: 2, ProbeAfter: 300 * time.Millisecond})
	stop := make(chan struct{})
	go ck.Run(stop)
	t.Cleanup(func() { close(stop) })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ck.Snapshot()[a] == health.Open {
			goto opened
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("circuit never opened")
opened:
	// recover the backend; the circuit must go half-open then closed
	h.Store(true)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ck.Snapshot()[a] == health.Closed && hits.Load() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("circuit never recovered")
}
