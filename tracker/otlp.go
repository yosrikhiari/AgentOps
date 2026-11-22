package tracker

// OTLP export makes the traces usable outside this binary. It is deliberately a pure
// translation plus a tiny transport: the internal span names stay what they are
// (model.generate, route.decide), and the export adds the OpenTelemetry GenAI aliases
// beside them rather than replacing them.
//
// The GenAI conventions are still all "Development" (semconv-genai@c739977, 2026-07-30),
// so a dashboard that depends on one attribute name is a bet. Dual-naming is how we keep
// the bet small: every gen_ai.* attribute we emit has the internal attribute next to it.
//
// Prompt content is never exported. The store already redacts it (RedactAttrs), and this
// file refuses it again on the way out - a second gate, because an exporter is a new
// surface and the rule is a policy test, not a convention.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ExportedSpan is one span ready for OTLP. Attrs are the raw JSON object the store keeps.
type ExportedSpan struct {
	TraceID  string
	SpanID   string
	ParentID string
	Name     string
	At       time.Time
	Attrs    map[string]any
}

// GenAIAttrs returns the attributes to export: the internal ones, plus the gen_ai.*
// aliases where the mapping is unambiguous. Prompt-shaped keys are dropped, whatever the
// caller passed.
func GenAIAttrs(name string, attrs map[string]any) map[string]any {
	out := make(map[string]any, len(attrs)+6)
	for k, v := range attrs {
		switch strings.ToLower(k) {
		case "prompt", "input", "text", "output", "messages", "content":
			continue // never export content, even if a future span forgets to redact
		}
		out[k] = v
	}
	if op := genAIOperation(name); op != "" {
		out["gen_ai.operation.name"] = op
	}
	if m, ok := out["model"].(string); ok && m != "" {
		out["gen_ai.request.model"] = m
		if genAIOperation(name) == "chat" {
			out["gen_ai.response.model"] = m
		}
	}
	if v, ok := number(out["prompt_tokens"]); ok {
		out["gen_ai.usage.input_tokens"] = v
	}
	if v, ok := number(out["completion_tokens"]); ok {
		out["gen_ai.usage.output_tokens"] = v
	}
	if genAIOperation(name) == "execute_tool" {
		if tool, ok := out["tool"].(string); ok && tool != "" {
			out["gen_ai.tool.name"] = tool
		} else if tool, ok := out["name"].(string); ok && tool != "" {
			out["gen_ai.tool.name"] = tool
		}
	}
	if genAIOperation(name) == "invoke_agent" {
		out["gen_ai.agent.name"] = name
	}
	return out
}

// genAIOperation maps an internal span name to a GenAI operation name. An unknown name
// gets no gen_ai.operation.name: inventing one would put a non-model span in the model
// call tree that dashboards build.
func genAIOperation(name string) string {
	switch name {
	case "model.generate", "router.respond":
		return "chat"
	case "mcp.tool":
		return "execute_tool"
	case "researcher", "drafter", "reviewer":
		return "invoke_agent"
	}
	return ""
}

func number(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

// floatNumber keeps the fraction: latency is seconds and truncating 0.5 to 0 would make
// every exported span zero-width.
func floatNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
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

// OTLPPayload builds an OTLP/HTTP JSON body (protobuf-JSON encoding) for one batch.
// Trace and span ids are zero-padded to the 16/8-byte widths OTLP requires; they are
// opaque to us, and a backend that rejects short ids would reject every span.
func OTLPPayload(spans []ExportedSpan, service string) ([]byte, error) {
	type anyValue struct {
		StringValue string  `json:"stringValue,omitempty"`
		IntValue    string  `json:"intValue,omitempty"`
		DoubleValue float64 `json:"doubleValue,omitempty"`
		BoolValue   bool    `json:"boolValue,omitempty"`
	}
	type kv struct {
		Key   string   `json:"key"`
		Value anyValue `json:"value"`
	}
	type otlpSpan struct {
		TraceID           string `json:"traceId"`
		SpanID            string `json:"spanId"`
		ParentSpanID      string `json:"parentSpanId,omitempty"`
		Name              string `json:"name"`
		Kind              int    `json:"kind"`
		StartTimeUnixNano string `json:"startTimeUnixNano"`
		EndTimeUnixNano   string `json:"endTimeUnixNano"`
		Attributes        []kv   `json:"attributes,omitempty"`
	}
	value := func(v any) anyValue {
		switch x := v.(type) {
		case string:
			return anyValue{StringValue: x}
		case bool:
			return anyValue{BoolValue: x}
		case float64:
			return anyValue{DoubleValue: x}
		case int:
			return anyValue{IntValue: strconv.Itoa(x)}
		case int64:
			return anyValue{IntValue: strconv.FormatInt(x, 10)}
		case json.Number:
			if i, err := x.Int64(); err == nil {
				return anyValue{IntValue: strconv.FormatInt(i, 10)}
			}
			if f, err := x.Float64(); err == nil {
				return anyValue{DoubleValue: f}
			}
		}
		// Nested objects and arrays are flattened by the caller's store before this point;
		// anything left is written as its JSON form rather than dropped silently.
		raw, err := json.Marshal(v)
		if err != nil {
			return anyValue{}
		}
		return anyValue{StringValue: string(raw)}
	}
	pad := func(id string, width int) string {
		id = strings.ToLower(strings.TrimSpace(id))
		if len(id) >= width {
			return id[len(id)-width:]
		}
		return strings.Repeat("0", width-len(id)) + id
	}
	out := make([]otlpSpan, 0, len(spans))
	for _, s := range spans {
		attrs := GenAIAttrs(s.Name, s.Attrs)
		names := make([]string, 0, len(attrs))
		for k := range attrs {
			names = append(names, k)
		}
		// Deterministic attribute order: a diff between two payloads should mean a change.
		sortStrings(names)
		kvs := make([]kv, 0, len(attrs))
		for _, k := range names {
			kvs = append(kvs, kv{Key: k, Value: value(attrs[k])})
		}
		start := s.At
		end := start
		// The span store has no end time; the recorded latency (when there is one) is the
		// only duration we know, and a zero-width span is honest about the rest.
		if secs, ok := floatNumber(s.Attrs["latency_s"]); ok && secs >= 0 {
			end = start.Add(time.Duration(secs * float64(time.Second)))
		}
		kind := 1 // INTERNAL
		if op := genAIOperation(s.Name); op == "chat" || op == "execute_tool" {
			kind = 3 // CLIENT: we are calling a model or a tool
		}
		out = append(out, otlpSpan{
			TraceID: pad(s.TraceID, 32), SpanID: pad(s.SpanID, 16),
			ParentSpanID: parentSpanID(s.ParentID),
			Name:         s.Name, Kind: kind,
			StartTimeUnixNano: strconv.FormatInt(start.UnixNano(), 10),
			EndTimeUnixNano:   strconv.FormatInt(end.UnixNano(), 10),
			Attributes:        kvs,
		})
	}
	body := map[string]any{
		"resourceSpans": []any{map[string]any{
			"resource": map[string]any{"attributes": []kv{
				{Key: "service.name", Value: anyValue{StringValue: service}},
			}},
			"scopeSpans": []any{map[string]any{
				"scope": map[string]any{"name": "tower", "version": ScopeVersion},
				"spans": out,
			}},
		}},
	}
	return json.Marshal(body)
}

// ScopeVersion names the mapping, not the binary. Bump it when a gen_ai.* key or the span
// shape changes, so a collector can tell two exporters apart.
const ScopeVersion = "otlp-genai-1"

// sortStrings keeps payloads diffable without pulling sort into every caller's head.
func sortStrings(s []string) {
	sort.Strings(s)
}

// parentSpanID pads a parent id only when there is one: padding an empty string produces
// sixteen zeros, and omitempty would then send a fake parent that no span has.
func parentSpanID(id string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	if len(id) >= 16 {
		return strings.ToLower(id)[len(id)-16:]
	}
	return strings.Repeat("0", 16-len(id)) + strings.ToLower(id)
}

// ValidateSpanName is a small guard used by the exporter: an empty name would produce a
// span no backend can group.
func ValidateSpanName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("empty span name")
	}
	return nil
}
