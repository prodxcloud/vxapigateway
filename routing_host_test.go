package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

func testCfg() Config {
	return Config{
		MaxIdleConns:      10,
		IdleConnTimeout:   30 * time.Second,
		EnableCompression: false,
		ConnectionTimeout: 5 * time.Second,
	}
}

// ============= host normalisation =============

func TestNormalizeHost(t *testing.T) {
	tests := []struct{ in, want string }{
		{"example.com", "example.com"},
		{"Example.COM", "example.com"},       // browsers do send mixed case
		{"example.com:8443", "example.com"},  // explicit port
		{"example.com.", "example.com"},      // FQDN trailing dot
		{"example.com.:8443", "example.com"}, // both
		{"  example.com  ", "example.com"},   // stray whitespace
		{"[::1]:8080", "::1"},                // IPv6 with port
		{"127.0.0.1:9777", "127.0.0.1"},      // IPv4 with port
		{"127.0.0.1", "127.0.0.1"},           // bare IPv4
		{"", ""},                             // no Host header
		{"api.sub.deep.example.com", "api.sub.deep.example.com"},
	}
	for _, tt := range tests {
		if got := normalizeHost(tt.in); got != tt.want {
			t.Errorf("normalizeHost(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHostMatches(t *testing.T) {
	tests := []struct {
		pattern, host string
		want          bool
		why           string
	}{
		{"", "anything.com", true, "empty pattern is a catch-all"},
		{"", "", true, "catch-all matches a missing Host too"},
		{"api.example.com", "api.example.com", true, "exact"},
		{"API.example.com", "api.example.com", true, "exact is case-insensitive"},
		{"api.example.com", "other.example.com", false, "exact does not match a sibling"},
		{"*.example.com", "api.example.com", true, "wildcard matches a subdomain"},
		{"*.example.com", "a.b.example.com", true, "wildcard matches deeper subdomains"},
		{"*.example.com", "example.com", false, "wildcard must not match the apex"},
		{"*.example.com", "notexample.com", false, "wildcard must not match a suffix collision"},
		{"*.example.com", "example.com.evil.com", false, "wildcard anchors at the end"},
		{"api.example.com", "api.example.com.attacker.net", false, "exact anchors fully"},
	}
	for _, tt := range tests {
		if got := hostMatches(tt.pattern, normalizeHost(tt.host)); got != tt.want {
			t.Errorf("hostMatches(%q, %q) = %v, want %v — %s", tt.pattern, tt.host, got, tt.want, tt.why)
		}
	}
}

// ============= upstream parsing =============

func TestNormalizeUpstream(t *testing.T) {
	ok := []struct{ in, want string }{
		{"http://localhost:8080", "http://localhost:8080"},
		{"https://api.example.com/v1", "https://api.example.com/v1"},
		// A bare host:port parses as scheme "10.0.0.5" with opaque "8080" under
		// url.Parse, which silently yields a broken backend. It must be treated
		// as a host and defaulted to http.
		{"10.0.0.5:8080", "http://10.0.0.5:8080"},
		{"example.com", "http://example.com"},
		{"worker1.internal:9000", "http://worker1.internal:9000"},
		{"  http://spaced.example.com  ", "http://spaced.example.com"},
	}
	for _, tt := range ok {
		u, err := normalizeUpstream(tt.in)
		if err != nil {
			t.Errorf("normalizeUpstream(%q) unexpected error: %v", tt.in, err)
			continue
		}
		if u.String() != tt.want {
			t.Errorf("normalizeUpstream(%q) = %q, want %q", tt.in, u.String(), tt.want)
		}
	}

	bad := []string{"", "   ", "ftp://example.com", "gopher://x"}
	for _, in := range bad {
		if u, err := normalizeUpstream(in); err == nil {
			t.Errorf("normalizeUpstream(%q) should have failed, got %v", in, u)
		}
	}
}

// ============= path matching on segment boundaries =============

func TestPathMatchesSegmentBoundary(t *testing.T) {
	tests := []struct {
		prefix, path string
		want         bool
	}{
		{"/api/v1", "/api/v1", true},
		{"/api/v1", "/api/v1/users", true},
		// The important one: a plain HasPrefix would send /api/v11 traffic to the
		// /api/v1 service.
		{"/api/v1", "/api/v11", false},
		{"/api/us", "/api/user", false},
		{"/api/user", "/api/user", true},
		{"/", "/anything", true},
		{"", "/anything", true},
		{"/api/", "/api/anything", true},
	}
	for _, tt := range tests {
		if got := pathMatches(tt.prefix, tt.path); got != tt.want {
			t.Errorf("pathMatches(%q, %q) = %v, want %v", tt.prefix, tt.path, got, tt.want)
		}
	}
}

// ============= routing across several domains =============

// newEchoServer returns a server that identifies itself, so a test can prove
// which upstream actually served a request.
func newEchoServer(name string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", name)
		fmt.Fprintf(w, "%s|%s|host=%s|xfh=%s|xfp=%s",
			name, r.URL.Path, r.Host,
			r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Forwarded-Proto"))
	}))
}

func TestRouteByHost(t *testing.T) {
	apiSrv := newEchoServer("api")
	defer apiSrv.Close()
	appSrv := newEchoServer("app")
	defer appSrv.Close()
	wildSrv := newEchoServer("wild")
	defer wildSrv.Close()
	anySrv := newEchoServer("catchall")
	defer anySrv.Close()

	router := NewServiceRouter()
	cfg := testCfg()

	// Registered deliberately out of specificity order to prove the table sorts
	// itself: a catch-all added first must not shadow the domain routes.
	router.AddRoute(ServiceRoute{Name: "catchall", Prefix: "/", TargetURL: anySrv.URL, TimeoutSecs: 5}, "round-robin", cfg)
	router.AddRoute(ServiceRoute{Name: "wild", Host: "*.example.com", Prefix: "/", TargetURL: wildSrv.URL, TimeoutSecs: 5}, "round-robin", cfg)
	router.AddRoute(ServiceRoute{Name: "api", Host: "api.example.com", Prefix: "/", TargetURL: apiSrv.URL, TimeoutSecs: 5}, "round-robin", cfg)
	router.AddRoute(ServiceRoute{Name: "app", Host: "app.other.org", Prefix: "/", TargetURL: appSrv.URL, TimeoutSecs: 5}, "round-robin", cfg)

	tests := []struct {
		host, path, wantRoute string
		why                   string
	}{
		{"api.example.com", "/v1/ping", "api", "exact host wins over the wildcard"},
		{"API.EXAMPLE.COM", "/v1/ping", "api", "host matching ignores case"},
		{"api.example.com:8443", "/v1/ping", "api", "an explicit port is ignored"},
		{"other.example.com", "/x", "wild", "a sibling falls through to the wildcard"},
		{"example.com", "/x", "catchall", "the apex is not covered by *.example.com"},
		{"app.other.org", "/x", "app", "a second, unrelated domain routes independently"},
		{"totally.unknown.tld", "/x", "catchall", "an unknown domain hits the catch-all"},
		{"", "/x", "catchall", "a request with no Host still routes"},
	}

	for _, tt := range tests {
		m := router.Route(tt.host, tt.path)
		if m == nil {
			t.Errorf("Route(%q, %q) = nil, want route %q (%s)", tt.host, tt.path, tt.wantRoute, tt.why)
			continue
		}
		if m.Name != tt.wantRoute {
			t.Errorf("Route(%q, %q) = %q, want %q — %s", tt.host, tt.path, m.Name, tt.wantRoute, tt.why)
		}
	}
}

func TestRouteHostPlusPrefixSpecificity(t *testing.T) {
	srv := newEchoServer("x")
	defer srv.Close()

	router := NewServiceRouter()
	cfg := testCfg()

	router.AddRoute(ServiceRoute{Name: "host-root", Host: "api.example.com", Prefix: "/", TargetURL: srv.URL, TimeoutSecs: 5}, "round-robin", cfg)
	router.AddRoute(ServiceRoute{Name: "host-deep", Host: "api.example.com", Prefix: "/v2/admin", TargetURL: srv.URL, TimeoutSecs: 5}, "round-robin", cfg)
	router.AddRoute(ServiceRoute{Name: "host-mid", Host: "api.example.com", Prefix: "/v2", TargetURL: srv.URL, TimeoutSecs: 5}, "round-robin", cfg)
	router.AddRoute(ServiceRoute{Name: "any-deep", Prefix: "/v2/admin/super", TargetURL: srv.URL, TimeoutSecs: 5}, "round-robin", cfg)

	tests := []struct{ host, path, want string }{
		{"api.example.com", "/v2/admin/thing", "host-deep"},
		{"api.example.com", "/v2/other", "host-mid"},
		{"api.example.com", "/elsewhere", "host-root"},
		// A more specific *path* on a catch-all host must still lose to a route
		// that matches the host, because host is the outer dimension.
		{"api.example.com", "/v2/admin/super/x", "host-deep"},
		{"nomatch.tld", "/v2/admin/super/x", "any-deep"},
	}
	for _, tt := range tests {
		m := router.Route(tt.host, tt.path)
		if m == nil {
			t.Errorf("Route(%q, %q) = nil, want %q", tt.host, tt.path, tt.want)
			continue
		}
		if m.Name != tt.want {
			t.Errorf("Route(%q, %q) = %q, want %q", tt.host, tt.path, m.Name, tt.want)
		}
	}
}

// ============= end to end through the proxy =============

func TestProxyForwardsToCorrectDomainUpstream(t *testing.T) {
	a := newEchoServer("alpha")
	defer a.Close()
	b := newEchoServer("beta")
	defer b.Close()

	router := NewServiceRouter()
	cfg := testCfg()
	router.AddRoute(ServiceRoute{Name: "alpha", Host: "alpha.test", Prefix: "/", TargetURL: a.URL, TimeoutSecs: 5}, "round-robin", cfg)
	router.AddRoute(ServiceRoute{Name: "beta", Host: "beta.test", Prefix: "/", TargetURL: b.URL, TimeoutSecs: 5}, "round-robin", cfg)

	for _, want := range []string{"alpha", "beta"} {
		req := httptest.NewRequest("GET", "http://"+want+".test/hello", nil)
		req.Host = want + ".test"
		rec := httptest.NewRecorder()

		m := router.Route(req.Host, req.URL.Path)
		if m == nil {
			t.Fatalf("no route for %s", req.Host)
		}
		backend := m.Pool.NextPeer("1.2.3.4")
		if backend == nil {
			t.Fatalf("no backend for %s", req.Host)
		}
		backend.ReverseProxy.ServeHTTP(rec, req)

		if got := rec.Header().Get("X-Upstream"); got != want {
			t.Errorf("host %s served by upstream %q, want %q", req.Host, got, want)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "xfh="+want+".test") {
			t.Errorf("X-Forwarded-Host not propagated for %s: %s", want, body)
		}
		if !strings.Contains(body, "xfp=http") {
			t.Errorf("X-Forwarded-Proto not set for %s: %s", want, body)
		}
	}
}

func TestPreserveHost(t *testing.T) {
	srv := newEchoServer("up")
	defer srv.Close()
	cfg := testCfg()

	for _, preserve := range []bool{false, true} {
		router := NewServiceRouter()
		router.AddRoute(ServiceRoute{
			Name: "r", Host: "public.test", Prefix: "/",
			TargetURL: srv.URL, TimeoutSecs: 5, PreserveHost: preserve,
		}, "round-robin", cfg)

		req := httptest.NewRequest("GET", "http://public.test/x", nil)
		req.Host = "public.test"
		rec := httptest.NewRecorder()
		m := router.Route(req.Host, req.URL.Path)
		m.Pool.NextPeer("1.2.3.4").ReverseProxy.ServeHTTP(rec, req)

		body := rec.Body.String()
		if preserve {
			if !strings.Contains(body, "host=public.test") {
				t.Errorf("preserve_host=true should forward the client Host, got: %s", body)
			}
		} else {
			if strings.Contains(body, "host=public.test") {
				t.Errorf("preserve_host=false should rewrite Host to the upstream, got: %s", body)
			}
		}
		// Either way the upstream must be able to learn the public domain.
		if !strings.Contains(body, "xfh=public.test") {
			t.Errorf("X-Forwarded-Host must always carry the original host, got: %s", body)
		}
	}
}

// ============= multiple upstreams per route =============

func TestMultipleUpstreamsRoundRobin(t *testing.T) {
	var servers []*httptest.Server
	var urls []string
	for _, n := range []string{"one", "two", "three"} {
		s := newEchoServer(n)
		defer s.Close()
		servers = append(servers, s)
		urls = append(urls, s.URL)
	}

	router := NewServiceRouter()
	router.AddRoute(ServiceRoute{
		Name: "pool", Host: "pool.test", Prefix: "/",
		Targets: urls, TimeoutSecs: 5,
	}, "round-robin", testCfg())

	m := router.Route("pool.test", "/x")
	if m == nil {
		t.Fatal("no route")
	}
	// Before this change AddRoute added exactly one backend, so the pool could
	// never distribute anything and every algorithm was dead code.
	if n := len(m.Pool.Backends()); n != 3 {
		t.Fatalf("pool has %d backends, want 3", n)
	}

	seen := map[string]int{}
	for i := 0; i < 30; i++ {
		b := m.Pool.NextPeer("1.2.3.4")
		if b == nil {
			t.Fatal("NextPeer returned nil")
		}
		req := httptest.NewRequest("GET", "http://pool.test/x", nil)
		rec := httptest.NewRecorder()
		b.ReverseProxy.ServeHTTP(rec, req)
		seen[rec.Header().Get("X-Upstream")]++
	}

	if len(seen) != 3 {
		t.Errorf("round-robin used %d of 3 upstreams: %v", len(seen), seen)
	}
	for name, count := range seen {
		if count == 0 {
			t.Errorf("upstream %s never selected", name)
		}
	}
}

func TestTargetURLAndTargetsAreMergedAndDeduplicated(t *testing.T) {
	s1 := newEchoServer("a")
	defer s1.Close()
	s2 := newEchoServer("b")
	defer s2.Close()

	router := NewServiceRouter()
	router.AddRoute(ServiceRoute{
		Name: "merged", Prefix: "/",
		TargetURL: s1.URL,
		// s1 repeated: listing an upstream twice must not double its share.
		Targets:     []string{s1.URL, s2.URL},
		TimeoutSecs: 5,
	}, "round-robin", testCfg())

	m := router.Route("", "/x")
	if got := len(m.Pool.Backends()); got != 2 {
		t.Errorf("merged pool has %d backends, want 2 (deduplicated)", got)
	}
}

func TestRouteWithNoUsableUpstreamIsNotRegistered(t *testing.T) {
	router := NewServiceRouter()
	router.AddRoute(ServiceRoute{Name: "bad", Prefix: "/x", TargetURL: "ftp://nope", TimeoutSecs: 5}, "round-robin", testCfg())
	if n := len(router.Routes()); n != 0 {
		t.Errorf("registered %d routes despite no usable upstream, want 0", n)
	}
	// And it must not panic or match anything.
	if m := router.Route("any.host", "/x"); m != nil {
		t.Errorf("unregistered route still matched: %+v", m)
	}
}

// ============= health checks =============

func TestHealthCheckUsesHTTPPathWhenGiven(t *testing.T) {
	var healthy bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			if healthy {
				w.WriteHeader(http.StatusOK)
			} else {
				// A listening socket that answers 503 is exactly the failure a
				// TCP dial cannot detect.
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	router := NewServiceRouter()
	router.AddRoute(ServiceRoute{
		Name: "h", Prefix: "/", TargetURL: srv.URL,
		HealthPath: "/healthz", TimeoutSecs: 5,
	}, "round-robin", testCfg())

	m := router.Route("", "/")
	b := m.Pool.Backends()[0]

	healthy = false
	m.Pool.HealthCheck()
	if b.IsAlive() {
		t.Error("backend answering 503 on its health path should be marked down")
	}

	healthy = true
	m.Pool.HealthCheck()
	if !b.IsAlive() {
		t.Error("backend answering 200 on its health path should be marked up")
	}
}

func TestHealthCheckFallsBackToTCPDial(t *testing.T) {
	srv := newEchoServer("up")
	router := NewServiceRouter()
	router.AddRoute(ServiceRoute{Name: "t", Prefix: "/", TargetURL: srv.URL, TimeoutSecs: 5}, "round-robin", testCfg())

	m := router.Route("", "/")
	b := m.Pool.Backends()[0]

	m.Pool.HealthCheck()
	if !b.IsAlive() {
		t.Error("a listening upstream should dial successfully")
	}

	srv.Close() // now nothing is listening
	m.Pool.HealthCheck()
	if b.IsAlive() {
		t.Error("a closed upstream should be marked down")
	}
}

// ============= response recorder =============

func TestResponseRecorderObservesRealStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	rr := newResponseRecorder(rec)

	if rr.Committed() {
		t.Error("recorder should start uncommitted")
	}
	if rr.Status() != http.StatusOK {
		t.Errorf("default status = %d, want 200", rr.Status())
	}

	rr.WriteHeader(http.StatusBadGateway)
	if rr.Status() != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 — this is what stopped every request being counted as 200", rr.Status())
	}
	if !rr.Committed() {
		t.Error("recorder should be committed after WriteHeader")
	}

	// A second WriteHeader must not change the recorded status, matching net/http.
	rr.WriteHeader(http.StatusOK)
	if rr.Status() != http.StatusBadGateway {
		t.Errorf("status changed on second WriteHeader to %d", rr.Status())
	}

	n, err := rr.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Errorf("Write = (%d, %v)", n, err)
	}
	if rr.BytesWritten() != 5 {
		t.Errorf("BytesWritten = %d, want 5", rr.BytesWritten())
	}
}

func TestResponseRecorderInfersStatusFromBareWrite(t *testing.T) {
	rr := newResponseRecorder(httptest.NewRecorder())
	if _, err := rr.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if rr.Status() != http.StatusOK || !rr.Committed() {
		t.Errorf("bare Write should imply 200 and commit; got %d committed=%v", rr.Status(), rr.Committed())
	}
}

// ============= configuration =============

func TestLoadServiceRoutesDefaultsToNothing(t *testing.T) {
	// Every ROUTE_*/SERVICE_ROUTES var cleared: the gateway must register no
	// routes at all. It used to invent nine localhost routes here, because the
	// guard read `if anySet || len(routes) > 0` and len(routes) was always 9.
	for _, kv := range os.Environ() {
		if k := strings.SplitN(kv, "=", 2)[0]; strings.HasPrefix(k, "ROUTE_") ||
			k == "SERVICE_ROUTES" || k == "ENABLE_VXCLOUD_DEFAULT_ROUTES" {
			t.Setenv(k, "")
		}
	}
	t.Setenv("SERVICE_ROUTES", "")
	t.Setenv("ENABLE_VXCLOUD_DEFAULT_ROUTES", "")

	if got := loadServiceRoutes(); len(got) != 0 {
		names := []string{}
		for _, r := range got {
			names = append(names, r.Prefix+"->"+r.TargetURL)
		}
		sort.Strings(names)
		t.Errorf("expected no routes with a clean environment, got %d: %v", len(got), names)
	}
}

func TestVxCloudDefaultRoutesAreOptIn(t *testing.T) {
	t.Setenv("SERVICE_ROUTES", "")
	t.Setenv("ENABLE_VXCLOUD_DEFAULT_ROUTES", "true")
	got := loadServiceRoutes()
	if len(got) != len(vxCloudDefaultRoutes) {
		t.Fatalf("opt-in defaults gave %d routes, want %d", len(got), len(vxCloudDefaultRoutes))
	}
	if got[0].TargetURL == "" {
		t.Error("default route has no upstream")
	}
}

func TestEnvNamedRoutesDiscoverAnyName(t *testing.T) {
	t.Setenv("SERVICE_ROUTES", "")
	t.Setenv("ENABLE_VXCLOUD_DEFAULT_ROUTES", "")
	// A name that is not in any built-in list, on a real domain, to a
	// non-localhost upstream: the whole point of the change.
	t.Setenv("ROUTE_BILLING_HOST", "billing.acme.example")
	t.Setenv("ROUTE_BILLING_PREFIX", "/v2")
	t.Setenv("ROUTE_BILLING_TARGETS", "10.0.0.7:8080, https://billing-2.acme.example")
	t.Setenv("ROUTE_BILLING_WEIGHTS", "3,1")
	t.Setenv("ROUTE_BILLING_HEALTH_PATH", "/healthz")
	t.Setenv("ROUTE_BILLING_PRESERVE_HOST", "true")
	t.Setenv("ROUTE_BILLING_REQUIRE_AUTH", "true")

	routes := loadServiceRoutes()
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1: %+v", len(routes), routes)
	}
	r := routes[0]
	if r.Host != "billing.acme.example" {
		t.Errorf("Host = %q", r.Host)
	}
	if r.Prefix != "/v2" {
		t.Errorf("Prefix = %q", r.Prefix)
	}
	if len(r.Targets) != 2 {
		t.Errorf("Targets = %v, want 2 entries", r.Targets)
	}
	if len(r.Weights) != 2 || r.Weights[0] != 3 {
		t.Errorf("Weights = %v", r.Weights)
	}
	if r.HealthPath != "/healthz" {
		t.Errorf("HealthPath = %q", r.HealthPath)
	}
	if !r.PreserveHost {
		t.Error("PreserveHost should be true")
	}
	if !r.RequireAuth {
		t.Error("RequireAuth should be true")
	}
}

func TestEnvNamedRouteWithoutUpstreamIsSkipped(t *testing.T) {
	t.Setenv("SERVICE_ROUTES", "")
	t.Setenv("ENABLE_VXCLOUD_DEFAULT_ROUTES", "")
	t.Setenv("ROUTE_ORPHAN_PREFIX", "/orphan") // declared, but nowhere to send it
	if got := loadServiceRoutes(); len(got) != 0 {
		t.Errorf("a route with no upstream should be skipped, got %+v", got)
	}
}

func TestServiceRoutesJSONAndEnvAreAdditive(t *testing.T) {
	t.Setenv("ENABLE_VXCLOUD_DEFAULT_ROUTES", "")
	t.Setenv("SERVICE_ROUTES", `[{"name":"json","host":"a.test","prefix":"/","target_url":"http://1.2.3.4:80"}]`)
	t.Setenv("ROUTE_EXTRA_URL", "http://5.6.7.8:90")
	t.Setenv("ROUTE_EXTRA_HOST", "b.test")

	routes := loadServiceRoutes()
	if len(routes) != 2 {
		t.Fatalf("JSON and env routes should combine, got %d: %+v", len(routes), routes)
	}
	hosts := map[string]bool{}
	for _, r := range routes {
		hosts[r.Host] = true
	}
	if !hosts["a.test"] || !hosts["b.test"] {
		t.Errorf("expected both a.test and b.test, got %v", hosts)
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" a , b,,c   d ")
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("splitList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("splitList[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
