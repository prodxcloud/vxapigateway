package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ============= TestServiceRouter_Route =============

func TestServiceRouter_Route(t *testing.T) {
	router := NewServiceRouter()

	cfg := Config{
		MaxIdleConns:      10,
		IdleConnTimeout:   30 * time.Second,
		EnableCompression: false,
	}

	// Add routes with different prefixes
	router.AddRoute(ServiceRoute{
		Prefix:      "/api/llm",
		TargetURL:   "http://localhost:8745",
		StripPrefix: true,
		RequireAuth: false,
		Weight:      1,
		TimeoutSecs: 30,
	}, "round-robin", cfg)

	router.AddRoute(ServiceRoute{
		Prefix:      "/api/infinity",
		TargetURL:   "http://localhost:8000",
		StripPrefix: true,
		RequireAuth: true,
		Weight:      1,
		TimeoutSecs: 15,
	}, "round-robin", cfg)

	router.AddRoute(ServiceRoute{
		Prefix:      "/api",
		TargetURL:   "http://localhost:9000",
		StripPrefix: false,
		RequireAuth: false,
		Weight:      1,
		TimeoutSecs: 30,
	}, "round-robin", cfg)

	tests := []struct {
		name        string
		path        string
		wantNil     bool
		wantAuth    bool
		wantTimeout time.Duration
	}{
		{
			name:        "matches /api/llm prefix",
			path:        "/api/llm/v1/chat",
			wantNil:     false,
			wantAuth:    false,
			wantTimeout: 30 * time.Second,
		},
		{
			name:        "matches /api/infinity prefix with auth",
			path:        "/api/infinity/embeddings",
			wantNil:     false,
			wantAuth:    true,
			wantTimeout: 15 * time.Second,
		},
		{
			name:        "matches /api fallback (shortest prefix)",
			path:        "/api/unknown/endpoint",
			wantNil:     false,
			wantAuth:    false,
			wantTimeout: 30 * time.Second,
		},
		{
			name:    "no match for unregistered path",
			path:    "/other/path",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Empty host: these routes declare no Host, so they match any domain.
			matched := router.Route("", tt.path)
			if tt.wantNil {
				if matched != nil {
					t.Errorf("expected nil match for path %q, got non-nil", tt.path)
				}
				return
			}
			if matched == nil {
				t.Fatalf("expected non-nil match for path %q, got nil", tt.path)
			}
			if matched.RequireAuth != tt.wantAuth {
				t.Errorf("RequireAuth = %v, want %v", matched.RequireAuth, tt.wantAuth)
			}
			if matched.Timeout != tt.wantTimeout {
				t.Errorf("Timeout = %v, want %v", matched.Timeout, tt.wantTimeout)
			}
		})
	}
}

// ============= TestServiceRouter_StripPrefix =============

func TestServiceRouter_StripPrefix(t *testing.T) {
	router := NewServiceRouter()

	cfg := Config{
		MaxIdleConns:      10,
		IdleConnTimeout:   30 * time.Second,
		EnableCompression: false,
	}

	router.AddRoute(ServiceRoute{
		Prefix:      "/api/llm",
		TargetURL:   "http://localhost:8745",
		StripPrefix: true,
		RequireAuth: false,
		Weight:      1,
		TimeoutSecs: 30,
	}, "round-robin", cfg)

	router.AddRoute(ServiceRoute{
		Prefix:      "/api/admin",
		TargetURL:   "http://localhost:8742",
		StripPrefix: false,
		RequireAuth: false,
		Weight:      1,
		TimeoutSecs: 30,
	}, "round-robin", cfg)

	tests := []struct {
		name         string
		path         string
		wantBackPath string
	}{
		{
			name:         "strip prefix /api/llm",
			path:         "/api/llm/v1/chat",
			wantBackPath: "/v1/chat",
		},
		{
			name:         "strip prefix leaves root",
			path:         "/api/llm",
			wantBackPath: "/",
		},
		{
			name:         "no strip keeps full path",
			path:         "/api/admin/users",
			wantBackPath: "/api/admin/users",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Empty host: these routes declare no Host, so they match any domain.
			matched := router.Route("", tt.path)
			if matched == nil {
				t.Fatalf("expected non-nil match for path %q", tt.path)
			}
			if matched.BackendPath != tt.wantBackPath {
				t.Errorf("BackendPath = %q, want %q", matched.BackendPath, tt.wantBackPath)
			}
		})
	}
}

// ============= TestCircuitBreaker =============

func TestCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker(3, 100*time.Millisecond)

	// Should be closed initially
	if cb.GetState() != "closed" {
		t.Errorf("Initial state should be closed, got %s", cb.GetState())
	}

	// Successful calls keep it closed
	for i := 0; i < 3; i++ {
		cb.Call(func() error {
			return nil
		})
	}
	if cb.GetState() != "closed" {
		t.Errorf("Should remain closed after successful calls, got %s", cb.GetState())
	}

	// Failures should open the breaker
	for i := 0; i < 3; i++ {
		cb.Call(func() error {
			return fmt.Errorf("backend error")
		})
	}
	if cb.GetState() != "open" {
		t.Errorf("Should be open after %d failures, got %s", 3, cb.GetState())
	}

	// While open, calls should fail immediately
	err := cb.Call(func() error {
		return nil
	})
	if err == nil {
		t.Error("Expected error when circuit breaker is open")
	}

	// After reset timeout, should go to half-open
	time.Sleep(150 * time.Millisecond)
	err = cb.Call(func() error {
		return nil
	})
	if err != nil {
		t.Errorf("Expected success in half-open state, got %v", err)
	}
	if cb.GetState() != "closed" {
		t.Errorf("Should be closed after successful call in half-open, got %s", cb.GetState())
	}
}

// ============= TestHashAPIKey =============

func TestHashAPIKey(t *testing.T) {
	key := "test-api-key"
	hash1 := hashAPIKey(key)
	hash2 := hashAPIKey(key)

	if hash1 != hash2 {
		t.Errorf("Hash should be consistent: %s != %s", hash1, hash2)
	}

	if len(hash1) != 64 { // SHA256 produces 64 char hex string
		t.Errorf("Hash length should be 64, got %d", len(hash1))
	}

	// Different keys produce different hashes
	hash3 := hashAPIKey("different-key")
	if hash1 == hash3 {
		t.Errorf("Different keys should produce different hashes")
	}
}

// ============= TestGetClientIP =============

func TestGetClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xRealIP    string
		xForwarded string
		expected   string
	}{
		{
			name:       "X-Real-IP header",
			remoteAddr: "192.168.1.1:12345",
			xRealIP:    "10.0.0.1",
			xForwarded: "",
			expected:   "10.0.0.1",
		},
		{
			name:       "X-Forwarded-For header",
			remoteAddr: "192.168.1.1:12345",
			xRealIP:    "",
			xForwarded: "10.0.0.2, 10.0.0.3",
			expected:   "10.0.0.2",
		},
		{
			name:       "RemoteAddr fallback",
			remoteAddr: "192.168.1.1:12345",
			xRealIP:    "",
			xForwarded: "",
			expected:   "192.168.1.1",
		},
		{
			name:       "X-Real-IP takes precedence over X-Forwarded-For",
			remoteAddr: "192.168.1.1:12345",
			xRealIP:    "10.0.0.1",
			xForwarded: "10.0.0.2",
			expected:   "10.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/test", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.xRealIP != "" {
				r.Header.Set("X-Real-IP", tt.xRealIP)
			}
			if tt.xForwarded != "" {
				r.Header.Set("X-Forwarded-For", tt.xForwarded)
			}

			result := getClientIP(r)
			if result != tt.expected {
				t.Errorf("getClientIP() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// ============= TestIsPublicEndpoint =============

func TestIsPublicEndpoint(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/health", true},
		{"/metrics", true},
		{"/login", true},
		{"/register", true},
		{"/api/v1/public/test", true},
		{"/api/v1/private", false},
		{"/admin", false},
		{"/api/llm/v1/chat", false},
		{"/", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := isPublicEndpoint(tt.path)
			if result != tt.expected {
				t.Errorf("isPublicEndpoint(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

// ============= TestDDoSProtection =============

func TestDDoSProtection(t *testing.T) {
	t.Run("blocks after threshold exceeded", func(t *testing.T) {
		ddos := NewDDoSProtection(5, 1*time.Second)

		ip := "192.168.1.100"
		for i := 0; i < 5; i++ {
			blocked := ddos.RecordRequest(ip)
			if blocked {
				t.Errorf("Should not be blocked at request %d (threshold=5)", i+1)
			}
		}

		// The 6th request should trigger blocking
		blocked := ddos.RecordRequest(ip)
		if !blocked {
			t.Error("Should be blocked after exceeding threshold")
		}

		// Subsequent checks should show blocked
		if !ddos.IsBlocked(ip) {
			t.Error("IP should be blocked")
		}
	})

	t.Run("whitelist bypasses blocking", func(t *testing.T) {
		ddos := NewDDoSProtection(5, 1*time.Minute)

		ip := "10.0.0.1"
		ddos.AddToWhitelist(ip)

		// Even many requests should not block
		for i := 0; i < 100; i++ {
			ddos.RecordRequest(ip)
		}

		if ddos.IsBlocked(ip) {
			t.Error("Whitelisted IP should not be blocked")
		}
	})

	t.Run("blacklist always blocks", func(t *testing.T) {
		ddos := NewDDoSProtection(1000, 1*time.Minute)

		ip := "172.16.0.1"
		ddos.AddToBlacklist(ip)

		if !ddos.IsBlocked(ip) {
			t.Error("Blacklisted IP should be blocked")
		}
	})

	t.Run("block expires after duration", func(t *testing.T) {
		ddos := NewDDoSProtection(2, 100*time.Millisecond)

		ip := "192.168.2.1"
		// Exceed threshold
		ddos.RecordRequest(ip)
		ddos.RecordRequest(ip)
		ddos.RecordRequest(ip) // triggers block

		if !ddos.IsBlocked(ip) {
			t.Error("Should be blocked immediately after threshold exceeded")
		}

		// Wait for block to expire
		time.Sleep(150 * time.Millisecond)

		if ddos.IsBlocked(ip) {
			t.Error("Block should have expired")
		}
	})
}

// ============= TestSecurityHeadersMiddleware =============

func TestSecurityHeadersMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := SecurityHeadersMiddleware(inner)
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	expectedHeaders := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"X-XSS-Protection":       "1; mode=block",
	}

	for header, expected := range expectedHeaders {
		got := rec.Header().Get(header)
		if got != expected {
			t.Errorf("Header %q = %q, want %q", header, got, expected)
		}
	}
}

// ============= TestRequestIDMiddleware =============

func TestRequestIDMiddleware(t *testing.T) {
	t.Run("generates request ID when missing", func(t *testing.T) {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("x-request-id")
			if reqID == "" {
				t.Error("Expected x-request-id to be set on request")
			}
			w.WriteHeader(http.StatusOK)
		})

		handler := RequestIDMiddleware(inner)
		req := httptest.NewRequest("GET", "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		respID := rec.Header().Get("x-request-id")
		if respID == "" {
			t.Error("Expected x-request-id in response headers")
		}
	})

	t.Run("preserves existing request ID", func(t *testing.T) {
		existingID := "my-custom-id-12345"
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("x-request-id")
			if reqID != existingID {
				t.Errorf("Expected preserved request ID %q, got %q", existingID, reqID)
			}
			w.WriteHeader(http.StatusOK)
		})

		handler := RequestIDMiddleware(inner)
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("x-request-id", existingID)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		respID := rec.Header().Get("x-request-id")
		if respID != existingID {
			t.Errorf("Response x-request-id = %q, want %q", respID, existingID)
		}
	})
}

// ============= BenchmarkHashAPIKey =============

func BenchmarkHashAPIKey(b *testing.B) {
	key := "benchmark-api-key-12345"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hashAPIKey(key)
	}
}

// ============= TestServicePool_NextPeer =============

func TestServicePool_LeastConnections(t *testing.T) {
	pool := NewServicePool("least-conn")

	// Create mock backends (we cannot dial them but we can test selection logic)
	for i := 0; i < 3; i++ {
		b := &Backend{
			Alive:  true,
			Weight: 1,
		}
		pool.AddBackend(b)
	}

	// All alive with 0 connections -- should return one
	peer := pool.NextPeer("127.0.0.1")
	if peer == nil {
		t.Error("Expected a backend from NextPeer, got nil")
	}
}

func TestServicePool_NoAliveBackends(t *testing.T) {
	pool := NewServicePool("round-robin")

	b := &Backend{
		Alive:  false,
		Weight: 1,
	}
	pool.AddBackend(b)

	peer := pool.NextPeer("127.0.0.1")
	if peer != nil {
		t.Error("Expected nil when no backends are alive")
	}
}

func TestServicePool_EmptyPool(t *testing.T) {
	pool := NewServicePool("round-robin")

	peer := pool.NextPeer("127.0.0.1")
	if peer != nil {
		t.Error("Expected nil for empty pool")
	}
}
