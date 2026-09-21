package main

// Track J follow-up: Ollama's own OLLAMA_MODELS means "models directory", so a
// path value must never be registered as a servable model.

import (
	"strings"
	"testing"
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
