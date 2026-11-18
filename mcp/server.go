package mcp

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"

	"tower/router"
	"tower/tracker"
)

// legacyProtocolVersion is what an initialize with no requested version gets, and
// what this server answered before it learned the stateless revision.
const legacyProtocolVersion = "2024-11-05"

// supportedVersions is newest first: 2026-07-28 is the stateless revision this
// server implements (per-request version in params._meta or the
// MCP-Protocol-Version header, mandatory server/discover); the rest are the legacy
// handshake revisions it still serves. It is also what an
// UnsupportedProtocolVersionError lists so a client can retry instead of guessing.
var supportedVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type Server struct {
	Router      *router.Server
	TraceLookup func(traceID string) (any, error)
	DriftLookup func() (any, error)
	// SpanSink, when set, receives one redacted mcp.tool span per tools/call.
	// Nil (tests, bare stdio) means silent — same optional-sink shape as router.
	SpanSink router.SpanSink
}

func NewServer(r *router.Server) *Server {
	return &Server{Router: r}
}

type rpcRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  any              `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type toolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

func schema(props map[string]any, required []string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func (s *Server) toolList() []toolDef {
	return []toolDef{
		{
			Name:        "list_models",
			Description: "List the models behind the router with their tiers, backends and last health probe.",
			InputSchema: schema(map[string]any{}, []string{}),
		},
		{
			Name:        "get_stats",
			Description: "Request counts and token totals per model since the router started.",
			InputSchema: schema(map[string]any{}, []string{}),
		},
		{
			Name:        "route_test_request",
			Description: "Send one prompt through the router and see which model handled it and why.",
			InputSchema: schema(map[string]any{
				"prompt": map[string]any{"type": "string", "description": "Prompt to route."},
			}, []string{"prompt"}),
		},
		{
			Name:        "inspect_trace",
			Description: "Show every span for one trace_id (prompts redacted). Works for router chats and tracker workflows.",
			InputSchema: schema(map[string]any{
				"trace_id": map[string]any{"type": "string", "description": "Trace ID from a chat response or workflow ID."},
			}, []string{"trace_id"}),
		},
		{
			Name:        "get_drift_report",
			Description: "Compare the last two golden eval runs: scores, delta, alert, worst cases.",
			InputSchema: schema(map[string]any{}, []string{}),
		},
	}
}

func textResult(v any) map[string]any {
	raw, _ := json.Marshal(v)
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(raw)}}}
}

func (s *Server) callTool(name string, args map[string]any) (any, *rpcError) {
	switch name {
	case "list_models":
		models := make([]any, 0, len(s.Router.Models))
		for _, ref := range s.Router.Models {
			m := map[string]any{"name": ref.Model, "tier": ref.Tier, "backend": ref.Backend.Name()}
			if s.Router.Prober != nil {
				m["up"] = s.Router.Prober.Up(ref.Backend.Name())
			}
			models = append(models, m)
		}
		out := map[string]any{"models": models}
		if s.Router.Prober != nil {
			out["backends"] = s.Router.Prober.Statuses()
		}
		return textResult(out), nil
	case "get_stats":
		snap := s.Router.Metrics.Snapshot()
		names := make([]string, 0, len(snap))
		for n := range snap {
			names = append(names, n)
		}
		sort.Strings(names)
		rows := make([]any, 0, len(names))
		for _, n := range names {
			rows = append(rows, map[string]any{
				"model": n, "requests": snap[n].Requests, "tokens": snap[n].Tokens,
			})
		}
		return textResult(map[string]any{"stats": rows}), nil
	case "route_test_request":
		prompt, _ := args["prompt"].(string)
		if prompt == "" {
			return nil, &rpcError{Code: -32602, Message: "prompt is required"}
		}
		out, err := s.Router.Chat(prompt, 0)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: "model backend unavailable"}
		}
		return textResult(map[string]any{
			"text": out.Text, "model": out.Model, "reason": out.Reason, "trace_id": out.TraceID,
		}), nil
	case "inspect_trace":
		traceID, _ := args["trace_id"].(string)
		if traceID == "" {
			return nil, &rpcError{Code: -32602, Message: "trace_id is required"}
		}
		if s.TraceLookup == nil {
			return nil, &rpcError{Code: -32000, Message: "trace store unavailable"}
		}
		data, err := s.TraceLookup(traceID)
		if errors.Is(err, tracker.ErrTraceNotFound) {
			return nil, &rpcError{Code: -32004, Message: fmt.Sprintf("trace %q not found", traceID)}
		}
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: "trace store unavailable"}
		}
		return textResult(data), nil
	case "get_drift_report":
		if s.DriftLookup == nil {
			return nil, &rpcError{Code: -32000, Message: "eval store unavailable"}
		}
		data, err := s.DriftLookup()
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: "eval store unavailable"}
		}
		return textResult(data), nil
	default:
		return nil, &rpcError{Code: -32601, Message: fmt.Sprintf("unknown tool %q", name)}
	}
}

// emitToolSpan records one span per tool call so the live view is never empty.
// Attributes carry the tool name and outcome only — never argument values, so a
// prompt passed to route_test_request cannot leak through telemetry.
func (s *Server) emitToolSpan(tool string, rpcErr *rpcError) {
	if s.SpanSink == nil {
		return
	}
	attrs := map[string]any{"tool": tool, "ok": rpcErr == nil}
	if rpcErr != nil {
		attrs["code"] = rpcErr.Code
	}
	raw, _ := json.Marshal(attrs)
	s.SpanSink(newToolTraceID(), newToolTraceID(), "", "mcp.tool", string(raw))
}

func newToolTraceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Server) handle(req rpcRequest, headerVersion string) *rpcResponse {
	version := paramsVersion(req.Params)
	if version == "" {
		version = headerVersion
	}
	if version != "" && !supportedVersion(version) {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{
			Code: -32022, Message: "Unsupported protocol version",
			Data: map[string]any{"supported": supportedVersions, "requested": version},
		}}
	}
	switch req.Method {
	case "server/discover":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"resultType":        "complete",
			"supportedVersions": supportedVersions,
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"_meta": map[string]any{
				"io.modelcontextprotocol/serverInfo": map[string]any{"name": "tower", "version": "0.1.0"},
			},
			"instructions": "Tower gateway: list served models and their health, read live stats, test which tier a prompt routes to, inspect a redacted trace, read the eval drift report.",
			"ttlMs":        3600000,
			"cacheScope":   "public",
		}}
	case "initialize":
		negotiated := legacyProtocolVersion
		if v := paramsProtocolVersion(req.Params); supportedVersion(v) {
			negotiated = v
		} else if v != "" {
			return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{
				Code: -32022, Message: "Unsupported protocol version",
				Data: map[string]any{"supported": supportedVersions, "requested": v},
			}}
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": negotiated,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "tower", "version": "0.1.0"},
		}}
	case "notifications/initialized":
		return nil
	case "ping":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": s.toolList()}}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "bad params"}}
		}
		result, rpcErr := s.callTool(params.Name, params.Arguments)
		s.emitToolSpan(params.Name, rpcErr)
		if rpcErr != nil {
			return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr}
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	default:
		if req.ID == nil {
			return nil
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: fmt.Sprintf("unknown method %q", req.Method)}}
	}
}

// paramsVersion reads the modern per-request version from params._meta. Empty
// means the caller is speaking a legacy revision.
func paramsVersion(raw json.RawMessage) string {
	var p struct {
		Meta struct {
			ProtocolVersion string `json:"io.modelcontextprotocol/protocolVersion"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}
	return p.Meta.ProtocolVersion
}

// paramsProtocolVersion reads the legacy initialize field.
func paramsProtocolVersion(raw json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}
	return p.ProtocolVersion
}

func supportedVersion(v string) bool {
	for _, s := range supportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// HandleOne serves a single JSON-RPC request body — the Streamable-HTTP
// transport. It reuses handle() so stdio and HTTP can never drift apart.
// Notifications (nil response) answer 202 with an empty result.
func (s *Server) HandleOne(body []byte) ([]byte, int) {
	return s.HandleOneHTTP(body, "")
}

// HandleOneHTTP is HandleOne plus the MCP-Protocol-Version header the 2026-07-28
// HTTP transport carries. The body's params._meta wins when both are present.
func (s *Server) HandleOneHTTP(body []byte, protocolVersion string) ([]byte, int) {
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		raw, _ := json.Marshal(&rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return raw, http.StatusOK
	}
	resp := s.handle(req, protocolVersion)
	if resp == nil {
		return []byte(`{"jsonrpc":"2.0","result":{}}`), http.StatusAccepted
	}
	raw, _ := json.Marshal(resp)
	// An unsupported version is a modern client's cue to retry: the transport
	// spec pairs it with 400 so a dual-era client can inspect the body and fall
	// back instead of treating it as a server fault.
	if resp.Error != nil && resp.Error.Code == -32022 {
		return raw, http.StatusBadRequest
	}
	return raw, http.StatusOK
}

func (s *Server) Run(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			if err := enc.Encode(&rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}}); err != nil {
				return err
			}
			continue
		}
		resp := s.handle(req, "")
		if resp == nil {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}
