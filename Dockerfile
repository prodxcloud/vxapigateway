# =============================================================================
# API Gateway — Production Container
# Ports  : 80 (nginx → reverse proxy) | 8080 (Go/Gin API)
#
# Build strategy (multi-stage):
#   builder  — compiles the Go binary; nothing else is shipped
#   runtime  — slim alpine with nginx for reverse proxy
# =============================================================================

# ── Stage 1: Compile ─────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git gcc musl-dev ca-certificates

WORKDIR /app

# Download dependencies first (cache-friendly layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source and compile a static binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /app/gateway .

# ── Stage 2: Runtime ─────────────────────────────────────────────────────────
FROM alpine:latest

LABEL maintainer="valtunox"
LABEL org.opencontainers.image.title="va-api-gateway"
LABEL org.opencontainers.image.description="API Gateway with nginx reverse proxy"

# System utilities: nginx for reverse proxy
RUN apk add --no-cache \
    bash \
    ca-certificates \
    curl \
    nginx \
    wget

WORKDIR /app

# ── Compiled binary from builder ──────────────────────────────────────────────
COPY --from=builder /app/gateway /app/gateway
RUN chmod +x /app/gateway

# ── Runtime assets ────────────────────────────────────────────────────────────
COPY config/ ./config/

# ── nginx configuration ───────────────────────────────────────────────────────
COPY nginx/nginx.conf /etc/nginx/nginx.conf
COPY nginx/static/ /usr/share/nginx/html/
RUN mkdir -p /var/log/nginx /var/cache/nginx /var/run/nginx \
    && mkdir -p /app/logs

# ── Startup script ────────────────────────────────────────────────────────────
# Starts nginx (daemon) then exec's the Go binary as PID 1 for signal handling
RUN printf '#!/bin/sh\nset -e\nnginx\nexec /app/gateway\n' \
    > /app/start.sh && chmod +x /app/start.sh

# ── Environment ───────────────────────────────────────────────────────────────
ENV GATEWAY_PORT=:9777

# ── Ports ─────────────────────────────────────────────────────────────────────
# 80    – nginx HTTP (public entry point, proxies to Go API)
# 9777  – Go API (internal)
EXPOSE 80 9777

# ── Healthcheck ───────────────────────────────────────────────────────────────
HEALTHCHECK --interval=30s --timeout=10s --start-period=15s --retries=3 \
    CMD curl -f http://localhost:9777/health || exit 1

CMD ["/app/start.sh"]
