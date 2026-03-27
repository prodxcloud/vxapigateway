# nginx Integration - Setup Guide

This document explains the nginx reverse proxy setup applied to `va_api_gateway_golang`, following the same patterns used in `va_golang_infra_provisionner`.

## What Changed

### 1. Dockerfile - Multi-Stage Build with nginx

**Before:**
- Simple 2-stage build (builder + alpine runtime)
- Direct exposure of Go application on port 8080
- No reverse proxy layer

**After:**
- Enhanced 2-stage build with nginx integration
- nginx as front-facing reverse proxy on port 80
- Go application runs internally on port 8080
- Startup script manages both nginx and Go processes
- nginx runs as daemon, Go app as PID 1 for proper signal handling

**Key Changes:**
```dockerfile
# Added nginx installation
RUN apk add --no-cache nginx

# Added nginx configuration
COPY nginx/nginx.conf /etc/nginx/nginx.conf

# Created startup script
RUN printf '#!/bin/sh\nset -e\nnginx\nexec /app/gateway\n' \
    > /app/start.sh && chmod +x /app/start.sh

# Exposed port 80 for nginx
EXPOSE 80 8080
```

### 2. docker-compose.yml - Production-Grade Configuration

**Before:**
- Basic service definition
- Simple port mapping
- Minimal configuration

**After:**
- Production-ready configuration with:
  - Resource limits (CPU: 2 cores max, Memory: 2GB max)
  - Resource reservations (CPU: 0.5 cores, Memory: 512MB)
  - Security options (`no-new-privileges:true`)
  - Volume mounts for nginx config and static files
  - Enhanced healthcheck configuration
  - Extra hosts for internal routing
  - Proper service ordering with dependencies

**Key Changes:**
```yaml
ports:
  - "80:80"           # nginx HTTP — public entry point
  - "8080:8080"       # Go/Gin API  — direct access (optional)

volumes:
  - type: bind
    source: ./nginx/nginx.conf
    target: /etc/nginx/nginx.conf
    read_only: true
  - type: bind
    source: ./nginx/static
    target: /usr/share/nginx/html

security_opt:
  - no-new-privileges:true

deploy:
  resources:
    limits:
      cpus: "2"
      memory: 2G
    reservations:
      cpus: "0.5"
      memory: 512M
```

### 3. nginx Configuration

**New File:** `nginx/nginx.conf`

Features:
- Rate limiting zones (100 req/s for API, 1000 req/s for DDoS protection)
- Upstream load balancing with `least_conn` algorithm
- Static file serving with 30-day cache
- Security headers (X-Frame-Options, X-Content-Type-Options, X-XSS-Protection)
- Gzip compression for text-based responses
- Custom error pages (404, 50x)
- Health check endpoint without access logging
- Proxy headers for real IP forwarding
- Request buffering and timeout configuration

**Rate Limiting:**
```nginx
limit_req_zone $binary_remote_addr zone=api_limit:10m rate=100r/s;
limit_req_zone $binary_remote_addr zone=ddos_protection:10m rate=1000r/s;
```

**Upstream Configuration:**
```nginx
upstream gateway_backend {
    least_conn;
    server localhost:8080 max_fails=3 fail_timeout=30s;
}
```

**Security Headers:**
```nginx
add_header X-Frame-Options "SAMEORIGIN" always;
add_header X-Content-Type-Options "nosniff" always;
add_header X-XSS-Protection "1; mode=block" always;
add_header Referrer-Policy "no-referrer-when-downgrade" always;
```

### 4. Static Files

**New Directory:** `nginx/static/`

Created custom error pages and landing page:
- `index.html` - Gateway landing page with status information
- `404.html` - Custom 404 error page
- `50x.html` - Custom 5xx error page

All pages feature:
- Modern, responsive design
- Gradient backgrounds
- Clean typography
- Consistent branding

### 5. .dockerignore

**New File:** `.dockerignore`

Optimizes Docker build by excluding:
- Git files and history
- IDE configurations
- Build artifacts
- Test files
- Documentation
- Environment files
- Logs
- Scripts and Kubernetes configs

Benefits:
- Faster build times
- Smaller build context
- Better security (no sensitive files in image)

## Architecture Comparison

### va_golang_infra_provisionner Pattern

```
Client → nginx (port 80) → Go API (port 5002)
         ↓
         Static Files (/usr/share/nginx/html)
```

### va_api_gateway_golang (Now Applied)

```
Client → nginx (port 80) → Go Gateway (port 8080) → Backend Services
         ↓
         Static Files (/usr/share/nginx/html)
         Rate Limiting (100r/s API, 1000r/s DDoS)
         Security Headers
         Gzip Compression
```

## Benefits of This Setup

1. **Security**
   - nginx handles SSL/TLS termination (when configured)
   - Rate limiting at nginx level (before reaching Go app)
   - Security headers automatically added
   - DDoS protection with request queuing

2. **Performance**
   - Static file serving by nginx (faster than Go)
   - Gzip compression at nginx level
   - Connection pooling and keep-alive
   - Request buffering

3. **Reliability**
   - nginx as battle-tested reverse proxy
   - Graceful error handling with custom pages
   - Health check endpoint for monitoring
   - Automatic failover with upstream configuration

4. **Scalability**
   - Easy to add more backend instances to upstream
   - Load balancing at nginx level
   - Resource limits prevent resource exhaustion
   - Horizontal scaling ready

5. **Operations**
   - Centralized logging at nginx level
   - Metrics endpoint protection
   - Easy SSL certificate management
   - Standard nginx configuration patterns

## Usage

### Development

```bash
# Build and run with docker-compose
docker-compose up -d

# Access via nginx (port 80)
curl http://localhost/health

# Access Go app directly (port 8080)
curl http://localhost:8080/health

# View nginx logs
docker-compose logs gateway | grep nginx

# View Go app logs
docker-compose logs gateway | grep gateway
```

### Production

```bash
# Build production image
docker build -t valtunox/api-gateway:latest .

# Run with production config
docker-compose -f docker-compose.yml up -d

# Check nginx status
docker exec va_api_gateway_go nginx -t

# Reload nginx config (without restart)
docker exec va_api_gateway_go nginx -s reload
```

### Testing Rate Limiting

```bash
# Test API rate limit (100 req/s)
ab -n 1000 -c 10 http://localhost/api/test

# Test DDoS protection (1000 req/s)
ab -n 10000 -c 100 http://localhost/health

# Monitor rate limiting
docker-compose logs gateway | grep "limiting requests"
```

## Configuration

### Adjusting Rate Limits

Edit `nginx/nginx.conf`:

```nginx
# Increase API rate limit to 200 req/s
limit_req_zone $binary_remote_addr zone=api_limit:10m rate=200r/s;

# Increase DDoS threshold to 2000 req/s
limit_req_zone $binary_remote_addr zone=ddos_protection:10m rate=2000r/s;
```

### Adding SSL/TLS

```nginx
server {
    listen 443 ssl http2;
    server_name api.yourdomain.com;

    ssl_certificate /etc/nginx/ssl/cert.pem;
    ssl_certificate_key /etc/nginx/ssl/key.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;

    # ... rest of configuration
}

# Redirect HTTP to HTTPS
server {
    listen 80;
    server_name api.yourdomain.com;
    return 301 https://$server_name$request_uri;
}
```

### Adding More Backend Instances

```nginx
upstream gateway_backend {
    least_conn;
    server localhost:8080 max_fails=3 fail_timeout=30s weight=3;
    server localhost:8081 max_fails=3 fail_timeout=30s weight=2;
    server localhost:8082 max_fails=3 fail_timeout=30s weight=1;
}
```

## Monitoring

### nginx Metrics

```bash
# Access logs
docker exec va_api_gateway_go tail -f /var/log/nginx/access.log

# Error logs
docker exec va_api_gateway_go tail -f /var/log/nginx/error.log

# nginx status (requires stub_status module)
curl http://localhost/nginx_status
```

### Health Checks

```bash
# Via nginx (port 80)
curl http://localhost/health

# Direct to Go app (port 8080)
curl http://localhost:8080/health

# Check nginx configuration
docker exec va_api_gateway_go nginx -t
```

## Troubleshooting

### nginx won't start

```bash
# Check configuration syntax
docker exec va_api_gateway_go nginx -t

# Check nginx error log
docker exec va_api_gateway_go cat /var/log/nginx/error.log

# Check if port 80 is available
lsof -i :80
```

### 502 Bad Gateway

This means nginx can't reach the Go backend:

```bash
# Check if Go app is running
docker exec va_api_gateway_go ps aux | grep gateway

# Check Go app health directly
docker exec va_api_gateway_go curl http://localhost:8080/health

# Check nginx upstream configuration
docker exec va_api_gateway_go cat /etc/nginx/nginx.conf | grep upstream
```

### Rate Limiting Too Aggressive

```bash
# Check current limits
docker exec va_api_gateway_go cat /etc/nginx/nginx.conf | grep limit_req_zone

# Temporarily disable rate limiting (for testing)
# Comment out limit_req lines in nginx.conf and reload
docker exec va_api_gateway_go nginx -s reload
```

## Next Steps

1. **SSL/TLS Setup** - Add SSL certificates for HTTPS
2. **Custom Domain** - Configure DNS and update server_name
3. **Monitoring** - Set up nginx log aggregation (ELK, Loki)
4. **Caching** - Add nginx caching for static API responses
5. **WAF** - Consider adding ModSecurity for web application firewall

## References

- [nginx Documentation](https://nginx.org/en/docs/)
- [nginx Rate Limiting](https://www.nginx.com/blog/rate-limiting-nginx/)
- [nginx Load Balancing](https://docs.nginx.com/nginx/admin-guide/load-balancer/http-load-balancer/)
- [nginx Security](https://www.nginx.com/blog/mitigating-ddos-attacks-with-nginx-and-nginx-plus/)

---

**Pattern Source:** `va_golang_infra_provisionner`  
**Applied To:** `va_api_gateway_golang`  
**Date:** March 28, 2026
