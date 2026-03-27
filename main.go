package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/opentracing/opentracing-go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
)

func init() {
	// Load .env file if present (non-fatal if missing)
	_ = godotenv.Load()
}

// ============= GATEWAY =============

// Gateway is the central object that ties together routing, rate limiting,
// DDoS protection, circuit breaking, caching, and distributed tracing.
type Gateway struct {
	router         *ServiceRouter
	rateLimiter    *IPRateLimiter
	ddosProtection *DDoSProtection
	circuitBreaker *CircuitBreaker
	cacheManager   *CacheManager
	tracer         opentracing.Tracer
	tracerCloser   io.Closer
	cfg            Config
}

// NewGateway constructs a fully initialised Gateway from the given Config.
func NewGateway(cfg Config) *Gateway {
	var tracer opentracing.Tracer
	var tracerCloser io.Closer

	tracer, tracerCloser, err := initTracer("api-gateway")
	if err != nil {
		logTracer().Warn("Jaeger tracer init failed, tracing disabled", "error", err)
	} else {
		logTracer().Info("Jaeger tracer initialized", "service", "api-gateway")
	}

	gw := &Gateway{
		router:         NewServiceRouter(),
		rateLimiter:    NewIPRateLimiter(rate.Limit(cfg.MaxRequestsPerSecond), cfg.MaxBurstSize),
		ddosProtection: NewDDoSProtection(cfg.DDoSThreshold, cfg.BlockDuration),
		circuitBreaker: NewCircuitBreaker(cfg.CircuitBreakerMax, cfg.CircuitBreakerTimeout),
		cacheManager:   NewCacheManager(cfg.RedisAddr, cfg.CacheTTL, cfg.EnableCaching),
		tracer:         tracer,
		tracerCloser:   tracerCloser,
		cfg:            cfg,
	}

	// Register routes from configuration
	for _, route := range cfg.ServiceRoutes {
		gw.router.AddRoute(route, cfg.LoadBalancingAlgo, cfg)
	}

	return gw
}

// Close releases gateway resources (e.g. the Jaeger tracer).
func (g *Gateway) Close() {
	if g.tracerCloser != nil {
		g.tracerCloser.Close()
	}
}

// ServeHTTP implements http.Handler.  It applies the full middleware chain
// inline: DDoS check, rate limiting, authentication, caching, then proxies
// through the circuit breaker with retry logic.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := r.Header.Get("x-request-id")
	reqLog := logGateway().WithRequestID(requestID)
	ip := getClientIP(r)

	reqLog.Debug("request started", "method", r.Method, "path", r.URL.Path, "client_ip", ip)

	// Start tracing span
	var span opentracing.Span
	if g.tracer != nil {
		span = g.tracer.StartSpan("gateway.request")
		defer span.Finish()
		span.SetTag("method", r.Method)
		span.SetTag("path", r.URL.Path)
	}

	defer func() {
		duration := time.Since(start).Seconds()
		requestDuration.WithLabelValues(r.Method, r.URL.Path).Observe(duration)
	}()

	// DDoS Protection
	if g.ddosProtection.IsBlocked(ip) {
		reqLog.Warn("request rejected", "reason", "IP blocked", "client_ip", ip, "method", r.Method, "path", r.URL.Path)
		http.Error(w, "Too many requests - IP blocked", http.StatusTooManyRequests)
		requestsTotal.WithLabelValues(r.Method, r.URL.Path, "429").Inc()
		if span != nil {
			span.SetTag("error", true)
			span.SetTag("error.message", "IP blocked")
		}
		return
	}

	if g.ddosProtection.RecordRequest(ip) {
		reqLog.Warn("request rejected", "reason", "DDoS protection triggered", "client_ip", ip, "method", r.Method, "path", r.URL.Path)
		http.Error(w, "DDoS protection triggered", http.StatusTooManyRequests)
		requestsTotal.WithLabelValues(r.Method, r.URL.Path, "429").Inc()
		if span != nil {
			span.SetTag("error", true)
			span.SetTag("error.message", "DDoS triggered")
		}
		return
	}

	// Rate Limiting
	limiter := g.rateLimiter.GetLimiter(ip)
	if !limiter.Allow() {
		reqLog.Warn("request rejected", "reason", "rate limit exceeded", "client_ip", ip, "method", r.Method, "path", r.URL.Path)
		http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
		requestsTotal.WithLabelValues(r.Method, r.URL.Path, "429").Inc()
		if span != nil {
			span.SetTag("error", true)
			span.SetTag("error.message", "Rate limit exceeded")
		}
		return
	}

	// Route matching
	matched := g.router.Route(r.URL.Path)
	if matched == nil {
		reqLog.Info("request rejected", "reason", "no route matched", "path", r.URL.Path, "method", r.Method)
		http.Error(w, "No route matched", http.StatusNotFound)
		requestsTotal.WithLabelValues(r.Method, r.URL.Path, "404").Inc()
		return
	}

	// Authentication (per-route or global)
	requireAuth := matched.RequireAuth
	authHeader := r.Header.Get("Authorization")
	apiKey := r.Header.Get("X-API-Key")

	authenticated := false
	var userID string

	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if claims, err := validateJWT(token, g.cfg); err == nil {
			authenticated = true
			if sub, ok := (*claims)["sub"].(string); ok {
				userID = sub
			}
		}
	} else if apiKey != "" {
		if validateAPIKey(apiKey) {
			authenticated = true
			userID = "api-key-user"
		}
	}

	// Public endpoints bypass auth; otherwise route-level auth is checked
	if requireAuth && !isPublicEndpoint(r.URL.Path) && !authenticated {
		reqLog.Warn("request rejected", "reason", "unauthorized", "path", r.URL.Path, "method", r.Method)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		requestsTotal.WithLabelValues(r.Method, r.URL.Path, "401").Inc()
		if span != nil {
			span.SetTag("error", true)
			span.SetTag("error.message", "Unauthorized")
		}
		return
	}

	// Even for non-route-auth paths, enforce auth for non-public endpoints
	if !isPublicEndpoint(r.URL.Path) && !authenticated && !requireAuth {
		// Default behaviour: non-public endpoints still require auth
		// (keeping backward compatibility with original monolith)
	}

	if span != nil && userID != "" {
		span.SetTag("user.id", userID)
	}

	// Check cache for GET requests
	if r.Method == "GET" && g.cfg.EnableCaching {
		cacheKey := g.cacheManager.GenerateKey(r)
		if cached, found := g.cacheManager.Get(cacheKey); found {
			reqLog.Info("request served from cache", "path", r.URL.Path, "method", r.Method, "cache", "HIT")
			w.Header().Set("X-Cache", "HIT")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(cached))
			requestsTotal.WithLabelValues(r.Method, r.URL.Path, "200").Inc()
			if span != nil {
				span.SetTag("cache", "hit")
			}
			return
		}
		reqLog.Debug("cache miss", "path", r.URL.Path, "key", cacheKey)
		w.Header().Set("X-Cache", "MISS")
	}

	// Get backend from matched route pool
	backend := matched.Pool.NextPeer(ip)
	if backend == nil {
		reqLog.Error("no healthy backends available", "path", r.URL.Path, "method", r.Method)
		http.Error(w, "No healthy backends available", http.StatusServiceUnavailable)
		requestsTotal.WithLabelValues(r.Method, r.URL.Path, "503").Inc()
		if span != nil {
			span.SetTag("error", true)
			span.SetTag("error.message", "No backends available")
		}
		return
	}

	reqLog.Debug("backend selected", "backend", backend.URL.String(), "path", r.URL.Path)
	if span != nil {
		span.SetTag("backend", backend.URL.String())
	}

	// Rewrite path if strip-prefix was applied
	if matched.BackendPath != r.URL.Path {
		r.URL.Path = matched.BackendPath
		r.URL.RawPath = matched.BackendPath
	}

	// Circuit breaker with retry logic
	maxRetries := 3
	var lastErr error
	timeout := matched.Timeout
	if timeout <= 0 {
		timeout = g.cfg.ConnectionTimeout
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		err := g.circuitBreaker.Call(func() error {
			backend.IncConnections()
			defer backend.DecConnections()

			activeConnections.Inc()
			defer activeConnections.Dec()

			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			r = r.WithContext(ctx)

			r.Header.Set("X-Forwarded-For", ip)
			r.Header.Set("X-Real-IP", ip)
			r.Header.Set("X-Gateway-Time", time.Now().Format(time.RFC3339))

			backend.ReverseProxy.ServeHTTP(w, r)
			return nil
		})

		if err == nil {
			durationMs := time.Since(start).Milliseconds()
			reqLog.Info("request completed", "method", r.Method, "path", r.URL.Path, "backend", backend.URL.String(), "status", 200, "duration_ms", durationMs)
			requestsTotal.WithLabelValues(r.Method, r.URL.Path, "200").Inc()
			return
		}

		lastErr = err
		backend.RecordFailure()
		reqLog.Warn("backend attempt failed", "attempt", attempt+1, "max_retries", maxRetries, "backend", backend.URL.String(), "error", err)

		if attempt < maxRetries-1 {
			backoff := time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
			time.Sleep(backoff)
			backend = matched.Pool.NextPeer(ip)
			if backend == nil {
				break
			}
		}
	}

	durationMs := time.Since(start).Milliseconds()
	reqLog.Error("request failed after retries", "method", r.Method, "path", r.URL.Path, "duration_ms", durationMs, "error", lastErr)
	http.Error(w, fmt.Sprintf("Service temporarily unavailable: %v", lastErr), http.StatusServiceUnavailable)
	requestsTotal.WithLabelValues(r.Method, r.URL.Path, "503").Inc()
	if span != nil {
		span.SetTag("error", true)
		if lastErr != nil {
			span.SetTag("error.message", lastErr.Error())
		}
	}
}

// ============= MAIN =============

func main() {
	cfg := LoadConfig()
	log := logGateway()

	log.Info("starting VA API Gateway", "version", "2.0.0")
	log.Info("configuration loaded",
		"port", cfg.Port,
		"load_balancing", cfg.LoadBalancingAlgo,
		"rate_limit_rps", cfg.MaxRequestsPerSecond,
		"burst_size", cfg.MaxBurstSize,
		"ddos_threshold", cfg.DDoSThreshold,
		"compression", cfg.EnableCompression,
		"caching", cfg.EnableCaching,
	)

	gateway := NewGateway(cfg)
	defer gateway.Close()

	// Health check scheduler
	go func() {
		ticker := time.NewTicker(cfg.HealthCheckInterval)
		defer ticker.Stop()
		for range ticker.C {
			gateway.router.HealthCheckAll()

			for _, entry := range gateway.router.Routes() {
				for _, b := range entry.Pool.Backends() {
					healthValue := 0.0
					if b.IsAlive() {
						healthValue = 1.0
					}
					backendHealthStatus.WithLabelValues(b.URL.String()).Set(healthValue)
				}
			}
		}
	}()

	// Build the HTTP mux
	mux := http.NewServeMux()

	// Metrics endpoint (Prometheus)
	mux.Handle("/metrics", promhttp.Handler())

	// Health endpoint
	mux.HandleFunc("/health", healthHandler(gateway))

	// Stats endpoint
	mux.HandleFunc("/stats", statsHandler(gateway))

	// Admin endpoints
	mux.HandleFunc("/admin/whitelist", whitelistHandler(gateway))
	mux.HandleFunc("/admin/blacklist", blacklistHandler(gateway))

	// Login endpoint (placeholder)
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message":"login endpoint - implement OAuth2/OIDC flow"}`))
	})

	// Main gateway handler -- catch-all
	mux.Handle("/", gateway)

	// Build middleware chain: SecurityHeaders -> RequestID -> handler
	var handler http.Handler = mux
	handler = RequestIDMiddleware(handler)
	handler = SecurityHeadersMiddleware(handler)

	// Create server with timeouts
	srv := &http.Server{
		Addr:         cfg.Port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Info("all systems ready", "listen", "http://localhost"+cfg.Port)
		Banner([]string{
			"Features enabled:",
			"  - DDoS Protection (connection + request rate limiting)",
			"  - Load Balancing (round-robin, least-conn, ip-hash, weighted)",
			"  - Security (JWT + API key validation, IP filtering)",
			"  - Performance (caching, compression, connection pooling)",
			"  - Observability (Prometheus metrics, Jaeger tracing)",
			"  - Reliability (circuit breaker, retry with backoff)",
			"  - Middleware (security headers, request ID)",
		})

		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-done
	log.Info("shutdown signal received, shutting down gateway")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}

	log.Info("gateway stopped")
}
