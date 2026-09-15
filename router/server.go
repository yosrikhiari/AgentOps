package router

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
)

type ChatRequest struct {
	Prompt    string `json:"prompt"`
	MaxTokens int    `json:"max_tokens"`
}

type ChatResponse struct {
	Text    string `json:"text"`
	Model   string `json:"model"`
	Reason  string `json:"reason"`
	TraceID string `json:"trace_id"`
}

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	TraceID string `json:"trace_id"`
}

// Generator answers one prompt on one model. maxTokens <= 0 means "model default".
type Generator interface {
	Generate(model, prompt string, maxTokens int) (string, int, error)
}

type SpanSink func(traceID, spanID, parentID, name, attrs string)

type Server struct {
	FastModel    string
	QualityModel string
	Ollama       Generator
	Metrics      *Metrics
	SpanSink     SpanSink
	mux          *http.ServeMux
}

func NewServer(fastModel, qualityModel string, gen Generator) *Server {
	s := &Server{FastModel: fastModel, QualityModel: qualityModel, Ollama: gen, Metrics: NewMetrics()}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	return s
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

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	traceID := newTraceID()
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "prompt is required", traceID)
		return
	}
	out, err := s.Chat(req.Prompt, req.MaxTokens)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ollama_unavailable", "model backend unavailable", out.TraceID)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) emitSpan(traceID, spanID, parentID, name, attrs string) {
	if s.SpanSink == nil {
		return
	}
	s.SpanSink(traceID, spanID, parentID, name, attrs)
}

func spanAttrs(kv map[string]string) string {
	raw, _ := json.Marshal(kv)
	return string(raw)
}

func (s *Server) Chat(prompt string, maxTokens int) (ChatResponse, error) {
	traceID := newTraceID()
	decideSpan := newTraceID()
	genSpan := newTraceID()
	respondSpan := newTraceID()
	start := time.Now()
	tier, reason := Classify(prompt)
	model := s.FastModel
	if tier == "quality" {
		model = s.QualityModel
	}
	s.emitSpan(traceID, decideSpan, "", "route.decide", spanAttrs(map[string]string{"tier": tier, "reason": reason}))
	text, tokens, err := s.Ollama.Generate(model, prompt, maxTokens)
	if err != nil {
		s.Metrics.ObserveError(model)
		return ChatResponse{TraceID: traceID}, err
	}
	s.emitSpan(traceID, genSpan, decideSpan, "model.generate", spanAttrs(map[string]string{"model": model}))
	s.Metrics.Observe(model, time.Since(start).Seconds(), tokens)
	s.emitSpan(traceID, respondSpan, genSpan, "router.respond", spanAttrs(map[string]string{"model": model}))
	return ChatResponse{Text: text, Model: model, Reason: reason, TraceID: traceID}, nil
}
