package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"agentops/router"
	"agentops/tracker"
)

const protocolVersion = "2024-11-05"

type Server struct {
	Router      *router.Server
	TraceLookup func(traceID string) (any, error)
	DriftLookup func() (any, error)
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
			Description: "List the models behind the router with their speed tiers.",
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
		return textResult(map[string]any{"models": []any{
			map[string]any{"name": s.Router.FastModel, "tier": "fast"},
			map[string]any{"name": s.Router.QualityModel, "tier": "quality"},
		}}), nil
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

func (s *Server) handle(req rpcRequest) *rpcResponse {
	switch req.Method {
	case "initialize":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "agentops", "version": "0.1.0"},
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
		resp := s.handle(req)
		if resp == nil {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}
