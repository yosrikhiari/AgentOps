package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Anthropic Messages API ingress (`POST /v1/messages`).
//
// The point is not to reimplement routing for a second wire format: the request is
// translated into the same ChatRequest the OpenAI endpoint uses, so keys, RPM,
// token budgets, fail-closed sensitive routing, fallback and spans all apply
// unchanged. Only the wire shapes differ.
//
// Not supported yet, and rejected loudly rather than silently ignored: `tools`
// (and `tool_choice`), and non-text content blocks. An agentic client that gets an
// answer without its tool_use block hangs; a 400 that says so is the honest reply
// until tool passthrough lands (docs/plans/2026-11-16-tier0-market-gaps.md, Task 2b).

// anthropicRequest is the subset of the Messages API this gateway understands.
type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      json.RawMessage    `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Stream      bool               `json:"stream"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	StopSeqs    []string           `json:"stop_sequences,omitempty"`
	Tools       []json.RawMessage  `json:"tools,omitempty"`
	ToolChoice  json.RawMessage    `json:"tool_choice,omitempty"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// apiError is a translated request problem: status and Anthropic error type.
type apiError struct {
	Status  int
	Type    string
	Message string
}

func (e apiError) Error() string { return e.Message }

func badRequest(msg string) apiError {
	return apiError{Status: http.StatusBadRequest, Type: "invalid_request_error", Message: msg}
}

// toChatRequest translates one Anthropic request. System is carried as the leading
// system message because the internal shape has no separate system field.
func toChatRequest(in anthropicRequest) (ChatRequest, error) {
	if len(in.Tools) > 0 || len(in.ToolChoice) > 0 {
		return ChatRequest{}, badRequest("tools are not translated yet: send plain text messages, or use /v1/chat/completions for an OpenAI-style tool loop")
	}
	req := ChatRequest{
		Model:       in.Model,
		MaxTokens:   in.MaxTokens,
		Stream:      in.Stream,
		Temperature: in.Temperature,
		TopP:        in.TopP,
	}
	if len(in.StopSeqs) > 0 {
		// ChatRequest.Stop is the OpenAI shape (string or []string); reuse it so a
		// backend that honours stop sequences honours these too.
		if raw, err := json.Marshal(in.StopSeqs); err == nil {
			req.Stop = raw
		}
	}
	if len(in.System) > 0 {
		text, err := contentText(in.System)
		if err != nil {
			return ChatRequest{}, err
		}
		if strings.TrimSpace(text) != "" {
			req.Messages = append(req.Messages, Message{Role: "system", Content: text})
		}
	}
	for i, m := range in.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "user" && role != "assistant" {
			return ChatRequest{}, badRequest(fmt.Sprintf("messages[%d].role %q is not user or assistant", i, m.Role))
		}
		text, err := contentText(m.Content)
		if err != nil {
			return ChatRequest{}, err
		}
		req.Messages = append(req.Messages, Message{Role: role, Content: text})
	}
	if len(req.Messages) == 0 {
		return ChatRequest{}, badRequest("messages is required")
	}
	return req, nil
}

// contentText accepts both Anthropic content forms: a bare string, or an array of
// blocks. Text blocks are joined with a newline; anything else is an error naming
// the block type, because dropping it would answer a different question.
func contentText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var blocks []anthropicBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", badRequest("content must be a string or an array of text blocks")
	}
	var parts []string
	for _, b := range blocks {
		if b.Type != "text" {
			return "", badRequest(fmt.Sprintf("content block type %q is not supported yet; only text blocks are translated", b.Type))
		}
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n"), nil
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	traceID := newTraceID()
	if rid := r.Header.Get("X-Request-ID"); rid != "" {
		w.Header().Set("X-Request-ID", rid)
	}
	w.Header().Set("X-Trace-ID", traceID)
	fail := func(w http.ResponseWriter, status int, code, msg, traceID string) {
		writeAnthropicError(w, status, anthropicErrorType(code), msg, traceID)
	}
	key, ok := s.authenticateWith(w, r, traceID, fail)
	if !ok {
		return
	}
	var in anthropicRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeAnthropicError(w, http.StatusRequestEntityTooLarge, "invalid_request_error",
				fmt.Sprintf("request body exceeds %d bytes", maxRequestBody), traceID)
			return
		}
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body", traceID)
		return
	}
	req, err := toChatRequest(in)
	if err != nil {
		var ae apiError
		if errors.As(err, &ae) {
			writeAnthropicError(w, ae.Status, ae.Type, ae.Message, traceID)
		} else {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), traceID)
		}
		return
	}
	if strings.EqualFold(r.Header.Get("X-Tower-Sensitive"), "true") {
		req.Sensitive = true
	}
	if v := r.Header.Get("X-Client-Ref"); v != "" {
		req.ClientRef = v
	}
	if v := r.Header.Get("X-Agent-Role"); v != "" {
		req.AgentRole = v
	}
	req.ClientRef = boundRef(req.ClientRef, maxClientRefLen)
	req.AgentRole = boundRef(req.AgentRole, maxAgentRoleLen)
	if req.ClientRef != "" {
		w.Header().Set("X-Client-Ref", req.ClientRef)
	}
	if req.AgentRole != "" {
		w.Header().Set("X-Agent-Role", req.AgentRole)
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.RequestTimeout)
	defer cancel()

	if !in.Stream {
		out, err := s.completeWithTrace(ctx, traceID, req, nil, nil)
		if err != nil {
			s.writeAnthropicChatError(w, err, traceID)
			return
		}
		s.chargeKey(key, out.Usage)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(anthropicBody(traceID, out))
		return
	}

	flusher, canFlush := w.(http.Flusher)
	started := false
	start := func(model string) {
		started = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		writeAnthropicSSE(w, "message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": "msg_" + traceID, "type": "message", "role": "assistant", "model": model,
				"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
				// Input tokens are only known once the backend reports usage; the
				// final message_delta carries the real numbers.
				"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		})
		writeAnthropicSSE(w, "content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
	}
	emit := func(model, delta string) {
		if !started {
			start(model)
		}
		if delta != "" {
			writeAnthropicSSE(w, "content_block_delta", map[string]any{
				"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "text_delta", "text": delta},
			})
		}
		if canFlush {
			flusher.Flush()
		}
	}
	out, err := s.completeWithTrace(ctx, traceID, req, emit, nil)
	if err != nil {
		if !started {
			s.writeAnthropicChatError(w, err, traceID)
			return
		}
		writeAnthropicSSE(w, "error", map[string]any{
			"type": "error", "error": map[string]any{"type": "api_error", "message": "model backend failed mid-stream"},
		})
		return
	}
	if !started {
		start(out.Model)
	}
	s.chargeKey(key, out.Usage)
	writeAnthropicSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	writeAnthropicSSE(w, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason(out), "stop_sequence": nil},
		"usage": map[string]any{"input_tokens": out.Usage.PromptTokens, "output_tokens": out.Usage.CompletionTokens},
	})
	writeAnthropicSSE(w, "message_stop", map[string]any{"type": "message_stop"})
	if canFlush {
		flusher.Flush()
	}
}

// anthropicBody is the non-streaming message object.
func anthropicBody(traceID string, out ChatResponse) map[string]any {
	text := out.Text
	if text == "" && len(out.Choices) > 0 {
		text = out.Choices[0].Message.Content
	}
	return map[string]any{
		"id": "msg_" + traceID, "type": "message", "role": "assistant", "model": out.Model,
		"content":     []any{map[string]any{"type": "text", "text": text}},
		"stop_reason": stopReason(out), "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": out.Usage.PromptTokens, "output_tokens": out.Usage.CompletionTokens},
		// Tower extensions, same as the OpenAI endpoint's: Anthropic clients
		// ignore unknown fields, and the console's explainability rides along.
		"reason": out.Reason, "backend": out.Backend, "trace_id": out.TraceID,
		"tier": out.Tier, "candidates": out.Candidates, "answered_by": out.AnsweredBy,
	}
}

// stopReason maps our finish_reason to Anthropic's vocabulary. A length cutoff is
// max_tokens; everything else is a normal end of turn.
func stopReason(out ChatResponse) string {
	for _, c := range out.Choices {
		if c.FinishReason == "length" {
			return "max_tokens"
		}
	}
	return "end_turn"
}

func writeAnthropicSSE(w http.ResponseWriter, event string, v any) {
	raw, _ := json.Marshal(v)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
}

func writeAnthropicError(w http.ResponseWriter, status int, typ, message, traceID string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Trace-ID", traceID)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": typ, "message": message},
	})
}

// anthropicErrorType translates the gateway's machine codes into the error types
// Anthropic SDKs switch on. Unknown codes fall back to api_error.
func anthropicErrorType(code string) string {
	switch code {
	case "missing_api_key", "invalid_api_key":
		return "authentication_error"
	case "api_key_disabled", "budget_exceeded":
		return "permission_error"
	case "rate_limited":
		return "rate_limit_error"
	case "unknown_model", "bad_request":
		return "invalid_request_error"
	case "sensitive_cloud_blocked":
		return "permission_error"
	default:
		return "api_error"
	}
}

// writeAnthropicChatError is writeChatError for the second wire format: same
// classification, Anthropic-shaped body.
func (s *Server) writeAnthropicChatError(w http.ResponseWriter, err error, traceID string) {
	status, code, msg := classifyChatError(err)
	writeAnthropicError(w, status, anthropicErrorType(code), msg, traceID)
}
