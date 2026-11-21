package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postMessages(srv *Server, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(body)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// anthropicError pulls the wire shape Anthropic clients parse. It is deliberately
// not the OpenAI {error:{code,message}} shape: SDKs read error.type.
func anthropicError(t *testing.T, rec *httptest.ResponseRecorder) (typ, msg string) {
	t.Helper()
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, rec.Body.String())
	}
	return body.Error.Type, body.Error.Message
}

// TestMessagesReturnsAnthropicShape: one request in the Anthropic shape becomes one
// Anthropic message out, with the system prompt carried as the leading system turn
// (Anthropic sends it separately; our internal shape does not).
func TestMessagesReturnsAnthropicShape(t *testing.T) {
	be := localFake("hello there", 10)
	srv := NewServer("fast-m", "quality-m", be)
	rec := postMessages(srv, `{
		"model":"auto","max_tokens":256,"system":"be brief",
		"messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Tier       string `json:"tier"`
		AnsweredBy string `json:"answered_by"`
		Candidates []struct {
			Model string `json:"model"`
			Tier  string `json:"tier"`
			Local bool   `json:"local"`
		} `json:"candidates"`
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != "message" || out.Role != "assistant" || !strings.HasPrefix(out.ID, "msg_") {
		t.Fatalf("not an Anthropic message: %+v", out)
	}
	if len(out.Content) != 1 || out.Content[0].Type != "text" || out.Content[0].Text != "hello there" {
		t.Fatalf("content = %+v", out.Content)
	}
	if out.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %q", out.StopReason)
	}
	if out.Usage.Input != 5 || out.Usage.Output != 5 {
		t.Fatalf("usage = %+v", out.Usage)
	}
	// The same explainability as the OpenAI shape: tier and the eligible chain.
	if out.Tier != "fast" || len(out.Candidates) != 2 || out.Candidates[0].Model != "fast-m" || !out.Candidates[0].Local {
		t.Fatalf("tier/candidates = %q %+v", out.Tier, out.Candidates)
	}
	if out.AnsweredBy != "" {
		t.Fatalf("answered_by should be empty when a model answered, got %q", out.AnsweredBy)
	}
	if len(be.lastMsgs) != 3 || !strings.HasPrefix(be.lastMsgs[0].Content, "You are the assistant behind Tower") {
		t.Fatalf("gateway system prompt not prepended: %+v", be.lastMsgs)
	}
	if be.lastMsgs[1].Role != "system" || be.lastMsgs[1].Content != "be brief" {
		t.Fatalf("system prompt not carried: %+v", be.lastMsgs)
	}
	if be.lastMaxTokens != 256 {
		t.Fatalf("max_tokens not forwarded: %d", be.lastMaxTokens)
	}
	if rec.Header().Get("X-Trace-ID") == "" {
		t.Fatal("no trace id header")
	}
}

// TestMessagesTravelsTheSameRouterPath: routing, fallback and budgets are not
// reimplemented for the second wire format. A quality prompt must pick quality-m.
func TestMessagesTravelsTheSameRouterPath(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("ok", 4))
	rec := postMessages(srv, `{"model":"quality-m","max_tokens":16,
		"messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Model  string `json:"model"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Model != "quality-m" || out.Reason != ExplicitReason {
		t.Fatalf("model=%q reason=%q", out.Model, out.Reason)
	}
}

// TestMessagesStreamsAnthropicEvents: the six-event sequence Anthropic clients
// expect, in order, with the deltas reassembling the answer.
func TestMessagesStreamsAnthropicEvents(t *testing.T) {
	be := localFake("", 0)
	be.deltas = []string{"Hel", "lo!"}
	srv := NewServer("fast-m", "quality-m", be)
	rec := postMessages(srv, `{"model":"auto","max_tokens":64,"stream":true,
		"messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d ct %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var events []string
	var text strings.Builder
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "event: ") {
			continue
		}
		ev := strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		events = append(events, ev)
	}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var chunk struct {
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
		}
		_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk)
		text.WriteString(chunk.Delta.Text)
	}
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v", events)
	}
	if text.String() != "Hello!" {
		t.Fatalf("text = %q", text.String())
	}
}

// TestMessagesRejectsTools: this router has no tool passthrough yet. Accepting a
// tools array and ignoring it would make an agentic client hang waiting for a
// tool_use block, so the request fails with a message that says what is missing.
func TestMessagesRejectsTools(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("ok", 4))
	rec := postMessages(srv, `{"model":"auto","max_tokens":16,
		"tools":[{"name":"read_file","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	typ, msg := anthropicError(t, rec)
	if typ != "invalid_request_error" || !strings.Contains(msg, "tools") {
		t.Fatalf("error = %q %q", typ, msg)
	}
}

// TestMessagesRejectsNonTextContent: image/document blocks are not translated
// either, and silently dropping them would answer a different question.
func TestMessagesRejectsNonTextContent(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("ok", 4))
	rec := postMessages(srv, `{"model":"auto","max_tokens":16,
		"messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	typ, msg := anthropicError(t, rec)
	if typ != "invalid_request_error" || !strings.Contains(msg, "image") {
		t.Fatalf("error = %q %q", typ, msg)
	}
}

// TestMessagesAcceptsXAPIKey: Claude Code and the Anthropic SDK send x-api-key,
// not Authorization: Bearer. Keys, budgets and rate limits must still apply.
func TestMessagesAcceptsXAPIKey(t *testing.T) {
	srv, ks := keyedServer(t)
	secret := ks.Create("claude-code", 0, 0)
	body := `{"model":"auto","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	if rec := postMessages(srv, body, map[string]string{"x-api-key": secret}); rec.Code != http.StatusOK {
		t.Fatalf("x-api-key: %d %s", rec.Code, rec.Body.String())
	}
	rec := postMessages(srv, body, map[string]string{"x-api-key": "ak_wrong"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad key: %d %s", rec.Code, rec.Body.String())
	}
	if typ, _ := anthropicError(t, rec); typ != "authentication_error" {
		t.Fatalf("error type = %q, want authentication_error", typ)
	}
}

// TestMessagesErrorsAreAnthropicShaped: a backend failure must not hand an
// Anthropic client the OpenAI error shape, which leaves error.type empty.
func TestMessagesErrorsAreAnthropicShaped(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", &fakeBackend{name: "ollama", local: true, err: errors.New("down")})
	rec := postMessages(srv, `{"model":"auto","max_tokens":16,
		"messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	typ, msg := anthropicError(t, rec)
	if typ != "api_error" || msg == "" {
		t.Fatalf("error = %q %q", typ, msg)
	}
}
