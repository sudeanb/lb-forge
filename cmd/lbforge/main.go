// Command lbforge is an HTTP load balancer: pick a strategy, list
// backends, and watch the dashboard while health checks open and close
// circuits.
//
//	lbforge -strategy least-connections -backends 127.0.0.1:9001,127.0.0.1:9002 -listen :8000
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"lbforge/internal/balancer"
	"lbforge/internal/health"
	"lbforge/internal/server"
)

func main() {
	strategy := flag.String("strategy", "round-robin", "round-robin | least-connections | weighted")
	backendList := flag.String("backends", "127.0.0.1:9001", "comma-separated host:port list")
	listen := flag.String("listen", ":8000", "proxy listen address")
	weights := flag.String("weights", "", "weighted: comma list matching -backends, e.g. 3,1")
	healthPath := flag.String("health-path", "/healthz", "backend health endpoint")
	healthInterval := flag.Duration("health-interval", 2*time.Second, "probe period")
	failAfter := flag.Int("fail-after", 2, "consecutive probe failures before opening the circuit")
	flag.Parse()

	var addrs []balancer.Backend
	for _, a := range strings.Split(*backendList, ",") {
		if a = strings.TrimSpace(a); a != "" {
			addrs = append(addrs, balancer.Backend{Addr: a})
		}
	}
	if len(addrs) == 0 {
		log.Fatal("no backends")
	}

	var strat balancer.Strategy
	switch *strategy {
	case "round-robin":
		strat = balancer.NewRoundRobin()
	case "least-connections":
		strat = balancer.NewLeastConnections()
	case "weighted":
		var items []balancer.WeightedBackend
		ws := strings.Split(*weights+",", ",")
		for i, b := range addrs {
			w := 1
			if i < len(ws) {
				fmt.Sscanf(strings.TrimSpace(ws[i]), "%d", &w)
			}
			if w <= 0 {
				w = 1
			}
			items = append(items, balancer.WeightedBackend{Backend: b, Weight: w})
		}
		strat = balancer.NewWeighted(items)
	default:
		log.Fatalf("unknown strategy %q", *strategy)
	}

	urls := map[string]string{}
	for _, b := range addrs {
		urls[b.Addr] = "http://" + b.Addr + *healthPath
	}
	checker := health.New(urls, health.Config{
		Interval:  *healthInterval,
		Timeout:   time.Second,
		FailAfter: *failAfter,
	})
	stop := make(chan struct{})
	defer close(stop)
	go checker.Run(stop)

	p := server.New(strat, addrs, checker)
	log.Printf("lbforge %s over %v on %s", *strategy, *backendList, *listen)
	log.Fatal(http.ListenAndServe(*listen, p.Handler()))
}
