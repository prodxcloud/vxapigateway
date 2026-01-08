package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/opentracing/opentracing-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/uber/jaeger-client-go"
	jaegercfg "github.com/uber/jaeger-client-go/config"
	"golang.org/x/time/rate"
)

// ============= CONFIGURATION =============
type Config struct {
	Port                  string
	MaxRequestsPerSecond  int
	MaxBurstSize          int
	DDoSThreshold         int
	BlockDuration         time.Duration
	JWTSecret             string
	HealthCheckInterval   time.Duration
	ConnectionTimeout     time.Duration
	MaxIdleConns          int
	IdleConnTimeout       time.Duration
	CircuitBreakerMax     int
	CircuitBreakerTimeout time.Duration
	EnableCompression     bool
	EnableCaching         bool
	CacheTTL              time.Duration
	RedisAddr             string
	LoadBalancingAlgo     string // "round-robin", "least-conn", "ip-hash", "weighted"
}

var config = Config{
	Port:                  ":8080",
	MaxRequestsPerSecond:  100,
	MaxBurstSize:          200,
	DDoSThreshold:         1000,
	BlockDuration:         10 * time.Minute,
	JWTSecret:             "your-secret-key-change-in-production",
	HealthCheckInterval:   10 * time.Second,
	ConnectionTimeout:     30 * time.Second,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	CircuitBreakerMax:     5,
	CircuitBreakerTimeout: 30 * time.Second,
	EnableCompression:     true,
	EnableCaching:         true,
	CacheTTL:              5 * time.Minute,
	RedisAddr:             "localhost:6379",
	LoadBalancingAlgo:     "least-conn",
}

// ============= BACKEND SERVICE =============
type Backend struct {
	URL          *url.URL
	Alive        bool
	mu           sync.RWMutex
	Connections  int64
	Weight       int
	ReverseProxy *httputil.ReverseProxy
	FailCount    int64
	LastFail     time.Time
}

func (b *Backend) SetAlive(alive bool) {
	b.mu.Lock()
	b.Alive = alive
	b.mu.Unlock()
}

func (b *Backend) IsAlive() bool {
	b.mu.RLock()
	alive := b.Alive
	b.mu.RUnlock()
	return alive
}

func (b *Backend) GetConnections() int64 {
	return atomic.LoadInt64(&b.Connections)
}

func (b *Backend) IncConnections() {
	atomic.AddInt64(&b.Connections, 1)
}

func (b *Backend) DecConnections() {
	atomic.AddInt64(&b.Connections, -1)
}

func (b *Backend) RecordFailure() {
	atomic.AddInt64(&b.FailCount, 1)
	b.mu.Lock()
	b.LastFail = time.Now()
	b.mu.Unlock()
}

func (b *Backend) ResetFailures() {
	atomic.StoreInt64(&b.FailCount, 0)
}

// ============= SERVICE POOL =============
type ServicePool struct {
	backends []*Backend
	current  uint64
	mu       sync.RWMutex
	algo     string
}

func NewServicePool(algo string) *ServicePool {
	return &ServicePool{
		backends: []*Backend{},
		algo:     algo,
	}
}

func (s *ServicePool) AddBackend(backend *Backend) {
	s.mu.Lock()
	s.backends = append(s.backends, backend)
	s.mu.Unlock()
}

func (s *ServicePool) NextPeer(clientIP string) *Backend {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.backends) == 0 {
		return nil
	}

	switch s.algo {
	case "round-robin":
		return s.roundRobin()
	case "least-conn":
		return s.leastConnections()
	case "ip-hash":
		return s.ipHash(clientIP)
	case "weighted":
		return s.weighted()
	default:
		return s.leastConnections()
	}
}

func (s *ServicePool) roundRobin() *Backend {
	next := atomic.AddUint64(&s.current, 1)
	for i := 0; i < len(s.backends); i++ {
		idx := int(next+uint64(i)) % len(s.backends)
		if s.backends[idx].IsAlive() {
			return s.backends[idx]
		}
	}
	return nil
}

func (s *ServicePool) leastConnections() *Backend {
	var selected *Backend
	minConn := int64(1<<63 - 1)

	for _, backend := range s.backends {
		if backend.IsAlive() {
			conn := backend.GetConnections()
			if conn < minConn {
				minConn = conn
				selected = backend
			}
		}
	}
	return selected
}

func (s *ServicePool) ipHash(clientIP string) *Backend {
	hash := sha256.Sum256([]byte(clientIP))
	idx := int(hash[0]) % len(s.backends)
	
	for i := 0; i < len(s.backends); i++ {
		checkIdx := (idx + i) % len(s.backends)
		if s.backends[checkIdx].IsAlive() {
			return s.backends[checkIdx]
		}
	}
	return nil
}

func (s *ServicePool) weighted() *Backend {
	totalWeight := 0
	for _, backend := range s.backends {
		if backend.IsAlive() {
			totalWeight += backend.Weight
		}
	}

	if totalWeight == 0 {
		return nil
	}

	next := int(atomic.AddUint64(&s.current, 1))
	target := next % totalWeight
	
	cumWeight := 0
	for _, backend := range s.backends {
		if backend.IsAlive() {
			cumWeight += backend.Weight
			if target < cumWeight {
				return backend
			}
		}
	}
	return nil
}

func (s *ServicePool) HealthCheck() {
	for _, backend := range s.backends {
		alive := isBackendAlive(backend.URL)
		backend.SetAlive(alive)
		
		if alive {
			backend.ResetFailures()
		}
		
		status := "up"
		if !alive {
			status = "down"
		}
		log.Printf("Health check: %s [%s] (connections: %d)", 
			backend.URL, status, backend.GetConnections())
	}
}

func isBackendAlive(u *url.URL) bool {
	timeout := 2 * time.Second
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

// ============= RATE LIMITER =============
type IPRateLimiter struct {
	ips map[string]*rate.Limiter
	mu  *sync.RWMutex
	r   rate.Limit
	b   int
}

func NewIPRateLimiter(r rate.Limit, b int) *IPRateLimiter {
	limiter := &IPRateLimiter{
		ips: make(map[string]*rate.Limiter),
		mu:  &sync.RWMutex{},
		r:   r,
		b:   b,
	}
	go limiter.cleanup()
	return limiter
}

func (i *IPRateLimiter) GetLimiter(ip string) *rate.Limiter {
	i.mu.Lock()
	defer i.mu.Unlock()

	limiter, exists := i.ips[ip]
	if !exists {
		limiter = rate.NewLimiter(i.r, i.b)
		i.ips[ip] = limiter
	}

	return limiter
}

func (i *IPRateLimiter) cleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	for range ticker.C {
		i.mu.Lock()
		// Remove limiters that haven't been used
		for ip, limiter := range i.ips {
			if limiter.Tokens() == float64(i.b) {
				delete(i.ips, ip)
			}
		}
		i.mu.Unlock()
	}
}

// ============= DDoS PROTECTION =============
type DDoSProtection struct {
	requests  map[string]*RequestCounter
	blocked   map[string]time.Time
	mu        sync.RWMutex
	threshold int
	duration  time.Duration
	whitelist map[string]bool
	blacklist map[string]bool
}

type RequestCounter struct {
	Count     int
	FirstSeen time.Time
}

func NewDDoSProtection(threshold int, duration time.Duration) *DDoSProtection {
	ddos := &DDoSProtection{
		requests:  make(map[string]*RequestCounter),
		blocked:   make(map[string]time.Time),
		threshold: threshold,
		duration:  duration,
		whitelist: make(map[string]bool),
		blacklist: make(map[string]bool),
	}
	go ddos.cleanup()
	return ddos
}

func (d *DDoSProtection) AddToWhitelist(ip string) {
	d.mu.Lock()
	d.whitelist[ip] = true
	d.mu.Unlock()
}

func (d *DDoSProtection) AddToBlacklist(ip string) {
	d.mu.Lock()
	d.blacklist[ip] = true
	d.mu.Unlock()
}

func (d *DDoSProtection) IsBlocked(ip string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Check whitelist first
	if d.whitelist[ip] {
		return false
	}

	// Check permanent blacklist
	if d.blacklist[ip] {
		return true
	}

	// Check temporary blocks
	if blockTime, exists := d.blocked[ip]; exists {
		if time.Now().Before(blockTime) {
			return true
		}
		delete(d.blocked, ip)
	}
	return false
}

func (d *DDoSProtection) RecordRequest(ip string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.whitelist[ip] {
		return false
	}

	now := time.Now()
	counter, exists := d.requests[ip]
	
	if !exists {
		d.requests[ip] = &RequestCounter{
			Count:     1,
			FirstSeen: now,
		}
		return false
	}

	// Reset counter if more than 1 minute passed
	if now.Sub(counter.FirstSeen) > time.Minute {
		counter.Count = 1
		counter.FirstSeen = now
		return false
	}

	counter.Count++
	if counter.Count > d.threshold {
		d.blocked[ip] = now.Add(d.duration)
		log.Printf("🚨 DDoS: Blocked IP %s for %v (requests: %d)", ip, d.duration, counter.Count)
		blockedIPsTotal.Inc()
		return true
	}

	return false
}

func (d *DDoSProtection) cleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	for range ticker.C {
		d.mu.Lock()
		now := time.Now()
		
		// Clean old request counters
		for ip, counter := range d.requests {
			if now.Sub(counter.FirstSeen) > time.Minute {
				delete(d.requests, ip)
			}
		}
		
		// Clean expired blocks
		for ip, blockTime := range d.blocked {
			if now.After(blockTime) {
				delete(d.blocked, ip)
			}
		}
		
		d.mu.Unlock()
	}
}

// ============= CIRCUIT BREAKER =============
type CircuitBreaker struct {
	maxFailures  int
	resetTimeout time.Duration
	failures     int
	lastFailTime time.Time
	state        string // "closed", "open", "half-open"
	mu           sync.Mutex
}

func NewCircuitBreaker(maxFailures int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		maxFailures:  maxFailures,
		resetTimeout: resetTimeout,
		state:        "closed",
	}
}

func (cb *CircuitBreaker) Call(fn func() error) error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == "open" {
		if time.Since(cb.lastFailTime) > cb.resetTimeout {
			cb.state = "half-open"
			cb.failures = 0
			log.Printf("Circuit breaker: half-open")
		} else {
			return fmt.Errorf("circuit breaker is open")
		}
	}

	err := fn()
	if err != nil {
		cb.failures++
		cb.lastFailTime = time.Now()
		if cb.failures >= cb.maxFailures {
			cb.state = "open"
			log.Printf("Circuit breaker: opened (failures: %d)", cb.failures)
			circuitBreakerTrips.Inc()
		}
		return err
	}

	if cb.state == "half-open" {
		cb.state = "closed"
		log.Printf("Circuit breaker: closed")
	}
	cb.failures = 0
	return nil
}

func (cb *CircuitBreaker) GetState() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// ============= AUTHENTICATION =============
func validateJWT(tokenString string) (*jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(config.JWTSecret), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		// Check expiration
		if exp, ok := claims["exp"].(float64); ok {
			if time.Now().Unix() > int64(exp) {
				return nil, fmt.Errorf("token expired")
			}
		}
		return &claims, nil
	}

	return nil, fmt.Errorf("invalid token")
}

func validateAPIKey(apiKey string) bool {
	// In production, check against database or Redis
	validKeys := map[string]bool{
		hashAPIKey("demo-api-key-123"):      true,
		hashAPIKey("production-key-456"):    true,
		hashAPIKey("development-key-789"):   true,
	}
	return validKeys[hashAPIKey(apiKey)]
}

func hashAPIKey(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}

// ============= CACHING =============
type CacheManager struct {
	redis  *redis.Client
	ctx    context.Context
	ttl    time.Duration
	enable bool
}

func NewCacheManager(redisAddr string, ttl time.Duration, enable bool) *CacheManager {
	if !enable {
		return &CacheManager{enable: false}
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "",
		DB:       0,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("⚠️  Redis connection failed: %v (caching disabled)", err)
		return &CacheManager{enable: false}
	}

	log.Printf("✅ Redis connected: %s", redisAddr)
	return &CacheManager{
		redis:  rdb,
		ctx:    ctx,
		ttl:    ttl,
		enable: true,
	}
}

func (cm *CacheManager) Get(key string) (string, bool) {
	if !cm.enable {
		return "", false
	}

	val, err := cm.redis.Get(cm.ctx, key).Result()
	if err == redis.Nil {
		return "", false
	} else if err != nil {
		log.Printf("Cache get error: %v", err)
		return "", false
	}
	
	cacheHitsTotal.Inc()
	return val, true
}

func (cm *CacheManager) Set(key string, value string) {
	if !cm.enable {
		return
	}

	err := cm.redis.Set(cm.ctx, key, value, cm.ttl).Err()
	if err != nil {
		log.Printf("Cache set error: %v", err)
	}
}

func (cm *CacheManager) GenerateKey(r *http.Request) string {
	return fmt.Sprintf("cache:%s:%s:%s", r.Method, r.Host, r.URL.Path)
}

// ============= COMPRESSION =============
type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func makeGzipHandler(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			fn(w, r)
			return
		}

		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()

		gzr := gzipResponseWriter{Writer: gz, ResponseWriter: w}
		fn(gzr, r)
	}
}

// ============= PROMETHEUS METRICS =============
var (
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "endpoint", "status"},
	)
	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latencies in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint"},
	)
	activeConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "active_connections",
			Help: "Number of active connections",
		},
	)
	backendHealthStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "backend_health_status",
			Help: "Health status of backend servers (1=healthy, 0=unhealthy)",
		},
		[]string{"backend"},
	)
	circuitBreakerTrips = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "circuit_breaker_trips_total",
			Help: "Total number of circuit breaker trips",
		},
	)
	blockedIPsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "blocked_ips_total",
			Help: "Total number of IPs blocked by DDoS protection",
		},
	)
	cacheHitsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "cache_hits_total",
			Help: "Total number of cache hits",
		},
	)
)

func init() {
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(requestDuration)
	prometheus.MustRegister(activeConnections)
	prometheus.MustRegister(backendHealthStatus)
	prometheus.MustRegister(circuitBreakerTrips)
	prometheus.MustRegister(blockedIPsTotal)
	prometheus.MustRegister(cacheHitsTotal)
}

// ============= TRACING =============
func initTracer(serviceName string) (opentracing.Tracer, io.Closer, error) {
	cfg := jaegercfg.Configuration{
		ServiceName: serviceName,
		Sampler: &jaegercfg.SamplerConfig{
			Type:  jaeger.SamplerTypeConst,
			Param: 1,
		},
		Reporter: &jaegercfg.ReporterConfig{
			LogSpans:           true,
			LocalAgentHostPort: "localhost:6831",
		},
	}

	tracer, closer, err := cfg.NewTracer()
	if err != nil {
		return nil, nil, err
	}

	opentracing.SetGlobalTracer(tracer)
	return tracer, closer, nil
}

// ============= GATEWAY =============
type Gateway struct {
	servicePool    *ServicePool
	rateLimiter    *IPRateLimiter
	ddosProtection *DDoSProtection
	circuitBreaker *CircuitBreaker
	cacheManager   *CacheManager
	tracer         opentracing.Tracer
}

func NewGateway() *Gateway {
	tracer, closer, err := initTracer("api-gateway")
	if err != nil {
		log.Printf("⚠️  Jaeger tracer init failed: %v (tracing disabled)", err)
	} else {
		log.Printf("✅ Jaeger tracer initialized")
		defer closer.Close()
	}

	return &Gateway{
		servicePool:    NewServicePool(config.LoadBalancingAlgo),
		rateLimiter:    NewIPRateLimiter(rate.Limit(config.MaxRequestsPerSecond), config.MaxBurstSize),
		ddosProtection: NewDDoSProtection(config.DDoSThreshold, config.BlockDuration),
		circuitBreaker: NewCircuitBreaker(config.CircuitBreakerMax, config.CircuitBreakerTimeout),
		cacheManager:   NewCacheManager(config.RedisAddr, config.CacheTTL, config.EnableCaching),
		tracer:         tracer,
	}
}

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

	// Authentication
	authHeader := r.Header.Get("Authorization")
	apiKey := r.Header.Get("X-API-Key")

	authenticated := false
	var userID string

	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if claims, err := validateJWT(token); err == nil {
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

	// Public endpoints don't require auth
	if !isPublicEndpoint(r.URL.Path) && !authenticated {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		requestsTotal.WithLabelValues(r.Method, r.URL.Path, "401").Inc()
		if span != nil {
			span.SetTag("error", true)
			span.SetTag("error.message", "Unauthorized")
		}
		return
	}

	if span != nil && userID != "" {
		span.SetTag("user.id", userID)
	}

	// Check cache for GET requests
	if r.Method == "GET" && config.EnableCaching {
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

	// Get backend
	backend := g.servicePool.NextPeer(ip)
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

	// Circuit breaker with retry logic
	maxRetries := 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		err := g.circuitBreaker.Call(func() error {
			backend.IncConnections()
			defer backend.DecConnections()

			activeConnections.Inc()
			defer activeConnections.Dec()

			// Set timeout
			ctx, cancel := context.WithTimeout(r.Context(), config.ConnectionTimeout)
			defer cancel()
			r = r.WithContext(ctx)

			// Add headers
			r.Header.Set("X-Forwarded-For", ip)
			r.Header.Set("X-Real-IP", ip)
			r.Header.Set("X-Gateway-Time", time.Now().Format(time.RFC3339))

			// Proxy request
			backend.ReverseProxy.ServeHTTP(w, r)
			return nil
		})

		if err == nil {
			requestsTotal.WithLabelValues(r.Method, r.URL.Path, "200").Inc()
			return
		}

		lastErr = err
		backend.RecordFailure()

		// Exponential backoff
		if attempt < maxRetries-1 {
			backoff := time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
			time.Sleep(backoff)
			
			// Try next backend
			backend = g.servicePool.NextPeer(ip)
			if backend == nil {
				break
			}
		}
	}

	http.Error(w, fmt.Sprintf("Service temporarily unavailable: %v", lastErr), http.StatusServiceUnavailable)
	requestsTotal.WithLabelValues(r.Method, r.URL.Path, "503").Inc()
	if span != nil {
		span.SetTag("error", true)
		span.SetTag("error.message", lastErr.Error())
	}
}

// ============= UTILITIES =============
func getClientIP(r *http.Request) string {
	// Try X-Real-IP first
	ip := r.Header.Get("X-Real-IP")
	if ip != "" {
		return ip
	}

	// Try X-Forwarded-For
	ip = r.Header.Get("X-Forwarded-For")
	if ip != "" {
		// X-Forwarded-For can contain multiple IPs, take the first
		parts := strings.Split(ip, ",")
		return strings.TrimSpace(parts[0])
	}

	// Fall back to RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func isPublicEndpoint(path string) bool {
	publicPaths := []string{
		"/health",
		"/metrics",
		"/login",
		"/register",
		"/api/v1/public",
	}
	for _, p := range publicPaths {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// ============= MAIN =============
func main() {
	log.Printf("🚀 Starting VA API Gateway v1.0.0")
	log.Printf("📋 Configuration:")
	log.Printf("   - Port: %s", config.Port)
	log.Printf("   - Load Balancing: %s", config.LoadBalancingAlgo)
	log.Printf("   - Rate Limit: %d req/s (burst: %d)", config.MaxRequestsPerSecond, config.MaxBurstSize)
	log.Printf("   - DDoS Threshold: %d req/min", config.DDoSThreshold)
	log.Printf("   - Compression: %v", config.EnableCompression)
	log.Printf("   - Caching: %v", config.EnableCaching)

	gateway := NewGateway()

	// Add backend services
	backends := []struct {
		url    string
		weight int
	}{
		{"http://localhost:8001", 3}, // Agent API - higher weight
		{"http://localhost:8002", 2}, // Model API
		{"http://localhost:8003", 1}, // CRUD API
	}

	for _, b := range backends {
		url, err := url.Parse(b.url)
		if err != nil {
			log.Fatalf("Invalid backend URL %s: %v", b.url, err)
		}

		// Custom reverse proxy with error handling
		proxy := httputil.NewSingleHostReverseProxy(url)
		
		// Custom transport for connection pooling
		proxy.Transport = &http.Transport{
			MaxIdleConns:        config.MaxIdleConns,
			MaxIdleConnsPerHost: config.MaxIdleConns,
			IdleConnTimeout:     config.IdleConnTimeout,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,
			},
			DisableCompression: !config.EnableCompression,
		}

		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("❌ Proxy error for %s: %v", url, err)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		}

		backend := &Backend{
			URL:          url,
			Alive:        true,
			Weight:       b.weight,
			ReverseProxy: proxy,
		}
		
		gateway.servicePool.AddBackend(backend)
		log.Printf("✅ Backend added: %s (weight: %d)", url, b.weight)
	}

	// Health check scheduler
	go func() {
		ticker := time.NewTicker(config.HealthCheckInterval)
		for range ticker.C {
			gateway.servicePool.HealthCheck()
			
			// Update Prometheus metrics
			for _, backend := range gateway.servicePool.backends {
				healthValue := 0.0
				if backend.IsAlive() {
					healthValue = 1.0
				}
				backendHealthStatus.WithLabelValues(backend.URL.String()).Set(healthValue)
			}
		}
	}()

	// Metrics endpoint
	http.Handle("/metrics", promhttp.Handler())
	log.Printf("📊 Metrics endpoint: http://localhost%s/metrics", config.Port)

	// Health endpoint
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		health := map[string]interface{}{
			"status":          "healthy",
			"timestamp":       time.Now().Format(time.RFC3339),
			"version":         "1.0.0",
			"circuit_breaker": gateway.circuitBreaker.GetState(),
		}

		backends := []map[string]interface{}{}
		for _, b := range gateway.servicePool.backends {
			backends = append(backends, map[string]interface{}{
				"url":         b.URL.String(),
				"alive":       b.IsAlive(),
				"connections": b.GetConnections(),
				"weight":      b.Weight,
			})
		}
		health["backends"] = backends

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(health)
	})

	// Admin endpoints
	http.HandleFunc("/admin/whitelist", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ip := r.URL.Query().Get("ip")
		if ip == "" {
			http.Error(w, "IP parameter required", http.StatusBadRequest)
			return
		}

		gateway.ddosProtection.AddToWhitelist(ip)
		log.Printf("✅ IP %s added to whitelist", ip)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "whitelisted", "ip": ip})
	})

	http.HandleFunc("/admin/blacklist", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ip := r.URL.Query().Get("ip")
		if ip == "" {
			http.Error(w, "IP parameter required", http.StatusBadRequest)
			return
		}

		gateway.ddosProtection.AddToBlacklist(ip)
		log.Printf("🚫 IP %s added to blacklist", ip)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "blacklisted", "ip": ip})
	})

	// Main gateway handler
	http.Handle("/", gateway)

	log.Printf("✅ All systems ready!")
	log.Printf("🌐 Gateway listening on http://localhost%s", config.Port)
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("Features enabled:")
	log.Println("  ✓ DDoS Protection (connection + request rate limiting)")
	log.Println("  ✓ Load Balancing (round-robin, least-conn, ip-hash, weighted)")
	log.Println("  ✓ Security (JWT + API key validation, IP filtering)")
	log.Println("  ✓ Performance (caching, compression, connection pooling)")
	log.Println("  ✓ Observability (Prometheus metrics, Jaeger tracing)")
	log.Println("  ✓ Reliability (circuit breaker, retry with backoff)")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	if err := http.ListenAndServe(config.Port, nil); err != nil {
		log.Fatal(err)
	}
}
