package main

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Prometheus metric collectors used throughout the gateway.
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
