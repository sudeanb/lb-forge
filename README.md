# LB Forge — a layer-7 load balancer with a live panel

An HTTP reverse proxy that spreads traffic across backends with three
strategies, watches them with active health checks, trips per-backend
circuit breakers when they fail, retries on the next healthy backend,
and shows all of it on a dashboard that updates every second.

```
# three backends, weighted 3:1
lbforge -strategy weighted -backends 127.0.0.1:9001,127.0.0.1:9002 \
        -weights 3,1 -listen :8000
# open http://localhost:8000/lb/status
```

## Strategies

| strategy | behaviour |
|---|---|
| `round-robin` | atomic counter walk; failed backends still advance it so rejoining traffic stays spread |
| `least-connections` | picks the backend with the fewest live proxied connections (counters shared with the proxy) |
| `weighted` | smooth weighted round robin (nginx style): every round adds each backend's weight to its current score, the highest score wins and hands back the total weight — a 3:1 pair lands 30/10 over 40 requests |

## Health + circuit breaker

Every `healthInterval`, each backend is probed with `GET healthPath`.
Two consecutive failures (configurable) **open** the circuit: the
backend leaves the pick set. After `probeAfter`, one probe **half-opens**
the circuit; success closes it, failure re-opens it. Probes returning
5xx count as failures, 2xx-4xx as successes.

## Proxying

`httputil.ReverseProxy` per request with `X-Forwarded-For` accumulation.
A dial-phase failure (refused/timeout/reset) is captured through the
proxy's error handler **before** bytes reach the client, the backend's
failure counter ticks, and the request retries against a fresh pick.
Zero dependencies beyond the standard library.

## Testing

- strategy unit tests: round-robin cycling, least-connections ordering
  under shifting load, weighted 3:1 distribution over 40 picks
- circuit breaker: opens after N failures, half-opens after the probe
  window, closes on recovery — all with real HTTP servers on loopback
- proxy integration: traffic actually spreads across two live
  backends, and a sick backend stops receiving requests without a
  single 5xx reaching the client

MIT licensed. Part of a 100-project systems-programming campaign.
