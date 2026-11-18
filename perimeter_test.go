package main

// Perimeter tests (audit 2026-11-18, Phase 0). The gateway's key check only ever
// guarded /v1/chat/*; the console, its mutating endpoints and the prompt history
// behind them were reachable by any host that could open a TCP connection. These
// tests pin the three controls that close that: a loopback default bind, a
// local-only console unless it is opted out, and browser-request defences.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDefaultAddrIsLoopback: the default deployment must be reachable from this
// machine only. "Bind to everything and remember to set auth" is the failure this
// pins shut; an operator who wants a wider bind says so with ADDR.
func TestDefaultAddrIsLoopback(t *testing.T) {
	t.Setenv("ADDR", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got, want := cfg.Addr, "127.0.0.1:8080"; got != want {
		t.Fatalf("default ADDR = %q, want %q — a default install must not listen on every interface", got, want)
	}
}

// TestExplicitAddrIsHonoured: the loopback default must not become a hard rule.
func TestExplicitAddrIsHonoured(t *testing.T) {
	t.Setenv("ADDR", "0.0.0.0:9000")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Addr != "0.0.0.0:9000" {
		t.Fatalf("ADDR = %q, want the operator's value", cfg.Addr)
	}
}

// TestConsoleGuardKeepsRemoteClientsOut: the console returns prompts verbatim
// (GET /v1/conversations/{id}/messages) and can start GPU work, so a client that
// is not this machine is refused unless the operator opted in.
func TestConsoleGuardKeepsRemoteClientsOut(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	guard := consoleGuard(false, ok)

	cases := []struct {
		name   string
		remote string
		want   int
	}{
		{"ipv4 loopback", "127.0.0.1:51000", http.StatusNoContent},
		{"ipv6 loopback", "[::1]:51000", http.StatusNoContent},
		{"lan peer", "192.168.1.44:51000", http.StatusForbidden},
		{"bridge gateway", "172.17.0.1:51000", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/conversations", nil)
			req.RemoteAddr = c.remote
			rec := httptest.NewRecorder()
			guard.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("remote %s: status %d, want %d", c.remote, rec.Code, c.want)
			}
			if rec.Code == http.StatusForbidden {
				if !strings.Contains(rec.Body.String(), "console_local_only") {
					t.Fatalf("refusal must carry the one error shape: %s", rec.Body.String())
				}
			}
		})
	}
}

// TestConsoleGuardOptIn: CONSOLE_REMOTE is the escape hatch for a container or a
// deliberate remote console, and it must actually work.
func TestConsoleGuardOptIn(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodGet, "/v1/conversations", nil)
	req.RemoteAddr = "192.168.1.44:51000"
	rec := httptest.NewRecorder()
	consoleGuard(true, ok).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("with CONSOLE_REMOTE the remote client is served, got %d", rec.Code)
	}
}

// TestSecurityHeaders: the console is an admin surface with no CSP today. These
// headers are cheap and stop framing, MIME sniffing and referrer leakage.
func TestSecurityHeaders(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	want := map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, must := range []string{"default-src 'self'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, must) {
			t.Errorf("CSP %q is missing %q", csp, must)
		}
	}
}

// TestCrossSitePostIsRejected: a malicious page must not be able to POST
// /v1/evals/run or DELETE a conversation through the operator's browser. The
// stdlib recognises the browser's own metadata; a non-browser client (curl, an
// SDK) sends none of it and must keep working.
func TestCrossSitePostIsRejected(t *testing.T) {
	h := withCrossOriginProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	crossSite := httptest.NewRequest(http.MethodPost, "http://localhost:8080/v1/evals/run", strings.NewReader("{}"))
	crossSite.Header.Set("Origin", "https://evil.example")
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, crossSite)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d, want 403", rec.Code)
	}

	curl := httptest.NewRequest(http.MethodPost, "http://localhost:8080/v1/evals/run", strings.NewReader("{}"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, curl)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("non-browser POST = %d, want it served", rec.Code)
	}
}
