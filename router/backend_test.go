package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Ollama /api/chat: one JSON object for stream:false, NDJSON with a final done:true
// object carrying the token counts for stream:true.
func TestOllamaChatAndStream(t *testing.T) {
	var gotReq map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/chat":
			_ = json.NewDecoder(r.Body).Decode(&gotReq)
			if gotReq["stream"] == true {
				_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"Hel"},"done":false}` + "\n" +
					`{"message":{"role":"assistant","content":"lo"},"done":false}` + "\n" +
					`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":5,"eval_count":2}` + "\n"))
				return
			}
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"Hello"},"done":true,"prompt_eval_count":5,"eval_count":1}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := NewOllamaClient(srv.URL)
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	msgs := []Message{{Role: "user", Content: "hi"}}
	text, u, err := c.Generate(context.Background(), "m", msgs, 8)
	if err != nil || text != "Hello" || u.TotalTokens != 6 {
		t.Fatalf("generate: %q %+v %v", text, u, err)
	}
	if opts, _ := gotReq["options"].(map[string]any); opts["num_predict"] != float64(8) {
		t.Fatalf("num_predict not forwarded: %v", gotReq["options"])
	}
	var got []string
	u, err = c.Stream(context.Background(), "m", msgs, 0, func(d string) { got = append(got, d) })
	if err != nil || strings.Join(got, "") != "Hello" || u.PromptTokens != 5 || u.CompletionTokens != 2 {
		t.Fatalf("stream: %v %+v %v", got, u, err)
	}
}

// OpenAI-compatible SSE: data: lines, usage either in the standard final chunk
// (stream_options.include_usage) or in Groq's x_groq block; [DONE] terminates.
func TestOpenAIBackendStreamAndAuth(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/models":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/chat/completions":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Bon\"}}]}\n\n" +
					"data: {\"choices\":[{\"delta\":{\"content\":\"jour\"}}]}\n\n" +
					"data: {\"choices\":[],\"x_groq\":{\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}}\n\n" +
					"data: [DONE]\n\n"))
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Bonjour"}}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
		}
	}))
	defer srv.Close()
	b := NewOpenAIBackend("groq", srv.URL, "sk-test")
	if b.Local() {
		t.Fatal("cloud backend must not be local")
	}
	if err := b.Health(context.Background()); err != nil || auth != "Bearer sk-test" {
		t.Fatalf("health: %v auth=%q", err, auth)
	}
	msgs := []Message{{Role: "user", Content: "salut"}}
	text, u, err := b.Generate(context.Background(), "llama", msgs, 0)
	if err != nil || text != "Bonjour" || u.TotalTokens != 4 {
		t.Fatalf("generate: %q %+v %v", text, u, err)
	}
	var got []string
	u, err = b.Stream(context.Background(), "llama", msgs, 0, func(d string) { got = append(got, d) })
	if err != nil || strings.Join(got, "") != "Bonjour" || u.TotalTokens != 5 {
		t.Fatalf("stream: %v %+v %v", got, u, err)
	}
}

// Plan puts local fallbacks before cloud, and drops cloud entirely for sensitive data.
func TestPlanCandidateOrder(t *testing.T) {
	local := &fakeBackend{name: "ollama", local: true}
	cloud := &fakeBackend{name: "groq"}
	refs := []ModelRef{
		{Tier: "fast", Model: "f", Backend: local},
		{Tier: "quality", Model: "q", Backend: local},
		{Tier: "cloud", Model: "c", Backend: cloud},
	}
	msgs := []Message{{Role: "user", Content: "hi"}}
	r, err := Plan("auto", msgs, false, refs)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(r.Candidates); got != "f,q,c" || r.Tier != "fast" {
		t.Fatalf("auto: %s tier=%s", got, r.Tier)
	}
	r, _ = Plan("", msgs, true, refs)
	if got := names(r.Candidates); got != "f,q" {
		t.Fatalf("sensitive: %s", got)
	}
	r, _ = Plan("groq/c", msgs, false, refs)
	if got := names(r.Candidates); got != "c" || r.Reason != ExplicitReason {
		t.Fatalf("explicit backend/model: %s %s", got, r.Reason)
	}
}

func names(refs []ModelRef) string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Model)
	}
	return strings.Join(out, ",")
}

// Ollama serves hosted ":cloud" models through the local API; they must never count as
// local, so a sensitive request cannot be routed to one even via an all-local backend.
func TestCloudSuffixModelIsNotLocal(t *testing.T) {
	local := &fakeBackend{name: "ollama", local: true}
	refs := []ModelRef{
		{Tier: "fast", Model: "qwen2.5:3b-instruct", Backend: local},
		{Tier: "quality", Model: "deepseek-v4-pro:cloud", Backend: local},
	}
	if refs[0].Local() != true || refs[1].Local() != false {
		t.Fatalf("locality: fast=%v cloud=%v", refs[0].Local(), refs[1].Local())
	}
	msgs := []Message{{Role: "user", Content: "my passport number is X"}}
	r, err := Plan("auto", msgs, true, refs)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Candidates {
		if IsCloudModel(c.Model) {
			t.Fatalf("sensitive plan contains a :cloud model: %s", names(r.Candidates))
		}
	}
	if _, err := Plan("deepseek-v4-pro:cloud", msgs, true, refs); err == nil {
		t.Fatal("explicit :cloud model on sensitive data must be refused")
	}
	// long sensitive prompt would classify to quality; with quality on :cloud it must stay on fast
	long := []Message{{Role: "user", Content: strings.Repeat("confidential ", 40)}}
	r, _ = Plan("auto", long, true, refs)
	if len(r.Candidates) != 1 || r.Candidates[0].Model != "qwen2.5:3b-instruct" {
		t.Fatalf("sensitive quality-tier request should fall to the local fast model only, got %s", names(r.Candidates))
	}
}
