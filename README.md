<div align="center">

# VA API Gateway

### The open-source, production-ready public API gateway written in Go

[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?style=for-the-badge&logo=go&logoColor=white)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-green?style=for-the-badge)](LICENSE)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=for-the-badge&logo=docker&logoColor=white)](Dockerfile)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-Ready-326CE5?style=for-the-badge&logo=kubernetes&logoColor=white)](k8s/)
[![Prometheus](https://img.shields.io/badge/Prometheus-Metrics-E6522C?style=for-the-badge&logo=prometheus&logoColor=white)](https://prometheus.io)
[![Redis](https://img.shields.io/badge/Redis-Cache-DC382D?style=for-the-badge&logo=redis&logoColor=white)](https://redis.io)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen?style=for-the-badge)](https://github.com/valtunox/va_api_gateway_golang/pulls)

<br />

**A high-performance, self-hostable public API gateway** with DDoS protection, load balancing, JWT/API-key auth, circuit breaking, Redis response caching, Prometheus metrics, and Jaeger tracing — all in a single Go binary.

<br />

[Quick Start](#-quick-start) · [Features](#-features) · [Architecture](#-architecture) · [API Reference](#-api-endpoints) · [Configuration](#-configuration) · [Contributing](#-contributing)

<br />

---

</div>

<br />

## Why VA API Gateway?

Most public gateways are either managed black boxes (Cloudflare, AWS API Gateway) or heavy enterprise licenses (NGINX Plus, Kong Enterprise). This one gives you **a public-facing, production-grade gateway you can run anywhere** — on a single VM, in Docker Compose, or on a Kubernetes cluster — with zero licensing.

- **Public-first design** — Built to sit at the edge of the internet, with DDoS rate-limiting, IP blacklisting, connection throttling, and a strict security-headers middleware applied to every response.
- **Dynamic routing** — Configure upstream services via environment variables or a single JSON string, and hot-swap backends without rebuilding the binary.
- **Resilient by default** — Per-backend health checks, circuit breaker, retry with exponential backoff, graceful shutdown, and automatic failover across a pool of backends.
- **Observability without glue code** — Prometheus metrics at `/metrics`, Jaeger traces, structured JSON logs, a built-in stats endpoint, and a live TTY dashboard when running interactively.
- **Single binary, zero runtime deps** — A statically compiled Go binary that boots in milliseconds. Redis is optional (caching falls back gracefully if it's unavailable).

<br />

## What's Inside

```
Edge Protection         Routing & Load-Balancing   Resilience              Observability
 Per-IP rate limiting    Longest-prefix matching    Circuit breaker         Prometheus metrics
 DDoS threshold block    Round-robin                Retry + backoff         Jaeger tracing
 IP whitelist/blacklist  Least-connections          Graceful shutdown       Structured logging
 Security headers        IP-hash (sticky)           Timeout per-route       Live TTY dashboard
 CORS                    Weighted                   Backend health checks   System monitor page

Authentication          Caching & Compression      Admin & Ops             Deployment
 JWT (HMAC + exp)        Redis response cache       /admin/whitelist        Multi-stage Dockerfile
 API-key (SHA-256)       Gzip (Accept-Encoding)     /admin/blacklist        docker-compose.yml
 Public-endpoint bypass  Per-route cache TTL        /stats · /monitor       Kubernetes manifests
 Per-route require_auth  Configurable compression   Graceful SIGTERM        nginx reverse proxy
```

<br />

## Tech Stack

| Layer | Technology |
|:------|:-----------|
| **Language** | Go 1.23+ (statically compiled single binary) |
| **Router** | `net/http` with a custom prefix-matching `ServiceRouter` |
| **Rate limiting** | `golang.org/x/time/rate` token bucket, per client IP |
| **Auth** | `github.com/golang-jwt/jwt/v5` (HS256) + SHA-256 API keys |
| **Cache** | Redis 7+ via `github.com/redis/go-redis/v9` (optional, graceful fallback) |
| **Metrics** | Prometheus via `github.com/prometheus/client_golang` |
| **Tracing** | OpenTracing + Jaeger (`github.com/uber/jaeger-client-go`) |
| **Config** | Environment variables (`github.com/joho/godotenv`) or JSON `SERVICE_ROUTES` |
| **Reverse proxy (edge)** | nginx (optional, see [NGINX_SETUP.md](NGINX_SETUP.md)) |
| **Container** | Multi-stage Dockerfile with distroless runtime layer |
| **Orchestration** | Docker Compose + Kubernetes manifests with HPA |

<br />

## Quick Start

### Prerequisites

| Requirement | Version | Notes |
|:------------|:--------|:------|
| Go | 1.23+ | Only for local builds |
| Docker | 24+ | Recommended for the full stack |
| Redis | 7+ | Optional — caching disabled if unreachable |
| Prometheus / Grafana / Jaeger | any | Optional observability stack |

### Option 1: Docker Compose (recommended)

```bash
git clone https://github.com/valtunox/va_api_gateway_golang.git
cd va_api_gateway_golang

cp .env.development .env         # tweak as needed
docker-compose up -d
```

The full stack (gateway, nginx, Redis, Prometheus, Grafana, Jaeger) is now live:

| Service | URL |
|:--------|:----|
| nginx (edge) | http://localhost |
| Gateway | http://localhost:9777 |
| Health | http://localhost:9777/health |
| Metrics | http://localhost:9777/metrics |
| Stats | http://localhost:9777/stats |
| System monitor | http://localhost:9777/monitor |
| Prometheus | http://localhost:9090 |
| Grafana | http://localhost:3000 (admin/admin) |
| Jaeger | http://localhost:16686 |

### Option 2: Local Go build

```bash
git clone https://github.com/valtunox/va_api_gateway_golang.git
cd va_api_gateway_golang

go mod download
make build          # produces ./gateway
./gateway           # starts on :9777
```

### Option 3: Kubernetes

```bash
kubectl apply -f k8s/redis.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/ingress.yaml

kubectl get pods -n va-gateway
kubectl logs -f deployment/api-gateway -n va-gateway
```

Kubernetes manifests include a Horizontal Pod Autoscaler (3–10 replicas), a Pod Disruption Budget (min 2 available), liveness/readiness probes, and an NGINX ingress with SSL.

### Verify

```bash
# Unauthenticated health check
curl -s http://localhost:9777/health | jq

# Authenticated request (demo API key — change in production!)
curl -H "X-API-Key: demo-api-key-123" http://localhost:9777/api/studio/

# Quick load test
wrk -t10 -c100 -d30s http://localhost:9777/health
```

<br />

## Features

### Edge protection

- **Per-IP token-bucket rate limiting** — `MAX_REQUESTS_PER_SECOND` + `MAX_BURST_SIZE`, with an eviction goroutine that cleans idle entries every 10 minutes.
- **DDoS counter-based blocking** — Any IP that exceeds `DDOS_THRESHOLD` requests per minute is blocked for `BLOCK_DURATION`. Counters and blocks are garbage-collected on a 1-minute tick.
- **IP whitelist / blacklist** — Runtime-mutable via `POST /admin/whitelist?ip=X` and `POST /admin/blacklist?ip=X`. Whitelist wins over every other check.
- **Security headers** — `X-Content-Type-Options`, `X-Frame-Options: DENY`, `Strict-Transport-Security`, `Content-Security-Policy`, `X-XSS-Protection`, `Referrer-Policy` — applied to every response.
- **CORS** — Configurable allowed origins via `CORS_ALLOWED_ORIGINS` (JSON array, defaults to `["*"]`).

### Dynamic routing — any domain, any upstream

The gateway matches on **Host and path**, so a single instance fronts any number
of unrelated domains, each with its own pool of upstreams on any ports.

- **Host matching** — `""` matches any domain, `api.example.com` is exact,
  `*.example.com` covers any subdomain but deliberately not the apex (matching
  DNS wildcard and wildcard-certificate semantics). Case, an explicit port
  (`example.com:8443`), a trailing FQDN dot and IPv6 brackets are all normalised
  away before comparison.
- **Most-specific-first matching** — the table is ordered exact host → wildcard
  host → catch-all, and within a host class by descending prefix length. A
  catch-all `/` route therefore cannot shadow a domain-specific route no matter
  what order they were registered in.
- **Segment-boundary prefixes** — `/api/v1` matches `/api/v1/users` but *not*
  `/api/v11`. A plain prefix test would quietly hand one service's traffic to
  another.
- **Many upstreams per route** — `targets` takes a list, so the load-balancing
  algorithms have something to choose between. `target_url` still works and is
  merged with `targets`, de-duplicated.
- **Forgiving upstream syntax** — `10.0.0.5:8080`, `example.com` and
  `https://api.example.com/v1` are all accepted. (A bare `host:port` is *not* a
  URL as far as `url.Parse` is concerned — it parses as scheme `10.0.0.5` — so it
  is detected and defaulted to `http`.)
- **HTTP health checks** — set `health_path` and liveness becomes "does the
  application answer" rather than "is a socket open". A listening socket in front
  of a wedged process is exactly what a TCP dial cannot detect.
- **Host forwarding** — the upstream always receives `X-Forwarded-Host` and
  `X-Forwarded-Proto` so it can reconstruct the public URL; `preserve_host`
  additionally forwards the original `Host` verbatim, for upstreams that serve
  virtual hosts themselves.
- **No implicit routes** — declaring nothing yields an empty table and a warning.
  The nine VxCloud localhost routes are opt-in via
  `ENABLE_VXCLOUD_DEFAULT_ROUTES`.
- **Per-route timeouts and auth** — each route declares its own `timeout_secs`
  and `require_auth`.

```bash
# Two unrelated domains, three upstreams, arbitrary ports.
export SERVICE_ROUTES='[
  {"name":"api","host":"api.acme.example","prefix":"/",
   "targets":["10.0.0.11:8080","10.0.0.12:8080"],
   "health_path":"/healthz","strip_prefix":false},
  {"name":"shop","host":"*.shop.example","prefix":"/v1",
   "target_url":"https://origin.internal","preserve_host":true}
]'

# Or per-route env vars, under any name you like.
export ROUTE_BILLING_HOST=billing.acme.example
export ROUTE_BILLING_TARGETS=10.0.0.7:8080,https://billing-2.internal
export ROUTE_BILLING_WEIGHTS=3,1
export ROUTE_BILLING_HEALTH_PATH=/healthz
```

### Load balancing

| Algorithm | Use case | Sticky sessions |
|:----------|:---------|:---------------:|
| `round-robin` | Equal backends | No |
| `least-conn` (default) | Varying request durations | No |
| `ip-hash` | Per-user session affinity | Yes |
| `weighted` | Priority or capacity-tiered backends | No |

All algorithms skip dead backends and honor the per-backend `Weight`.

> These were unreachable before multi-upstream routes existed: every pool was
> built with exactly one backend, so there was never anything to balance.

### Retry, failover and status accounting

A failed round trip is reported by `httputil.ReverseProxy` through its
`ErrorHandler`, not as a return value. The dispatcher now observes both the
error and the real response, which makes three things work that previously did
not:

- **Retry and failover** — a connect-time failure moves to another backend in the
  pool. Retrying stops as soon as any byte has reached the client, because a
  response cannot be restarted once it is committed.
- **Honest metrics and logs** — `http_requests_total` records the status the
  upstream actually returned. Every proxied request used to be counted as `200`,
  including ones answered with `502`.
- **A circuit breaker that trips** — real backend failures now reach
  `RecordFailure` and the breaker.

Verified end to end: an upstream replying 404 or 500 is recorded as 404 or 500, a
route whose only upstream is dead returns 503 after retries, and killing one of
two upstreams mid-run leaves the remaining traffic at 8/8 × 200.

### Resilience

- **Circuit breaker** — Classic closed → open → half-open state machine. Opens after `CIRCUIT_BREAKER_MAX` consecutive failures, retries after `CIRCUIT_BREAKER_TIMEOUT`.
- **Retry with exponential backoff** — Up to 3 attempts per request, with `100ms * 2^attempt` backoff between retries, each rolling to the next healthy backend.
- **Active health checks** — Every `HEALTH_CHECK_INTERVAL`, the gateway dials every backend's TCP host and flips liveness. Prometheus `backend_health_status` is updated each tick.
- **Graceful shutdown** — SIGINT / SIGTERM triggers a 30-second context-bounded shutdown that drains in-flight requests.

### Authentication

- **JWT (HS256)** — `Authorization: Bearer <token>` with signature + expiry validation. User ID from the `sub` claim is attached to the trace span.
- **API keys** — `X-API-Key: <key>` checked against a SHA-256 hash of the allow-list. Replace the demo keys in [middleware.go](middleware.go) with a database-backed lookup before going public.
- **Public endpoints** — `/health`, `/metrics`, `/login`, `/register`, `/api/v1/public` bypass auth. Every other path honors the route's `require_auth` flag.

### Caching & compression

- **Redis response cache** — GET requests are looked up by `cache:<method>:<host>:<path>`. Misses populate the cache with the configured `CACHE_TTL`. If Redis is unreachable the gateway logs a warning and serves uncached.
- **Gzip** — Responses are gzip-encoded when the client sends `Accept-Encoding: gzip`, controlled by `ENABLE_COMPRESSION`.
- **HTTP keep-alive** — `MaxIdleConns` (default 100) idle connections per host, with `IdleConnTimeout` (default 90s).

### Observability

**Prometheus metrics (scrape at `/metrics`):**

| Metric | Type | Labels |
|:-------|:-----|:-------|
| `http_requests_total` | Counter | method, endpoint, status |
| `http_request_duration_seconds` | Histogram | method, endpoint |
| `active_connections` | Gauge | — |
| `backend_health_status` | Gauge | backend |
| `circuit_breaker_trips_total` | Counter | — |
| `blocked_ips_total` | Counter | — |
| `cache_hits_total` | Counter | — |

**Tracing:** Every request starts a `gateway.request` span with `method`, `path`, `user.id`, `backend`, and error tags.

**Logging:** Structured JSON logs per subsystem (`gateway`, `router`, `proxy`, `cache`, `circuit`, `ddos`, `admin`, `tracer`, `config`) with a consistent `request_id` propagated via the `x-request-id` header.

**Live TTY dashboard:** When the gateway runs attached to a terminal, a real-time dashboard shows total requests, active connections, blocked IPs, circuit state, and backend health.

<br />

## Public-Gateway Comparison

| Capability | Cloudflare | NGINX Plus | AWS API Gateway | Kong OSS | **VA API Gateway** |
|:-----------|:----------:|:----------:|:---------------:|:--------:|:------------------:|
| DDoS protection | Yes | Yes | Yes | Partial | **Yes** |
| 4 load-balancing algorithms | Yes | Yes | No | Partial | **Yes** |
| JWT validation | Yes | Yes | Yes | Yes | **Yes** |
| API-key auth | Yes | Yes | Yes | Yes | **Yes** |
| Circuit breaker | Yes | Yes | No | Plugin | **Yes** |
| Redis response cache | Yes | Yes | No | Plugin | **Yes** |
| Prometheus native | No | No | No | Plugin | **Yes** |
| Jaeger tracing | No | No | Partial | Plugin | **Yes** |
| Live TTY dashboard | No | No | No | No | **Yes** |
| Single self-hosted binary | No | Partial | No | No | **Yes** |
| Open source | No | No | No | Yes | **Yes (MIT)** |

<br />

## Architecture

```
                         ┌─────────────────────────┐
                         │       Public Clients     │
                         │   (browsers, SDKs, IoT)  │
                         └───────────┬─────────────┘
                                     │ HTTPS
                         ┌───────────▼─────────────────────┐
                         │  nginx (optional edge proxy)    │
                         │  SSL termination · static files │
                         │  Coarse rate-limit · gzip       │
                         └───────────┬─────────────────────┘
                                     │ HTTP :9777
                   ┌─────────────────▼─────────────────────┐
                   │          VA API Gateway (Go)          │
                   │                                       │
                   │  DDoS guard ─► Rate limiter ─► Auth   │
                   │         │                             │
                   │         ▼                             │
                   │  Router (longest-prefix match)        │
                   │         │                             │
                   │         ▼                             │
                   │  Pool (4 algos) ─► Circuit breaker    │
                   │         │                             │
                   │         ▼                             │
                   │  Cache (Redis) ─► Reverse proxy       │
                   └─────────┬─────────────────────────────┘
                             │
           ┌──────────┬──────┼──────┬──────────┬─────────┐
           ▼          ▼      ▼      ▼          ▼         ▼
     ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐ ┌──────────┐
     │ Studio │ │   AI   │ │  Core  │ │  LLM   │ │  Agent   │
     │  :3000 │ │ :8741  │ │ :8743  │ │ :8745  │ │  :8788   │
     └────────┘ └────────┘ └────────┘ └────────┘ └──────────┘

                   ┌───────────────────────────────┐
                   │  Redis · Prometheus · Jaeger  │
                   └───────────────────────────────┘
```

**Request lifecycle (see [main.go](main.go) `Gateway.ServeHTTP`):**

1. Extract client IP (`X-Real-IP` → `X-Forwarded-For` → `RemoteAddr`)
2. DDoS check → per-IP rate limit → route match
3. Auth check (JWT *or* API key) unless the path is public
4. Redis cache lookup for `GET` requests
5. Pick a healthy backend from the matched pool using the configured algorithm
6. Proxy through the circuit breaker with retry + exponential backoff
7. Emit metrics, finish trace span, log the completed request

<br />

## API Endpoints

### Gateway endpoints

| Method | Path | Auth | Description |
|:-------|:-----|:-----|:------------|
| `GET` | `/health` | Public | Liveness + backend status + registered routes |
| `GET` | `/metrics` | Public | Prometheus scrape endpoint |
| `GET` | `/stats` | Public | Runtime stats (circuit state, algo, flags, route count) |
| `GET` | `/monitor` | Public | HTML system monitor (CPU/RAM/disk) |
| `GET` | `/api/system-metrics` | Public | JSON system metrics for the monitor |
| `POST` | `/login` | Public | Placeholder — wire your OIDC/OAuth2 flow here |
| `POST` | `/admin/whitelist?ip=X` | Admin | Add IP to DDoS whitelist |
| `POST` | `/admin/blacklist?ip=X` | Admin | Add IP to DDoS blacklist |

### Proxied routes (defaults — override via env vars)

| Prefix | Target | Weight | Strip prefix |
|:-------|:-------|:------:|:------------:|
| `/api/studio` | `http://localhost:3000` | 1 | Yes |
| `/api/studio2` | `http://localhost:3001` | 1 | Yes |
| `/api/ai` | `http://localhost:8741` | 1 | Yes |
| `/api/admin` | `http://localhost:8242` | 1 | Yes |
| `/api/core` | `http://localhost:8743` | 1 | Yes |
| `/api/node` | `http://localhost:8744` | 1 | Yes |
| `/api/llm` | `http://localhost:8745` | 1 | Yes |
| `/api/llm2` | `http://localhost:8746` | 1 | Yes |
| `/api/agent` | `http://localhost:8788` | 1 | Yes |

### Authentication

**JWT:**
```bash
curl -H "Authorization: Bearer eyJhbGc..." http://localhost:9777/api/agent/
```

**API key:**
```bash
curl -H "X-API-Key: demo-api-key-123" http://localhost:9777/api/core/
```

<br />

## Configuration

Every config value is read from environment variables at boot. See [config.go](config.go) for the authoritative list and defaults.

### Core

| Variable | Default | Description |
|:---------|:--------|:------------|
| `GATEWAY_PORT` | `:9777` | Bind address for the gateway |
| `JWT_SECRET` | `your-secret-key-change-in-production` | **Must change before deployment** |
| `LOAD_BALANCING_ALGO` | `least-conn` | One of `round-robin`, `least-conn`, `ip-hash`, `weighted` |
| `REDIS_ADDR` | `localhost:6379` | Redis address; caching degrades gracefully if unreachable |
| `CORS_ALLOWED_ORIGINS` | `["*"]` | JSON array of allowed origins |

### Edge protection

| Variable | Default | Description |
|:---------|:--------|:------------|
| `MAX_REQUESTS_PER_SECOND` | `100` | Token-bucket refill rate, per IP |
| `MAX_BURST_SIZE` | `200` | Token-bucket burst size, per IP |
| `DDOS_THRESHOLD` | `1000` | Requests per minute before an IP is blocked |
| `BLOCK_DURATION` | `10m` | How long an IP stays blocked |

### Backend connections

| Variable | Default | Description |
|:---------|:--------|:------------|
| `HEALTH_CHECK_INTERVAL` | `10s` | Per-backend TCP health check cadence |
| `CONNECTION_TIMEOUT` | `30s` | Default per-route timeout (overridable per route) |
| `MAX_IDLE_CONNS` | `100` | HTTP keep-alive pool size |
| `IDLE_CONN_TIMEOUT` | `90s` | Idle connection reaper |
| `CIRCUIT_BREAKER_MAX` | `5` | Consecutive failures before the breaker opens |
| `CIRCUIT_BREAKER_TIMEOUT` | `30s` | How long the breaker stays open before half-open |

### Cache & compression

| Variable | Default | Description |
|:---------|:--------|:------------|
| `ENABLE_COMPRESSION` | `true` | Gzip when the client accepts it |
| `ENABLE_CACHING` | `true` | Redis response cache for `GET` requests |
| `CACHE_TTL` | `5m` | TTL for cached responses |

### Routes (three ways, additive)

`SERVICE_ROUTES` and the `ROUTE_*` variables are combined, so a JSON baseline can
be extended per deployment without re-encoding the whole array.

**Option A — a JSON array.** The full surface.

```bash
export SERVICE_ROUTES='[
  {"name":"api","host":"api.acme.example","prefix":"/",
   "targets":["10.0.0.11:8080","10.0.0.12:8080"],"weights":[3,1],
   "health_path":"/healthz","strip_prefix":false,"timeout_secs":30},
  {"name":"shop","host":"*.shop.example","prefix":"/v1",
   "target_url":"https://origin.internal","preserve_host":true,"require_auth":true}
]'
```

| Field | Meaning |
|:------|:--------|
| `host` | `""` any · `api.example.com` exact · `*.example.com` subdomains (not the apex) |
| `prefix` | path prefix, matched on segment boundaries; `/` matches everything on the host |
| `target_url` | one upstream |
| `targets` | several upstreams, balanced by `LOAD_BALANCING_ALGO`; merged with `target_url` and de-duplicated |
| `weights` | positional against `targets`, for the `weighted` algorithm |
| `health_path` | probe with an HTTP GET (anything below 500 is healthy) instead of a TCP dial |
| `preserve_host` | forward the client's `Host` rather than rewriting it to the upstream's |
| `strip_prefix` | trim the matched prefix before forwarding |
| `require_auth` | demand a valid JWT or API key |
| `timeout_secs` | per-route upstream timeout |

Upstreams may be written as `host:port`, `host`, or a full URL.

**Option B — `ROUTE_<NAME>_*` variables.** `<NAME>` is anything you choose; it is
not checked against a built-in list, so you can declare as many routes to as many
domains as you like without touching the code. A route needs at least a `_URL` or
`_TARGETS`, or it is skipped with a warning.

```bash
export ROUTE_BILLING_HOST=billing.acme.example
export ROUTE_BILLING_PREFIX=/v2
export ROUTE_BILLING_TARGETS=10.0.0.7:8080,https://billing-2.internal
export ROUTE_BILLING_WEIGHTS=3,1
export ROUTE_BILLING_HEALTH_PATH=/healthz
export ROUTE_BILLING_PRESERVE_HOST=true
```

Recognised suffixes: `URL`, `TARGETS`, `PREFIX`, `HOST`, `STRIP_PREFIX`,
`REQUIRE_AUTH`, `WEIGHT`, `WEIGHTS`, `TIMEOUT_SECS`, `HEALTH_PATH`,
`PRESERVE_HOST`.

**Option C — the VxCloud localhost topology.** The nine fixed ports in the table
above, behind an opt-in flag:

```bash
export ENABLE_VXCLOUD_DEFAULT_ROUTES=true
```

> **Behaviour change.** These nine routes used to be registered unconditionally
> on every boot. The guard meant to suppress them read
> `if anySet || len(routes) > 0`, and the loop above it always appended nine
> entries — so the condition was always true and there was no way to run the
> gateway with a different topology, with fewer routes, or with none. Declaring
> no routes now yields an empty table and a warning. Set the flag above to
> restore the old behaviour.

### Secrets

`.env`, `.env.development` and `.env.production` are gitignored; only
[`.env.example`](.env.example) is tracked. Copy it and generate real values:

```bash
cp .env.example .env
openssl rand -base64 48   # JWT_SECRET
```

<br />

## Project Structure

```
va_api_gateway_golang/
│
├── main.go                 # Gateway struct, ServeHTTP, main(), startup banner
├── main_test.go            # Unit tests for gateway components
├── config.go               # Env-driven Config + ServiceRoute loader
├── routing.go              # Backend · ServicePool · 4 load-balancing algos · ServiceRouter
├── middleware.go           # IPRateLimiter · DDoSProtection · CircuitBreaker · JWT/API-key · security headers · request ID
├── handlers.go             # Health · stats · admin whitelist/blacklist · cache · compression · Jaeger init
├── metrics.go              # Prometheus counters, histograms, gauges
├── logger.go               # Structured JSON logging per subsystem
├── dashboard.go            # Live TTY dashboard (totals, active conns, backend health)
├── system_monitor.go       # /monitor HTML + /api/system-metrics JSON
│
├── Makefile                # make build · run · docker-up · k8s-deploy · load-test
├── Dockerfile              # Multi-stage with optional nginx sidecar
├── docker-compose.yml      # Gateway + nginx + Redis + Prometheus + Grafana + Jaeger
├── .env.development        # Dev defaults
├── .env.production         # Prod defaults (do not commit secrets)
│
├── nginx/
│   ├── nginx.conf          # Edge reverse-proxy config
│   └── static/             # Landing page, 404, 50x
│
├── config/
│   └── prometheus.yml      # Scrape config for the gateway + backends
│
├── k8s/
│   ├── deployment.yaml     # Deployment · HPA (3-10) · Service · PDB
│   ├── redis.yaml          # Redis StatefulSet + PVC
│   └── ingress.yaml        # NGINX Ingress with SSL
│
├── scripts/
│   ├── deploy.sh           # Automated deployment
│   └── load-test.sh        # wrk / ab load test harness
│
├── NGINX_SETUP.md          # Edge-proxy configuration guide
├── CHANGELOG.md            # Release history (keep-a-changelog)
├── TODO.md                 # Public roadmap
├── CONTRIBUTORS.md         # Primary contributors
└── LICENSE                 # MIT
```

<br />

## Development

### Common commands

```bash
# Build & run
make build                  # compile ./gateway
make run                    # build + run

# Docker
make docker-build           # build image
make docker-up              # start the full stack
make docker-logs            # tail gateway logs
make docker-down            # stop everything

# Kubernetes
make k8s-deploy             # apply all manifests
make k8s-status             # kubectl get pods,svc
make k8s-logs               # follow deployment logs
make k8s-delete             # tear down

# Testing
make test                   # go test ./...
make load-test              # wrk-based load test (see scripts/)

# Health & metrics
make health                 # curl /health | jq
make metrics                # curl /metrics
```

### Adding a new upstream

1. Add the env var pair (or append to `SERVICE_ROUTES`):
   ```bash
   export ROUTE_BILLING_PREFIX=/api/billing
   export ROUTE_BILLING_URL=http://billing:7000
   ```
2. Restart the gateway — the router picks it up automatically and begins health-checking.
3. (Optional) Extend `loadServiceRoutes()` in [config.go](config.go) so the route is part of the built-in defaults.

### Running tests

```bash
go test -v -race -cover ./...
```

### Load testing

```bash
# Public endpoint, high concurrency
wrk -t10 -c500 -d60s http://localhost:9777/health

# Authenticated, through a proxied route
wrk -t10 -c100 -d30s -H "X-API-Key: demo-api-key-123" http://localhost:9777/api/core/

# DDoS simulation — watch blocked_ips_total tick up
wrk -t50 -c1000 -d15s http://localhost:9777/health
```

<br />

## Deployment

### Production checklist

- [ ] Generate a strong random `JWT_SECRET` (32+ bytes)
- [ ] Replace the demo API keys in [middleware.go](middleware.go) with a database-backed store
- [ ] Terminate TLS at nginx or your load balancer
- [ ] Set `CORS_ALLOWED_ORIGINS` explicitly (drop the `*` default)
- [ ] Tune `MAX_REQUESTS_PER_SECOND`, `MAX_BURST_SIZE`, and `DDOS_THRESHOLD` to your actual traffic profile
- [ ] Whitelist your CI/CD, monitoring, and health-checker IPs (`POST /admin/whitelist`)
- [ ] Run Redis with a password and TLS
- [ ] Point Prometheus at `/metrics` and Jaeger's agent at `localhost:6831`
- [ ] Configure log aggregation (JSON is structured and parse-friendly)
- [ ] Enable `liveness` + `readiness` probes in Kubernetes (already in `k8s/deployment.yaml`)

### Performance benchmarks

Tested on a 2-core, 4 GB VM with Redis on the same host:

| Metric | Value |
|:-------|:------|
| Throughput (public) | ~50,000 req/sec |
| Throughput (with JWT) | ~30,000 req/sec |
| Latency p50 | ~2 ms |
| Latency p95 | ~10 ms |
| Latency p99 | ~25 ms |
| Memory | ~100 MB base + ~1 MB per 1,000 connections |
| CPU | ~30 % at 10k req/sec/core |

<br />

## Troubleshooting

### Gateway won't start
```bash
lsof -i :9777
docker-compose logs gateway
```

### Backends always unhealthy
```bash
curl http://localhost:9777/health | jq '.routes[].backends'
# Check the backend directly
curl http://localhost:3000/health
```

### Rate limits feel too aggressive
- Raise `MAX_REQUESTS_PER_SECOND` and `MAX_BURST_SIZE`
- Whitelist any trusted IPs (`POST /admin/whitelist?ip=X`)

### `503 Service temporarily unavailable`
- All backends in the matched pool are down or the circuit breaker is open
- Check `/stats` for the circuit breaker state and `/health` for backend liveness

### Redis connection failed on boot
- Caching auto-disables; the gateway keeps serving
- Check `REDIS_ADDR` and network policy

<br />

## Contributing

We welcome contributions! This is a **public, MIT-licensed** gateway — bug reports, perf improvements, docs, and new features are all appreciated.

1. **Fork** the repository
2. **Create** your feature branch: `git checkout -b feature/amazing-feature`
3. **Commit** your changes: `git commit -m 'feat: add amazing feature'`
4. **Push** to your branch: `git push origin feature/amazing-feature`
5. **Open** a Pull Request

See [TODO.md](TODO.md) for the public roadmap and [CONTRIBUTORS.md](CONTRIBUTORS.md) for the core team.

### Good first issues

- Replace in-memory API key list with a pluggable backend (Postgres / Redis / file)
- Add WebSocket proxying support (currently HTTP/1.1 only)
- mTLS support between gateway and upstreams
- OAuth2 / OIDC login flow on `/login`
- Per-route body-size limits and request-body validation hooks
- Grafana dashboard JSON for the Prometheus metrics

### Development guidelines

- Keep the gateway a **single binary with no runtime dependencies** (Redis stays optional)
- Every feature gets a Prometheus metric or a log line — ideally both
- New env vars must have sensible defaults in `LoadConfig()`
- Run `go vet ./...` and `go test -race ./...` before opening a PR

<br />

## Security

- Report vulnerabilities privately to the maintainers listed in [CONTRIBUTORS.md](CONTRIBUTORS.md) — do **not** open a public GitHub issue for security bugs.
- Always rotate `JWT_SECRET` and API keys when staff changes hands.
- Consider running the gateway behind a WAF (AWS WAF, Cloudflare) for Layer 7 attacks that pre-date connection-level rate limiting.

<br />

## License

MIT License — see [LICENSE](LICENSE) for details.

Free to use in personal and commercial projects.

<br />

---

<div align="center">

**Built with [Go](https://go.dev), [Prometheus](https://prometheus.io), [Redis](https://redis.io), and [Jaeger](https://www.jaegertracing.io)**

Maintained by **[prodxcloud](https://github.com/prodxcloud)** and **[joelwembo](https://github.com/joelwembo)** — see [CONTRIBUTORS.md](CONTRIBUTORS.md)

[Report Bug](https://github.com/valtunox/va_api_gateway_golang/issues) · [Request Feature](https://github.com/valtunox/va_api_gateway_golang/issues) · [Roadmap](TODO.md) · [Changelog](CHANGELOG.md)

</div>
