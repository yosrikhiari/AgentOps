package router

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GenParams are the per-call generation parameters a client may forward through the
// gateway. v1.0 forwarded only max_tokens; the first real client (Versatile's LangGraph
// writing orchestrator) needed temperature and a seed for a deterministic judge, a JSON
// schema for structured output, and the Ollama options that place a model on a device
// (num_gpu), size its context (num_ctx) and keep it resident (keep_alive). Everything here
// is a value, never a prompt: the whole struct is safe to record on a span.
type GenParams struct {
	MaxTokens   int
	Temperature *float64
	TopP        *float64
	Seed        *int
	Stop        []string
	// Format is Ollama-native structured output: "json", or a JSON-schema object. An
	// OpenAI-shaped response_format on the request is translated into it.
	Format any
	// Options is the Ollama options passthrough (num_ctx, num_gpu, repeat_penalty,
	// repeat_last_n, min_p, …). Keys named above (temperature, top_p, seed, stop,
	// num_predict) win over duplicates here.
	Options map[string]any
	// KeepAlive is Ollama's keep_alive duration string, e.g. "30m".
	KeepAlive string
	// Think toggles a reasoning model's chain-of-thought (Ollama top-level `think`).
	// A client that budgets tokens for prose sends false: on qwen3:8b the reasoning
	// is billed against num_predict and can consume the whole budget.
	Think *bool
}

// ParamBackend is implemented by backends that accept GenParams. Backends that only
// implement Backend keep working: the server falls back to Generate/Stream with
// MaxTokens alone, which is exactly v1.0 behaviour.
type ParamBackend interface {
	GenerateWith(ctx context.Context, model string, msgs []Message, p GenParams) (string, Usage, error)
	StreamWith(ctx context.Context, model string, msgs []Message, p GenParams, emit func(delta string)) (Usage, error)
}

// paramsOf lifts the request's optional fields into GenParams. `stop` may be a string
// or a list (both are valid in the OpenAI shape); `response_format` becomes Format.
func paramsOf(req ChatRequest) GenParams {
	p := GenParams{
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Seed:        req.Seed,
		Options:     req.Options,
		KeepAlive:   req.KeepAlive,
		Format:      req.Format,
		Think:       req.Think,
	}
	if len(req.Stop) > 0 {
		var one string
		var many []string
		if err := json.Unmarshal(req.Stop, &one); err == nil && one != "" {
			p.Stop = []string{one}
		} else if err := json.Unmarshal(req.Stop, &many); err == nil {
			p.Stop = many
		}
	}
	if p.Format == nil && req.ResponseFormat != nil {
		switch strings.ToLower(asString(req.ResponseFormat["type"])) {
		case "json_object":
			p.Format = "json"
		case "json_schema":
			if js, ok := req.ResponseFormat["json_schema"].(map[string]any); ok {
				if schema, ok := js["schema"]; ok {
					p.Format = schema
				}
			}
		}
	}
	return p
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// Client-supplied options reach the machine that hosts the model: num_ctx sizes
// the KV cache, num_gpu places layers on the card, repeat_last_n and num_predict
// bound the work, and keep_alive pins VRAM. A value chosen by a remote client must
// not be able to take the card down for everyone else, so each has a ceiling and
// an out-of-range value is a 400 rather than a silent clamp (audit 2026-11-18 S3).
const (
	maxNumCtx      = 32768
	maxNumPredict  = 8192
	maxNumGPU      = 99
	maxNumThread   = 256
	maxRepeatLastN = 4096
	maxKeepAlive   = time.Hour
)

// ErrParamOutOfRange is a client mistake, not a backend failure: classifyChatError
// maps it to 400 option_out_of_range on both wire formats.
type ErrParamOutOfRange struct{ Option, Message string }

func (e ErrParamOutOfRange) Error() string { return e.Message }

// Validate bounds the resource knobs a request may set. Unknown option keys are
// left alone: Ollama ignores what it does not know, and rejecting them would break
// clients that forward a newer knob this gateway has not seen.
func (p GenParams) Validate() error {
	for _, l := range []struct {
		key string
		max float64
	}{
		{"num_ctx", maxNumCtx},
		{"num_predict", maxNumPredict},
		{"num_gpu", maxNumGPU},
		{"num_thread", maxNumThread},
		{"repeat_last_n", maxRepeatLastN},
	} {
		v, ok := p.Options[l.key]
		if !ok {
			continue
		}
		f, ok := toFloat(v)
		if !ok {
			return ErrParamOutOfRange{l.key, fmt.Sprintf("option %q must be a number", l.key)}
		}
		if f < 0 || f > l.max {
			return ErrParamOutOfRange{l.key, fmt.Sprintf("option %q must be between 0 and %v", l.key, l.max)}
		}
	}
	if p.KeepAlive != "" {
		d, ok := keepAliveDuration(p.KeepAlive)
		if !ok {
			return ErrParamOutOfRange{"keep_alive", fmt.Sprintf("keep_alive %q is not a duration", p.KeepAlive)}
		}
		if d < 0 || d > maxKeepAlive {
			return ErrParamOutOfRange{"keep_alive", fmt.Sprintf("keep_alive must be between 0 and %s", maxKeepAlive)}
		}
	}
	return nil
}

// toFloat accepts the shapes a JSON number can arrive in.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// keepAliveDuration reads Ollama's keep_alive: a duration string ("30m"), a number
// of seconds, or "-1" for "never unload". The last one is reported as negative so
// Validate refuses it — pinning VRAM forever is the behaviour under attack.
func keepAliveDuration(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "-1" {
		return -1, true
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, true
	}
	if secs, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(secs * float64(time.Second)), true
	}
	return 0, false
}

// SpanAttrs is what a span records about the parameters: the values that change what the
// model does, never the prompt. A schema is reported by kind, not content.
func (p GenParams) SpanAttrs() map[string]any {
	out := map[string]any{}
	if p.MaxTokens > 0 {
		out["max_tokens"] = p.MaxTokens
	}
	if p.Temperature != nil {
		out["temperature"] = *p.Temperature
	}
	if p.TopP != nil {
		out["top_p"] = *p.TopP
	}
	if p.Seed != nil {
		out["seed"] = *p.Seed
	}
	if len(p.Stop) > 0 {
		out["stop_count"] = len(p.Stop)
	}
	switch f := p.Format.(type) {
	case nil:
	case string:
		out["format"] = f
	default:
		out["format"] = "json_schema"
	}
	if p.KeepAlive != "" {
		out["keep_alive"] = p.KeepAlive
	}
	if p.Think != nil {
		out["think"] = *p.Think
	}
	for _, k := range []string{"num_ctx", "num_gpu", "repeat_penalty", "repeat_last_n", "min_p", "num_predict"} {
		if v, ok := p.Options[k]; ok {
			out[k] = v
		}
	}
	return out
}

// ollamaOptions merges everything Ollama reads from `options`, with the named fields
// winning over the passthrough map.
func (p GenParams) ollamaOptions() map[string]any {
	opts := map[string]any{}
	for k, v := range p.Options {
		opts[k] = v
	}
	if p.MaxTokens > 0 {
		opts["num_predict"] = p.MaxTokens
	}
	if p.Temperature != nil {
		opts["temperature"] = *p.Temperature
	}
	if p.TopP != nil {
		opts["top_p"] = *p.TopP
	}
	if p.Seed != nil {
		opts["seed"] = *p.Seed
	}
	if len(p.Stop) > 0 {
		opts["stop"] = p.Stop
	}
	if len(opts) == 0 {
		return nil
	}
	return opts
}

// openAIBody adds the parameters an OpenAI-style API understands. Ollama-only knobs
// (Options, KeepAlive) are dropped: a hosted API has no GPU of ours to place a model on.
func (p GenParams) openAIBody(req map[string]any) {
	if p.MaxTokens > 0 {
		req["max_tokens"] = p.MaxTokens
	}
	if p.Temperature != nil {
		req["temperature"] = *p.Temperature
	}
	if p.TopP != nil {
		req["top_p"] = *p.TopP
	}
	if p.Seed != nil {
		req["seed"] = *p.Seed
	}
	if len(p.Stop) > 0 {
		req["stop"] = p.Stop
	}
	switch f := p.Format.(type) {
	case nil:
	case string:
		if f == "json" {
			req["response_format"] = map[string]any{"type": "json_object"}
		}
	default:
		req["response_format"] = map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "response", "schema": f},
		}
	}
}

// clientRef and agentRole are opaque strings a client attaches to join the gateway's
// trace to its own record (Versatile: "<session>/<turn>/<role>/<n>"). Bounded so a span
// attribute can never become a dumping ground for a prompt.
const maxClientRefLen = 64
const maxAgentRoleLen = 32

func boundRef(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max]
	}
	return s
}
