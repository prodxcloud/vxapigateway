package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"
)

// ============= IP RATE LIMITER =============

// IPRateLimiter maintains a per-IP token-bucket rate limiter.
type IPRateLimiter struct {
	ips map[string]*rate.Limiter
	mu  *sync.RWMutex
	r   rate.Limit
	b   int
}

// NewIPRateLimiter creates a new per-IP rate limiter with the given rate and
// burst size, and starts a background goroutine that periodically evicts idle
// entries.
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

// GetLimiter returns the rate.Limiter for the given IP, creating one if it
// does not yet exist.
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
		for ip, limiter := range i.ips {
			if limiter.Tokens() == float64(i.b) {
				delete(i.ips, ip)
			}
		}
		i.mu.Unlock()
	}
}

// ============= DDoS PROTECTION =============

// RequestCounter tracks the number of requests from a single IP within a
// sliding window.
type RequestCounter struct {
	Count     int
	FirstSeen time.Time
}

// DDoSProtection provides connection-rate based DDoS detection and IP
// blocking, with support for permanent whitelists and blacklists.
type DDoSProtection struct {
	requests  map[string]*RequestCounter
	blocked   map[string]time.Time
	mu        sync.RWMutex
	threshold int
	duration  time.Duration
	whitelist map[string]bool
	blacklist map[string]bool
}

// NewDDoSProtection creates a new DDoS protection instance.  threshold is the
// maximum number of requests per minute before an IP is blocked for duration.
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

// AddToWhitelist permanently exempts an IP from DDoS blocking.
func (d *DDoSProtection) AddToWhitelist(ip string) {
	d.mu.Lock()
	d.whitelist[ip] = true
	d.mu.Unlock()
}

// AddToBlacklist permanently blocks an IP.
func (d *DDoSProtection) AddToBlacklist(ip string) {
	d.mu.Lock()
	d.blacklist[ip] = true
	d.mu.Unlock()
}

// IsBlocked returns true when the given IP should be denied service.
func (d *DDoSProtection) IsBlocked(ip string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.whitelist[ip] {
		return false
	}

	if d.blacklist[ip] {
		return true
	}

	if blockTime, exists := d.blocked[ip]; exists {
		if time.Now().Before(blockTime) {
			return true
		}
		delete(d.blocked, ip)
	}
	return false
}

// RecordRequest increments the request counter for the given IP and returns
// true if the IP was just blocked (threshold exceeded).
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
		logDDoS().Warn("IP blocked by DDoS protection", "ip", ip, "block_duration", d.duration, "request_count", counter.Count, "threshold", d.threshold)
		blockedIPsTotal.Inc()
		atomic.AddInt64(&dashStats.BlockedIPs, 1)
		return true
	}

	return false
}

func (d *DDoSProtection) cleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	for range ticker.C {
		d.mu.Lock()
		now := time.Now()

		for ip, counter := range d.requests {
			if now.Sub(counter.FirstSeen) > time.Minute {
				delete(d.requests, ip)
			}
		}

		for ip, blockTime := range d.blocked {
			if now.After(blockTime) {
				delete(d.blocked, ip)
			}
		}

		d.mu.Unlock()
	}
}

// ============= CIRCUIT BREAKER =============

// CircuitBreaker implements the classic closed/open/half-open pattern to
// prevent cascading failures when a backend is unresponsive.
type CircuitBreaker struct {
	maxFailures  int
	resetTimeout time.Duration
	failures     int
	lastFailTime time.Time
	state        string // "closed", "open", "half-open"
	mu           sync.Mutex
}

// NewCircuitBreaker creates a breaker that opens after maxFailures consecutive
// errors and transitions to half-open after resetTimeout.
func NewCircuitBreaker(maxFailures int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		maxFailures:  maxFailures,
		resetTimeout: resetTimeout,
		state:        "closed",
	}
}

// Call executes fn inside the circuit breaker.  It returns an error
// immediately when the breaker is open (and the reset timeout has not elapsed).
func (cb *CircuitBreaker) Call(fn func() error) error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == "open" {
		if time.Since(cb.lastFailTime) > cb.resetTimeout {
			cb.state = "half-open"
			cb.failures = 0
			logCircuit().Info("circuit breaker transition", "state", "half-open")
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
			logCircuit().Warn("circuit breaker opened", "failures", cb.failures, "max_failures", cb.maxFailures)
			circuitBreakerTrips.Inc()
			atomic.AddInt64(&dashStats.CircuitTrips, 1)
		}
		return err
	}

	if cb.state == "half-open" {
		cb.state = "closed"
		logCircuit().Info("circuit breaker transition", "state", "closed")
	}
	cb.failures = 0
	return nil
}

// GetState returns the current breaker state ("closed", "open", or
// "half-open").
func (cb *CircuitBreaker) GetState() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// ============= AUTHENTICATION =============

// validateJWT parses and validates a JWT token string using the configured
// HMAC secret.  It returns the claims on success.
func validateJWT(tokenString string, cfg Config) (*jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(cfg.JWTSecret), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		if exp, ok := claims["exp"].(float64); ok {
			if time.Now().Unix() > int64(exp) {
				return nil, fmt.Errorf("token expired")
			}
		}
		return &claims, nil
	}

	return nil, fmt.Errorf("invalid token")
}

// validateAPIKey returns true when the given key matches one of the known
// demonstration keys (compared via SHA-256 hash).
func validateAPIKey(apiKey string) bool {
	validKeys := map[string]bool{
		hashAPIKey("demo-api-key-123"):    true,
		hashAPIKey("production-key-456"):  true,
		hashAPIKey("development-key-789"): true,
	}
	return validKeys[hashAPIKey(apiKey)]
}

// hashAPIKey returns the lowercase hex SHA-256 digest of key.
func hashAPIKey(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}

// ============= SECURITY HEADERS MIDDLEWARE =============

// SecurityHeadersMiddleware adds standard security headers to every response:
// X-Content-Type-Options, X-Frame-Options, Strict-Transport-Security, and
// Content-Security-Policy.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// ============= REQUEST ID MIDDLEWARE =============

// generateRequestID produces a hex-encoded random string suitable for use as a
// unique request identifier.  It uses crypto/rand for uniqueness without
// requiring an external UUID library.
func generateRequestID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// RequestIDMiddleware generates a unique request ID for every incoming request
// and attaches it as both a request header and a response header
// (x-request-id).  If the client already provides an x-request-id header it
// is preserved.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("x-request-id")
		if requestID == "" {
			requestID = generateRequestID()
			r.Header.Set("x-request-id", requestID)
		}
		w.Header().Set("x-request-id", requestID)
		next.ServeHTTP(w, r)
	})
}

// ============= UTILITIES =============

// getClientIP extracts the real client IP from the request, checking
// X-Real-IP, then X-Forwarded-For, and falling back to RemoteAddr.
func getClientIP(r *http.Request) string {
	ip := r.Header.Get("X-Real-IP")
	if ip != "" {
		return ip
	}

	ip = r.Header.Get("X-Forwarded-For")
	if ip != "" {
		parts := strings.Split(ip, ",")
		return strings.TrimSpace(parts[0])
	}

	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// isPublicEndpoint returns true for paths that do not require authentication.
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
