package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"agentops/router"
)

func testServer() *Server {
	return NewServer(router.NewServer("fast-m", "quality-m", &stubGen{text: "hi"}))
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, raw)
	}
	return out
}

// TestDiscoverListsVersionsAndTools: server/discover is mandatory in 2026-07-28;
// a client that calls it before anything else must learn the versions, the tool
// capability and the server identity in one response.
func TestDiscoverListsVersionsAndTools(t *testing.T) {
	raw, status := testServer().HandleOne([]byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover"}`))
	if status != 200 {
		t.Fatalf("status %d: %s", status, raw)
	}
	res := decode(t, raw)["result"].(map[string]any)
	versions := res["supportedVersions"].([]any)
	found := false
	for _, v := range versions {
		if v == "2026-07-28" {
			found = true
		}
	}
	if !found {
		t.Fatalf("supportedVersions = %v", versions)
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("tools capability missing: %v", res["capabilities"])
	}
	meta := res["_meta"].(map[string]any)["io.modelcontextprotocol/serverInfo"].(map[string]any)
	if meta["name"] != "agentops" {
		t.Fatalf("serverInfo = %v", meta)
	}
}

// TestModernRequestNeedsNoInitialize: a 2026-07-28 request carries its version in
// params._meta and is served statelessly; no handshake.
func TestModernRequestNeedsNoInitialize(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	raw, status := testServer().HandleOne([]byte(body))
	if status != 200 {
		t.Fatalf("status %d: %s", status, raw)
	}
	res := decode(t, raw)["result"].(map[string]any)
	if len(res["tools"].([]any)) == 0 {
		t.Fatalf("no tools: %v", res)
	}
}

// TestUnsupportedVersionIsRejected: -32022 with the versions we do support, so
// the client can retry instead of guessing.
func TestUnsupportedVersionIsRejected(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01"}}}`
	raw, status := testServer().HandleOne([]byte(body))
	if status != 400 {
		t.Fatalf("status %d: %s", status, raw)
	}
	e := decode(t, raw)["error"].(map[string]any)
	if int(e["code"].(float64)) != -32022 {
		t.Fatalf("code = %v", e["code"])
	}
	data := e["data"].(map[string]any)
	if data["requested"] != "1900-01-01" || len(data["supported"].([]any)) == 0 {
		t.Fatalf("data = %v", data)
	}
}

// TestHTTPHeaderVersionNegotiates: on Streamable HTTP the version also arrives in
// the MCP-Protocol-Version header, for a body that carries no _meta.
func TestHTTPHeaderVersionNegotiates(t *testing.T) {
	srv := testServer()
	body := []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	if raw, status := srv.HandleOneHTTP(body, "2026-07-28"); status != 200 || strings.Contains(string(raw), "error") {
		t.Fatalf("supported header: %d %s", status, raw)
	}
	raw, status := srv.HandleOneHTTP(body, "1900-01-01")
	if status != 400 || !strings.Contains(string(raw), "-32022") {
		t.Fatalf("unsupported header: %d %s", status, raw)
	}
}

// TestLegacyInitializeStillWorks: a 2024 client that asks for its own version gets
// it echoed; one that asks for nothing gets the legacy default. Both then use
// tools/list exactly as before.
func TestLegacyInitializeStillWorks(t *testing.T) {
	srv := testServer()
	raw, _ := srv.HandleOne([]byte(`{"jsonrpc":"2.0","id":5,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`))
	if got := decode(t, raw)["result"].(map[string]any)["protocolVersion"]; got != "2024-11-05" {
		t.Fatalf("echoed %v, want the client's version", got)
	}
	raw, _ = srv.HandleOne([]byte(`{"jsonrpc":"2.0","id":6,"method":"initialize","params":{}}`))
	if got := decode(t, raw)["result"].(map[string]any)["protocolVersion"]; got != "2024-11-05" {
		t.Fatalf("default %v, want 2024-11-05", got)
	}
}
