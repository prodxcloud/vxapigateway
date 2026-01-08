# 🚀 Quick Start Guide

## ⚡ 60-Second Deploy

```bash
# Clone and enter directory
cd va_api_gateway_golang

# Option 1: Local Development (fastest)
make build && ./gateway

# Option 2: Docker (recommended)
make docker-up

# Option 3: Kubernetes (production)
make k8s-deploy
```

## 🧪 Test It

```bash
# Health check
curl http://localhost:8080/health

# With authentication
curl -H "X-API-Key: demo-api-key-123" \
  http://localhost:8080/api/test

# Load test
make load-test
```

## 📊 Monitor It

- Gateway: http://localhost:8080
- Metrics: http://localhost:8080/metrics
- Prometheus: http://localhost:9090
- Grafana: http://localhost:3000 (admin/admin)
- Jaeger: http://localhost:16686

## 🔧 Common Commands

```bash
# Build
make build

# Run locally
make run

# Tests
make test

# Docker
make docker-build
make docker-up
make docker-logs
make docker-down

# Kubernetes
make k8s-deploy
make k8s-status
make k8s-logs
make k8s-delete

# Health & Metrics
make health
make metrics
```

## 🎯 Feature Checklist

- ✅ DDoS Protection (rate limiting, IP blocking)
- ✅ Load Balancing (4 algorithms)
- ✅ Security (JWT, API keys, IP filtering)
- ✅ Performance (caching, compression, pooling)
- ✅ Observability (Prometheus, Jaeger, logging)
- ✅ Reliability (circuit breaker, retries, failover)

## 🔐 Security Setup

```bash
# 1. Change JWT secret (IMPORTANT!)
export JWT_SECRET="your-production-secret-here"

# 2. Whitelist trusted IPs
curl -X POST "http://localhost:8080/admin/whitelist?ip=1.2.3.4"

# 3. Blacklist malicious IPs
curl -X POST "http://localhost:8080/admin/blacklist?ip=5.6.7.8"
```

## 📝 Configuration

Edit [main.go](main.go) or set environment variables:

```bash
export JWT_SECRET="your-secret"
export MAX_RPS=1000
export DDOS_THRESHOLD=5000
export REDIS_ADDR="redis:6379"
export LOAD_BALANCING_ALGO="least-conn"
```

## 🐛 Troubleshooting

### Gateway won't start
```bash
# Check port
lsof -i :8080

# View logs
make docker-logs
```

### Backends unhealthy
```bash
# Check health
make health

# View backend status
curl http://localhost:8080/health | jq '.backends'
```

### High memory
- Reduce `MaxIdleConns` (default: 100)
- Decrease cache TTL
- Check for connection leaks

## 📚 Learn More

- [README.md](README.md) - Full documentation
- [IMPLEMENTATION.md](IMPLEMENTATION.md) - Complete feature list
- [main.go](main.go) - Source code with comments

## 🎓 Architecture

```
Client → Gateway → [Load Balancer] → Backend Services
              ↓
         [Redis Cache]
              ↓
      [Prometheus Metrics]
              ↓
       [Jaeger Tracing]
```

## 💡 Pro Tips

1. **Use least-conn for APIs** - Best for varying request times
2. **Enable caching for reads** - Reduces backend load
3. **Monitor circuit breaker** - Indicates backend issues
4. **Whitelist CI/CD IPs** - Avoid rate limiting builds
5. **Scale horizontally** - Run multiple gateway instances

## 📞 Need Help?

- 📖 Documentation: [README.md](README.md)
- 🐛 Issues: [GitHub Issues](https://github.com/valtunox/va_api_gateway_golang/issues)
- 💬 Discussions: GitHub Discussions

---

**Built with ❤️ in Go | Production-Ready | Open Source**
