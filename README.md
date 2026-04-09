<div align="center">

# VA API Gateway

**A high-performance API gateway built in Go with DDoS protection, load balancing, caching, and observability.**

[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://github.com/valtunox/va_api_gateway_golang/pulls)

[Quick Start](#quick-start) · [Features](#features) · [Configuration](#configuration) · [Deployment](#deployment) · [Contributing](#contributing)

</div>

---

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Quick Start](#quick-start)
- [Features](#features)
  - [NGINX Reverse Proxy](#nginx-reverse-proxy)
  - [DDoS Protection](#ddos-protection)
  - [Load Balancing](#load-balancing)
  - [Security](#security)
  - [Caching & Performance](#caching--performance)
  - [Observability](#observability)
  - [Reliability](#reliability)
  - [Live Dashboard](#live-dashboard)
- [Configuration](#configuration)
  - [Environment Variables](#environment-variables)
  - [Service Routes](#service-routes)
- [API Reference](#api-reference)
- [Deployment](#deployment)
  - [Docker Compose](#docker-compose)
  - [Kubernetes](#kubernetes)
  - [Standalone Binary](#standalone-binary)
- [Development](#development)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)
- [Contributing](#contributing)
- [License](#license)

## Overview

VA API Gateway is a self-hosted, open-source API gateway written in Go. It sits between your clients and backend services, providing:

- **Protection** — Per-IP rate limiting, DDoS detection with automatic blocking, IP whitelists/blacklists
- **Routing** — Prefix-based routing with longest-match, path stripping, and per-route auth/timeout settings
- **Load Balancing** — Four algorithms (round-robin, least-connections, IP-hash, weighted)
- **Caching** — Redis-backed response caching with configurable TTL
- **Observability** — Prometheus metrics, Jaeger distributed tracing, structured logging, live terminal dashboard
- **Reliability** — Circuit breaker pattern, retry with exponential backoff, active health checks

It is designed to be used with an NGINX reverse proxy layer for SSL termination, static file serving, and an additional rate-limiting tier.

## Architecture

```
                    ┌──────────────────┐
                    │     Clients      │
                    └────────┬─────────┘
                             │
                    ┌────────▼───────────────────────┐
                    │  NGINX Reverse Proxy (Port 80)  │
                    │  • SSL/TLS termination          │
                    │  • Rate limiting (100 r/s)      │
                    │  • DDoS protection (1000 r/s)   │
                    │  • Static file serving           │
                    │  • Gzip compression              │
                    │  • Security headers              │
                    └────────┬───────────────────────┘
                             │
                    ┌────────▼───────────────────────┐
                    │  Go API Gateway (Port 9777)     │
                    │  • JWT / API key auth           │
                    │  • Per-IP rate limiting          │
                    │  • Redis response caching        │
                    │  • Circuit breaker               │
                    │  • Retry with backoff            │
                    │  • Prefix-based routing          │
                    │  • 4 load-balancing algorithms   │
                    └────────┬───────────────────────┘
                             │
          ┌──────────────────┼──────────────────┐
          │                  │                  │
   ┌──────▼──────┐   ┌──────▼──────┐   ┌──────▼──────┐
   │  Service A  │   │  Service B  │   │  Service C  │
   │  /api/llm   │   │  /api/admin │   │  /api/studio│
   └─────────────┘   └─────────────┘   └─────────────┘
          │                  │                  │
   ┌──────▼──────────────────▼──────────────────▼─────┐
   │  Shared Infrastructure                           │
   │  • Redis (cache & rate limiting)                 │
   │  • Prometheus (metrics)                          │
   │  • Jaeger (distributed tracing)                  │
   │  • Grafana (dashboards)                          │
   └──────────────────────────────────────────────────┘
```

> Ports, service names, and route prefixes are fully configurable via environment variables. The diagram above shows the default Docker Compose setup.

## Quick Start

### Prerequisites

- **Go 1.23+** — [Install Go](https://go.dev/dl/)
- **Docker & Docker Compose** (optional) — for containerised deployment
- **Redis** (optional) — required only if caching is enabled

### Run Locally

```bash
git clone https://github.com/valtunox/va_api_gateway_golang.git
cd va_api_gateway_golang

# Build and run
make build
./gateway
```

The gateway starts on **port 9777** by default (configurable via `GATEWAY_PORT`).

### Run with Docker Compose

```bash
docker-compose up -d
```

This starts the gateway, Redis, Prometheus, Grafana, and Jaeger. The NGINX layer is available on port **80**, and the Go API is accessible directly on port **9777**.

### Verify

```bash
# Health check
curl http://localhost:9777/health | jq

# Through NGINX
curl http://localhost/health | jq

# With API key authentication
curl -H "X-API-Key: demo-api-key-123" http://localhost:9777/api/llm/v1/models
```

### Service URLs

| Service    | URL                             | Notes                    |
|------------|---------------------------------|--------------------------|
| NGINX      | `http://localhost:80`           | Public entry point       |
| Gateway    | `http://localhost:9777`         | Direct access            |
| Health     | `http://localhost:9777/health`  | JSON health + backends   |
| Metrics    | `http://localhost:9777/metrics` | Prometheus format        |
| Monitor    | `http://localhost:9777/monitor` | System metrics (JSON)    |
| Prometheus | `http://localhost:9090`         | Metrics UI               |
| Grafana    | `http://localhost:3000`         | Dashboards (admin/admin) |
| Jaeger     | `http://localhost:16686`        | Distributed tracing UI   |

## Features

### NGINX Reverse Proxy

The included NGINX configuration ([`nginx/nginx.conf`](nginx/nginx.conf)) provides:

- Front-facing reverse proxy on port 80
- Rate limiting zones (100 req/s for API traffic, 1000 req/s burst protection)
- Static file serving with 30-day cache headers
- Security headers (X-Frame-Options, X-Content-Type-Options, X-XSS-Protection)
- Gzip compression for text-based responses
- Custom error pages (404, 50x)
- Upstream health checking with `least_conn` algorithm
- Proxy headers for real client IP forwarding

See [NGINX_SETUP.md](NGINX_SETUP.md) for detailed NGINX configuration and SSL/TLS setup instructions.

### DDoS Protection

**Implementation:** [`middleware.go`](middleware.go) — `DDoSProtection` struct

- Per-IP request counting with 1-minute sliding windows
- Automatic IP blocking after configurable threshold (default: 1000 req/min)
- Temporary blocks with configurable duration (default: 10 minutes)
- Permanent whitelist and blacklist via admin API
- Background cleanup of expired blocks and stale counters
- `blocked_ips_total` Prometheus counter

### Load Balancing

**Implementation:** [`routing.go`](routing.go) — `ServicePool` struct

| Algorithm        | Best For                     | Sticky Sessions |
|------------------|------------------------------|-----------------|
| `round-robin`    | Uniform backends             | No              |
| `least-conn`     | Varying request durations    | No              |
| `ip-hash`        | Session affinity             | Yes             |
| `weighted`       | Heterogeneous backends       | No              |

All algorithms automatically skip unhealthy backends. Active health checks run on a configurable interval (default: 10 s) using TCP dial.

### Security

**Implementation:** [`middleware.go`](middleware.go) — authentication, rate limiting, headers

- **JWT authentication** — HMAC signature verification with expiration checking
- **API key authentication** — SHA-256 hashed key comparison
- **Per-IP rate limiting** — Token bucket algorithm via `golang.org/x/time/rate`
- **Security headers** — `X-Content-Type-Options`, `X-Frame-Options`, `Strict-Transport-Security`, `Content-Security-Policy`, `X-XSS-Protection`, `Referrer-Policy`
- **Request ID middleware** — Generates or preserves `x-request-id` for traceability
- **Public endpoint bypass** — Configurable list of paths that skip authentication (`/health`, `/metrics`, `/login`, `/register`, `/api/v1/public`)

### Caching & Performance

**Implementation:** [`handlers.go`](handlers.go) — `CacheManager` struct, `makeGzipHandler()`

- Redis-backed response caching for GET requests
- Cache key generation from method, host, and path
- `X-Cache: HIT` / `X-Cache: MISS` response headers
- Configurable TTL (default: 5 minutes)
- Gzip compression for clients that accept it
- Connection pooling with configurable idle connections and timeouts

### Observability

**Prometheus metrics** — defined in [`metrics.go`](metrics.go), exposed at `/metrics`:

| Metric                            | Type      | Description                          |
|-----------------------------------|-----------|--------------------------------------|
| `http_requests_total`             | Counter   | Requests by method, endpoint, status |
| `http_request_duration_seconds`   | Histogram | Latency distribution                 |
| `active_connections`              | Gauge     | Current active connections           |
| `backend_health_status`           | Gauge     | Per-backend health (1 = up, 0 = down) |
| `circuit_breaker_trips_total`     | Counter   | Circuit breaker activations          |
| `blocked_ips_total`               | Counter   | IPs blocked by DDoS protection       |
| `cache_hits_total`                | Counter   | Cache hits                           |

**Distributed tracing** — Jaeger integration via OpenTracing ([`handlers.go`](handlers.go) — `initTracer()`). Every request gets a span tagged with method, path, user ID, backend URL, and error status.

**Structured logging** — Custom leveled logger ([`logger.go`](logger.go)) with component tags, request IDs, colour output, and configurable level (`LOG_LEVEL` env var).

### Reliability

**Implementation:** [`middleware.go`](middleware.go) — `CircuitBreaker` struct

- **Circuit breaker** — Opens after configurable consecutive failures (default: 5), transitions to half-open after timeout (default: 30 s), closes on successful probe
- **Retry with exponential backoff** — Up to 3 attempts per request with 100 ms / 200 ms / 400 ms delays
- **Configurable timeouts** — Per-route and global connection timeouts
- **Graceful shutdown** — Catches `SIGTERM`/`SIGINT`, drains in-flight requests with a 30 s deadline

### Live Dashboard

**Implementation:** [`dashboard.go`](dashboard.go)

When running in a TTY, the gateway displays a live terminal dashboard (similar to `htop`/`nvtop`) showing:

- CPU and memory usage with colour-coded bars
- Per-core CPU breakdown
- Active connections, total requests, blocked IPs, circuit breaker trips
- Route/backend table with live health status and connection counts
- Request history sparkline

Set `NO_DASHBOARD=1` to disable the live dashboard.

## Configuration

All configuration is read from environment variables with sensible defaults. See [`config.go`](config.go) for the full `Config` struct.

| Variable                  | Default                               | Description                             |
|---------------------------|---------------------------------------|-----------------------------------------|
| `GATEWAY_PORT`            | `:9777`                               | Listen address                          |
| `JWT_SECRET`              | `your-secret-key-change-in-production`| HMAC signing key for JWT tokens         |
| `MAX_REQUESTS_PER_SECOND` | `100`                                 | Per-IP rate limit (requests/sec)        |
| `MAX_BURST_SIZE`          | `200`                                 | Token bucket burst size                 |
| `DDOS_THRESHOLD`          | `1000`                                | Max requests per minute before IP block |
| `BLOCK_DURATION`          | `10m`                                 | Duration of DDoS IP blocks              |
| `HEALTH_CHECK_INTERVAL`   | `10s`                                 | Backend health check frequency          |
| `CONNECTION_TIMEOUT`      | `30s`                                 | Proxy timeout per request               |
| `MAX_IDLE_CONNS`          | `100`                                 | HTTP transport idle connection pool     |
| `IDLE_CONN_TIMEOUT`       | `90s`                                 | Idle connection lifetime                |
| `CIRCUIT_BREAKER_MAX`     | `5`                                   | Failures before circuit opens           |
| `CIRCUIT_BREAKER_TIMEOUT` | `30s`                                 | Time before half-open probe             |
| `ENABLE_COMPRESSION`      | `true`                                | Enable gzip compression                 |
| `ENABLE_CACHING`          | `true`                                | Enable Redis response caching           |
| `CACHE_TTL`               | `5m`                                  | Cache entry time-to-live                |
| `REDIS_ADDR`              | `localhost:6379`                      | Redis server address                    |
| `LOAD_BALANCING_ALGO`     | `least-conn`                          | Algorithm: `round-robin`, `least-conn`, `ip-hash`, `weighted` |
| `LOG_LEVEL`               | `info`                                | Logging level: `debug`, `info`, `warn`, `error` |
| `CORS_ALLOWED_ORIGINS`    | `["*"]`                               | JSON array of allowed CORS origins      |
| `NO_DASHBOARD`            | _(unset)_                             | Set to any value to disable live TUI    |
| `NO_COLOR`                | _(unset)_                             | Disable coloured log output             |

### Environment Variables

Example `.env` file for local development:

```bash
GATEWAY_PORT=:9777
JWT_SECRET=change-me-in-production
MAX_REQUESTS_PER_SECOND=100
MAX_BURST_SIZE=200
DDOS_THRESHOLD=1000
REDIS_ADDR=localhost:6379
LOAD_BALANCING_ALGO=least-conn
ENABLE_CACHING=true
CACHE_TTL=5m
LOG_LEVEL=debug
```

The gateway automatically loads a `.env` file from the working directory if present.

### Service Routes

Routes map URL prefixes to backend services. There are three ways to configure them (checked in order):

**1. JSON array** (`SERVICE_ROUTES` env var):

```bash
export SERVICE_ROUTES='[
  {"prefix":"/api/llm","target_url":"http://llm-service:8745","strip_prefix":true,"require_auth":false,"weight":1,"timeout_secs":30},
  {"prefix":"/api/admin","target_url":"http://admin-service:8742","strip_prefix":true,"require_auth":true,"weight":1,"timeout_secs":15}
]'
```

**2. Individual env vars** (per-route `ROUTE_<NAME>_PREFIX` / `ROUTE_<NAME>_URL` pairs):

```bash
export ROUTE_LLM_PREFIX=/api/llm
export ROUTE_LLM_URL=http://llm-service:8745
export ROUTE_ADMIN_PREFIX=/api/admin
export ROUTE_ADMIN_URL=http://admin-service:8742
```

**3. Built-in defaults** — If no route env vars are set, the gateway registers a set of default development routes (see [`config.go`](config.go) — `loadServiceRoutes()`).

Each route supports:

| Field          | Description                                           |
|----------------|-------------------------------------------------------|
| `prefix`       | URL path prefix to match (longest-match wins)         |
| `target_url`   | Backend service URL                                   |
| `strip_prefix` | Remove the matched prefix before forwarding           |
| `require_auth` | Require JWT/API key for this route                    |
| `weight`       | Load-balancing weight (for `weighted` algorithm)      |
| `timeout_secs` | Per-route proxy timeout (overrides global default)    |

## API Reference

### Public Endpoints

| Method | Path                  | Description                              |
|--------|-----------------------|------------------------------------------|
| GET    | `/health`             | Health status with backend details       |
| GET    | `/metrics`            | Prometheus metrics                       |
| GET    | `/stats`              | Runtime gateway statistics               |
| GET    | `/monitor`            | System metrics (CPU, memory, processes)  |
| GET    | `/api/system-metrics` | System metrics (alias)                   |
| POST   | `/login`              | Authentication endpoint (placeholder)    |

### Admin Endpoints

| Method | Path                            | Description                |
|--------|---------------------------------|----------------------------|
| POST   | `/admin/whitelist?ip=<address>` | Add IP to DDoS whitelist   |
| POST   | `/admin/blacklist?ip=<address>` | Add IP to DDoS blacklist   |

### Protected / Proxied Endpoints

All requests matching a configured route prefix are forwarded to the corresponding backend. Authentication can be enforced per route.

**With JWT:**

```bash
curl -H "Authorization: Bearer <token>" \
  http://localhost:9777/api/llm/v1/models
```

**With API key:**

```bash
curl -H "X-API-Key: demo-api-key-123" \
  http://localhost:9777/api/llm/v1/models
```

### Health Check Response

```json
{
  "status": "healthy",
  "timestamp": "2026-04-09T12:00:00Z",
  "version": "2.0.0",
  "circuit_breaker": "closed",
  "routes": [
    {
      "prefix": "/api/llm",
      "require_auth": false,
      "strip_prefix": true,
      "backends": [
        {
          "url": "http://localhost:8745",
          "alive": true,
          "connections": 0,
          "weight": 1
        }
      ]
    }
  ]
}
```

## Deployment

### Docker Compose

The included [`docker-compose.yml`](docker-compose.yml) starts the full observability stack:

```bash
# Start all services
docker-compose up -d

# View gateway logs
docker-compose logs -f gateway

# Stop everything
docker-compose down
```

Services included: gateway (with NGINX), Redis, Prometheus, Grafana, Jaeger.

### Kubernetes

```bash
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/redis.yaml
kubectl apply -f k8s/ingress.yaml

# Check status
kubectl get pods -n va-gateway
kubectl logs -f deployment/api-gateway -n va-gateway
```

The Kubernetes manifests include:

- Horizontal Pod Autoscaler (3–10 replicas)
- Resource limits and requests
- Liveness and readiness probes on `/health`
- NGINX Ingress with TLS support

### Standalone Binary

```bash
# Build
go build -o gateway .

# Run (reads .env if present)
./gateway

# Or with explicit config
GATEWAY_PORT=:8080 REDIS_ADDR=redis:6379 ./gateway
```

## Development

### Prerequisites

- Go 1.23+
- (Optional) Redis for caching
- (Optional) `golangci-lint` for linting
- (Optional) `air` for hot reloading

### Make Targets

```bash
make help              # Show all available targets

# Build & Run
make build             # Compile binary
make run               # Run with `go run`
make dev               # Hot reload with air

# Quality
make test              # Run tests with coverage
make test-race         # Run tests with race detector
make fmt               # Format code
make lint              # Run golangci-lint

# Docker
make docker-build      # Build Docker image
make docker-up         # Start docker-compose stack
make docker-down       # Stop docker-compose stack
make docker-logs       # Tail gateway logs

# Kubernetes
make k8s-deploy        # Apply K8s manifests
make k8s-status        # Show pods, services, ingress
make k8s-logs          # Tail deployment logs
make k8s-delete        # Remove K8s resources

# Utilities
make health            # curl /health
make metrics           # curl /metrics (filtered)
make load-test         # Run scripts/load-test.sh
make deps              # Download and tidy modules
make clean             # Remove build artefacts
```

### Running Tests

```bash
# All tests with verbose output and coverage
go test -v -cover ./...

# With race detector
go test -v -race ./...

# Benchmarks
go test -bench=. -benchmem ./...
```

### Load Testing

```bash
# Using the included script
./scripts/load-test.sh

# Using wrk
wrk -t10 -c100 -d30s http://localhost:9777/health

# With authentication
wrk -t10 -c100 -d30s \
  -H "X-API-Key: demo-api-key-123" \
  http://localhost:9777/api/llm/v1/models
```

## Project Structure

```
va_api_gateway_golang/
├── main.go                 # Gateway struct, ServeHTTP, server bootstrap
├── main_test.go            # Unit tests and benchmarks
├── config.go               # Configuration loading from environment
├── handlers.go             # Cache manager, gzip, tracing, health/admin handlers
├── routing.go              # Backend, ServicePool, ServiceRouter, health checks
├── middleware.go            # Rate limiter, DDoS, circuit breaker, auth, headers
├── metrics.go              # Prometheus metric definitions and registration
├── logger.go               # Structured leveled logger with colour support
├── dashboard.go            # Live terminal dashboard (htop-style TUI)
├── system_monitor.go       # System metrics endpoint (CPU, memory, processes)
├── go.mod / go.sum         # Go module dependencies
├── Makefile                # Build, test, deploy automation
├── Dockerfile              # Multi-stage build (Go + NGINX)
├── docker-compose.yml      # Full stack: gateway, Redis, Prometheus, Grafana, Jaeger
├── .env.development        # Development environment defaults
├── .env.production         # Production environment template
├── .dockerignore           # Docker build context exclusions
├── .gitignore              # Git ignore rules
├── LICENSE                 # MIT License
├── NGINX_SETUP.md          # Detailed NGINX integration guide
├── nginx/
│   ├── nginx.conf          # NGINX reverse proxy configuration
│   └── static/             # Static files served by NGINX
├── config/
│   └── prometheus.yml      # Prometheus scrape configuration
├── k8s/
│   ├── deployment.yaml     # Gateway deployment + HPA
│   ├── redis.yaml          # Redis StatefulSet
│   └── ingress.yaml        # NGINX Ingress with TLS
└── scripts/
    ├── deploy.sh           # Deployment automation
    └── load-test.sh        # Load testing with wrk/ab
```

## Dependencies

| Library                               | Version | Purpose                     |
|---------------------------------------|---------|-----------------------------|
| [`golang-jwt/jwt/v5`](https://github.com/golang-jwt/jwt)       | v5.2.0  | JWT authentication          |
| [`prometheus/client_golang`](https://github.com/prometheus/client_golang) | v1.18.0 | Prometheus metrics          |
| [`redis/go-redis/v9`](https://github.com/redis/go-redis)       | v9.4.0  | Redis client for caching    |
| [`uber/jaeger-client-go`](https://github.com/jaegertracing/jaeger-client-go) | v2.30.0 | Jaeger distributed tracing  |
| [`opentracing/opentracing-go`](https://github.com/opentracing/opentracing-go) | v1.2.0  | OpenTracing interface       |
| [`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate) | v0.5.0  | Token bucket rate limiting  |
| [`shirou/gopsutil/v3`](https://github.com/shirou/gopsutil)     | v3.24.5 | System metrics (CPU, memory)|
| [`joho/godotenv`](https://github.com/joho/godotenv)            | v1.5.1  | `.env` file loading         |

## Troubleshooting

### Gateway won't start

```bash
# Check if the port is already in use
lsof -i :9777

# Check Docker logs
docker-compose logs gateway
```

### All backends show "down"

Backends are health-checked via TCP dial. Ensure your backend services are running and reachable from the gateway's network.

```bash
# Check health endpoint for backend status
curl -s http://localhost:9777/health | jq '.routes[].backends'
```

### Rate limiting too aggressive

Increase `MAX_REQUESTS_PER_SECOND` and `MAX_BURST_SIZE`, or whitelist trusted IPs via the admin endpoint:

```bash
curl -X POST "http://localhost:9777/admin/whitelist?ip=10.0.0.1"
```

### 503 Service Unavailable

This means all backends for the matched route are down, or the circuit breaker is open. Check:

```bash
# Backend health
curl -s http://localhost:9777/health | jq '.routes'

# Circuit breaker state
curl -s http://localhost:9777/stats | jq '.circuit_breaker_state'
```

### High memory usage

- Reduce `MAX_IDLE_CONNS` (default: 100)
- Lower `CACHE_TTL` to reduce Redis memory
- Check for connection leaks with the `/stats` endpoint

### NGINX returns 502 Bad Gateway

NGINX cannot reach the Go backend. Check that the gateway process is running:

```bash
docker exec va_api_gateway_go curl -f http://localhost:9777/health
```

## Security Considerations

> **⚠️ Before deploying to production:**

1. **Change `JWT_SECRET`** — The default value is insecure. Use a strong, random secret.
2. **Move API keys to a database** — The demo keys are hardcoded for development only.
3. **Enable HTTPS** — Configure SSL/TLS certificates in NGINX (see [NGINX_SETUP.md](NGINX_SETUP.md)).
4. **Tune rate limits** — Adjust `MAX_REQUESTS_PER_SECOND`, `DDOS_THRESHOLD`, and NGINX rate zones based on your traffic patterns.
5. **Whitelist trusted IPs** — Add CI/CD runners, monitoring systems, and internal services.
6. **Encrypt Redis connections** — Enable TLS on Redis in production.
7. **Use secrets management** — Store sensitive config in Vault, K8s Secrets, or your platform's secret manager.
8. **Keep dependencies updated** — Run `go get -u` and review changelogs regularly.

## Contributing

Contributions are welcome! Here's how to get started:

1. **Fork** the repository
2. **Create a branch** for your feature or fix:
   ```bash
   git checkout -b feature/my-feature
   ```
3. **Make your changes** and add tests where appropriate
4. **Run the test suite** to make sure nothing is broken:
   ```bash
   make test
   ```
5. **Commit** with a clear message:
   ```bash
   git commit -m "feat: add support for custom health check paths"
   ```
6. **Push** and open a **Pull Request**

Please keep PRs focused on a single change. For larger changes, open an issue first to discuss the approach.

## License

This project is licensed under the **MIT License** — see the [LICENSE](LICENSE) file for details.

## Acknowledgements

- [NGINX](https://nginx.org/) — battle-tested reverse proxy patterns
- [Netflix Hystrix](https://github.com/Netflix/Hystrix) — circuit breaker pattern
- [Prometheus](https://prometheus.io/) — observability and monitoring
- [Jaeger](https://www.jaegertracing.io/) — distributed tracing

---

<div align="center">
  <sub>Built by the <a href="https://github.com/valtunox">Valtunox</a> team</sub>
</div>
