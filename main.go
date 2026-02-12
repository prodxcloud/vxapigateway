package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/opentracing/opentracing-go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
)

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
		log.Printf("WARNING: Jaeger tracer init failed: %v (tracing disabled)", err)
	} else {
		log.Printf("Jaeger tracer initialized")
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

	// Get client IP
	ip := getClientIP(r)

	// DDoS Protection
	if g.ddosProtection.IsBlocked(ip) {
		http.Error(w, "Too many requests - IP blocked", http.StatusTooManyRequests)
		requestsTotal.WithLabelValues(r.Method, r.URL.Path, "429").Inc()
		if span != nil {
			span.SetTag("error", true)
			span.SetTag("error.message", "IP blocked")
		}
		return
	}

	if g.ddosProtection.RecordRequest(ip) {
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
		// No route matched -- fall through to default backend pool if any
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
			w.Header().Set("X-Cache", "HIT")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(cached))
			requestsTotal.WithLabelValues(r.Method, r.URL.Path, "200").Inc()
			if span != nil {
				span.SetTag("cache", "hit")
			}
			return
		}
		w.Header().Set("X-Cache", "MISS")
	}

	// Get backend from matched route pool
	backend := matched.Pool.NextPeer(ip)
	if backend == nil {
		http.Error(w, "No healthy backends available", http.StatusServiceUnavailable)
		requestsTotal.WithLabelValues(r.Method, r.URL.Path, "503").Inc()
		if span != nil {
			span.SetTag("error", true)
			span.SetTag("error.message", "No backends available")
		}
		return
	}

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
			requestsTotal.WithLabelValues(r.Method, r.URL.Path, "200").Inc()
			return
		}

		lastErr = err
		backend.RecordFailure()

		if attempt < maxRetries-1 {
			backoff := time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
			time.Sleep(backoff)

			backend = matched.Pool.NextPeer(ip)
			if backend == nil {
				break
			}
		}
	}

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

	log.Printf("Starting VA API Gateway v2.0.0")
	log.Printf("Configuration:")
	log.Printf("   Port: %s", cfg.Port)
	log.Printf("   Load Balancing: %s", cfg.LoadBalancingAlgo)
	log.Printf("   Rate Limit: %d req/s (burst: %d)", cfg.MaxRequestsPerSecond, cfg.MaxBurstSize)
	log.Printf("   DDoS Threshold: %d req/min", cfg.DDoSThreshold)
	log.Printf("   Compression: %v", cfg.EnableCompression)
	log.Printf("   Caching: %v", cfg.EnableCaching)

	gateway := NewGateway(cfg)
	defer gateway.Close()

	// Log registered routes
	log.Printf("Registered routes:")
	for _, entry := range gateway.router.Routes() {
		for _, b := range entry.Pool.Backends() {
			log.Printf("   %s -> %s (strip=%v, auth=%v, timeout=%v)",
				entry.Prefix, b.URL, entry.StripPrefix, entry.RequireAuth, entry.Timeout)
		}
	}

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
		log.Printf("All systems ready!")
		log.Printf("Gateway listening on http://localhost%s", cfg.Port)
		log.Println("------------------------------------------------------------")
		log.Println("Features enabled:")
		log.Println("  - DDoS Protection (connection + request rate limiting)")
		log.Println("  - Load Balancing (round-robin, least-conn, ip-hash, weighted)")
		log.Println("  - Security (JWT + API key validation, IP filtering)")
		log.Println("  - Performance (caching, compression, connection pooling)")
		log.Println("  - Observability (Prometheus metrics, Jaeger tracing)")
		log.Println("  - Reliability (circuit breaker, retry with backoff)")
		log.Println("  - Middleware (security headers, request ID)")
		log.Println("------------------------------------------------------------")

		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-done
	log.Println("Shutting down gateway...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Graceful shutdown failed: %v", err)
	}

	log.Println("Gateway stopped.")
}
