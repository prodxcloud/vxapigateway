# Changelog

All notable changes to **VA API Gateway** will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added
- Placeholder — see [TODO.md](TODO.md) for planned work.

---

## [2.0.0] — 2026-04-22

First public open-source release. The gateway has been hardened for public-facing deployment and re-licensed under MIT.

### Added
- **Open-source release** — MIT license, public repository, public roadmap ([TODO.md](TODO.md)), and a [CONTRIBUTORS.md](CONTRIBUTORS.md) listing the core maintainers.
- **Dynamic routing** — Configure upstreams via a `SERVICE_ROUTES` JSON env var, individual `ROUTE_<NAME>_PREFIX` / `ROUTE_<NAME>_URL` pairs, or built-in defaults. See `loadServiceRoutes()` in `config.go`.
- **Longest-prefix route matching** with per-route `strip_prefix`, `require_auth`, `weight`, and `timeout_secs`.
- **Live TTY dashboard** — `dashboard.go` renders total requests, active connections, blocked IPs, circuit state, and backend health in real time when attached to a terminal.
- **System monitor** — New `/monitor` HTML page and `/api/system-metrics` JSON endpoint (`system_monitor.go`) exposing CPU, memory, and disk usage of the host.
- **`/stats` endpoint** — Quick runtime summary: circuit state, load-balancing algorithm, caching and compression flags, registered route count.
- **Structured JSON logging** — Per-subsystem loggers (`gateway`, `router`, `proxy`, `cache`, `circuit`, `ddos`, `admin`, `tracer`, `config`) with consistent `request_id` propagation via `x-request-id`.
- **CORS configuration** — `CORS_ALLOWED_ORIGINS` env var (JSON array, defaults to `["*"]`).
- **`.env` loading** — `godotenv` auto-loads `.env` at startup; non-fatal if missing.
- **Kubernetes manifests** — Deployment with HPA (3–10 replicas), Pod Disruption Budget, Redis StatefulSet with PVC, and NGINX Ingress with SSL.
- **nginx edge proxy** — Optional front-facing nginx container with rate limiting, gzip, static files, and security headers (`nginx/nginx.conf`, `NGINX_SETUP.md`).

### Changed
- **Default port** moved to `:9777` (was `:8080`).
- **Configuration model** — All tunables are now environment variables with sensible defaults, consolidated in `Config` / `LoadConfig()`.
- **Security headers** — Expanded to include `Referrer-Policy` and stricter `Content-Security-Policy` defaults.
- **Proxy transport** — Per-route `MaxIdleConns`, `MaxIdleConnsPerHost`, and `IdleConnTimeout`; TLS verification is on by default.
- **Retry behaviour** — Exponential backoff (`100ms * 2^attempt`) now rolls to the next healthy backend between attempts instead of retrying the same one.

### Fixed
- **Graceful shutdown** — 30-second bounded context on SIGINT/SIGTERM; in-flight requests drain before the process exits.
- **Rate-limiter memory leak** — Idle per-IP limiters are now evicted every 10 minutes.
- **DDoS counter window** — Request counters reset cleanly when the 1-minute window rolls over, preventing false positives after traffic bursts.

### Security
- JWT validation now explicitly rejects any algorithm other than HMAC and enforces the `exp` claim.
- API keys are compared via SHA-256 hash rather than plaintext equality.
- Default CSP hardened to `default-src 'self'`.

---

## [1.0.0] — 2026-01-08

Initial internal release inside the VA Studio platform.

### Added
- Core Go reverse proxy with `net/http` and `httputil.ReverseProxy`.
- Per-IP token-bucket rate limiting (`golang.org/x/time/rate`).
- DDoS protection with request-rate thresholds and temporary IP blocks.
- Four load-balancing algorithms: round-robin, least-connections, IP-hash, weighted.
- Classic circuit breaker (closed / open / half-open).
- Redis-backed GET response cache with gzip compression.
- JWT (HS256) and API-key authentication.
- Prometheus metrics: requests, duration, active connections, backend health, circuit trips, blocked IPs, cache hits.
- Jaeger distributed tracing via OpenTracing.
- Multi-stage Dockerfile and `docker-compose.yml` with Redis, Prometheus, Grafana, and Jaeger.
- `/health`, `/metrics`, `/admin/whitelist`, `/admin/blacklist` admin endpoints.
- Initial unit tests (`main_test.go`) and load-test script (`scripts/load-test.sh`).

---

[Unreleased]: https://github.com/valtunox/va_api_gateway_golang/compare/v2.0.0...HEAD
[2.0.0]: https://github.com/valtunox/va_api_gateway_golang/compare/v1.0.0...v2.0.0
[1.0.0]: https://github.com/valtunox/va_api_gateway_golang/releases/tag/v1.0.0
