# Roadmap / TODO

Public roadmap for **VA API Gateway**. Anything here is fair game for community contribution — pick an item, open an issue to claim it, and send a PR.

Legend: `[ ]` not started · `[~]` in progress · `[x]` done (moved to CHANGELOG on release)

---

## Near-term (next minor release)

### Security hardening for a public edge

- [ ] Replace the hardcoded API-key map in `middleware.go` with a pluggable store (interface + Redis/Postgres/file backends).
- [ ] Per-route request-body size limits (`max_body_bytes` on `ServiceRoute`).
- [ ] Per-route per-method allow-lists (reject `PUT` on a `GET`-only endpoint at the edge).
- [ ] Optional mTLS between the gateway and upstream services.
- [ ] Signed request timestamps (HMAC over `X-Timestamp` + path) to mitigate replay on the public `/login`.
- [ ] Secrets source abstraction — pull `JWT_SECRET` from AWS Secrets Manager, GCP Secret Manager, or HashiCorp Vault instead of an env var.

### Observability polish

- [ ] Grafana dashboard JSON (`dashboards/gateway.json`) shipping alongside `config/prometheus.yml`.
- [ ] OpenTelemetry exporter alongside the existing Jaeger client (`OTEL_EXPORTER_OTLP_ENDPOINT`).
- [ ] `/metrics` label for `route` so requests can be aggregated per upstream regardless of the raw path.
- [ ] Prometheus histogram for backend response latency (separate from total gateway latency).

### Routing features

- [ ] WebSocket proxying (upgrade detection + bidirectional copy).
- [ ] gRPC passthrough with HTTP/2 support.
- [ ] Path rewrite rules (regex `from` → template `to`), not only prefix stripping.
- [ ] Canary / weighted traffic splitting between multiple target URLs on the same prefix.

---

## Mid-term

### Plug-in architecture

- [ ] Middleware plugin contract: request-phase + response-phase hooks with `next()` semantics.
- [ ] Reference plugins:
  - [ ] Request/response body transformer (JSON field mapping).
  - [ ] Outbound webhook mirror (shadow traffic to a second upstream).
  - [ ] Response header rewrites.
- [ ] Hot-reload plugin list from config without restart.

### Configuration surface

- [ ] YAML config file (`gateway.yaml`) as an alternative to env vars, with hot-reload on SIGHUP.
- [ ] Dynamic route API (`POST /admin/routes`, `DELETE /admin/routes/:prefix`) persisted to Redis.
- [ ] Admin UI (small HTML/JS page) to visualise routes, backends, and breaker state without reading `/health`.

### Caching

- [ ] Cache invalidation API (`POST /admin/cache/invalidate?pattern=...`).
- [ ] Per-route cache policy (TTL, vary-by headers, bypass on cookie).
- [ ] Stale-while-revalidate for cached GETs.

---

## Long-term

- [ ] Built-in TLS termination (ACME / Let's Encrypt) so the gateway can run without nginx in front of it.
- [ ] HTTP/3 (QUIC) listener.
- [ ] Multi-instance coordination — share rate-limit, DDoS counters, and breaker state across gateway replicas via Redis.
- [ ] Cluster-aware service discovery (Consul, Kubernetes EndpointSlices) replacing the static route table.
- [ ] Built-in WAF rules (OWASP CRS subset) for Layer 7 attacks.
- [ ] Protocol-buffer admin API + optional gRPC reflection for tooling.

---

## Documentation

- [ ] Architecture deep-dive doc (`docs/ARCHITECTURE.md`) covering the request lifecycle diagram, the middleware chain, and where to hook in.
- [ ] Production-deployment guide for AWS (ALB → gateway → private upstreams).
- [ ] Production-deployment guide for GCP (GCLB + Cloud Armor → gateway).
- [ ] Production-deployment guide for bare-metal with nginx + systemd.
- [ ] Threat model doc — what the gateway defends against and what it explicitly does not.

---

## Good first issues

Small, self-contained tasks ideal for first-time contributors:

- [ ] Add `go vet` and `golangci-lint` to a GitHub Actions workflow.
- [ ] Add a `make fmt` target (`gofmt -s -w .`).
- [ ] Wire a `/version` endpoint returning build info (git SHA, build time, Go version) via `-ldflags`.
- [ ] Extend `main_test.go` to cover the `weighted` and `ip-hash` algorithms.
- [ ] Add a test matrix to CI for Go 1.23 and 1.24.
- [ ] Document each env var in `.env.development` with a one-line comment.
- [ ] Switch `main_test.go` fixtures to `httptest.NewServer` where they currently dial localhost.
- [ ] Replace `fmt.Println` banners in `dashboard.go` with the structured logger when `NO_DASHBOARD=1`.

---

## Out of scope

Things we are **not** planning to build — feel free to open a discussion if you disagree:

- Full API management (developer portals, monetization, quota billing) — use Kong or WSO2 for that.
- GUI route editor beyond a minimal admin page — infra-as-code is the right answer.
- Non-HTTP protocols (MQTT, AMQP) — keep the gateway HTTP/gRPC focused.

---

Maintained by the contributors listed in [CONTRIBUTORS.md](CONTRIBUTORS.md). Item moved to [CHANGELOG.md](CHANGELOG.md) when shipped.
