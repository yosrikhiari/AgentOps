package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClassifyShortGoesFast(t *testing.T) {
	tier, reason := Classify("hi")
	if tier != "fast" || reason != FastReason {
		t.Fatalf("got %s %s", tier, reason)
	}
}

func TestClassifyLongGoesQuality(t *testing.T) {
	tier, reason := Classify(strings.Repeat("a", 201))
	if tier != "quality" || reason != QualityReason {
		t.Fatalf("got %s %s", tier, reason)
	}
}

func TestClassifyKeywordGoesQuality(t *testing.T) {
	tier, _ := Classify("please compare these two contracts")
	if tier != "quality" {
		t.Fatalf("got %s", tier)
	}
}

type fakeGen struct {
	text          string
	tokens        int
	err           error
	model         string
	lastMaxTokens int
}

func (f *fakeGen) Generate(model, prompt string, maxTokens int) (string, int, error) {
	f.lastMaxTokens = maxTokens
	f.model = model
	return f.text, f.tokens, f.err
}

func TestHandleChatRoutesFast(t *testing.T) {
	gen := &fakeGen{text: "hello"}
	srv := NewServer("fast-m", "quality-m", gen)
	body, _ := json.Marshal(ChatRequest{Prompt: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Model != "fast-m" || out.Reason != FastReason || out.TraceID == "" {
		t.Fatalf("bad response %+v", out)
	}
}

func TestHandleChatBadRequest(t *testing.T) {
	gen := &fakeGen{text: "x"}
	srv := NewServer("fast-m", "quality-m", gen)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestHandleChatOllamaDown(t *testing.T) {
	gen := &fakeGen{err: errors.New("down")}
	srv := NewServer("fast-m", "quality-m", gen)
	body, _ := json.Marshal(ChatRequest{Prompt: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d", rec.Code)
	}
	var out ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Error.TraceID == "" {
		t.Fatal("missing trace_id")
	}
}

func TestMetricsRecordedPerRequest(t *testing.T) {
	gen := &fakeGen{text: "hello", tokens: 12}
	srv := NewServer("fast-m", "quality-m", gen)
	for i := 0; i < 3; i++ {
		body, _ := json.Marshal(ChatRequest{Prompt: "hi"})
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	out := rec.Body.String()
	if !strings.Contains(out, `router_requests_total{model="fast-m"} 3`) {
		t.Fatalf("missing requests counter:\n%s", out)
	}
	if !strings.Contains(out, `router_tokens_total{model="fast-m"} 36`) {
		t.Fatalf("missing tokens counter:\n%s", out)
	}
	if !strings.Contains(out, "router_latency_seconds_bucket") {
		t.Fatalf("missing histogram:\n%s", out)
	}
	// Prometheus exposition: every label value must be quoted, `le` included.
	if !strings.Contains(out, `router_latency_seconds_bucket{model="fast-m",le="0.05"}`) {
		t.Fatalf("le label must be quoted:\n%s", out)
	}
	if strings.Contains(out, "le=0.05") {
		t.Fatalf("unquoted le label leaks:\n%s", out)
	}
	if !strings.Contains(out, `router_errors_total{model="fast-m"} 0`) {
		t.Fatalf("missing errors counter:\n%s", out)
	}
}

func TestMaxTokensForwarded(t *testing.T) {
	gen := &fakeGen{text: "hello", tokens: 1}
	srv := NewServer("fast-m", "quality-m", gen)
	body, _ := json.Marshal(ChatRequest{Prompt: "hi", MaxTokens: 42})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if gen.lastMaxTokens != 42 {
		t.Fatalf("max_tokens not forwarded, got %d", gen.lastMaxTokens)
	}
}

func TestOllamaDownCountsError(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", &fakeGen{err: errors.New("down")})
	body, _ := json.Marshal(ChatRequest{Prompt: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d", rec.Code)
	}
	snap := srv.Metrics.Snapshot()["fast-m"]
	if snap.Errors != 1 || snap.Requests != 0 {
		t.Fatalf("errors=%d requests=%d, want 1/0", snap.Errors, snap.Requests)
	}
}

// A histogram bucket is cumulative: every bucket must be <= the next one and the last
// finite bucket must equal +Inf (= _count). Three ~0s requests must read 3 everywhere.
func TestHistogramBucketsAreCumulativeOnce(t *testing.T) {
	m := NewMetrics()
	for i := 0; i < 3; i++ {
		m.Observe("m", 0.001, 1)
	}
	out := m.Expose()
	for _, b := range latencyBuckets {
		want := fmt.Sprintf("router_latency_seconds_bucket{model=\"m\",le=\"%g\"} 3\n", b)
		if !strings.Contains(out, want) {
			t.Fatalf("bucket le=%g should be 3 (double-cumulated?):\n%s", b, out)
		}
	}
	if !strings.Contains(out, `router_latency_seconds_bucket{model="m",le="+Inf"} 3`) {
		t.Fatalf("+Inf should be 3:\n%s", out)
	}
	if !strings.Contains(out, `router_latency_seconds_count{model="m"} 3`) {
		t.Fatalf("_count should be 3:\n%s", out)
	}
}
