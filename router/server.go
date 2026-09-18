package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ChatRequest accepts both the OpenAI shape (messages[]) and the MVP's {prompt}.
type ChatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Prompt    string    `json:"prompt"`
	MaxTokens int       `json:"max_tokens"`
	Stream    bool      `json:"stream"`
	Sensitive bool      `json:"sensitive"`

	// v1.1 generation parameters (ADR-0006). All optional; absent means model default,
	// exactly as v1.0 behaved. Values only — none of these carries prompt text.
	Temperature    *float64        `json:"temperature,omitempty"`
	TopP           *float64        `json:"top_p,omitempty"`
	Seed           *int            `json:"seed,omitempty"`
	Stop           json.RawMessage `json:"stop,omitempty"`            // string or []string
	ResponseFormat map[string]any  `json:"response_format,omitempty"` // OpenAI shape: {type: json_object | json_schema}
	Format         any             `json:"format,omitempty"`          // Ollama shape: "json" or a JSON schema
	Options        map[string]any  `json:"options,omitempty"`         // Ollama options passthrough (num_ctx, num_gpu, …)
	KeepAlive      string          `json:"keep_alive,omitempty"`      // Ollama keep_alive, e.g. "30m"
	Think          *bool           `json:"think,omitempty"`           // Ollama think: false stops a reasoning model spending the budget on chain-of-thought

	// ClientRef and AgentRole join this trace to the client's own record. They arrive as
	// X-Client-Ref / X-Agent-Role headers (or these fields) and land on every span.
	ClientRef string `json:"client_ref,omitempty"`
	AgentRole string `json:"agent_role,omitempty"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// ChatResponse is an OpenAI chat.completion plus AgentOps extensions (text, reason,
// backend, trace_id, fallback). OpenAI clients ignore the extras; the MVP's clients keep
// reading text/model/reason/trace_id.
type ChatResponse struct {
	ID       string   `json:"id"`
	Object   string   `json:"object"`
	Created  int64    `json:"created"`
	Model    string   `json:"model"`
	Choices  []Choice `json:"choices"`
	Usage    Usage    `json:"usage"`
	Text     string   `json:"text"`
	Reason   string   `json:"reason"`
	Backend  string   `json:"backend"`
	TraceID  string   `json:"trace_id"`
	Fallback string   `json:"fallback,omitempty"`
}

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	TraceID string `json:"trace_id"`
}

type SpanSink func(traceID, spanID, parentID, name, attrs string)

type Server struct {
	FastModel    string
	QualityModel string
	Models       []ModelRef
	Metrics      *Metrics
	SpanSink     SpanSink
	// Keys enables API-key auth when non-nil; RequireKey rejects anonymous requests.
	Keys       KeyStore
	RequireKey bool
	Limiter    *RateLimiter
	// Prober, when set, answers /v1/models health and lets the router skip backends that
	// the last probe found down.
	Prober            *HealthProber
	SensitiveKeywords []string
	RequestTimeout    time.Duration
	mux               *http.ServeMux
}

// NewServer builds a gateway with one local backend serving the fast and quality tiers.
// Add cloud or extra models with AddModel.
func NewServer(fastModel, qualityModel string, local Backend) *Server {
	s := &Server{
		FastModel:         fastModel,
		QualityModel:      qualityModel,
		Metrics:           NewMetrics(),
		Limiter:           NewRateLimiter(),
		SensitiveKeywords: DefaultSensitiveKeywords,
		RequestTimeout:    300 * time.Second,
	}
	s.Models = []ModelRef{
		{Tier: "fast", Model: fastModel, Backend: local},
		{Tier: "quality", Model: qualityModel, Backend: local},
	}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	return s
}

// AddModel registers another model (typically a cloud fallback with Tier "cloud").
func (s *Server) AddModel(ref ModelRef) { s.Models = append(s.Models, ref) }

func newTraceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeError(w http.ResponseWriter, status int, code, message, traceID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorBody{Error: ErrorDetail{Code: code, Message: message, TraceID: traceID}})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(s.Metrics.Expose()))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleModels is the OpenAI list shape with tier/backend/health extensions.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
		Tier    string `json:"tier"`
		Local   bool   `json:"local"`
		Up      *bool  `json:"up,omitempty"`
	}
	out := struct {
		Object string  `json:"object"`
		Data   []model `json:"data"`
	}{Object: "list"}
	for _, ref := range s.Models {
		m := model{ID: ref.Model, Object: "model", OwnedBy: ref.Backend.Name(), Tier: ref.Tier, Local: ref.local()}
		if s.Prober != nil {
			up := s.Prober.Up(ref.Backend.Name())
			m.Up = &up
		}
		out.Data = append(out.Data, m)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// authenticate resolves the API key on a request. ok=false means a response was written.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, traceID string) (*APIKey, bool) {
	if s.Keys == nil {
		return nil, true
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" {
		if s.RequireKey {
			writeError(w, http.StatusUnauthorized, "missing_api_key", "Authorization: Bearer <api key> is required", traceID)
			return nil, false
		}
		return nil, true
	}
	secret := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer"))
	key, err := s.Keys.Lookup(r.Context(), HashSecret(secret))
	if errors.Is(err, ErrKeyNotFound) {
		s.Metrics.ObserveKey("unknown", true)
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "api key not recognised", traceID)
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "key_store_unavailable", "api key store unavailable", traceID)
		return nil, false
	}
	if key.Disabled {
		s.Metrics.ObserveKey(key.Name, true)
		writeError(w, http.StatusForbidden, "api_key_disabled", "api key is disabled", traceID)
		return nil, false
	}
	if key.BudgetTokens > 0 && key.TokensUsed >= key.BudgetTokens {
		s.Metrics.ObserveKey(key.Name, true)
		writeError(w, http.StatusForbidden, "budget_exceeded",
			fmt.Sprintf("token budget exhausted (%d/%d)", key.TokensUsed, key.BudgetTokens), traceID)
		return nil, false
	}
	if ok, wait := s.Limiter.Allow(key.ID, key.RPM); !ok {
		s.Metrics.ObserveKey(key.Name, true)
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited",
			fmt.Sprintf("key %q allows %d requests per minute", key.Name, key.RPM), traceID)
		return nil, false
	}
	return &key, true
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	traceID := newTraceID()
	if rid := r.Header.Get("X-Request-ID"); rid != "" {
		w.Header().Set("X-Request-ID", rid)
	}
	w.Header().Set("X-Trace-ID", traceID)
	key, ok := s.authenticate(w, r, traceID)
	if !ok {
		return
	}
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body", traceID)
		return
	}
	if req.Prompt == "" && len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "prompt or messages is required", traceID)
		return
	}
	if strings.EqualFold(r.Header.Get("X-AgentOps-Sensitive"), "true") {
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

	if !req.Stream {
		out, err := s.completeWithTrace(ctx, traceID, req, nil)
		if err != nil {
			s.writeChatError(w, err, traceID)
			return
		}
		s.chargeKey(key, out.Usage)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
		return
	}

	// Streaming: SSE in the OpenAI chunk shape. Errors before the first delta are still
	// proper HTTP errors; after that they become a terminal error event.
	flusher, canFlush := w.(http.Flusher)
	started := false
	emit := func(model, delta string) {
		if !started {
			started = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
		}
		writeSSE(w, map[string]any{
			"id": "chatcmpl-" + traceID, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": delta}, "finish_reason": nil}},
		})
		if canFlush {
			flusher.Flush()
		}
	}
	out, err := s.completeWithTrace(ctx, traceID, req, emit)
	if err != nil {
		if !started {
			s.writeChatError(w, err, traceID)
			return
		}
		writeSSE(w, map[string]any{"error": ErrorDetail{Code: "stream_failed", Message: "model backend failed mid-stream", TraceID: traceID}})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	if !started {
		emit(out.Model, "")
	}
	s.chargeKey(key, out.Usage)
	writeSSE(w, map[string]any{
		"id": "chatcmpl-" + traceID, "object": "chat.completion.chunk", "created": out.Created, "model": out.Model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		"usage":   out.Usage, "reason": out.Reason, "backend": out.Backend, "trace_id": out.TraceID,
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
}

func writeSSE(w http.ResponseWriter, v any) {
	raw, _ := json.Marshal(v)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
}

func (s *Server) writeChatError(w http.ResponseWriter, err error, traceID string) {
	var unknown ErrUnknownModel
	var sens ErrSensitiveCloud
	switch {
	case errors.As(err, &unknown):
		writeError(w, http.StatusBadRequest, "unknown_model", err.Error(), traceID)
	case errors.As(err, &sens):
		writeError(w, http.StatusForbidden, "sensitive_cloud_blocked", err.Error(), traceID)
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "timeout", "model did not answer in time", traceID)
	default:
		writeError(w, http.StatusBadGateway, "ollama_unavailable", "model backend unavailable", traceID)
	}
}

// chargeKey records usage off the request path; a lost update costs at most one request
// of budget precision.
func (s *Server) chargeKey(key *APIKey, u Usage) {
	if key == nil {
		return
	}
	s.Metrics.ObserveKey(key.Name, false)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Keys.AddUsage(ctx, key.ID, int64(u.TotalTokens)); err != nil {
			log.Printf("api key %q: usage not recorded: %v", key.Name, err)
		}
	}()
}

func (s *Server) emitSpan(traceID, spanID, parentID, name, attrs string) {
	if s.SpanSink == nil {
		return
	}
	s.SpanSink(traceID, spanID, parentID, name, attrs)
}

func spanAttrs(kv map[string]any) string {
	raw, _ := json.Marshal(kv)
	return string(raw)
}

// Chat is the MVP entry point kept for the MCP tool and callers that have one prompt.
func (s *Server) Chat(prompt string, maxTokens int) (ChatResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.RequestTimeout)
	defer cancel()
	return s.completeWithTrace(ctx, newTraceID(), ChatRequest{Prompt: prompt, MaxTokens: maxTokens}, nil)
}

// Complete routes and answers one request (no HTTP). emit, when set, receives streamed
// deltas with the model that produced them.
func (s *Server) Complete(ctx context.Context, req ChatRequest, emit func(model, delta string)) (ChatResponse, error) {
	return s.completeWithTrace(ctx, newTraceID(), req, emit)
}

func (s *Server) completeWithTrace(ctx context.Context, traceID string, req ChatRequest, emit func(model, delta string)) (ChatResponse, error) {
	msgs := req.Messages
	if len(msgs) == 0 {
		msgs = []Message{{Role: "user", Content: req.Prompt}}
	}
	sensitive := req.Sensitive || IsSensitive(msgs, s.SensitiveKeywords)
	route, err := Plan(req.Model, msgs, sensitive, s.Models)
	if err != nil {
		return ChatResponse{TraceID: traceID}, err
	}
	decideSpan, genSpan, respondSpan := newTraceID(), newTraceID(), newTraceID()
	start := time.Now()
	names := make([]string, 0, len(route.Candidates))
	for _, c := range route.Candidates {
		names = append(names, c.Backend.Name()+"/"+c.Model)
	}
	params := paramsOf(req)
	// The client's reference rides on every span so a trace can be found from the
	// client's side (Versatile: "<session>/<turn>/<role>/<n>") and grouped by agent role.
	ref := func(kv map[string]any) map[string]any {
		if req.ClientRef != "" {
			kv["client_ref"] = req.ClientRef
		}
		if req.AgentRole != "" {
			kv["agent_role"] = req.AgentRole
		}
		return kv
	}
	s.emitSpan(traceID, decideSpan, "", "route.decide", spanAttrs(ref(map[string]any{
		"tier": route.Tier, "reason": route.Reason, "sensitive": sensitive, "candidates": names, "requested": req.Model,
	})))

	var lastErr error
	var fallback []string
	for i, cand := range route.Candidates {
		if s.Prober != nil && i > 0 && !s.Prober.Up(cand.Backend.Name()) {
			continue // a known-down fallback is not worth a timeout
		}
		var text string
		var usage Usage
		var err error
		text, usage, err = generateOn(ctx, cand.Backend, cand.Model, msgs, params, emit)
		if err != nil {
			lastErr = err
			s.Metrics.ObserveError(cand.Model)
			if ctx.Err() != nil {
				break
			}
			fallback = append(fallback, cand.Backend.Name()+"/"+cand.Model)
			log.Printf("trace %s: %s/%s failed (%v); trying next candidate", traceID, cand.Backend.Name(), cand.Model, err)
			continue
		}
		latency := time.Since(start).Seconds()
		s.Metrics.Observe(cand.Model, latency, usage.TotalTokens)
		genAttrs := ref(map[string]any{"model": cand.Model, "backend": cand.Backend.Name(), "latency_s": latency,
			"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens})
		if len(fallback) > 0 {
			genAttrs["fallback_from"] = fallback
		}
		if pa := params.SpanAttrs(); len(pa) > 0 {
			genAttrs["params"] = pa
			genAttrs["params_forwarded"] = forwardsParams(cand.Backend)
		}
		s.emitSpan(traceID, genSpan, decideSpan, "model.generate", spanAttrs(genAttrs))
		s.emitSpan(traceID, respondSpan, genSpan, "router.respond", spanAttrs(ref(map[string]any{"model": cand.Model})))
		out := ChatResponse{
			ID: "chatcmpl-" + traceID, Object: "chat.completion", Created: time.Now().Unix(), Model: cand.Model,
			Choices: []Choice{{Index: 0, Message: Message{Role: "assistant", Content: text}, FinishReason: "stop"}},
			Usage:   usage, Text: text, Reason: route.Reason, Backend: cand.Backend.Name(), TraceID: traceID,
		}
		if len(fallback) > 0 {
			out.Fallback = strings.Join(fallback, ",") + "->" + cand.Backend.Name() + "/" + cand.Model
		}
		return out, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no candidate available")
	}
	s.emitSpan(traceID, genSpan, decideSpan, "model.generate", spanAttrs(ref(map[string]any{"error": lastErr.Error(), "tried": fallback})))
	return ChatResponse{TraceID: traceID}, lastErr
}

// generateOn runs one candidate. A ParamBackend gets the full GenParams; a plain Backend
// gets max_tokens only (v1.0 behaviour), and the span says so via params_forwarded.
func generateOn(ctx context.Context, b Backend, model string, msgs []Message, p GenParams, emit func(model, delta string)) (string, Usage, error) {
	if pb, ok := b.(ParamBackend); ok {
		if emit != nil {
			u, err := pb.StreamWith(ctx, model, msgs, p, func(d string) { emit(model, d) })
			return "", u, err
		}
		return pb.GenerateWith(ctx, model, msgs, p)
	}
	if emit != nil {
		u, err := b.Stream(ctx, model, msgs, p.MaxTokens, func(d string) { emit(model, d) })
		return "", u, err
	}
	return b.Generate(ctx, model, msgs, p.MaxTokens)
}

func forwardsParams(b Backend) bool {
	_, ok := b.(ParamBackend)
	return ok
}
