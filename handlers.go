package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/opentracing/opentracing-go"
	"github.com/redis/go-redis/v9"
	"github.com/uber/jaeger-client-go"
	jaegercfg "github.com/uber/jaeger-client-go/config"
)

// ============= CACHING =============

// CacheManager provides optional Redis-backed response caching.
type CacheManager struct {
	redis  *redis.Client
	ctx    context.Context
	ttl    time.Duration
	enable bool
}

// NewCacheManager connects to Redis at redisAddr.  If the connection fails or
// enable is false, caching is silently disabled.
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
		log.Printf("WARNING: Redis connection failed: %v (caching disabled)", err)
		return &CacheManager{enable: false}
	}

	log.Printf("Redis connected: %s", redisAddr)
	return &CacheManager{
		redis:  rdb,
		ctx:    ctx,
		ttl:    ttl,
		enable: true,
	}
}

// Get retrieves a cached value by key.  The second return value indicates
// whether the key was found.
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

// Set stores a value in the cache with the configured TTL.
func (cm *CacheManager) Set(key string, value string) {
	if !cm.enable {
		return
	}

	err := cm.redis.Set(cm.ctx, key, value, cm.ttl).Err()
	if err != nil {
		log.Printf("Cache set error: %v", err)
	}
}

// GenerateKey builds a cache key from the request method, host, and path.
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

// makeGzipHandler wraps an http.HandlerFunc to transparently gzip-compress the
// response when the client advertises gzip support.
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

// ============= TRACING =============

// initTracer initialises a Jaeger tracer and sets it as the global
// OpenTracing tracer.
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

// ============= HEALTH HANDLER =============

// healthHandler returns a JSON response with gateway status, backend health,
// and registered routes information.
func healthHandler(gw *Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		health := map[string]interface{}{
			"status":          "healthy",
			"timestamp":       time.Now().Format(time.RFC3339),
			"version":         "2.0.0",
			"circuit_breaker": gw.circuitBreaker.GetState(),
		}

		// Collect backend info from all route pools
		type backendInfo struct {
			URL         string `json:"url"`
			Alive       bool   `json:"alive"`
			Connections int64  `json:"connections"`
			Weight      int    `json:"weight"`
		}

		type routeInfo struct {
			Prefix      string        `json:"prefix"`
			RequireAuth bool          `json:"require_auth"`
			StripPrefix bool          `json:"strip_prefix"`
			Backends    []backendInfo `json:"backends"`
		}

		var routeInfos []routeInfo
		for _, entry := range gw.router.Routes() {
			ri := routeInfo{
				Prefix:      entry.Prefix,
				RequireAuth: entry.RequireAuth,
				StripPrefix: entry.StripPrefix,
			}
			for _, b := range entry.Pool.Backends() {
				ri.Backends = append(ri.Backends, backendInfo{
					URL:         b.URL.String(),
					Alive:       b.IsAlive(),
					Connections: b.GetConnections(),
					Weight:      b.Weight,
				})
			}
			routeInfos = append(routeInfos, ri)
		}
		health["routes"] = routeInfos

		// Legacy backends list for backward compatibility
		var allBackends []map[string]interface{}
		for _, entry := range gw.router.Routes() {
			for _, b := range entry.Pool.Backends() {
				allBackends = append(allBackends, map[string]interface{}{
					"url":         b.URL.String(),
					"alive":       b.IsAlive(),
					"connections": b.GetConnections(),
					"weight":      b.Weight,
				})
			}
		}
		health["backends"] = allBackends

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(health)
	}
}

// ============= ADMIN HANDLERS =============

// whitelistHandler handles POST /admin/whitelist?ip=<addr> to add an IP to
// the DDoS protection whitelist.
func whitelistHandler(gw *Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ip := r.URL.Query().Get("ip")
		if ip == "" {
			http.Error(w, "IP parameter required", http.StatusBadRequest)
			return
		}

		gw.ddosProtection.AddToWhitelist(ip)
		log.Printf("IP %s added to whitelist", ip)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "whitelisted", "ip": ip})
	}
}

// blacklistHandler handles POST /admin/blacklist?ip=<addr> to add an IP to
// the DDoS protection blacklist.
func blacklistHandler(gw *Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ip := r.URL.Query().Get("ip")
		if ip == "" {
			http.Error(w, "IP parameter required", http.StatusBadRequest)
			return
		}

		gw.ddosProtection.AddToBlacklist(ip)
		log.Printf("IP %s added to blacklist", ip)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "blacklisted", "ip": ip})
	}
}

// statsHandler returns runtime statistics for the gateway.
func statsHandler(gw *Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats := map[string]interface{}{
			"circuit_breaker_state": gw.circuitBreaker.GetState(),
			"load_balancing_algo":   gw.cfg.LoadBalancingAlgo,
			"caching_enabled":       gw.cfg.EnableCaching,
			"compression_enabled":   gw.cfg.EnableCompression,
			"routes_count":          len(gw.router.Routes()),
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(stats)
	}
}
