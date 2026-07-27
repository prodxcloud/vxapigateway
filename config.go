package main

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ServiceRoute defines a host+prefix route to a pool of upstream servers.
type ServiceRoute struct {
	// Host restricts this route to a domain.
	//
	//	""                 matches any Host header — a path-only gateway, which
	//	                   is what every pre-existing config gets, unchanged.
	//	"api.example.com"  exact match, case-insensitive, port ignored.
	//	"*.example.com"    any subdomain, but not the apex (the usual
	//	                   convention, and what a wildcard TLS cert covers).
	//
	// Nothing here is limited to a fixed set of names or ports: the host and the
	// upstream are both free-form, so the gateway fronts whatever domains and
	// servers you point it at.
	Host string `json:"host"`

	Prefix string `json:"prefix"`

	// TargetURL is the single-upstream form. Kept because every existing config
	// and env var uses it.
	TargetURL string `json:"target_url"`

	// Targets is the multi-upstream form. Both may be set; they are merged, so
	// TargetURL keeps working while Targets adds a pool around it. This is what
	// makes the load-balancing algorithms real — before, every pool held exactly
	// one backend, so round-robin, least-conn, ip-hash and weighted were all
	// unreachable code.
	Targets []string `json:"targets"`

	StripPrefix bool `json:"strip_prefix"`
	RequireAuth bool `json:"require_auth"`

	// Weight applies to TargetURL, or to every target when Weights is empty.
	Weight int `json:"weight"`
	// Weights is positional against the merged target list; missing entries
	// fall back to Weight.
	Weights []int `json:"weights"`

	TimeoutSecs int `json:"timeout_secs"`

	// HealthPath turns liveness checking from "can I open a TCP connection" into
	// "does the application answer". A listening socket in front of a wedged app
	// is exactly the case a TCP dial cannot see.
	HealthPath string `json:"health_path"`

	// PreserveHost forwards the client's original Host header to the upstream
	// instead of rewriting it to the target's host. Needed when the upstream
	// itself serves virtual hosts; wrong when it validates its own Host.
	PreserveHost bool `json:"preserve_host"`

	// Name is cosmetic, used in logs and on the stats endpoint.
	Name string `json:"name"`
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
		logConfig().Warn("invalid integer env, using default", "key", key, "value", v, "default", fallback)
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
		logConfig().Warn("invalid boolean env, using default", "key", key, "value", v, "default", fallback)
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
		logConfig().Warn("invalid duration env, using default", "key", key, "value", v, "default", fallback)
		return fallback
	}
	return d
}

// vxCloudDefaultRoutes is the VxCloud-specific localhost topology this gateway
// grew up in front of.  It is opt-in via ENABLE_VXCLOUD_DEFAULT_ROUTES.
//
// It used to be unconditional, and that was the single biggest limitation in the
// gateway: nine routes pinned to nine hardcoded localhost ports were registered
// on every boot whether the operator wanted them or not. The guard that was
// supposed to suppress them read `if anySet || len(routes) > 0`, and the loop
// above it always appended nine entries — so `len(routes)` was always 9, the
// condition was always true, and `anySet` was dead. There was no way to run the
// gateway with a different topology, or with fewer routes, or none.
var vxCloudDefaultRoutes = []ServiceRoute{
	{Name: "studio", Prefix: "/api/studio", TargetURL: "http://localhost:3000"},
	{Name: "studio2", Prefix: "/api/studio2", TargetURL: "http://localhost:3001"},
	{Name: "ai", Prefix: "/api/ai", TargetURL: "http://localhost:8741"},
	{Name: "admin", Prefix: "/api/admin", TargetURL: "http://localhost:8242"},
	{Name: "core", Prefix: "/api/core", TargetURL: "http://localhost:8743"},
	{Name: "node", Prefix: "/api/node", TargetURL: "http://localhost:8744"},
	{Name: "llm", Prefix: "/api/llm", TargetURL: "http://localhost:8745"},
	{Name: "llm2", Prefix: "/api/llm2", TargetURL: "http://localhost:8746"},
	{Name: "agent", Prefix: "/api/agent", TargetURL: "http://localhost:8788"},
}

// splitList parses a comma- or whitespace-separated list, dropping empties.
func splitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// routeEnvSuffixes are the per-route knobs recognised on ROUTE_<NAME>_<SUFFIX>.
var routeEnvSuffixes = []string{
	"URL", "TARGETS", "PREFIX", "HOST", "STRIP_PREFIX", "REQUIRE_AUTH",
	"WEIGHT", "WEIGHTS", "TIMEOUT_SECS", "HEALTH_PATH", "PRESERVE_HOST",
}

// loadEnvNamedRoutes discovers routes by scanning the environment for
// ROUTE_<NAME>_<SUFFIX> variables.
//
// The name is whatever the operator chooses — it is not matched against a
// built-in list, so any number of routes to any hosts and any upstreams can be
// declared without touching this file. A route is only created when it has
// somewhere to send traffic (URL or TARGETS), which is also what makes the
// "declare nothing, get nothing" case work.
func loadEnvNamedRoutes() []ServiceRoute {
	// Group every ROUTE_<NAME>_<SUFFIX> by NAME. Longest suffix first so that
	// ROUTE_X_STRIP_PREFIX is not mistaken for name "X_STRIP" suffix "PREFIX".
	suffixes := make([]string, len(routeEnvSuffixes))
	copy(suffixes, routeEnvSuffixes)
	sort.Slice(suffixes, func(i, j int) bool { return len(suffixes[i]) > len(suffixes[j]) })

	byName := map[string]map[string]string{}
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		key, val := kv[:eq], kv[eq+1:]
		if !strings.HasPrefix(key, "ROUTE_") || val == "" {
			continue
		}
		rest := key[len("ROUTE_"):]
		for _, sfx := range suffixes {
			if len(rest) > len(sfx)+1 && strings.HasSuffix(rest, "_"+sfx) {
				name := rest[:len(rest)-len(sfx)-1]
				if byName[name] == nil {
					byName[name] = map[string]string{}
				}
				byName[name][sfx] = val
				break
			}
		}
	}

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic registration order

	var routes []ServiceRoute
	for _, name := range names {
		f := byName[name]

		targets := splitList(f["TARGETS"])
		if f["URL"] == "" && len(targets) == 0 {
			logConfig().Warn("route declared with no upstream, skipped",
				"route", name, "hint", "set ROUTE_"+name+"_URL or ROUTE_"+name+"_TARGETS")
			continue
		}

		prefix := f["PREFIX"]
		if prefix == "" {
			prefix = "/" // host-only route: match everything on that domain
		}

		r := ServiceRoute{
			Name:         strings.ToLower(name),
			Host:         f["HOST"],
			Prefix:       prefix,
			TargetURL:    f["URL"],
			Targets:      targets,
			StripPrefix:  true,
			Weight:       1,
			TimeoutSecs:  30,
			HealthPath:   f["HEALTH_PATH"],
			PreserveHost: parseBoolOr(f["PRESERVE_HOST"], false),
		}
		if v, ok := f["STRIP_PREFIX"]; ok {
			r.StripPrefix = parseBoolOr(v, true)
		}
		if v, ok := f["REQUIRE_AUTH"]; ok {
			r.RequireAuth = parseBoolOr(v, false)
		}
		if v, ok := f["WEIGHT"]; ok {
			r.Weight = parseIntOr(v, 1)
		}
		if v, ok := f["TIMEOUT_SECS"]; ok {
			r.TimeoutSecs = parseIntOr(v, 30)
		}
		for _, w := range splitList(f["WEIGHTS"]) {
			r.Weights = append(r.Weights, parseIntOr(w, 1))
		}
		routes = append(routes, r)
	}
	return routes
}

func parseBoolOr(v string, fallback bool) bool {
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return b
}

func parseIntOr(v string, fallback int) int {
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}

// loadServiceRoutes builds the route table, in precedence order:
//
//  1. SERVICE_ROUTES — a JSON array, the most expressive form.
//  2. ROUTE_<NAME>_* environment variables, for any names the operator invents.
//  3. The VxCloud localhost defaults, only when ENABLE_VXCLOUD_DEFAULT_ROUTES
//     is set.
//
// Sources 1 and 2 are additive, so a JSON base can be extended per deployment
// without re-encoding the whole array. Declaring nothing yields an empty table
// and a loud warning rather than nine invented localhost routes.
func loadServiceRoutes() []ServiceRoute {
	var routes []ServiceRoute

	if raw := os.Getenv("SERVICE_ROUTES"); raw != "" {
		var parsed []ServiceRoute
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			logConfig().Error("failed to parse SERVICE_ROUTES JSON; ignoring it", "error", err)
		} else {
			routes = append(routes, parsed...)
		}
	}

	routes = append(routes, loadEnvNamedRoutes()...)

	if envOrBool("ENABLE_VXCLOUD_DEFAULT_ROUTES", false) {
		for _, d := range vxCloudDefaultRoutes {
			d.StripPrefix = true
			d.Weight = 1
			d.TimeoutSecs = 30
			routes = append(routes, d)
		}
		logConfig().Info("VxCloud default localhost routes enabled", "count", len(vxCloudDefaultRoutes))
	}

	if len(routes) == 0 {
		logConfig().Warn("no routes configured; every request will get 404",
			"hint", "set SERVICE_ROUTES, or ROUTE_<NAME>_URL / ROUTE_<NAME>_HOST, "+
				"or ENABLE_VXCLOUD_DEFAULT_ROUTES=true")
	}
	return routes
}

// loadCorsOrigins reads CORS_ALLOWED_ORIGINS as a JSON array, defaulting to
// ["*"] if unset.
func loadCorsOrigins() []string {
	if raw := os.Getenv("CORS_ALLOWED_ORIGINS"); raw != "" {
		var origins []string
		if err := json.Unmarshal([]byte(raw), &origins); err != nil {
			logConfig().Warn("failed to parse CORS_ALLOWED_ORIGINS, using default", "error", err)
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
		Port:                  envOr("GATEWAY_PORT", ":9777"),
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
