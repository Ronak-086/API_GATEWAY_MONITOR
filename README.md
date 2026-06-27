# 🚀 Gateway — High-Performance Distributed API Gateway in Go

[![Go Version](https://img.shields.io/badge/Go-1.22-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://www.docker.com/)
[![Redis](https://img.shields.io/badge/Redis-7-DC382D?style=flat&logo=redis)](https://redis.io/)
[![Prometheus](https://img.shields.io/badge/Prometheus-Monitored-E6522C?style=flat&logo=prometheus)](https://prometheus.io/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](#license)

A production-grade, horizontally-scalable **API Gateway** built from scratch in Go using clean architecture principles — featuring a distributed Redis-backed token-bucket rate limiter, an atomic Lua-scripted execution layer, a self-healing circuit breaker, a native reverse proxy engine, and full Prometheus observability. No third-party gateway frameworks. No black boxes. Every line of the request lifecycle — from signal handling to graceful shutdown — is engineered, tested, and containerized.

---

## 📐 Why This Project Exists

Most engineers integrate an API gateway. This project **is** one.

It was built to demonstrate systems-level engineering competency that goes beyond CRUD APIs: concurrency-safe state machines, atomic distributed algorithms, network resilience patterns, and infrastructure-as-code — the exact skill set expected of a backend/platform engineer working on traffic-critical systems.

---

## 🏗️ Architecture Overview

```
                                   ┌─────────────────────────────┐
                                   │        Client Request       │
                                   └──────────────┬───────────────┘
                                                  │
                                   ┌──────────────▼───────────────┐
                                   │   Metrics Middleware         │  ← Prometheus instrumentation
                                   │   (latency + status capture) │
                                   └──────────────┬───────────────┘
                                                  │
                                   ┌──────────────▼───────────────┐
                                   │  Rate Limiter Middleware     │  ← Distributed token bucket
                                   │  (atomic Redis Lua script)   │     (Redis-backed, fail-open)
                                   └──────────────┬───────────────┘
                                                  │
                                   ┌──────────────▼───────────────┐
                                   │  Circuit Breaker Middleware  │  ← Closed → Open → Half-Open
                                   │  (thread-safe state machine) │     self-healing failure isolation
                                   └──────────────┬───────────────┘
                                                  │
                                   ┌──────────────▼───────────────┐
                                   │   Reverse Proxy Engine       │  ← httputil.ReverseProxy
                                   │   (header rewrite, errors)   │     custom Director + ErrorHandler
                                   └──────────────┬───────────────┘
                                                  │
                                   ┌──────────────▼───────────────┐
                                   │     Upstream Microservice    │
                                   └──────────────────────────────┘
```

Every request flows through a deterministic middleware chain. Each layer is independently testable, independently replaceable, and fails predictably — rate limiting fails *open* (never blocks traffic on a Redis outage), while the circuit breaker fails *closed* (protects downstream services from cascading failure).

---

## 🧱 Clean Architecture & Project Structure

The codebase enforces strict separation of concerns across distinct boundaries — configuration, infrastructure clients, domain logic, middleware orchestration, and the application entrypoint never leak into one another.

```
gateway/
├── cmd/
│   └── gateway/
│       └── main.go              # Application bootstrap & lifecycle orchestration
├── config/
│   └── config.go                # Viper-backed structured configuration loader
├── internal/
│   └── middleware/
│       └── gateway_middleware.go # Rate limit + circuit breaker HTTP middleware
├── pkg/
│   ├── circuitbreaker/
│   │   ├── breaker.go            # Thread-safe Closed/Open/Half-Open state machine
│   │   └── breaker_test.go
│   ├── limiter/
│   │   ├── ratelimiter.go        # Atomic Redis Lua token-bucket rate limiter
│   │   └── ratelimiter_test.go
│   ├── logger/
│   │   └── logger.go             # Global thread-safe Zap logger
│   ├── metrics/
│   │   └── prometheus.go         # Counter + Histogram instrumentation
│   ├── proxy/
│   │   └── proxy.go              # Reverse proxy: Director + ErrorHandler
│   └── redis/
│       └── redis.go              # Pooled Redis client wrapper + health checks
├── Dockerfile                    # Multi-stage build (golang:alpine → alpine)
├── docker-compose.yml            # gateway + redis + prometheus + mock-backend
├── prometheus.yml                # 5s scrape interval configuration
├── go.mod / go.sum
└── .env.example
```

| Layer | Responsibility | Depends On |
|---|---|---|
| `cmd/gateway` | Wiring, lifecycle, signal handling | everything |
| `config` | Environment-driven settings | nothing |
| `pkg/*` | Pure, reusable infrastructure & domain logic | `config` only |
| `internal/middleware` | Glues `pkg/*` into the HTTP request pipeline | `pkg/*`, `config` |

This is a textbook **Dependency Rule** implementation: business logic in `pkg/` has zero knowledge of HTTP, and `cmd/gateway` is the only file that knows how everything fits together.

---

## ⚙️ Core Engineering Highlights

### 🪣 Distributed Token-Bucket Rate Limiter (Atomic Redis Lua)
Most rate limiters built on Redis suffer from race conditions between the read, calculate, and write steps when multiple gateway instances run concurrently. This implementation eliminates that entirely by pushing the **full token-bucket calculation into a single atomic Lua script** executed server-side inside Redis:

- Calculates token replenishment based on elapsed wall-clock time since the last request
- Atomically checks availability and decrements in one uninterruptible operation
- Self-expires idle buckets via dynamically computed TTL — no memory leaks, no cron cleanup jobs
- Scales horizontally: spin up 50 gateway replicas, and the rate limit is still enforced *globally and correctly*

### 🔌 Self-Healing Circuit Breaker
A hand-rolled, mutex-guarded state machine implementing the classic **Closed → Open → Half-Open** pattern:

- Tracks rolling failure rate against a configurable minimum sample size (avoids false trips on small bursts)
- Trips to `Open` when the failure-rate threshold is breached, instantly shedding load from a failing dependency
- After a cooldown window, permits exactly one **canary request** in `Half-Open` state
- A successful canary fully resets the breaker; a failed canary immediately re-opens it

### 🌐 Native Reverse Proxy Engine
Built directly on `net/http/httputil.ReverseProxy` with a custom `Director` and `ErrorHandler`:

- Rewrites `X-Forwarded-For`, `X-Forwarded-Proto`, and `X-Forwarded-Host` correctly, appending to existing chains rather than overwriting them
- Strips hop-by-hop headers (`Connection`, `Upgrade`, `Te`, `Trailer`) to prevent protocol leakage
- Distinguishes timeout errors from connection-refused/reset errors, mapping each to the *correct* HTTP status (`504` vs `502`) instead of a generic failure

### 📊 Full Observability via Prometheus
- `CounterVec` tracking total requests by method, path, and status code
- `HistogramVec` with tuned latency buckets for accurate P95/P99 percentile resolution
- `/metrics` endpoint deliberately bypasses the rate-limit/circuit-breaker chain so scrapers always succeed
- Live-tested: confirmed scraping every 5 seconds with `gateway` target reporting `UP`

### 🛑 Graceful Shutdown
- Listens for `SIGINT`/`SIGTERM` via a dedicated signal channel
- Drains in-flight requests within a hard 10-second `context.WithTimeout` barrier
- Closes the Redis connection pool cleanly *after* the HTTP listener has fully shut down
- Verified end-to-end: clean shutdown sequence with zero dropped connections, zero force-kills

---

## 🧰 Tech Stack

| Concern | Technology |
|---|---|
| Language | Go 1.22 |
| Configuration | [Viper](https://github.com/spf13/viper) |
| Structured Logging | [Zap](https://github.com/uber-go/zap) |
| Caching / Coordination | [go-redis v9](https://github.com/redis/go-redis) + Redis 7 |
| Observability | [Prometheus client_golang](https://github.com/prometheus/client_golang) |
| Testing | Native `testing` package + [miniredis](https://github.com/alicebob/miniredis) (in-memory Redis for CI) |
| Containerization | Multi-stage Docker (golang:alpine → alpine) |
| Orchestration | Docker Compose |

---

## 🚀 Quick Start

### Prerequisites
- [Docker Desktop](https://www.docker.com/products/docker-desktop/) running locally
- Go 1.22+ (optional — only needed for running tests outside Docker)

### Run the entire stack

```bash
git clone <your-repo-url>
cd gateway
cp .env.example .env
docker compose up --build
```

This spins up four interconnected containers:

| Service | Port | Purpose |
|---|---|---|
| `gateway` | `8080` | The API gateway itself |
| `redis` | `6379` | Rate limiter state store |
| `prometheus` | `9090` | Metrics scraping & querying |
| `mock-backend` | `9000` | Stand-in downstream service for local testing |

### Verify it's alive

```bash
curl http://localhost:8080/healthz   # → ok
curl http://localhost:8080/readyz    # → ready (validates live Redis connectivity)
curl -i http://localhost:8080/       # → proxied response from mock-backend
```

### Watch the rate limiter trip in real time

```bash
for i in $(seq 1 150); do curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/; done
```

### Inspect live metrics

```bash
curl http://localhost:8080/metrics | grep gateway_http_requests_total
```

Or visually, via the Prometheus UI: **http://localhost:9090/targets**

---

## 🧪 Testing

Every concurrency-sensitive component is covered by deterministic unit tests — no flaky sleeps disguised as assertions, no untested edge cases on the failure paths.

```bash
go test ./... -v
```

**Circuit Breaker (`pkg/circuitbreaker`)**
- ✅ Trips from `Closed → Open` on sustained failure rate breach
- ✅ Does *not* trip below the minimum sample threshold (avoids false positives)
- ✅ Transitions `Open → Half-Open` only after the cooldown window elapses
- ✅ A successful half-open canary fully closes the circuit
- ✅ A failed half-open canary immediately re-opens it

**Rate Limiter (`pkg/limiter`)**
- ✅ Allows requests within bucket capacity
- ✅ Denies requests once the bucket is exhausted
- ✅ Correctly refills tokens proportional to elapsed time
- ✅ Rejects invalid configuration (zero/negative capacity, zero/negative refill rate, empty keys)

Tests run against an **in-memory Redis instance (miniredis)** — meaning the full Lua scripting path is exercised in CI without requiring a live Redis server, keeping the suite fast and hermetic.

```
PASS
ok      github.com/platform/gateway/pkg/circuitbreaker   1.633s
PASS
ok      github.com/platform/gateway/pkg/limiter          2.130s
```

---

## 📦 Configuration

All runtime behavior is environment-driven via Viper, with sane production defaults baked in (see `.env.example`):

| Variable | Default | Description |
|---|---|---|
| `SERVER_PORT` | `8080` | HTTP listen port |
| `REDIS_URL` | `redis://localhost:6379/0` | Redis connection string |
| `POSTGRES_URL` | — | Reserved for future persistence layer |
| `RATE_LIMITER_CAPACITY` | `100` | Max tokens per bucket (burst size) |
| `RATE_LIMITER_REFILL_RATE` | `10.0` | Tokens replenished per second |
| `CIRCUIT_BREAKER_FAILURE_THRESHOLD_RATE` | `0.5` | Failure ratio that trips the breaker |
| `CIRCUIT_BREAKER_COOLDOWN_TIMEOUT` | `30s` | Time before attempting a half-open canary |
| `GATEWAY_UPSTREAM_URL` | `http://localhost:9000` | Downstream target for the reverse proxy |

---

## 🗺️ Roadmap

This gateway is intentionally built to extend cleanly:

- [ ] Typed multi-route configuration (path-prefix → upstream mapping) for fronting multiple services
- [ ] JWT / API-key authentication middleware ahead of the rate limiter
- [ ] Grafana dashboards layered on top of the existing Prometheus metrics
- [ ] Postgres-backed persistence for tenant configs, API keys, and audit logging (URL already wired into config)
- [ ] Distributed tracing (OpenTelemetry) across the proxy hop

---

## 👤 About This Build

This project was architected and implemented end-to-end — configuration management, structured logging, Redis-backed atomic algorithms, concurrent state machines, native reverse proxying, Prometheus instrumentation, container orchestration, and automated testing — as a demonstration of production-grade Go systems engineering.

Every component listed above was independently verified through live local testing: the rate limiter was load-tested to confirm `200 → 429` transitions under both natural traffic and parallel load, the circuit breaker's full state cycle was validated through automated unit tests, Prometheus scraping was confirmed live via the Targets dashboard, and graceful shutdown was verified to cleanly drain connections and close the Redis pool on `SIGINT`/`SIGTERM`.

---

## 📄 License

MIT — free to use, fork, and build upon.