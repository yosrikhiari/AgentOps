package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A backend that also implements ParamBackend, recording what it was handed.
type paramFake struct {
	fakeBackend
	lastParams GenParams
	withCalls  int
}

func (f *paramFake) GenerateWith(ctx context.Context, model string, msgs []Message, p GenParams) (string, Usage, error) {
	f.withCalls++
	f.lastParams = p
	return f.Generate(ctx, model, msgs, p.MaxTokens)
}

func (f *paramFake) StreamWith(ctx context.Context, model string, msgs []Message, p GenParams, emit func(string)) (Usage, error) {
	f.withCalls++
	f.lastParams = p
	return f.Stream(ctx, model, msgs, p.MaxTokens, emit)
}

func spanRecorder(spans map[string]map[string]any) SpanSink {
	return func(traceID, spanID, parentID, name, attrs string) {
		var kv map[string]any
		_ = json.Unmarshal([]byte(attrs), &kv)
		spans[name] = kv
	}
}

// v1.1: the OpenAI-shaped parameters, the Ollama-shaped options / format / keep_alive, and
// a string-or-list stop all reach a ParamBackend; a plain Backend still gets max_tokens.
func TestParamsReachParamBackend(t *testing.T) {
	pf := &paramFake{fakeBackend: fakeBackend{name: "ollama", local: true, text: "ok", tokens: 2}}
	srv := NewServer("fast-m", "quality-m", pf)
	body := `{"prompt":"hi","max_tokens":9,"temperature":0.2,"top_p":0.9,"seed":7,"stop":"END",
	  "response_format":{"type":"json_object"},"options":{"num_ctx":8192,"num_gpu":0},"keep_alive":"30m"}`
	rec := post(srv, body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	p := pf.lastParams
	if pf.withCalls != 1 || p.MaxTokens != 9 || p.Temperature == nil || *p.Temperature != 0.2 || p.TopP == nil || *p.TopP != 0.9 ||
		p.Seed == nil || *p.Seed != 7 || len(p.Stop) != 1 || p.Stop[0] != "END" || p.Format != "json" ||
		p.Options["num_ctx"] != float64(8192) || p.Options["num_gpu"] != float64(0) || p.KeepAlive != "30m" {
		t.Fatalf("params not forwarded: %+v", p)
	}

	// stop as a list, response_format json_schema → Format is the schema object.
	rec = post(srv, `{"prompt":"hi","stop":["a","b"],"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":{"type":"object"}}}}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	p = pf.lastParams
	schema, _ := p.Format.(map[string]any)
	if len(p.Stop) != 2 || schema["type"] != "object" {
		t.Fatalf("list stop / schema not forwarded: %+v", p)
	}

	// Streaming goes through StreamWith with the same params.
	pf.deltas = []string{"o", "k"}
	rec = post(srv, `{"prompt":"hi","stream":true,"seed":3}`, nil)
	if rec.Code != http.StatusOK || pf.withCalls != 3 || pf.lastParams.Seed == nil || *pf.lastParams.Seed != 3 {
		t.Fatalf("stream params: %d calls=%d %+v", rec.Code, pf.withCalls, pf.lastParams)
	}

	// A v1.0 backend keeps working: max_tokens only.
	plain := localFake("ok", 2)
	srv2 := NewServer("fast-m", "quality-m", plain)
	rec = post(srv2, `{"prompt":"hi","max_tokens":5,"temperature":0.1}`, nil)
	if rec.Code != http.StatusOK || plain.lastMaxTokens != 5 {
		t.Fatalf("plain backend: %d max_tokens=%d", rec.Code, plain.lastMaxTokens)
	}
}

// The model.generate span records the parameters (values, never prompt text) and whether
// the backend could honour them.
func TestParamsOnSpan(t *testing.T) {
	spans := map[string]map[string]any{}
	pf := &paramFake{fakeBackend: fakeBackend{name: "ollama", local: true, text: "ok", tokens: 2}}
	srv := NewServer("fast-m", "quality-m", pf)
	srv.SpanSink = spanRecorder(spans)
	rec := post(srv, `{"prompt":"secret prompt text","temperature":0.2,"seed":7,"options":{"num_gpu":0},"keep_alive":"30m","response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object","properties":{"leak":{}}}}}}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	gen := spans["model.generate"]
	params, _ := gen["params"].(map[string]any)
	if params["temperature"] != 0.2 || params["seed"] != float64(7) || params["num_gpu"] != float64(0) ||
		params["keep_alive"] != "30m" || params["format"] != "json_schema" || gen["params_forwarded"] != true {
		t.Fatalf("span params: %v", gen)
	}
	for name, kv := range spans {
		raw, _ := json.Marshal(kv)
		if strings.Contains(string(raw), "secret prompt") || strings.Contains(string(raw), "leak") {
			t.Fatalf("span %s leaks request content: %s", name, raw)
		}
	}

	plain := localFake("ok", 2)
	srv2 := NewServer("fast-m", "quality-m", plain)
	srv2.SpanSink = srv.SpanSink
	post(srv2, `{"prompt":"hi","temperature":0.5}`, nil)
	if spans["model.generate"]["params_forwarded"] != false {
		t.Fatalf("plain backend must report params_forwarded=false: %v", spans["model.generate"])
	}
}

// X-Client-Ref / X-Agent-Role: land on all three spans, are echoed back, and are bounded.
func TestClientRefOnSpans(t *testing.T) {
	spans := map[string]map[string]any{}
	srv := NewServer("fast-m", "quality-m", localFake("ok", 2))
	srv.SpanSink = spanRecorder(spans)
	rec := post(srv, `{"prompt":"hi"}`, map[string]string{"X-Client-Ref": "s1/t3/critic/2", "X-Agent-Role": "critic"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"route.decide", "model.generate", "router.respond"} {
		if spans[name]["client_ref"] != "s1/t3/critic/2" || spans[name]["agent_role"] != "critic" {
			t.Fatalf("span %s missing client ref: %v", name, spans[name])
		}
	}
	if rec.Header().Get("X-Client-Ref") != "s1/t3/critic/2" || rec.Header().Get("X-Agent-Role") != "critic" {
		t.Fatalf("headers not echoed: %v", rec.Header())
	}

	// Body fields work too, and a long ref is cut so a span attribute cannot carry a prompt.
	long := strings.Repeat("x", 500)
	rec = post(srv, `{"prompt":"hi","client_ref":"`+long+`","agent_role":"writer"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := spans["model.generate"]["client_ref"].(string); len(got) != maxClientRefLen {
		t.Fatalf("client_ref not bounded: %d", len(got))
	}
	if spans["model.generate"]["agent_role"] != "writer" {
		t.Fatalf("agent_role from body: %v", spans["model.generate"])
	}

	// The failed-generate span carries it as well.
	down := &fakeBackend{name: "ollama", local: true, err: context.Canceled}
	srv3 := NewServer("fast-m", "quality-m", down)
	srv3.SpanSink = srv.SpanSink
	post(srv3, `{"prompt":"hi"}`, map[string]string{"X-Client-Ref": "r"})
	if spans["model.generate"]["client_ref"] != "r" || spans["model.generate"]["error"] == nil {
		t.Fatalf("error span missing ref: %v", spans["model.generate"])
	}
}

// The Ollama wire shape: named params merged into `options` (winning over the passthrough
// map), `format` and `keep_alive` top-level; num_predict from max_tokens.
func TestOllamaForwardsParams(t *testing.T) {
	var gotReq map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"{}"},"done":true,"prompt_eval_count":1,"eval_count":1}`))
	}))
	defer srv.Close()
	c := NewOllamaClient(srv.URL)
	temp := 0.3
	seed := 42
	p := GenParams{MaxTokens: 4, Temperature: &temp, Seed: &seed, Stop: []string{"END"}, Format: "json", KeepAlive: "30m",
		Options: map[string]any{"num_ctx": 8192, "num_gpu": 0, "temperature": 0.9}}
	if _, _, err := c.GenerateWith(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, p); err != nil {
		t.Fatal(err)
	}
	opts, _ := gotReq["options"].(map[string]any)
	if opts["num_predict"] != float64(4) || opts["temperature"] != 0.3 || opts["seed"] != float64(42) ||
		opts["num_ctx"] != float64(8192) || opts["num_gpu"] != float64(0) {
		t.Fatalf("options: %v", opts)
	}
	if stop, _ := opts["stop"].([]any); len(stop) != 1 || stop[0] != "END" {
		t.Fatalf("stop: %v", opts["stop"])
	}
	if gotReq["format"] != "json" || gotReq["keep_alive"] != "30m" {
		t.Fatalf("format/keep_alive: %v %v", gotReq["format"], gotReq["keep_alive"])
	}
	// A schema object goes through as-is.
	p.Format = map[string]any{"type": "object"}
	if _, _, err := c.GenerateWith(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, p); err != nil {
		t.Fatal(err)
	}
	if f, _ := gotReq["format"].(map[string]any); f["type"] != "object" {
		t.Fatalf("schema format: %v", gotReq["format"])
	}
	// No params at all → no options key (v1.0 wire shape unchanged).
	if _, _, err := c.GenerateWith(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, GenParams{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotReq["options"]; ok {
		t.Fatalf("empty params must not send options: %v", gotReq)
	}
}

// The OpenAI wire shape: temperature/top_p/seed/stop/response_format forwarded; Ollama-only
// options and keep_alive never leave the gateway.
func TestOpenAIForwardsParams(t *testing.T) {
	var gotReq map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()
	b := NewOpenAIBackend("groq", srv.URL, "k")
	temp, topP, seed := 0.3, 0.8, 42
	p := GenParams{MaxTokens: 4, Temperature: &temp, TopP: &topP, Seed: &seed, Stop: []string{"END"},
		Format: map[string]any{"type": "object"}, KeepAlive: "30m", Options: map[string]any{"num_gpu": 0}}
	if _, _, err := b.GenerateWith(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, p); err != nil {
		t.Fatal(err)
	}
	if gotReq["max_tokens"] != float64(4) || gotReq["temperature"] != 0.3 || gotReq["top_p"] != 0.8 || gotReq["seed"] != float64(42) {
		t.Fatalf("params: %v", gotReq)
	}
	rf, _ := gotReq["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format: %v", gotReq["response_format"])
	}
	if _, ok := gotReq["options"]; ok {
		t.Fatalf("ollama options leaked to an OpenAI API: %v", gotReq)
	}
	if _, ok := gotReq["keep_alive"]; ok {
		t.Fatalf("keep_alive leaked to an OpenAI API: %v", gotReq)
	}
	p.Format = "json"
	_, _, _ = b.GenerateWith(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, p)
	if rf, _ := gotReq["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Fatalf("json → json_object: %v", gotReq["response_format"])
	}
}

// Extra local models registered with tier "local" are addressable by name and count as
// local for the fail-closed rule.
func TestExtraLocalModelExplicit(t *testing.T) {
	local := localFake("ok", 2)
	srv := NewServer("fast-m", "quality-m", local)
	srv.AddModel(ModelRef{Tier: "local", Model: "qwen2.5:3b-instruct", Backend: local})
	rec := post(srv, `{"model":"qwen2.5:3b-instruct","prompt":"my password is hunter2","sensitive":true}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out ChatResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Model != "qwen2.5:3b-instruct" || out.Reason != ExplicitReason || local.model != "qwen2.5:3b-instruct" {
		t.Fatalf("explicit local model: %+v", out)
	}
	rec = post(srv, `{"model":"not-registered","prompt":"hi"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown model should be 400, got %d", rec.Code)
	}
}
