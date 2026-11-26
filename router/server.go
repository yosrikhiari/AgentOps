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
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	// Decoded only to be refused. The OpenAI shape accepts these and the gateway
	// cannot honour them; silently dropping them made a tool-using client believe
	// its loop ran when it received plain prose (audit 2026-11-18, contract).
	Tools       json.RawMessage `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
	N           *int            `json:"n,omitempty"`
	Logprobs    *bool           `json:"logprobs,omitempty"`
	TopLogprobs *int            `json:"top_logprobs,omitempty"`
}

// unsupportedFields names the OpenAI parameters this gateway cannot honour.
// Refusing them is the same decision the Anthropic path already makes for
// `tools`: a client that asked for tool calls must not get a 200 that pretends
// they happened. `n=1` and `logprobs=false` are the neutral values and are
// accepted, because they ask for exactly what the gateway already does.
func unsupportedFields(req ChatRequest) string {
	var bad []string
	if len(req.Tools) > 0 {
		bad = append(bad, "tools")
	}
	if len(req.ToolChoice) > 0 {
		bad = append(bad, "tool_choice")
	}
	if req.N != nil && *req.N != 1 {
		bad = append(bad, "n")
	}
	if req.Logprobs != nil && *req.Logprobs {
		bad = append(bad, "logprobs")
	}
	if req.TopLogprobs != nil {
		bad = append(bad, "top_logprobs")
	}
	if len(bad) == 0 {
		return ""
	}
	return "unsupported field(s): " + strings.Join(bad, ", ") +
		"; this gateway speaks plain text - tool calls, multiple choices and logprobs are not implemented"
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// ChatResponse is an OpenAI chat.completion plus Tower extensions (text, reason,
// backend, trace_id, fallback, tier, candidates, answered_by). OpenAI clients ignore
// the extras; the MVP's clients keep reading text/model/reason/trace_id.
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
	// Tier is the tier this turn was routed to; Candidates is the whole eligible
	// chain, so the console can show what else was considered without re-deriving it.
	Tier       string      `json:"tier,omitempty"`
	Candidates []Candidate `json:"candidates,omitempty"`
	// AnsweredBy is "router" when no model ran: the router answered from its own
	// recorded labels (a gateway question). Absent means a model answered.
	AnsweredBy string `json:"answered_by,omitempty"`
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
	// DecisionSink records one routing decision per request (Phase 0 instrumentation).
	// Nil is a no-op; implementations must be non-blocking.
	DecisionSink DecisionSink
	// Keys enables API-key auth when non-nil; RequireKey rejects anonymous requests.
	Keys       KeyStore
	RequireKey bool
	Limiter    *RateLimiter
	// Prober, when set, answers /v1/models health and lets the router skip backends that
	// the last probe found down.
	Prober *HealthProber
	// Exhaustion, when set, benches backends that return quota/rate-limit (or persistent
	// transient) errors and lets the router skip them until the cooldown elapses.
	Exhaustion *ExhaustionTracker
	// RouterPolicy selects how the tier is chosen: "heuristic" (default, ClassifyThread)
	// or "learned" (use Classifier, falling back to the heuristic on error/timeout).
	RouterPolicy string
	// Classifier, when set and RouterPolicy=="learned", picks the tier for a prompt.
	// It must return "fast" or "quality" (and a reason label). On error the router
	// falls back to the heuristic classifier.
	Classifier        func(ctx context.Context, prompt string) (tier, reason string, err error)
	SensitiveKeywords []string
	RequestTimeout    time.Duration
	mux               *http.ServeMux
	// allowed maps a known path to its one method, so an unmatched request can tell
	// "no such route" (404) from "right route, wrong verb" (405 with Allow) without
	// asking net/http, whose 404 and 405 bodies are plain text.
	allowed map[string]string
	// missingLogged remembers which models already got a "not pulled" log line
	// so every probe tick does not re-log them; a model is forgotten when it
	// reappears on disk.
	missingMu     sync.Mutex
	missingLogged map[string]bool
	// lastRoute is the last turn a model answered, for gateway questions
	// ("why are you using this model"). Labels only, in memory: the gateway has
	// no store of its own (ADR-0010).
	lastMu    sync.RWMutex
	lastRoute *LastRoute

	// chargeCh is a single worker that records API-key usage off the request
	// path, so a chat never waits on the database and never spawns a goroutine
	// per request (audit §5.3). Started lazily on the first chargeKey call;
	// a full queue drops the update and counts it. A lost update costs at most
	// one request of budget precision, which is the existing contract.
	chargeCh      chan chargeJob
	chargeOnce    sync.Once
	chargeDropped atomic.Uint64
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
	routes := []struct {
		method, path string
		handle       http.HandlerFunc
	}{
		{http.MethodPost, "/v1/chat/completions", s.handleChat},
		{http.MethodPost, "/v1/messages", s.handleMessages},
		{http.MethodGet, "/v1/models", s.handleModels},
		{http.MethodGet, "/health", s.handleHealth},
		{http.MethodGet, "/metrics", s.handleMetrics},
	}
	s.allowed = make(map[string]string, len(routes))
	for _, rt := range routes {
		s.mux.HandleFunc(rt.method+" "+rt.path, rt.handle)
		s.allowed[rt.path] = rt.method
	}
	// Unmatched paths used to fall through to net/http's plain-text 404 - the only
	// response the gateway produced that was not the JSON error envelope. A client
	// parsing every response as JSON got a parse error instead of a code to branch on.
	s.mux.HandleFunc("/", s.unknownRoute)
	return s
}

// unknownRoute answers what the mux did not match: 405 with Allow when the path exists
// but the method does not, 404 otherwise, both in the one error shape.
func (s *Server) unknownRoute(w http.ResponseWriter, r *http.Request) {
	// A 404/405 is still a response a client may need to correlate with its own
	// log; emitting an empty trace id made the documented header a lie.
	traceID := newTraceID()
	w.Header().Set("X-Trace-ID", traceID)
	if allow, ok := s.allowed[r.URL.Path]; ok {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			r.Method+" is not allowed on "+r.URL.Path+"; use "+allow, traceID)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "no route for "+r.Method+" "+r.URL.Path, traceID)
}

// AddModel registers another model (typically a cloud fallback with Tier "cloud").
func (s *Server) AddModel(ref ModelRef) { s.Models = append(s.Models, ref) }

// presentRefs splits Models into routable refs and refs whose model is known
// missing from disk. Only positive knowledge of absence removes a ref —
// unknown presence (no prober, backend down, cloud backend) never filters.
func (s *Server) presentRefs() (present, dropped []ModelRef) {
	for _, r := range s.Models {
		if s.Prober != nil {
			if pulled, known := s.Prober.Pulled(r.Backend.Name(), r.Model); known && !pulled {
				dropped = append(dropped, r)
				continue
			}
		}
		present = append(present, r)
	}
	return present, dropped
}

// droppedTierModel names the missing model for the tier this request needs, or ""
// when that tier is still served (or was never configured). requested is the client's
// model field: a tier alias names the tier outright, anything else is classified from
// the prompt.
func droppedTierModel(msgs []Message, requested string, dropped []ModelRef) string {
	tier := ""
	if IsTierName(requested) {
		tier = strings.ToLower(strings.TrimSpace(requested))
	} else {
		tier, _ = ClassifyThread(lastUserContent(msgs), msgs)
	}
	for _, d := range dropped {
		if d.Tier == tier {
			return d.Model
		}
	}
	return ""
}

// RefreshPulled logs every served model that is known missing from disk, once
// per model until it reappears. Called at boot and after every probe tick.
func (s *Server) RefreshPulled(_ context.Context) {
	if s.Prober == nil {
		return
	}
	_, dropped := s.presentRefs()
	s.missingMu.Lock()
	defer s.missingMu.Unlock()
	if s.missingLogged == nil {
		s.missingLogged = map[string]bool{}
	}
	seen := map[string]bool{}
	for _, d := range dropped {
		seen[d.Model] = true
		if !s.missingLogged[d.Model] {
			s.missingLogged[d.Model] = true
			if strings.ContainsAny(d.Model, `/\`) {
				log.Printf("model %q is configured but not pulled (not a pullable model name)", d.Model)
			} else {
				log.Printf("model %q not pulled — run: ollama pull %s", d.Model, d.Model)
			}
		}
	}
	for m := range s.missingLogged {
		if !seen[m] {
			delete(s.missingLogged, m)
		}
	}
}

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
// up is backend-health AND presence: a served-but-unpulled model reads
// up=false with reason "not_pulled" instead of surprising at request time.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID      string  `json:"id"`
		Object  string  `json:"object"`
		OwnedBy string  `json:"owned_by"`
		Tier    string  `json:"tier"`
		Local   bool    `json:"local"`
		Up      *bool   `json:"up,omitempty"`
		Reason  *string `json:"reason,omitempty"`
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
			if pulled, known := s.Prober.Pulled(ref.Backend.Name(), ref.Model); known && !pulled {
				f := false
				m.Up = &f
				reason := "not_pulled"
				m.Reason = &reason
			}
		}
		out.Data = append(out.Data, m)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// errWriter writes a machine-code failure in whichever wire shape the endpoint
// speaks. Both formats share the key resolution and the checks around it.
type errWriter func(w http.ResponseWriter, status int, code, message, traceID string)

// maxRequestBody bounds a chat body. It is generous for a real prompt and RAG
// context (4 MiB) and small enough that N parallel requests cannot exhaust the
// heap before a backend is even chosen.
const maxRequestBody = 4 << 20

// Authenticate is authenticate for callers outside this package — /mcp over HTTP
// uses it so key validity, disabled, budget and RPM are decided in one place
// instead of a second, weaker copy (audit 2026-11-18 S2).
func (s *Server) Authenticate(w http.ResponseWriter, r *http.Request, traceID string) (*APIKey, bool) {
	return s.authenticate(w, r, traceID)
}

// authenticate resolves the API key on a request. ok=false means a response was written.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, traceID string) (*APIKey, bool) {
	return s.authenticateWith(w, r, traceID, writeError)
}

// authenticateWith accepts both credential headers: OpenAI clients send
// Authorization: Bearer, Claude Code and the Anthropic SDK send x-api-key. Keys,
// budgets and rate limits are the same either way.
func (s *Server) authenticateWith(w http.ResponseWriter, r *http.Request, traceID string, fail errWriter) (*APIKey, bool) {
	if s.Keys == nil {
		return nil, true
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	secret := ""
	if auth != "" {
		secret = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer"))
	} else {
		secret = strings.TrimSpace(r.Header.Get("x-api-key"))
	}
	if secret == "" {
		if s.RequireKey {
			fail(w, http.StatusUnauthorized, "missing_api_key",
				"send Authorization: Bearer <api key> or x-api-key: <api key>", traceID)
			return nil, false
		}
		return nil, true
	}
	key, err := s.Keys.Lookup(r.Context(), HashSecret(secret))
	if errors.Is(err, ErrKeyNotFound) {
		s.Metrics.ObserveKey("unknown", true)
		fail(w, http.StatusUnauthorized, "invalid_api_key", "api key not recognised", traceID)
		return nil, false
	}
	if err != nil {
		fail(w, http.StatusBadGateway, "key_store_unavailable", "api key store unavailable", traceID)
		return nil, false
	}
	if key.Disabled {
		s.Metrics.ObserveKey(key.Name, true)
		fail(w, http.StatusForbidden, "api_key_disabled", "api key is disabled", traceID)
		return nil, false
	}
	if key.BudgetTokens > 0 && key.TokensUsed >= key.BudgetTokens {
		s.Metrics.ObserveKey(key.Name, true)
		fail(w, http.StatusForbidden, "budget_exceeded",
			fmt.Sprintf("token budget exhausted (%d/%d)", key.TokensUsed, key.BudgetTokens), traceID)
		return nil, false
	}
	if ok, wait := s.Limiter.Allow(key.ID, key.RPM); !ok {
		s.Metrics.ObserveKey(key.Name, true)
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		fail(w, http.StatusTooManyRequests, "rate_limited",
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
	// The body is materialised before any backend call, so the cap is on the
	// reader rather than on Ollama's context window (audit 2026-11-18 S3).
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large",
				fmt.Sprintf("request body exceeds %d bytes", maxRequestBody), traceID)
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body", traceID)
		return
	}
	if req.Prompt == "" && len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "prompt or messages is required", traceID)
		return
	}
	if msg := unsupportedFields(req); msg != "" {
		writeError(w, http.StatusBadRequest, "unsupported_field", msg, traceID)
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

	if !req.Stream {
		out, err := s.completeWithTrace(ctx, traceID, req, nil, nil)
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
	// X-Accel-Buffering: no tells reverse proxies (nginx/Cloudflare) not to buffer the
	// response, which would otherwise defeat streaming on a hosted deployment.
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, canFlush := w.(http.Flusher)
	var mu sync.Mutex
	started := false
	done := make(chan struct{})
	// Heartbeat: keep the connection alive during long gaps (cold load, eviction) so
	// proxies and clients don't time out before the first token. 30s cadence.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				mu.Lock()
				if !started {
					started = true
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Cache-Control", "no-cache")
					w.WriteHeader(http.StatusOK)
				}
				_, _ = fmt.Fprint(w, ": ping\n\n")
				if canFlush {
					flusher.Flush()
				}
				mu.Unlock()
			}
		}
	}()
	emit := func(model, delta string) {
		mu.Lock()
		defer mu.Unlock()
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
	emitReasoning := func(model, delta string) {
		mu.Lock()
		defer mu.Unlock()
		if !started {
			started = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
		}
		writeSSE(w, map[string]any{
			"id": "chatcmpl-" + traceID, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"reasoning": delta}, "finish_reason": nil}},
		})
		if canFlush {
			flusher.Flush()
		}
	}
	out, err := s.completeWithTrace(ctx, traceID, req, emit, emitReasoning)
	close(done)
	if err != nil {
		mu.Lock()
		if !started {
			mu.Unlock()
			s.writeChatError(w, err, traceID)
			return
		}
		writeSSE(w, map[string]any{"error": ErrorDetail{Code: "stream_failed", Message: "model backend failed mid-stream", TraceID: traceID}})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if canFlush {
			flusher.Flush()
		}
		mu.Unlock()
		return
	}
	if !started {
		emit(out.Model, "")
	}
	mu.Lock()
	writeSSE(w, map[string]any{
		"id": "chatcmpl-" + traceID, "object": "chat.completion.chunk", "created": out.Created, "model": out.Model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		"usage":   out.Usage, "reason": out.Reason, "backend": out.Backend, "trace_id": out.TraceID,
		"tier": out.Tier, "candidates": out.Candidates, "answered_by": out.AnsweredBy,
			"cost_usd": costFor(out.Model, out.Usage),
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
	mu.Unlock()
	s.chargeKey(key, out.Usage)
}

func writeSSE(w http.ResponseWriter, v any) {
	raw, _ := json.Marshal(v)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
}

// classifyChatError maps a completion failure to one machine code, shared by both
// wire formats so they cannot drift.
func classifyChatError(err error) (status int, code, msg string) {
	var unknown ErrUnknownModel
	var sens ErrSensitiveCloud
	var notPulled ErrModelNotPulled
	var badParam ErrParamOutOfRange
	switch {
	case errors.As(err, &badParam):
		return http.StatusBadRequest, "option_out_of_range", err.Error()
	case errors.As(err, &unknown):
		return http.StatusBadRequest, "unknown_model", err.Error()
	case errors.As(err, &sens):
		return http.StatusForbidden, "sensitive_cloud_blocked", err.Error()
	case errors.As(err, &notPulled):
		return http.StatusServiceUnavailable, "model_not_pulled", err.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout", "model did not answer in time"
	default:
		return http.StatusBadGateway, "ollama_unavailable", "model backend unavailable"
	}
}

func (s *Server) writeChatError(w http.ResponseWriter, err error, traceID string) {
	status, code, msg := classifyChatError(err)
	writeError(w, status, code, msg, traceID)
}

// chargeJob is one queued usage update for the charge worker.
type chargeJob struct {
	key    *APIKey
	tokens int64
}

// chargeKey records usage off the request path; a lost update costs at most one
// request of budget precision. A single worker drains the queue so a chat never
// waits on the database and never spawns a goroutine per request (audit §5.3).
func (s *Server) chargeKey(key *APIKey, u Usage) {
	if key == nil || s.Keys == nil {
		return
	}
	s.Metrics.ObserveKey(key.Name, false)
	s.chargeOnce.Do(func() {
		s.chargeCh = make(chan chargeJob, 1024)
		go s.chargeLoop()
	})
	select {
	case s.chargeCh <- chargeJob{key: key, tokens: int64(u.TotalTokens)}:
	default:
		s.chargeDropped.Add(1)
		log.Printf("api key %q: usage queue full, dropped (total dropped=%d)", key.Name, s.chargeDropped.Load())
	}
}

// chargeLoop is the single worker behind chargeKey. It exits when the channel is
// closed; until then it processes one update at a time.
func (s *Server) chargeLoop() {
	for job := range s.chargeCh {
		if s.Keys == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.Keys.AddUsage(ctx, job.key.ID, job.tokens); err != nil {
			log.Printf("api key %q: usage not recorded: %v", job.key.Name, err)
		}
		cancel()
	}
}

// ChargeDropped reports how many usage updates were dropped because the queue
// was full. Exposed for tests and operators.
func (s *Server) ChargeDropped() uint64 { return s.chargeDropped.Load() }

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
	return s.completeWithTrace(ctx, newTraceID(), ChatRequest{Prompt: prompt, MaxTokens: maxTokens}, nil, nil)
}

// Complete routes and answers one request (no HTTP). emit, when set, receives streamed
// deltas with the model that produced them.
func (s *Server) Complete(ctx context.Context, req ChatRequest, emit func(model, delta string)) (ChatResponse, error) {
	return s.completeWithTrace(ctx, newTraceID(), req, emit, nil)
}

func (s *Server) completeWithTrace(ctx context.Context, traceID string, req ChatRequest, emit func(model, delta string), emitReasoning func(model, delta string)) (ChatResponse, error) {
	msgs := req.Messages
	if len(msgs) == 0 {
		msgs = []Message{{Role: "user", Content: req.Prompt}}
	}
	prompt := lastUserContent(msgs)
	sensitive := req.Sensitive || IsSensitive(msgs, s.SensitiveKeywords)
	// Presence first: an explicitly requested model or tier that is known missing
	// from disk is a 503 naming it (403 still wins for sensitive-to-cloud). An
	// auto-tier whose model is missing is caught after Plan below.
	present, dropped := s.presentRefs()
	if req.Model != "" && req.Model != "auto" {
		tier := strings.ToLower(strings.TrimSpace(req.Model))
		for _, d := range dropped {
			if d.Model == req.Model || d.Backend.Name()+"/"+d.Model == req.Model ||
				(IsTierName(req.Model) && d.Tier == tier) {
				if sensitive && !d.Local() {
					return ChatResponse{TraceID: traceID}, ErrSensitiveCloud{Model: req.Model}
				}
				return ChatResponse{TraceID: traceID}, ErrModelNotPulled{Model: d.Model}
			}
		}
	}
	route, err := PlanWithPolicy(req.Model, msgs, sensitive, present, s.RouterPolicy, s.Classifier)
	if err != nil {
		if req.Model == "" || req.Model == "auto" || IsTierName(req.Model) {
			if m := droppedTierModel(msgs, req.Model, dropped); m != "" {
				return ChatResponse{TraceID: traceID}, ErrModelNotPulled{Model: m}
			}
		}
		return ChatResponse{TraceID: traceID}, err
	}
	decideSpan, genSpan, respondSpan := newTraceID(), newTraceID(), newTraceID()
	start := time.Now()
	sig := Analyse(prompt)
	names := make([]string, 0, len(route.Candidates))
	for _, c := range route.Candidates {
		names = append(names, c.Backend.Name()+"/"+c.Model)
	}
	params := paramsOf(req)
	if err := params.Validate(); err != nil {
		return ChatResponse{TraceID: traceID}, err
	}
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
		// Phase 0 labels: a closed intent, a bounded confidence, a difficulty scalar.
		// Labels only, never prompt text - the policy test scans for prompt-shaped attrs.
		"intent": string(sig.Intent), "intent_confidence": sig.Confidence,
		"difficulty": sig.Difficulty, "policy": PolicyHeuristicV0,
	})))

	// A gateway question is answered from labels the router already has. Calling a
	// model here would be asking the component under discussion to describe itself
	// from nothing; the router's answer says plainly that no model ran.
	if IsGatewayQuestion(prompt, msgs) {
		return s.answerFromRouter(traceID, decideSpan, respondSpan, route, sig, names, ref)
	}

	roster := Roster(s.Models)
	var lastErr error
	var fallback []string
	for i, cand := range route.Candidates {
		if s.Prober != nil && i > 0 && !s.Prober.Up(cand.Backend.Name()) {
			continue // a known-down fallback is not worth a timeout
		}
		// A benched backend (exhausted quota / persistent transient failure)
		// is skipped until its cooldown elapses. This is the ladder's whole point:
		// do not pay the penalty of an exhausted free API on every request.
		if s.Exhaustion != nil {
			if benched, until := s.Exhaustion.Benched(cand.Backend.Name()); benched {
				log.Printf("trace %s: %s/%s benched until %s, skipping", traceID, cand.Backend.Name(), cand.Model, until.Format(time.RFC3339))
				fallback = append(fallback, cand.Backend.Name()+"/"+cand.Model+" (benched)")
				continue
			}
		}
		var text string
		var usage Usage
		var err error
		// Rebuilt per candidate: a model that answers should be looking at its own
		// name in the prompt, not the planned one's, so a fallback cannot be told it
		// is a different model.
		turn := withSystemPrompt(msgs, SystemPrompt(roster, cand.Model, route.Reason))
		// Track whether any delta was emitted, so a mid-stream failure does not
		// silently replace partial output already sent to the client.
		emitted := false
		wrappedEmit := emit
		if emit != nil {
			wrappedEmit = func(model, delta string) {
				emitted = true
				emit(model, delta)
			}
		}
		text, usage, err = generateOn(ctx, cand.Backend, cand.Model, turn, params, wrappedEmit, emitReasoning)
		if err != nil {
			lastErr = err
			s.Metrics.ObserveError(cand.Model)
			fallback = append(fallback, cand.Backend.Name()+"/"+cand.Model)
			// Bench the rung so subsequent requests skip it until the cooldown.
			// This runs before the deadline check below: a backend that made us
			// wait until REQUEST_TIMEOUT must not be retried on the next request.
			if s.Exhaustion != nil {
				if isExhaustion(err) {
					d, kind := CooldownFor(err)
					s.Exhaustion.Bench(cand.Backend.Name(), time.Now().Add(d), kind)
					log.Printf("trace %s: %s/%s exhausted (%v); benched for %s", traceID, cand.Backend.Name(), cand.Model, err, d)
				} else if isTransient(err) {
					transient := 60 * time.Second
					if v := os.Getenv("EXHAUST_COOLDOWN_TRANSIENT"); v != "" {
						if d, perr := time.ParseDuration(v); perr == nil && d >= time.Second {
							transient = d
						}
					}
					s.Exhaustion.Bench(cand.Backend.Name(), time.Now().Add(transient), "transient")
				}
			}
			if ctx.Err() != nil {
				break
			}
			// If we already streamed partial output, do not fall through: the client
			// has seen it and a silent replacement would be worse than an error.
			if emitted {
				log.Printf("trace %s: %s/%s failed after emitting (%v); not falling through", traceID, cand.Backend.Name(), cand.Model, err)
				break
			}
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
			Tier: route.Tier, Candidates: candidatesOf(route),
		}
		if len(fallback) > 0 {
			out.Fallback = strings.Join(fallback, ",") + "->" + cand.Backend.Name() + "/" + cand.Model
		}
		s.rememberRoute(LastRoute{
			Model: cand.Model, Backend: cand.Backend.Name(), Tier: route.Tier, Reason: route.Reason,
			LatencyS: latency, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
			At: time.Now().UTC(),
		})
		s.recordDecision(traceID, sig, route, sensitive, names,
			cand.Backend.Name()+"/"+cand.Model, len(fallback) > 0, false)
		return out, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no candidate available")
	}
	s.emitSpan(traceID, genSpan, decideSpan, "model.generate", spanAttrs(ref(map[string]any{"error": lastErr.Error(), "tried": fallback})))
	// The router still answered - with an error. Without this span a failed request
	// and a lost telemetry write looked identical: two spans, no reason.
	s.emitSpan(traceID, respondSpan, genSpan, "router.respond", spanAttrs(ref(map[string]any{"error": lastErr.Error()})))
	s.recordDecision(traceID, sig, route, sensitive, names, "", len(fallback) > 0, true)
	return ChatResponse{TraceID: traceID}, lastErr
}

// answerFromRouter answers a gateway question from the last model turn and the roster.
// It emits route.decide -> router.answer -> router.respond: three spans, no
// model.generate, because no model generated anything.
func (s *Server) answerFromRouter(traceID, decideSpan, respondSpan string, route Route, sig Signal, names []string, ref func(map[string]any) map[string]any) (ChatResponse, error) {
	answerSpan := newTraceID()
	planned := route.Candidates[0]
	text := MetaAnswer(route.Tier, route.Reason, Roster(s.Models), s.LastTurn())
	s.emitSpan(traceID, answerSpan, decideSpan, "router.answer", spanAttrs(ref(map[string]any{
		"model": planned.Model, "tier": route.Tier, "reason": route.Reason,
		"answered_by": "router", "policy": PolicyHeuristicV0,
	})))
	s.emitSpan(traceID, respondSpan, answerSpan, "router.respond", spanAttrs(ref(map[string]any{
		"model": planned.Model, "answered_by": "router",
	})))
	out := ChatResponse{
		ID: "chatcmpl-" + traceID, Object: "chat.completion", Created: time.Now().Unix(), Model: planned.Model,
		Choices: []Choice{{Index: 0, Message: Message{Role: "assistant", Content: text}, FinishReason: "stop"}},
		Text:    text, Reason: route.Reason, Backend: planned.Backend.Name(), TraceID: traceID,
		Tier: route.Tier, Candidates: candidatesOf(route), AnsweredBy: "router",
	}
	// Deliberately not rememberRoute: "the last model turn" must stay the last turn a
	// model answered, so a run of gateway questions keeps reporting the real one
	// instead of reporting itself with zero latency.
	s.recordDecision(traceID, sig, route, route.Sensitive, names, planned.Backend.Name()+"/"+planned.Model, false, false)
	return out, nil
}

// candidatesOf projects the route chain into the JSON view. It copies, so a caller
// cannot observe route state being reused.
func candidatesOf(route Route) []Candidate {
	out := make([]Candidate, 0, len(route.Candidates))
	for _, c := range route.Candidates {
		out = append(out, Candidate{Model: c.Model, Tier: c.Tier, Local: c.Local()})
	}
	return out
}

// recordDecision hands one decision to the sink once the outcome is known, so a replay can
// credit the model that actually answered. It copies the candidate slice so a sink cannot
// retain a view into route state, and never panics on a nil sink.
func (s *Server) recordDecision(traceID string, sig Signal, route Route, sensitive bool, candidates []string, chosen string, fellBack, failed bool) {
	if s.DecisionSink == nil {
		return
	}
	d := Decision{
		TraceID: traceID, Intent: sig.Intent, IntentConfidence: sig.Confidence,
		Difficulty: sig.Difficulty, Policy: PolicyHeuristicV0,
		Tier: route.Tier, Reason: route.Reason, Sensitive: sensitive,
		Candidates: append([]string(nil), candidates...), At: time.Now().UTC(),
		Chosen: chosen, Fallback: fellBack, Failed: failed,
	}
	if len(candidates) > 0 {
		d.Planned = candidates[0]
	}
	s.DecisionSink(d)
}

// generateOn runs one candidate. A ParamBackend gets the full GenParams; a plain Backend
// gets max_tokens only (v1.0 behaviour), and the span says so via params_forwarded.
func generateOn(ctx context.Context, b Backend, model string, msgs []Message, p GenParams, emit func(model, delta string), emitReasoning func(model, delta string)) (string, Usage, error) {
	if rb, ok := b.(ReasoningBackend); ok {
		if emit != nil {
			u, err := rb.StreamWithReasoning(ctx, model, msgs, p,
				func(d string) { if emit != nil { emit(model, d) } },
				func(d string) { if emitReasoning != nil { emitReasoning(model, d) } })
			return "", u, err
		}
		return rb.GenerateWith(ctx, model, msgs, p)
	}
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

// costFor estimates the USD cost of a completed turn. Local models are free (0);
// a global per-1k-token rate (COST_PER_1K_TOKENS, default 0) applies otherwise,
// so the default behaviour adds no spurious cost. Operators running a paid cloud
// backend can set the env var to their provider's blended rate.
func costFor(model string, u Usage) float64 {
	rate := 0.0
	if v := os.Getenv("COST_PER_1K_TOKENS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			rate = f
		}
	}
	if rate == 0 {
		return 0
	}
	return float64(u.PromptTokens+u.CompletionTokens) / 1000.0 * rate
}
