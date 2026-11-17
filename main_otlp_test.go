package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestOTLPExporterShipsASpan is the transport's contract: one POST per batch, the OTLP
// JSON content type, and the gen_ai.* aliases present in the body.
func TestOTLPExporterShipsASpan(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content type = %q", ct)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := newOTLPExporter(srv.URL)
	e.Add(spanRecord{traceID: "aad6e9c4658f7842", spanID: "a1", name: "model.generate",
		attrs: `{"model":"phi4-mini","prompt_tokens":7,"latency_s":0.5}`, at: time.Now().UTC()})
	e.Close(2 * time.Second)
	if e.sent.Load() != 1 || e.dropped.Load() != 0 {
		t.Fatalf("sent/dropped = %d/%d", e.sent.Load(), e.dropped.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("bodies = %d", len(bodies))
	}
	for _, want := range []string{`"gen_ai.operation.name"`, `"chat"`, `"gen_ai.request.model"`, `"resourceSpans"`} {
		if !strings.Contains(bodies[0], want) {
			t.Fatalf("payload missing %s: %s", want, bodies[0])
		}
	}
}

// TestOTLPExporterNeverShipsPromptText: the exporter re-runs the store's redaction, so a
// span whose attrs still carried content cannot leak it to a collector.
func TestOTLPExporterNeverShipsPromptText(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	e := newOTLPExporter(srv.URL)
	e.Add(spanRecord{traceID: "aa", spanID: "bb", name: "model.generate",
		attrs: `{"prompt":"the secret plan","text":"the secret plan","model":"m"}`, at: time.Now().UTC()})
	e.Close(2 * time.Second)
	if strings.Contains(body, "secret") {
		t.Fatalf("prompt text reached the collector: %s", body)
	}
}

// TestOTLPExporterDoesNotBlockOnADeadCollector: the export is a copy, never a dependency.
// Add must return immediately even with nothing listening, and Close must not hang.
func TestOTLPExporterDoesNotBlockOnADeadCollector(t *testing.T) {
	e := newOTLPExporter("http://127.0.0.1:1/v1/traces")
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			e.Add(spanRecord{traceID: "aa", spanID: "bb", name: "route.decide", attrs: `{}`, at: time.Now()})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Add blocked on a dead collector")
	}
	start := time.Now()
	e.Close(3 * time.Second)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Close took %s with a dead collector", elapsed)
	}
	if e.dropped.Load() == 0 {
		t.Fatal("a dead collector must count dropped spans, not pretend they were sent")
	}
}
