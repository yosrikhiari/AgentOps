package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agentops/router"
	"agentops/tracker"
)

type stubGen struct {
	text string
}

func (g *stubGen) Name() string { return "ollama" }
func (g *stubGen) Local() bool  { return true }
func (g *stubGen) Generate(ctx context.Context, model string, msgs []router.Message, maxTokens int) (string, router.Usage, error) {
	return g.text, router.Usage{TotalTokens: 9}, nil
}
func (g *stubGen) Stream(ctx context.Context, model string, msgs []router.Message, maxTokens int, emit func(string)) (router.Usage, error) {
	emit(g.text)
	return router.Usage{TotalTokens: 9}, nil
}
func (g *stubGen) Health(ctx context.Context) error { return nil }

func runSession(t *testing.T, srv *Server, lines ...string) []map[string]any {
	t.Helper()
	var in bytes.Buffer
	for _, l := range lines {
		in.WriteString(l + "\n")
	}
	var out bytes.Buffer
	if err := srv.Run(&in, &out); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		resps = append(resps, r)
	}
	return resps
}

func TestInitializeAndList(t *testing.T) {
	rs := router.NewServer("fast-m", "quality-m", &stubGen{text: "hi"})
	srv := NewServer(rs)
	resps := runSession(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(resps) != 2 {
		t.Fatalf("got %d responses", len(resps))
	}
	init := resps[0]["result"].(map[string]any)
	if init["protocolVersion"] != protocolVersion {
		t.Fatalf("bad handshake %+v", init)
	}
	tools := resps[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 5 {
		t.Fatalf("want 5 tools, got %d", len(tools))
	}
}

func TestCallTools(t *testing.T) {
	rs := router.NewServer("fast-m", "quality-m", &stubGen{text: "hello"})
	srv := NewServer(rs)
	if _, err := rs.Chat("warm up", 0); err != nil {
		t.Fatal(err)
	}
	resps := runSession(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_models","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_stats","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"route_test_request","arguments":{"prompt":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
	)
	textOf := func(r map[string]any) string {
		content := r["result"].(map[string]any)["content"].([]any)
		return content[0].(map[string]any)["text"].(string)
	}
	if !strings.Contains(textOf(resps[0]), "fast-m") {
		t.Fatalf("list_models: %s", textOf(resps[0]))
	}
	if !strings.Contains(textOf(resps[1]), "fast-m") {
		t.Fatalf("get_stats should show routed model: %s", textOf(resps[1]))
	}
	if !strings.Contains(textOf(resps[2]), "quality-m") && !strings.Contains(textOf(resps[2]), "fast-m") {
		t.Fatalf("route_test_request: %s", textOf(resps[2]))
	}
	if resps[3]["error"] == nil {
		t.Fatal("unknown tool should error")
	}
}

func TestCallMissingPrompt(t *testing.T) {
	rs := router.NewServer("fast-m", "quality-m", &stubGen{text: "hi"})
	srv := NewServer(rs)
	resps := runSession(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"route_test_request","arguments":{}}}`,
	)
	if resps[0]["error"] == nil {
		t.Fatal("missing prompt should error")
	}
}

func TestInspectTrace(t *testing.T) {
	rs := router.NewServer("fast-m", "quality-m", &stubGen{text: "hi"})
	srv := NewServer(rs)
	srv.TraceLookup = func(id string) (any, error) {
		return map[string]any{"trace_id": id, "spans": []any{
			map[string]any{"trace_id": id, "span_id": "s1", "parent_id": "", "name": "route.decide"},
		}}, nil
	}
	resps := runSession(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"inspect_trace","arguments":{"trace_id":"abc"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"inspect_trace","arguments":{}}}`,
	)
	content := resps[0]["result"].(map[string]any)["content"].([]any)
	if !strings.Contains(content[0].(map[string]any)["text"].(string), "abc") {
		t.Fatalf("inspect_trace should return trace: %+v", resps[0])
	}
	if resps[1]["error"] == nil {
		t.Fatal("missing trace_id should error")
	}
	bare := NewServer(rs)
	missing := runSession(t, bare,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"inspect_trace","arguments":{"trace_id":"abc"}}}`,
	)
	if missing[0]["error"] == nil {
		t.Fatal("nil lookup should error")
	}
	nf := NewServer(rs)
	nf.TraceLookup = func(id string) (any, error) { return nil, tracker.ErrTraceNotFound }
	notFound := runSession(t, nf,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"inspect_trace","arguments":{"trace_id":"nope"}}}`,
	)
	e, _ := notFound[0]["error"].(map[string]any)
	if e == nil || e["code"].(float64) != -32004 || !strings.Contains(e["message"].(string), "nope") {
		t.Fatalf("unknown trace should be a distinct not-found error: %+v", notFound[0])
	}
}

func TestDriftReport(t *testing.T) {
	rs := router.NewServer("fast-m", "quality-m", &stubGen{text: "hi"})
	srv := NewServer(rs)
	srv.DriftLookup = func() (any, error) {
		return map[string]any{"score_then": 0.95, "score_now": 0.55, "delta": -0.4, "alert": true}, nil
	}
	resps := runSession(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_drift_report","arguments":{}}}`,
	)
	content := resps[0]["result"].(map[string]any)["content"].([]any)
	if !strings.Contains(content[0].(map[string]any)["text"].(string), "score_now") {
		t.Fatalf("drift report should return scores: %+v", resps[0])
	}
	bare := NewServer(rs)
	missing := runSession(t, bare,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_drift_report","arguments":{}}}`,
	)
	if missing[0]["error"] == nil {
		t.Fatal("nil drift lookup should error")
	}
}

func TestToolCallEmitsRedactedSpan(t *testing.T) {
	rs := router.NewServer("fast-m", "quality-m", &stubGen{text: "hi"})
	srv := NewServer(rs)
	var spans []map[string]string
	srv.SpanSink = func(traceID, spanID, parentID, name, attrs string) {
		spans = append(spans, map[string]string{"trace": traceID, "span": spanID, "name": name, "attrs": attrs})
	}
	resps := runSession(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"route_test_request","arguments":{"prompt":"CANARY-mcp-do-not-store hello"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_models","arguments":{}}}`,
	)
	if resps[0]["error"] != nil || resps[1]["error"] != nil {
		t.Fatalf("tool calls should succeed: %+v", resps)
	}
	if len(spans) != 2 {
		t.Fatalf("want 1 span per tool call, got %d", len(spans))
	}
	for _, sp := range spans {
		if sp["name"] != "mcp.tool" {
			t.Fatalf("span name = %q, want mcp.tool", sp["name"])
		}
		if sp["trace"] == "" || sp["span"] == "" {
			t.Fatalf("span needs trace and span ids: %+v", sp)
		}
		if strings.Contains(sp["attrs"], "CANARY-mcp-do-not-store") {
			t.Fatalf("prompt text leaked into mcp.tool span: %s", sp["attrs"])
		}
	}
	if !strings.Contains(spans[0]["attrs"], "route_test_request") {
		t.Fatalf("tool span should name the tool: %s", spans[0]["attrs"])
	}
	for _, sp := range spans {
		var keys map[string]any
		if err := json.Unmarshal([]byte(sp["attrs"]), &keys); err != nil {
			t.Fatalf("tool span attrs are not JSON: %s", sp["attrs"])
		}
		for k := range keys {
			if k != "tool" && k != "ok" && k != "code" {
				t.Fatalf("tool span carries non-allowlisted key %q: %s", k, sp["attrs"])
			}
		}
	}
}

func TestHandleOneServesHTTPTransport(t *testing.T) {
	rs := router.NewServer("fast-m", "quality-m", &stubGen{text: "hi"})
	srv := NewServer(rs)
	raw, status := srv.HandleOne([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(string(raw), "list_models") {
		t.Fatalf("tools/list over HTTP: %s", raw)
	}
	raw, _ = srv.HandleOne([]byte(`{bad json`))
	if !strings.Contains(string(raw), "parse error") {
		t.Fatalf("malformed body should be a parse error: %s", raw)
	}
}
