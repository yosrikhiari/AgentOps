package gpu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOllamaPSDecodesResident(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[
			{"name":"qwen2.5:3b-instruct","size":2500000000,"size_vram":2100000000,
			 "context_length":4096,"expires_at":"2026-11-16T12:00:00Z"}]}`))
	}))
	defer srv.Close()

	got, err := OllamaPS{URL: srv.URL}.Resident(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "qwen2.5:3b-instruct" {
		t.Fatalf("got %+v", got)
	}
	if got[0].SizeVRAMBytes != 2100000000 || got[0].ContextLength != 4096 {
		t.Fatalf("got %+v", got[0])
	}
	// Ollama's expires_at is RFC3339; it must survive as a real instant, not a
	// string, so the console can say "unloads in 4 minutes".
	if want := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC); !got[0].ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", got[0].ExpiresAt, want)
	}
}

// TestOllamaPSEmptyIsNotAnError: no resident model is a normal state
// (idle box), and the console shows "nothing resident", not an error.
func TestOllamaPSEmptyIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()
	got, err := OllamaPS{URL: srv.URL}.Resident(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// TestOllamaPSNon200IsAnError: a daemon that answers "404 page not found"
// is a different fact from an idle box, and the message must say which.
func TestOllamaPSNon200IsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer srv.Close()
	got, err := OllamaPS{URL: srv.URL}.Resident(context.Background())
	if err == nil {
		t.Fatalf("want error for 502, got %+v", got)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("error does not name the status: %q", err.Error())
	}
}
