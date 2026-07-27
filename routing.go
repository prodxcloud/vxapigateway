package main

import (
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"io"
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

	// HealthPath, when set, makes the health probe an HTTP request instead of a
	// bare TCP dial. See Backend.probe.
	HealthPath   string
	healthClient *http.Client
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

// HealthCheck probes every backend in the pool and updates its alive status.
func (s *ServicePool) HealthCheck() {
	for _, backend := range s.Backends() {
		alive := backend.probe()
		was := backend.IsAlive()
		backend.SetAlive(alive)

		if alive {
			backend.ResetFailures()
		}

		status := "up"
		if !alive {
			status = "down"
		}
		// Only log transitions at info; steady state goes to debug so a
		// hundred-backend fleet does not flood the log every interval.
		entry := logRouter().Debug
		if was != alive {
			entry = logRouter().Info
		}
		entry("health check",
			"backend", backend.URL.String(),
			"status", status,
			"changed", was != alive,
			"connections", backend.GetConnections(),
		)
	}
}

// probe reports whether the backend is usable.
//
// With a HealthPath it issues a real HTTP request and requires a non-5xx reply,
// because a listening socket in front of a wedged application is precisely the
// failure a TCP dial cannot see. Without one it falls back to a dial, which is
// all you can do for an opaque TCP upstream.
func (b *Backend) probe() bool {
	if b.HealthPath == "" {
		return dialable(b.URL)
	}

	target := *b.URL
	target.Path = singleJoiningSlash(b.URL.Path, b.HealthPath)
	target.RawQuery = ""

	req, err := http.NewRequest(http.MethodGet, target.String(), nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "vx-api-gateway/health")

	resp, err := b.healthClient.Do(req)
	if err != nil {
		return false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
	}()
	// 4xx still proves the application is answering; 5xx does not.
	return resp.StatusCode < 500
}

// dialable performs a TCP connect to the URL's host, filling in the default
// port for the scheme when the URL omits it.
func dialable(u *url.URL) bool {
	host := u.Host
	if u.Port() == "" {
		switch u.Scheme {
		case "https":
			host = net.JoinHostPort(u.Hostname(), "443")
		default:
			host = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

// singleJoiningSlash joins two URL path segments with exactly one slash.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// ============= ROUTE ENTRY / MATCHED ROUTE =============

// RouteEntry represents a single host+prefix-to-pool mapping in the router.
type RouteEntry struct {
	Name        string
	Host        string // "" any · "api.example.com" exact · "*.example.com" wildcard
	Prefix      string
	Pool        *ServicePool
	StripPrefix bool
	RequireAuth bool
	Timeout     time.Duration
}

// MatchedRoute is the result of a successful route lookup.
type MatchedRoute struct {
	Name        string
	Pool        *ServicePool
	BackendPath string
	RequireAuth bool
	Timeout     time.Duration
}

// Host-pattern specificity, used to order the routing table. A request that
// matches both an exact-host route and a catch-all must take the exact one.
const (
	hostAny      = 0 // ""
	hostWildcard = 1 // "*.example.com"
	hostExact    = 2 // "api.example.com"
)

func hostSpecificity(pattern string) int {
	switch {
	case pattern == "":
		return hostAny
	case strings.HasPrefix(pattern, "*."):
		return hostWildcard
	default:
		return hostExact
	}
}

// normalizeHost reduces a Host header to a comparable form: lower-cased, port
// removed, trailing FQDN dot removed, IPv6 brackets stripped.
//
// All four matter in practice. Browsers send "Example.COM"; an explicit port
// arrives as "example.com:8443"; a resolver-style FQDN arrives as "example.com.";
// and an IPv6 literal arrives as "[::1]:8080".
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" {
		return ""
	}
	// Strip the port first: SplitHostPort handles the bracketed IPv6 form, and
	// errors (leaving h alone) when there is no port at all.
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	// "example.com." and "example.com" are the same name.
	if len(h) > 1 {
		h = strings.TrimSuffix(h, ".")
	}
	return h
}

// hostMatches reports whether a route's host pattern covers the request host.
//
// "*.example.com" deliberately does not match the apex "example.com", matching
// both DNS wildcard semantics and what a wildcard TLS certificate covers. It
// does match any depth of subdomain, which is more permissive than a certificate
// — the gateway is not the thing enforcing certificate scope.
func hostMatches(pattern, host string) bool {
	if pattern == "" {
		return true
	}
	pattern = normalizeHost(pattern)
	if pattern == host {
		return true
	}
	if suffix, ok := strings.CutPrefix(pattern, "*"); ok {
		// suffix is ".example.com"; require at least one label before it.
		return len(host) > len(suffix) && strings.HasSuffix(host, suffix)
	}
	return false
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

// normalizeUpstream turns an operator-supplied upstream into a usable URL.
//
// Being forgiving here is the difference between "any server" and "any server
// spelled exactly right": "10.0.0.5:8080", "example.com" and
// "https://api.example.com/v1" are all things people reasonably write, and only
// the last is a URL url.Parse would interpret as one. A bare "host:port" parses
// as scheme "10.0.0.5" with opaque "8080", which silently produces a broken
// backend — so it is detected and defaulted to http.
func normalizeUpstream(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty upstream")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, fmt.Errorf("upstream %q has no host", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("upstream %q has unsupported scheme %q", raw, u.Scheme)
	}
	return u, nil
}

// proxyErrSink carries a proxy failure back to the dispatcher.
//
// httputil.ReverseProxy reports round-trip failures by calling ErrorHandler, not
// by returning an error, so without this the dispatcher cannot tell a successful
// proxy from a dead backend — which is what made retry, failover and the circuit
// breaker unreachable, and made every request count as a 200.
type proxyErrSink struct{ err error }

type ctxKeyErrSink struct{}

// AddRoute builds a pool of backends for the route and registers it.
//
// Every entry in Targets plus TargetURL becomes its own backend with its own
// reverse proxy, which is what finally gives the load-balancing algorithms
// something to choose between.
func (sr *ServiceRouter) AddRoute(cfg ServiceRoute, algo string, transportCfg Config) {
	pool := NewServicePool(algo)

	// Merge the single- and multi-upstream forms, de-duplicated so listing a
	// target in both places does not double its share of traffic.
	raw := make([]string, 0, len(cfg.Targets)+1)
	if cfg.TargetURL != "" {
		raw = append(raw, cfg.TargetURL)
	}
	raw = append(raw, cfg.Targets...)

	seen := map[string]bool{}
	defaultWeight := cfg.Weight
	if defaultWeight <= 0 {
		defaultWeight = 1
	}

	// One shared transport per route: connection pooling only helps if the
	// backends actually reuse it.
	transport := &http.Transport{
		MaxIdleConns:        transportCfg.MaxIdleConns,
		MaxIdleConnsPerHost: transportCfg.MaxIdleConns,
		IdleConnTimeout:     transportCfg.IdleConnTimeout,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: false},
		DisableCompression:  !transportCfg.EnableCompression,
		ForceAttemptHTTP2:   true,
	}
	healthClient := &http.Client{
		Timeout:   3 * time.Second,
		Transport: transport,
		// A health probe follows nothing: a redirect is an answer in itself.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	for i, r := range raw {
		targetURL, err := normalizeUpstream(r)
		if err != nil {
			logRouter().Warn("invalid upstream, skipped",
				"route", cfg.Name, "host", cfg.Host, "prefix", cfg.Prefix, "upstream", r, "error", err)
			continue
		}
		if seen[targetURL.String()] {
			continue
		}
		seen[targetURL.String()] = true

		weight := defaultWeight
		if i < len(cfg.Weights) && cfg.Weights[i] > 0 {
			weight = cfg.Weights[i]
		}

		backend := &Backend{
			URL:          targetURL,
			Alive:        true,
			Weight:       weight,
			HealthPath:   cfg.HealthPath,
			healthClient: healthClient,
		}
		backend.ReverseProxy = newBackendProxy(targetURL, transport, cfg.PreserveHost)
		pool.AddBackend(backend)
	}

	if len(pool.Backends()) == 0 {
		logRouter().Error("route has no usable upstream, not registered",
			"route", cfg.Name, "host", cfg.Host, "prefix", cfg.Prefix)
		return
	}

	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 1 * time.Hour
	}

	prefix := cfg.Prefix
	if prefix == "" {
		prefix = "/"
	}

	sr.mu.Lock()
	sr.routes = append(sr.routes, RouteEntry{
		Name:        cfg.Name,
		Host:        normalizeHost(cfg.Host),
		Prefix:      prefix,
		Pool:        pool,
		StripPrefix: cfg.StripPrefix,
		RequireAuth: cfg.RequireAuth,
		Timeout:     timeout,
	})
	// Order the table so the first match is the most specific one: exact host
	// beats wildcard beats catch-all, and within a host class a longer prefix
	// beats a shorter one. Without the host tier, a catch-all "/" route would
	// shadow every domain-specific route registered after it.
	sort.SliceStable(sr.routes, func(i, j int) bool {
		si, sj := hostSpecificity(sr.routes[i].Host), hostSpecificity(sr.routes[j].Host)
		if si != sj {
			return si > sj
		}
		if len(sr.routes[i].Host) != len(sr.routes[j].Host) {
			return len(sr.routes[i].Host) > len(sr.routes[j].Host)
		}
		return len(sr.routes[i].Prefix) > len(sr.routes[j].Prefix)
	})
	sr.mu.Unlock()

	hostLabel := cfg.Host
	if hostLabel == "" {
		hostLabel = "*"
	}
	upstreams := make([]string, 0, len(pool.Backends()))
	for _, b := range pool.Backends() {
		upstreams = append(upstreams, b.URL.String())
	}
	logRouter().Info("route registered",
		"route", cfg.Name,
		"host", hostLabel,
		"prefix", prefix,
		"upstreams", strings.Join(upstreams, ","),
		"backends", len(upstreams),
		"algo", algo,
		"strip_prefix", cfg.StripPrefix,
		"require_auth", cfg.RequireAuth,
		"preserve_host", cfg.PreserveHost,
		"health_path", cfg.HealthPath,
		"timeout", timeout,
	)
}

// newBackendProxy builds the reverse proxy for one upstream.
func newBackendProxy(target *url.URL, transport http.RoundTripper, preserveHost bool) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	inner := proxy.Director

	proxy.Director = func(req *http.Request) {
		// Capture what the client asked for before the director rewrites it, so
		// the upstream can still reconstruct the public URL. A gateway fronting
		// several domains is useless to the upstream without this.
		origHost := req.Host
		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}
		if xf := req.Header.Get("X-Forwarded-Proto"); xf != "" {
			scheme = xf
		}

		inner(req)

		if req.Header.Get("X-Forwarded-Host") == "" && origHost != "" {
			req.Header.Set("X-Forwarded-Host", origHost)
		}
		req.Header.Set("X-Forwarded-Proto", scheme)

		if preserveHost {
			// Go sends req.Host as the Host header when it is non-empty, so
			// leaving it alone is what forwards the client's domain.
			req.Host = origHost
		} else {
			req.Host = target.Host
		}
	}

	proxy.Transport = transport
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, proxyErr error) {
		// Hand the failure to the dispatcher when it is listening, so it can try
		// another backend. ReverseProxy only reaches here before writing
		// anything to the client, so retrying is safe at this point.
		if sink, ok := r.Context().Value(ctxKeyErrSink{}).(*proxyErrSink); ok {
			sink.err = proxyErr
			return
		}
		logProxy().Error("proxy error",
			"backend", target.String(), "error", proxyErr, "req", r.Header.Get("x-request-id"))
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}
	return proxy
}

// Route resolves a request's Host and path to a backend pool.
//
// Matching is host-then-path: the table is ordered most-specific-first, so the
// first entry whose host pattern and path prefix both match wins.
func (sr *ServiceRouter) Route(host, path string) *MatchedRoute {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	h := normalizeHost(host)

	for _, entry := range sr.routes {
		if !hostMatches(entry.Host, h) {
			continue
		}
		if !pathMatches(entry.Prefix, path) {
			continue
		}
		backendPath := path
		if entry.StripPrefix && entry.Prefix != "/" {
			backendPath = strings.TrimPrefix(path, entry.Prefix)
			if backendPath == "" || backendPath[0] != '/' {
				backendPath = "/" + backendPath
			}
		}
		return &MatchedRoute{
			Name:        entry.Name,
			Pool:        entry.Pool,
			BackendPath: backendPath,
			RequireAuth: entry.RequireAuth,
			Timeout:     entry.Timeout,
		}
	}
	return nil
}

// pathMatches reports whether a prefix covers a request path on a segment
// boundary.
//
// A plain strings.HasPrefix would make "/api/user" match a route registered for
// "/api/us", quietly proxying one service's traffic to another. Requiring the
// match to end at "/" or end-of-path avoids that.
func pathMatches(prefix, path string) bool {
	if prefix == "" || prefix == "/" {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) == len(prefix) {
		return true
	}
	// "/api/v1" matches "/api/v1/x" but not "/api/v11".
	return path[len(prefix)] == '/' || strings.HasSuffix(prefix, "/")
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
