package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"time"
)

// ServiceRoute defines a prefix-based route to a backend service pool.
type ServiceRoute struct {
	Prefix      string `json:"prefix"`
	TargetURL   string `json:"target_url"`
	StripPrefix bool   `json:"strip_prefix"`
	RequireAuth bool   `json:"require_auth"`
	Weight      int    `json:"weight"`
	TimeoutSecs int    `json:"timeout_secs"`
}

// Config holds all gateway configuration values, loaded from environment
// variables with sensible defaults.
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
	ServiceRoutes         []ServiceRoute
	CorsAllowedOrigins    []string
}

// envOr returns the value of the environment variable identified by key, or
// the provided fallback if the variable is unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envOrInt returns the integer value of the environment variable identified by
// key, or the provided fallback if the variable is unset, empty, or not a
// valid integer.
func envOrInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("WARNING: invalid integer for %s=%q, using default %d", key, v, fallback)
		return fallback
	}
	return n
}

// envOrBool returns the boolean value of the environment variable identified
// by key, or the provided fallback if the variable is unset, empty, or not a
// valid boolean.
func envOrBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Printf("WARNING: invalid boolean for %s=%q, using default %v", key, v, fallback)
		return fallback
	}
	return b
}

// envOrDuration returns the duration value of the environment variable
// identified by key (parsed via time.ParseDuration), or the provided fallback
// if the variable is unset, empty, or not a valid duration string.
func envOrDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("WARNING: invalid duration for %s=%q, using default %v", key, v, fallback)
		return fallback
	}
	return d
}

// loadServiceRoutes builds the list of ServiceRoute entries.  It first checks
// the SERVICE_ROUTES env var for a JSON array.  If that is not set it falls
// back to individual ROUTE_<NAME>_PREFIX / ROUTE_<NAME>_URL pairs, and
// finally to hardcoded defaults.
func loadServiceRoutes() []ServiceRoute {
	// 1. Try SERVICE_ROUTES JSON array
	if raw := os.Getenv("SERVICE_ROUTES"); raw != "" {
		var routes []ServiceRoute
		if err := json.Unmarshal([]byte(raw), &routes); err != nil {
			log.Printf("WARNING: failed to parse SERVICE_ROUTES JSON: %v, falling back to individual vars", err)
		} else if len(routes) > 0 {
			return routes
		}
	}

	// 2. Try individual ROUTE_* env vars
	type routeDef struct {
		envPrefix string
		envURL    string
		prefix    string
		url       string
	}

	defs := []routeDef{
		{"ROUTE_INFINITY_PREFIX", "ROUTE_INFINITY_URL", "/api/infinity", "http://localhost:8000"},
		{"ROUTE_LLM_PREFIX", "ROUTE_LLM_URL", "/api/llm", "http://localhost:8745"},
		{"ROUTE_STUDIO_PREFIX", "ROUTE_STUDIO_URL", "/api/studio", "http://localhost:8000"},
		{"ROUTE_ADMIN_PREFIX", "ROUTE_ADMIN_URL", "/api/admin", "http://localhost:8742"},
	}

	var routes []ServiceRoute
	anySet := false

	for _, d := range defs {
		prefix := envOr(d.envPrefix, d.prefix)
		targetURL := envOr(d.envURL, d.url)
		if os.Getenv(d.envPrefix) != "" || os.Getenv(d.envURL) != "" {
			anySet = true
		}
		routes = append(routes, ServiceRoute{
			Prefix:      prefix,
			TargetURL:   targetURL,
			StripPrefix: true,
			RequireAuth: false,
			Weight:      1,
			TimeoutSecs: 30,
		})
	}

	if anySet || len(routes) > 0 {
		return routes
	}

	// 3. Defaults (should not be reached since defs always produce routes)
	return routes
}

// loadCorsOrigins reads CORS_ALLOWED_ORIGINS as a JSON array, defaulting to
// ["*"] if unset.
func loadCorsOrigins() []string {
	if raw := os.Getenv("CORS_ALLOWED_ORIGINS"); raw != "" {
		var origins []string
		if err := json.Unmarshal([]byte(raw), &origins); err != nil {
			log.Printf("WARNING: failed to parse CORS_ALLOWED_ORIGINS: %v, using default [\"*\"]", err)
			return []string{"*"}
		}
		return origins
	}
	return []string{"*"}
}

// LoadConfig reads all configuration from environment variables and returns a
// fully populated Config struct.
func LoadConfig() Config {
	return Config{
		Port:                  envOr("GATEWAY_PORT", ":8080"),
		MaxRequestsPerSecond:  envOrInt("MAX_REQUESTS_PER_SECOND", 100),
		MaxBurstSize:          envOrInt("MAX_BURST_SIZE", 200),
		DDoSThreshold:         envOrInt("DDOS_THRESHOLD", 1000),
		BlockDuration:         envOrDuration("BLOCK_DURATION", 10*time.Minute),
		JWTSecret:             envOr("JWT_SECRET", "your-secret-key-change-in-production"),
		HealthCheckInterval:   envOrDuration("HEALTH_CHECK_INTERVAL", 10*time.Second),
		ConnectionTimeout:     envOrDuration("CONNECTION_TIMEOUT", 30*time.Second),
		MaxIdleConns:          envOrInt("MAX_IDLE_CONNS", 100),
		IdleConnTimeout:       envOrDuration("IDLE_CONN_TIMEOUT", 90*time.Second),
		CircuitBreakerMax:     envOrInt("CIRCUIT_BREAKER_MAX", 5),
		CircuitBreakerTimeout: envOrDuration("CIRCUIT_BREAKER_TIMEOUT", 30*time.Second),
		EnableCompression:     envOrBool("ENABLE_COMPRESSION", true),
		EnableCaching:         envOrBool("ENABLE_CACHING", true),
		CacheTTL:              envOrDuration("CACHE_TTL", 5*time.Minute),
		RedisAddr:             envOr("REDIS_ADDR", "localhost:6379"),
		LoadBalancingAlgo:     envOr("LOAD_BALANCING_ALGO", "least-conn"),
		ServiceRoutes:         loadServiceRoutes(),
		CorsAllowedOrigins:    loadCorsOrigins(),
	}
}
