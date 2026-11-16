package main

// Track J follow-up: Ollama's own OLLAMA_MODELS means "models directory", so a
// path value must never be registered as a servable model.

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentops/gpu"
	"agentops/tracker"
)

func TestExtraModelsSkipsPaths(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", `C:\Users\yosri\.ollama,qwen3:8b`)
	t.Setenv("GROQ_API_KEY", "")
	srv, _ := buildBackends(Config{OllamaURL: "http://127.0.0.1:1", FastModel: "fast-m", QualityModel: "quality-m"})
	for _, ref := range srv.Models {
		if strings.ContainsAny(ref.Model, `/\`) {
			t.Fatalf("path registered as model: %q", ref.Model)
		}
	}
	found := false
	for _, ref := range srv.Models {
		if ref.Model == "qwen3:8b" {
			found = true
		}
	}
	if !found {
		t.Fatal("real extra model qwen3:8b missing")
	}
}

// TestTraceJSONIncludesNearestGPUSample: a span set plus a nearest-sample
// reader becomes one body. gpu_ok is the flag the page keys on: a zero sample
// must never be distinguishable from a real 0% reading by omission alone.
func TestTraceJSONIncludesNearestGPUSample(t *testing.T) {
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	spans := []tracker.Span{
		{TraceID: "t1", SpanID: "s1", Name: "chat", StartedAt: start},
		{TraceID: "t1", SpanID: "s2", Name: "model.generate", StartedAt: start.Add(time.Second)},
	}
	out := traceJSON(context.Background(), "t1", spans, func(_ context.Context, at time.Time) (gpu.Sample, bool, error) {
		if !at.Equal(start) {
			t.Fatalf("correlated on %v, want the first span's start %v", at, start)
		}
		return gpu.Sample{At: start, Name: "GTX 1650", UtilPct: 39, MemUsedBytes: 77 << 20}, true, nil
	})
	if out["gpu_ok"] != true {
		t.Fatalf("gpu_ok = %v", out["gpu_ok"])
	}
	g, ok := out["gpu"].(gpu.Sample)
	if !ok || g.UtilPct != 39 {
		t.Fatalf("gpu = %#v", out["gpu"])
	}
}

// TestTraceJSONOmitsGPUWithoutASample: no sample in the window is not "0%".
// The keys must be absent so the page renders no chip at all.
func TestTraceJSONOmitsGPUWithoutASample(t *testing.T) {
	out := traceJSON(context.Background(), "t1", []tracker.Span{{TraceID: "t1", StartedAt: time.Now()}},
		func(context.Context, time.Time) (gpu.Sample, bool, error) {
			return gpu.Sample{}, false, nil
		})
	if _, ok := out["gpu"]; ok {
		t.Fatal("gpu present without a sample")
	}
	if _, ok := out["gpu_ok"]; ok {
		t.Fatal("gpu_ok present without a sample")
	}
}
