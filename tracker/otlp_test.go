package tracker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestGenAIAttrsDualNames: the internal attribute stays, the GenAI alias is added beside
// it. The conventions are Development, so a dashboard must be able to fall back.
func TestGenAIAttrsDualNames(t *testing.T) {
	got := GenAIAttrs("model.generate", map[string]any{
		"model": "phi4-mini:3.8b", "backend": "ollama",
		"prompt_tokens": float64(7), "completion_tokens": float64(5),
		"latency_s": 0.42,
	})
	if got["gen_ai.operation.name"] != "chat" {
		t.Fatalf("operation = %v", got["gen_ai.operation.name"])
	}
	if got["gen_ai.request.model"] != "phi4-mini:3.8b" || got["gen_ai.response.model"] != "phi4-mini:3.8b" {
		t.Fatalf("model aliases = %v/%v", got["gen_ai.request.model"], got["gen_ai.response.model"])
	}
	if got["gen_ai.usage.input_tokens"] != int64(7) || got["gen_ai.usage.output_tokens"] != int64(5) {
		t.Fatalf("usage aliases = %v/%v", got["gen_ai.usage.input_tokens"], got["gen_ai.usage.output_tokens"])
	}
	// Dual-named, not renamed.
	if got["model"] != "phi4-mini:3.8b" || got["backend"] != "ollama" || got["latency_s"] != 0.42 {
		t.Fatalf("internal attributes lost: %+v", got)
	}
	if _, ok := got["gen_ai.operation.name"]; !ok {
		t.Fatal("no operation name")
	}
}

// TestGenAIAttrsNeverExportsPromptText is the policy in code: an exporter is a new surface,
// so content is refused here even if a future span forgets the store's redaction.
func TestGenAIAttrsNeverExportsPromptText(t *testing.T) {
	got := GenAIAttrs("model.generate", map[string]any{
		"prompt": "my secret prompt", "input": "secret", "text": "secret",
		"output": "secret", "messages": []any{"secret"}, "content": "secret",
		"model": "m",
	})
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("content escaped the exporter: %s", raw)
	}
	// Non-content attributes survive, including ones that merely contain the word.
	ok := GenAIAttrs("model.generate", map[string]any{"prompt_tokens": float64(3), "text_format": "json"})
	if ok["prompt_tokens"] == nil || ok["text_format"] != "json" {
		t.Fatalf("over-eager redaction: %+v", ok)
	}
}

// TestGenAIOperationNames: only spans that are genuinely model or tool calls get an
// operation name. route.decide is ours; forcing it into the GenAI tree would corrupt the
// call tree dashboards render.
func TestGenAIOperationNames(t *testing.T) {
	cases := map[string]string{
		"model.generate": "chat", "router.respond": "chat",
		"mcp.tool":   "execute_tool",
		"researcher": "invoke_agent", "drafter": "invoke_agent", "reviewer": "invoke_agent",
		"route.decide": "",
	}
	for name, want := range cases {
		if got := genAIOperation(name); got != want {
			t.Fatalf("%s: operation = %q, want %q", name, got, want)
		}
	}
	toolAttr := GenAIAttrs("mcp.tool", map[string]any{"tool": "get_stats"})
	if toolAttr["gen_ai.tool.name"] != "get_stats" {
		t.Fatalf("tool name = %v", toolAttr["gen_ai.tool.name"])
	}
	agent := GenAIAttrs("researcher", nil)
	if agent["gen_ai.agent.name"] != "researcher" {
		t.Fatalf("agent name = %v", agent["gen_ai.agent.name"])
	}
}

// TestOTLPPayloadShape: OTLP ids are fixed widths, so ours are zero-padded rather than
// rejected by the collector; a span without a parent omits parentSpanId instead of sending
// zeros; the end time is start + recorded latency, since the store keeps no end time.
func TestOTLPPayloadShape(t *testing.T) {
	at := time.Date(2026, 11, 17, 22, 0, 0, 0, time.UTC)
	raw, err := OTLPPayload([]ExportedSpan{
		{TraceID: "aad6e9c4658f7842", SpanID: "a1", Name: "model.generate", At: at,
			Attrs: map[string]any{"model": "m", "latency_s": 0.5}},
		{TraceID: "aad6e9c4658f7842", SpanID: "b2", ParentID: "a1", Name: "route.decide", At: at,
			Attrs: map[string]any{"tier": "fast"}},
	}, "agentops")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	spans := body["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)
	if len(spans) != 2 {
		t.Fatalf("spans = %d", len(spans))
	}
	first := spans[0].(map[string]any)
	if id := first["traceId"].(string); len(id) != 32 || !strings.HasPrefix(id, "0000000000000000") {
		t.Fatalf("trace id = %q", id)
	}
	if id := first["spanId"].(string); len(id) != 16 {
		t.Fatalf("span id = %q", id)
	}
	if _, ok := first["parentSpanId"]; ok {
		t.Fatalf("root span must omit parentSpanId: %v", first["parentSpanId"])
	}
	if first["kind"].(float64) != 3 {
		t.Fatalf("chat kind = %v, want CLIENT(3)", first["kind"])
	}
	start, _ := first["startTimeUnixNano"].(string)
	end, _ := first["endTimeUnixNano"].(string)
	if start == end {
		t.Fatalf("latency 0.5s did not extend the span: %s..%s", start, end)
	}
	second := spans[1].(map[string]any)
	if second["kind"].(float64) != 1 {
		t.Fatalf("route.decide kind = %v, want INTERNAL(1)", second["kind"])
	}
	if second["parentSpanId"].(string) != "00000000000000a1" {
		t.Fatalf("parent = %v", second["parentSpanId"])
	}
	// Deterministic attribute order keeps two payloads diffable.
	attrs := first["attributes"].([]any)
	if len(attrs) < 4 || attrs[0].(map[string]any)["key"].(string) != "gen_ai.operation.name" {
		t.Fatalf("attributes not sorted/exposed: %v", attrs)
	}
}
