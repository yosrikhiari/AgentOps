package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// fakeBackend answers with canned text, records calls, and can fail or stream.
type fakeBackend struct {
	name          string
	local         bool
	text          string
	tokens        int
	err           error
	healthErr     error
	calls         int
	model         string
	lastMaxTokens int
	lastMsgs      []Message
	deltas        []string
}

func (f *fakeBackend) Name() string { return f.name }
func (f *fakeBackend) Local() bool  { return f.local }
func (f *fakeBackend) Generate(ctx context.Context, model string, msgs []Message, maxTokens int) (string, Usage, error) {
	f.calls++
	f.model, f.lastMaxTokens, f.lastMsgs = model, maxTokens, msgs
	if f.err != nil {
		return "", Usage{}, f.err
	}
	return f.text, Usage{PromptTokens: f.tokens / 2, CompletionTokens: f.tokens - f.tokens/2, TotalTokens: f.tokens}, nil
}
func (f *fakeBackend) Stream(ctx context.Context, model string, msgs []Message, maxTokens int, emit func(string)) (Usage, error) {
	f.calls++
	f.model = model
	if f.err != nil {
		return Usage{}, f.err
	}
	for _, d := range f.deltas {
		emit(d)
	}
	return Usage{TotalTokens: f.tokens}, nil
}
func (f *fakeBackend) Health(ctx context.Context) error { return f.healthErr }

func localFake(text string, tokens int) *fakeBackend {
	return &fakeBackend{name: "ollama", local: true, text: text, tokens: tokens}
}

func post(srv *Server, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(body)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestHandleChatRoutesFast(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("hello", 4))
	rec := post(srv, `{"prompt":"hi"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Model != "fast-m" || out.Reason != FastReason || out.TraceID == "" || out.Text != "hello" {
		t.Fatalf("bad response %+v", out)
	}
	if rec.Header().Get("X-Trace-ID") != out.TraceID {
		t.Fatalf("X-Trace-ID header %q != %q", rec.Header().Get("X-Trace-ID"), out.TraceID)
	}
}

// The OpenAI shape in and out: messages[] request, choices[0].message + usage response,
// and a chat.completion object id. The legacy prompt body keeps working alongside.
func TestOpenAIShape(t *testing.T) {
	be := localFake("bonjour", 10)
	srv := NewServer("fast-m", "quality-m", be)
	rec := post(srv, `{"model":"auto","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],"max_tokens":42}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out ChatResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Object != "chat.completion" || !strings.HasPrefix(out.ID, "chatcmpl-") || len(out.Choices) != 1 {
		t.Fatalf("not an OpenAI completion: %+v", out)
	}
	if out.Choices[0].Message.Role != "assistant" || out.Choices[0].Message.Content != "bonjour" || out.Choices[0].FinishReason != "stop" {
		t.Fatalf("bad choice %+v", out.Choices[0])
	}
	if out.Usage.TotalTokens != 10 || out.Usage.PromptTokens+out.Usage.CompletionTokens != 10 {
		t.Fatalf("bad usage %+v", out.Usage)
	}
	if len(be.lastMsgs) != 2 || be.lastMsgs[0].Role != "system" || be.lastMaxTokens != 42 {
		t.Fatalf("messages/max_tokens not forwarded: %+v %d", be.lastMsgs, be.lastMaxTokens)
	}
}

func TestExplicitModel(t *testing.T) {
	be := localFake("x", 1)
	srv := NewServer("fast-m", "quality-m", be)
	rec := post(srv, `{"model":"quality-m","messages":[{"role":"user","content":"hi"}]}`, nil)
	var out ChatResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || out.Model != "quality-m" || out.Reason != ExplicitReason {
		t.Fatalf("explicit model: %d %+v", rec.Code, out)
	}
	rec = post(srv, `{"model":"gpt-9","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown_model") {
		t.Fatalf("unknown model: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatBadRequest(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("x", 1))
	if rec := post(srv, `{}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	if rec := post(srv, `{not json`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestHandleChatOllamaDown(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", &fakeBackend{name: "ollama", local: true, err: errors.New("down")})
	rec := post(srv, `{"prompt":"hi"}`, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d", rec.Code)
	}
	var out ErrorBody
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Error.TraceID == "" || out.Error.Code != "ollama_unavailable" {
		t.Fatalf("bad error %+v", out)
	}
}

func TestMetricsRecordedPerRequest(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("hello", 12))
	for i := 0; i < 3; i++ {
		if rec := post(srv, `{"prompt":"hi"}`, nil); rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	out := rec.Body.String()
	for _, want := range []string{
		`router_requests_total{model="fast-m"} 3`,
		`router_tokens_total{model="fast-m"} 36`,
		`router_latency_seconds_bucket{model="fast-m",le="0.05"}`,
		`router_errors_total{model="fast-m"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "le=0.05") {
		t.Fatalf("unquoted le label leaks:\n%s", out)
	}
}

func TestMaxTokensForwarded(t *testing.T) {
	be := localFake("hello", 1)
	srv := NewServer("fast-m", "quality-m", be)
	if rec := post(srv, `{"prompt":"hi","max_tokens":42}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if be.lastMaxTokens != 42 {
		t.Fatalf("max_tokens not forwarded, got %d", be.lastMaxTokens)
	}
}

func TestOllamaDownCountsError(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", &fakeBackend{name: "ollama", local: true, err: errors.New("down")})
	if rec := post(srv, `{"prompt":"hi"}`, nil); rec.Code != http.StatusBadGateway {
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

// ---- streaming ----

func readSSE(t *testing.T, body string) (deltas []string, final map[string]any, done bool) {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			done = true
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			t.Fatalf("bad SSE json %q: %v", data, err)
		}
		if ch, ok := m["choices"].([]any); ok && len(ch) > 0 {
			c := ch[0].(map[string]any)
			if d, ok := c["delta"].(map[string]any)["content"].(string); ok && d != "" {
				deltas = append(deltas, d)
			}
			if c["finish_reason"] == "stop" {
				final = m
			}
		}
	}
	return
}

func TestStreamSSE(t *testing.T) {
	be := localFake("", 7)
	be.deltas = []string{"Hel", "lo", "!"}
	srv := NewServer("fast-m", "quality-m", be)
	rec := post(srv, `{"messages":[{"role":"user","content":"hi"}],"stream":true}`, nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d ct %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	deltas, final, done := readSSE(t, rec.Body.String())
	if strings.Join(deltas, "") != "Hello!" || !done || final == nil {
		t.Fatalf("deltas=%v done=%v final=%v", deltas, done, final)
	}
	if final["usage"].(map[string]any)["total_tokens"].(float64) != 7 {
		t.Fatalf("usage missing in final chunk: %v", final)
	}
	// counted once, with the streamed usage
	if snap := srv.Metrics.Snapshot()["fast-m"]; snap.Requests != 1 || snap.Tokens != 7 {
		t.Fatalf("metrics after stream: %+v", snap)
	}
}

// ---- fallback + fail-closed ----

func twoBackends(localErr error) (*Server, *fakeBackend, *fakeBackend) {
	local := &fakeBackend{name: "ollama", local: true, text: "local", tokens: 1, err: localErr}
	cloud := &fakeBackend{name: "groq", local: false, text: "cloud", tokens: 1}
	srv := NewServer("fast-m", "quality-m", local)
	srv.AddModel(ModelRef{Tier: "cloud", Model: "llama-8b", Backend: cloud})
	return srv, local, cloud
}

func TestFallsBackToCloudWhenLocalFails(t *testing.T) {
	srv, local, cloud := twoBackends(errors.New("ollama down"))
	rec := post(srv, `{"prompt":"hi"}`, nil)
	var out ChatResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || out.Backend != "groq" || out.Text != "cloud" || out.Fallback == "" {
		t.Fatalf("expected cloud fallback: %d %+v", rec.Code, out)
	}
	if local.calls != 2 || cloud.calls != 1 { // fast-m, quality-m both local and failing, then cloud
		t.Fatalf("calls local=%d cloud=%d", local.calls, cloud.calls)
	}
}

// The fail-closed rule: sensitive data never reaches a cloud backend, even when every
// local backend is down and the cloud one is healthy.
func TestSensitiveNeverLeavesBox(t *testing.T) {
	for name, hdr := range map[string]map[string]string{
		"header":  {"X-AgentOps-Sensitive": "true"},
		"keyword": nil,
	} {
		srv, _, cloud := twoBackends(errors.New("ollama down"))
		body := `{"prompt":"hi"}`
		if name == "keyword" {
			body = `{"prompt":"my IBAN is TN59 1000 6035 1835 9847 8831"}`
		}
		rec := post(srv, body, hdr)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("%s: sensitive request should fail closed, got %d %s", name, rec.Code, rec.Body.String())
		}
		if cloud.calls != 0 {
			t.Fatalf("%s: cloud backend was called %d times for sensitive data", name, cloud.calls)
		}
	}
	// and an explicit cloud model on sensitive data is refused up front
	srv, _, cloud := twoBackends(nil)
	rec := post(srv, `{"model":"llama-8b","prompt":"the password is hunter2"}`, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "sensitive_cloud_blocked") || cloud.calls != 0 {
		t.Fatalf("explicit cloud + sensitive: %d %s", rec.Code, rec.Body.String())
	}
}

// ---- API keys ----

func keyedServer(t *testing.T) (*Server, *MemKeyStore) {
	t.Helper()
	srv := NewServer("fast-m", "quality-m", localFake("ok", 10))
	ks := NewMemKeyStore()
	srv.Keys = ks
	srv.RequireKey = true
	return srv, ks
}

func TestAPIKeyRequired(t *testing.T) {
	srv, ks := keyedServer(t)
	if rec := post(srv, `{"prompt":"hi"}`, nil); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "missing_api_key") {
		t.Fatalf("no key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(srv, `{"prompt":"hi"}`, map[string]string{"Authorization": "Bearer ak_nope"}); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid_api_key") {
		t.Fatalf("bad key: %d %s", rec.Code, rec.Body.String())
	}
	secret := ks.Create("alice", 0, 0)
	if rec := post(srv, `{"prompt":"hi"}`, map[string]string{"Authorization": "Bearer " + secret}); rec.Code != http.StatusOK {
		t.Fatalf("good key: %d %s", rec.Code, rec.Body.String())
	}
	ks.Disable(secret)
	if rec := post(srv, `{"prompt":"hi"}`, map[string]string{"Authorization": "Bearer " + secret}); rec.Code != http.StatusForbidden {
		t.Fatalf("disabled key: %d", rec.Code)
	}
}

func TestAPIKeyRateLimit(t *testing.T) {
	srv, ks := keyedServer(t)
	secret := ks.Create("bob", 2, 0)
	h := map[string]string{"Authorization": "Bearer " + secret}
	for i := 0; i < 2; i++ {
		if rec := post(srv, `{"prompt":"hi"}`, h); rec.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	rec := post(srv, `{"prompt":"hi"}`, h)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "rate_limited") {
		t.Fatalf("third request: %d retry-after=%q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	// next minute the window resets
	srv.Limiter.now = func() time.Time { return time.Now().Add(61 * time.Second) }
	if rec := post(srv, `{"prompt":"hi"}`, h); rec.Code != http.StatusOK {
		t.Fatalf("after window: %d", rec.Code)
	}
}

func TestAPIKeyBudget(t *testing.T) {
	srv, ks := keyedServer(t)
	secret := ks.Create("carol", 0, 15) // each fake answer costs 10 tokens
	h := map[string]string{"Authorization": "Bearer " + secret}
	if rec := post(srv, `{"prompt":"hi"}`, h); rec.Code != http.StatusOK {
		t.Fatalf("first: %d", rec.Code)
	}
	// usage is charged asynchronously; wait for it
	deadline := time.Now().Add(2 * time.Second)
	for {
		k, _ := ks.Lookup(context.Background(), HashSecret(secret))
		if k.TokensUsed == 10 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec := post(srv, `{"prompt":"hi"}`, h); rec.Code != http.StatusOK { // 10 < 15, still allowed
		t.Fatalf("second: %d %s", rec.Code, rec.Body.String())
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		k, _ := ks.Lookup(context.Background(), HashSecret(secret))
		if k.TokensUsed == 20 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec := post(srv, `{"prompt":"hi"}`, h); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "budget_exceeded") {
		t.Fatalf("over budget: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(srv.Metrics.Expose(), `router_key_rejected_total{key="carol"} 1`) {
		t.Fatalf("rejection not counted:\n%s", srv.Metrics.Expose())
	}
}

// ---- health ----

func TestHealthProberGaugeAndModels(t *testing.T) {
	local := localFake("x", 1)
	cloud := &fakeBackend{name: "groq", healthErr: errors.New("401")}
	srv := NewServer("fast-m", "quality-m", local)
	srv.AddModel(ModelRef{Tier: "cloud", Model: "llama-8b", Backend: cloud})
	srv.Prober = NewHealthProber(srv.Metrics, local, cloud)
	srv.Prober.ProbeOnce(context.Background())
	out := srv.Metrics.Expose()
	if !strings.Contains(out, `router_backend_up{backend="ollama"} 1`) || !strings.Contains(out, `router_backend_up{backend="groq"} 0`) {
		t.Fatalf("gauge:\n%s", out)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var list struct {
		Data []struct {
			ID    string `json:"id"`
			Tier  string `json:"tier"`
			Local bool   `json:"local"`
			Up    *bool  `json:"up"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Data) != 3 || list.Data[2].ID != "llama-8b" || list.Data[2].Local || list.Data[2].Up == nil || *list.Data[2].Up {
		t.Fatalf("models: %s", rec.Body.String())
	}
	// a fallback known to be down is skipped instead of timing out
	local.err = errors.New("down")
	if rec := post(srv, `{"prompt":"hi"}`, nil); rec.Code != http.StatusBadGateway || cloud.calls != 0 {
		t.Fatalf("down fallback should be skipped: %d cloud calls=%d", rec.Code, cloud.calls)
	}
}
