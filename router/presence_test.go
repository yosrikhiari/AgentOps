package router

// Track J — startup presence check (TDD RED: PulledModels, ErrModelNotPulled,
// Prober.Pulled and the /v1/models reason do not exist yet).

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestOllamaPulledModelsDecodesTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen2.5:3b-instruct:latest"},{"name":"nomic-embed-text:latest"}]}`))
	}))
	defer srv.Close()
	c := NewOllamaClient(srv.URL)
	pulled, err := c.PulledModels(context.Background())
	if err != nil {
		t.Fatalf("PulledModels: %v", err)
	}
	if !pulled["qwen2.5:3b-instruct"] {
		t.Fatal("fast model without :latest tag should read as pulled")
	}
	if !pulled["qwen2.5:3b-instruct:latest"] {
		t.Fatal("full tag name should read as pulled")
	}
	if pulled["qwen2.5:7b-instruct-q4_K_M"] {
		t.Fatal("missing quality model must not read as pulled")
	}
}

func TestOllamaPulledModelsDownIsError(t *testing.T) {
	c := NewOllamaClient("http://127.0.0.1:1")
	if _, err := c.PulledModels(context.Background()); err == nil {
		t.Fatal("unreachable Ollama must be an error, not an empty set")
	}
}

// presenceServer has both tiers on one ollama backend whose pulled set holds
// only the fast model.
func presenceServer() (*Server, *fakeBackend) {
	local := &fakeBackend{name: "ollama", local: true, text: "hi"}
	srv := NewServer("qwen2.5:3b-instruct", "qwen2.5:7b-instruct-q4_K_M", local)
	p := NewHealthProber(srv.Metrics, local)
	p.pulled = map[string]map[string]bool{"ollama": {"qwen2.5:3b-instruct": true}}
	srv.Prober = p
	return srv, local
}

func TestPresenceSkipsUnpulled(t *testing.T) {
	srv, local := presenceServer()
	ctx := context.Background()
	long := strings.Repeat("a", 201)
	msgs := []Message{{Role: "user", Content: long}}

	// Auto-tier classifies quality, whose model is not on disk: no 502 surprise.
	_, err := srv.Complete(ctx, ChatRequest{Messages: msgs}, nil)
	var notPulled ErrModelNotPulled
	if !errors.As(err, &notPulled) || notPulled.Model != "qwen2.5:7b-instruct-q4_K_M" {
		t.Fatalf("auto quality with missing model: got %v", err)
	}
	if local.calls != 0 {
		t.Fatalf("missing model must never be attempted, calls=%d", local.calls)
	}

	// Explicit request for the missing model names it.
	_, err = srv.Complete(ctx, ChatRequest{Model: "qwen2.5:7b-instruct-q4_K_M", Messages: msgs}, nil)
	if !errors.As(err, &notPulled) {
		t.Fatalf("explicit missing model: got %v", err)
	}

	// Unknown names are still unknown, not "not pulled".
	_, err = srv.Complete(ctx, ChatRequest{Model: "nope", Messages: msgs}, nil)
	var unknown ErrUnknownModel
	if !errors.As(err, &unknown) {
		t.Fatalf("unknown model: got %v", err)
	}

	// The pulled tier still serves.
	if _, err = srv.Complete(ctx, ChatRequest{Model: "qwen2.5:3b-instruct", Messages: msgs}, nil); err != nil {
		t.Fatalf("pulled model must serve: %v", err)
	}
	if local.calls != 1 {
		t.Fatalf("exactly one attempt, calls=%d", local.calls)
	}
}

func TestPresenceNilProberIsPassthrough(t *testing.T) {
	local := &fakeBackend{name: "ollama", local: true, text: "hi"}
	srv := NewServer("qwen2.5:3b-instruct", "qwen2.5:7b-instruct-q4_K_M", local)
	long := strings.Repeat("a", 201)
	if _, err := srv.Complete(context.Background(),
		ChatRequest{Messages: []Message{{Role: "user", Content: long}}}, nil); err != nil {
		t.Fatalf("no prober means no presence info, must not filter: %v", err)
	}
	if local.calls != 1 {
		t.Fatalf("calls=%d, want 1", local.calls)
	}
}

func TestModelsShowsNotPulledReason(t *testing.T) {
	srv, _ := presenceServer()
	srv.Prober.ProbeOnce(context.Background()) // fake is healthy: backend up
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"reason":"not_pulled"`) {
		t.Fatalf("missing model needs reason not_pulled: %s", body)
	}
	if strings.Count(body, `"up":false`) != 1 {
		t.Fatalf("exactly the missing model is down: %s", body)
	}
}

func TestRefreshPulledLogsGapOnce(t *testing.T) {
	srv, _ := presenceServer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	srv.RefreshPulled(context.Background())
	if !strings.Contains(buf.String(), `"qwen2.5:7b-instruct-q4_K_M" not pulled`) {
		t.Fatalf("gap not logged: %q", buf.String())
	}
	buf.Reset()
	srv.RefreshPulled(context.Background())
	if buf.Len() != 0 {
		t.Fatalf("gap re-logged on second refresh: %q", buf.String())
	}
	// Model reappears → forgotten → a later gap logs again.
	srv.Prober.pulled["ollama"]["qwen2.5:7b-instruct-q4_K_M"] = true
	srv.RefreshPulled(context.Background())
	delete(srv.Prober.pulled["ollama"], "qwen2.5:7b-instruct-q4_K_M")
	buf.Reset()
	srv.RefreshPulled(context.Background())
	if !strings.Contains(buf.String(), "not pulled") {
		t.Fatal("gap after reappearance not logged")
	}
}

func TestAfterProbeRunsEachTick(t *testing.T) {
	local := &fakeBackend{name: "ollama", local: true}
	p := NewHealthProber(NewMetrics(), local)
	calls := 0
	p.AfterProbe = func(context.Context) { calls++ }
	p.ProbeOnce(context.Background())
	if calls != 1 {
		t.Fatalf("AfterProbe calls=%d, want 1", calls)
	}
}
