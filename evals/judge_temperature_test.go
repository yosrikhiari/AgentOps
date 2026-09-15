package evals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The local judge must be deterministic: every Ollama call it makes carries temperature 0.
func TestOllamaJudgeSendsTemperatureZero(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"response":"quoted evidence.\nVERDICT: SUPPORTED"}`))
	}))
	defer srv.Close()
	j := NewOllamaJudge(srv.URL, "m")
	ok, _, err := j.JudgeClaim(context.Background(), "claim text here", "context")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	opts, _ := got["options"].(map[string]any)
	if temp, _ := opts["temperature"].(float64); temp != 0 || opts == nil {
		t.Fatalf("judge request options = %v, want temperature 0", got["options"])
	}
}
