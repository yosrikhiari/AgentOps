package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUnknownAPIPathUsesTheJSONErrorShape: every other response in the gateway is the
// JSON envelope, and an agent client parses JSON unconditionally. Falling through to
// net/http's plain-text "404 page not found" turned a typo'd path into a parse error
// instead of a code the caller can branch on.
func TestUnknownAPIPathUsesTheJSONErrorShape(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", &fakeBackend{name: "ollama", local: true, text: "hi"})
	cases := []struct{ method, path string }{
		{http.MethodGet, "/v1/nope"},
		{http.MethodPost, "/v1/chat/completion"}, // the typo that actually happens
		{http.MethodGet, "/v2/models"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: status %d, want 404 (%s)", c.method, c.path, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("%s %s: content type %q, want application/json", c.method, c.path, ct)
		}
		var body ErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s %s: body is not the error envelope: %q", c.method, c.path, rec.Body.String())
		}
		if body.Error.Code != "not_found" || body.Error.Message == "" {
			t.Fatalf("%s %s: body = %+v", c.method, c.path, body)
		}
	}
}

// TestKnownPathWrongMethodIs405WithAllow: a 404 for the right path with the wrong verb
// sends the caller looking for a route that exists. Say 405 and name the verb.
func TestKnownPathWrongMethodIs405WithAllow(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", &fakeBackend{name: "ollama", local: true, text: "hi"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/models", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405 (%s)", rec.Code, rec.Body.String())
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", allow)
	}
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not the error envelope: %q", rec.Body.String())
	}
	if body.Error.Code != "method_not_allowed" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

// TestProberLogsFirstStateAndTransitions: a local backend that is down from boot is the
// dangerous case - every request silently fails over to the cloud, and the only evidence
// used to be a line per request. The prober now says it once, and says when it returns.
func TestProberLogsFirstStateAndTransitions(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	local := &fakeBackend{name: "ollama", local: true, healthErr: errors.New("dial tcp [::1]:11434: connection refused")}
	p := NewHealthProber(NewMetrics(), local)
	p.ProbeOnce(context.Background())
	first := buf.String()
	if !strings.Contains(first, "backend ollama is unreachable") ||
		!strings.Contains(first, "fail over to a cloud backend") {
		t.Fatalf("boot-time outage not reported: %q", first)
	}
	buf.Reset()
	p.ProbeOnce(context.Background())
	if buf.Len() != 0 {
		t.Fatalf("still down must not repeat every tick: %q", buf.String())
	}
	local.healthErr = nil
	p.ProbeOnce(context.Background())
	if !strings.Contains(buf.String(), "backend ollama is reachable") {
		t.Fatalf("recovery not reported: %q", buf.String())
	}
}
