package main

import (
	"crypto/sha256"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ============= BACKEND SERVICE =============

// Backend represents a single upstream server that can receive proxied
// requests.  It tracks liveness, connection count, weight, and failure
// statistics for use by load-balancing and circuit-breaker logic.
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

// SetAlive updates the health status of the backend.
func (b *Backend) SetAlive(alive bool) {
	b.mu.Lock()
	b.Alive = alive
	b.mu.Unlock()
}

// IsAlive returns the current health status of the backend.
func (b *Backend) IsAlive() bool {
	b.mu.RLock()
	alive := b.Alive
	b.mu.RUnlock()
	return alive
}

// GetConnections returns the current number of active connections.
func (b *Backend) GetConnections() int64 {
	return atomic.LoadInt64(&b.Connections)
}

// IncConnections atomically increments the active connection counter.
func (b *Backend) IncConnections() {
	atomic.AddInt64(&b.Connections, 1)
}

// DecConnections atomically decrements the active connection counter.
func (b *Backend) DecConnections() {
	atomic.AddInt64(&b.Connections, -1)
}

// RecordFailure increments the failure counter and records the time.
func (b *Backend) RecordFailure() {
	atomic.AddInt64(&b.FailCount, 1)
	b.mu.Lock()
	b.LastFail = time.Now()
	b.mu.Unlock()
}

// ResetFailures zeroes the failure counter.
func (b *Backend) ResetFailures() {
	atomic.StoreInt64(&b.FailCount, 0)
}

// ============= SERVICE POOL =============

// ServicePool manages a collection of Backend instances and selects among
// them using the configured load-balancing algorithm.
type ServicePool struct {
	backends []*Backend
	current  uint64
	mu       sync.RWMutex
	algo     string
}

// NewServicePool creates a new pool with the given algorithm name.
func NewServicePool(algo string) *ServicePool {
	return &ServicePool{
		backends: []*Backend{},
		algo:     algo,
	}
}

// AddBackend appends a backend to the pool.
func (s *ServicePool) AddBackend(backend *Backend) {
	s.mu.Lock()
	s.backends = append(s.backends, backend)
	s.mu.Unlock()
}

// Backends returns a snapshot of the current backend slice.
func (s *ServicePool) Backends() []*Backend {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Backend, len(s.backends))
	copy(out, s.backends)
	return out
}

// NextPeer selects the next healthy backend using the configured algorithm.
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

// HealthCheck pings every backend in the pool and updates its alive status.
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
		logRouter().Info("health check",
			"backend", backend.URL.String(),
			"status", status,
			"connections", backend.GetConnections(),
		)
	}
}

// isBackendAlive performs a TCP dial to the given URL's host to determine
// reachability.
func isBackendAlive(u *url.URL) bool {
	timeout := 2 * time.Second
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

// ============= ROUTE ENTRY / MATCHED ROUTE =============

// RouteEntry represents a single prefix-to-pool mapping in the router.
type RouteEntry struct {
	Prefix      string
	Pool        *ServicePool
	StripPrefix bool
	RequireAuth bool
	Timeout     time.Duration
}

// MatchedRoute is the result of a successful route lookup.
type MatchedRoute struct {
	Pool        *ServicePool
	BackendPath string
	RequireAuth bool
	Timeout     time.Duration
}

// ============= SERVICE ROUTER =============

// ServiceRouter holds all registered route entries and performs longest-prefix
// matching against incoming request paths.
type ServiceRouter struct {
	routes []RouteEntry
	mu     sync.RWMutex
}

// NewServiceRouter creates an empty router.
func NewServiceRouter() *ServiceRouter {
	return &ServiceRouter{}
}

// AddRoute creates a new ServicePool for the given ServiceRoute configuration,
// populates it with a backend built from the route's TargetURL, and registers
// the resulting RouteEntry.
func (sr *ServiceRouter) AddRoute(cfg ServiceRoute, algo string, transportCfg Config) {
	pool := NewServicePool(algo)

	targetURL, err := url.Parse(cfg.TargetURL)
	if err != nil {
		logRouter().Warn("invalid target URL, route skipped", "prefix", cfg.Prefix, "target_url", cfg.TargetURL, "error", err)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = &http.Transport{
		MaxIdleConns:        transportCfg.MaxIdleConns,
		MaxIdleConnsPerHost: transportCfg.MaxIdleConns,
		IdleConnTimeout:     transportCfg.IdleConnTimeout,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
		},
		DisableCompression: !transportCfg.EnableCompression,
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, proxyErr error) {
		reqID := r.Header.Get("x-request-id")
		logProxy().Error("proxy error", "backend", targetURL.String(), "error", proxyErr, "req", reqID)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	weight := cfg.Weight
	if weight <= 0 {
		weight = 1
	}

	backend := &Backend{
		URL:          targetURL,
		Alive:        true,
		Weight:       weight,
		ReverseProxy: proxy,
	}
	pool.AddBackend(backend)

	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 1 * time.Hour
	}

	sr.mu.Lock()
	sr.routes = append(sr.routes, RouteEntry{
		Prefix:      cfg.Prefix,
		Pool:        pool,
		StripPrefix: cfg.StripPrefix,
		RequireAuth: cfg.RequireAuth,
		Timeout:     timeout,
	})
	// Keep routes sorted by prefix length descending for longest-match.
	sort.Slice(sr.routes, func(i, j int) bool {
		return len(sr.routes[i].Prefix) > len(sr.routes[j].Prefix)
	})
	sr.mu.Unlock()

	logRouter().Info("route registered",
		"prefix", cfg.Prefix,
		"target", cfg.TargetURL,
		"strip_prefix", cfg.StripPrefix,
		"require_auth", cfg.RequireAuth,
		"weight", weight,
		"timeout", timeout,
	)
}

// Route performs longest-prefix matching on the given path and returns a
// MatchedRoute with the appropriate pool and rewritten backend path, or nil if
// no route matches.
func (sr *ServiceRouter) Route(path string) *MatchedRoute {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	for _, entry := range sr.routes {
		if strings.HasPrefix(path, entry.Prefix) {
			backendPath := path
			if entry.StripPrefix {
				backendPath = strings.TrimPrefix(path, entry.Prefix)
				if backendPath == "" || backendPath[0] != '/' {
					backendPath = "/" + backendPath
				}
			}
			return &MatchedRoute{
				Pool:        entry.Pool,
				BackendPath: backendPath,
				RequireAuth: entry.RequireAuth,
				Timeout:     entry.Timeout,
			}
		}
	}
	return nil
}

// Routes returns a snapshot of all registered route entries.
func (sr *ServiceRouter) Routes() []RouteEntry {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	out := make([]RouteEntry, len(sr.routes))
	copy(out, sr.routes)
	return out
}

// HealthCheckAll runs health checks on every pool in every registered route.
func (sr *ServiceRouter) HealthCheckAll() {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	for _, entry := range sr.routes {
		entry.Pool.HealthCheck()
	}
}
