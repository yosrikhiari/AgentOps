// Package policy turns the rules in docs/RULES.md into tests. Each test names the rule it
// enforces; if a rule cannot be checked mechanically it is not here, it is in the doc with
// the word "review" next to it. CI runs this package like any other.
package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"agentops/mcp"
	"agentops/router"
	"agentops/tracker"
)

var repo = func() string {
	wd, _ := os.Getwd()
	return filepath.Dir(wd) // policy/ lives one level below the module root
}()

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// goFiles returns every non-test .go file under the module, excluding this package.
func goFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(repo, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "policy" || d.Name() == "lessons") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// RULE dependencies: the only direct dependency is the Postgres driver. Anything that does
// "the CV-story thing" (a gateway, an eval framework, a workflow engine) must be written here.
func TestPolicySingleDirectDependency(t *testing.T) {
	mod := read(t, "go.mod")
	var direct []string
	inBlock := false
	for _, line := range strings.Split(mod, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "require ("):
			inBlock = true
		case line == ")":
			inBlock = false
		case inBlock && line != "" && !strings.Contains(line, "// indirect"):
			direct = append(direct, strings.Fields(line)[0])
		case strings.HasPrefix(line, "require ") && !strings.Contains(line, "("):
			direct = append(direct, strings.Fields(line)[1])
		}
	}
	if len(direct) != 1 || direct[0] != "github.com/jackc/pgx/v5" {
		t.Fatalf("direct dependencies must be exactly [github.com/jackc/pgx/v5], got %v", direct)
	}
}

// RULE privacy: the gateway never persists a prompt. Send a canary through the router with
// a span sink attached and assert the canary appears in no span attribute.
func TestPolicyPromptsNeverReachSpans(t *testing.T) {
	canary := "CANARY-7f3a-do-not-store"
	be := &fake{text: "ok " + canary + " echoed"} // even an echoing model must not leak via spans
	srv := router.NewServer("fast-m", "quality-m", be)
	var attrs []string
	srv.SpanSink = func(traceID, spanID, parentID, name, a string) { attrs = append(attrs, a) }
	if _, err := srv.Chat("please remember "+canary, 0); err != nil {
		t.Fatal(err)
	}
	if len(attrs) != 3 {
		t.Fatalf("want 3 spans, got %d", len(attrs))
	}
	for _, a := range attrs {
		if strings.Contains(a, canary) {
			t.Fatalf("prompt/answer text leaked into span attrs: %s", a)
		}
	}
	// and the read path redacts the keys a future span might carry
	if out := tracker.RedactAttrs(`{"prompt":"x","input":"y","text":"z","output":"w","model":"m"}`); strings.Contains(out, "x") || !strings.Contains(out, `"model"`) {
		t.Fatalf("RedactAttrs must drop prompt/input/text/output and keep the rest, got %s", out)
	}
}

// RULE fail-closed: a sensitive request has no non-local candidate, whatever was asked.
func TestPolicySensitiveHasNoCloudCandidate(t *testing.T) {
	local := &fake{name: "ollama", local: true}
	cloud := &fake{name: "groq"}
	refs := []router.ModelRef{
		{Tier: "fast", Model: "f", Backend: local},
		{Tier: "quality", Model: "q", Backend: local},
		{Tier: "cloud", Model: "c", Backend: cloud},
	}
	msgs := []router.Message{{Role: "user", Content: "my iban is TN59"}}
	r, err := router.Plan("auto", msgs, true, refs)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Candidates {
		if c.Backend.Name() == "groq" {
			t.Fatalf("cloud candidate present for sensitive request: %+v", r.Candidates)
		}
	}
	if _, err := router.Plan("c", msgs, true, refs); err == nil {
		t.Fatal("explicit cloud model on sensitive data must be refused")
	}
	// Ollama-hosted models look local to the backend; the model name must veto locality.
	hosted := []router.ModelRef{
		{Tier: "fast", Model: "qwen2.5:3b-instruct", Backend: local},
		{Tier: "quality", Model: "kimi-k2.6:cloud", Backend: local},
	}
	r, err = router.Plan("auto", []router.Message{{Role: "user", Content: strings.Repeat("secret ", 40)}}, true, hosted)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Candidates {
		if !c.Local() {
			t.Fatalf("hosted :cloud model reachable by a sensitive request: %+v", c.Model)
		}
	}
}

// RULE one error shape: every non-2xx from the router carries {error:{code,message,trace_id}}.
func TestPolicyOneErrorShape(t *testing.T) {
	srv := router.NewServer("fast-m", "quality-m", &fake{err: errors.New("down")})
	ks := router.NewMemKeyStore()
	srv.Keys, srv.RequireKey = ks, true
	cases := []struct {
		body   string
		header map[string]string
		want   int
	}{
		{`{"prompt":"hi"}`, nil, 401},
		{`{"prompt":"hi"}`, map[string]string{"Authorization": "Bearer ak_bad"}, 401},
		{`{}`, map[string]string{"Authorization": "Bearer " + ks.Create("a", 0, 0)}, 400},
		{`{"prompt":"hi"}`, map[string]string{"Authorization": "Bearer " + ks.Create("b", 0, 0)}, 502},
		{`{"model":"nope","prompt":"hi"}`, map[string]string{"Authorization": "Bearer " + ks.Create("c", 0, 0)}, 400},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(c.body)))
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Fatalf("%s: status %d, want %d", c.body, rec.Code, c.want)
		}
		var generic map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &generic); err != nil {
			t.Fatalf("%d: not JSON: %s", rec.Code, rec.Body.String())
		}
		e, _ := generic["error"].(map[string]any)
		if e == nil || e["code"] == "" || e["message"] == "" || e["trace_id"] == "" {
			t.Fatalf("%d: error shape violated: %s", rec.Code, rec.Body.String())
		}
	}
}

// RULE bounded async: nothing on the request path blocks on telemetry. The span sink must
// be a non-blocking send — enforced by reading the implementation for the select/default.
func TestPolicySpanSinkIsNonBlocking(t *testing.T) {
	src := read(t, "main.go")
	i := strings.Index(src, "func (w *spanWriter) Sink()")
	if i < 0 {
		t.Fatal("spanWriter.Sink not found")
	}
	body := src[i:]
	body = body[:strings.Index(body, "\n}\n")+3]
	if !strings.Contains(body, "select {") || !strings.Contains(body, "default:") {
		t.Fatalf("Sink must use a select with a default branch (drop, never block):\n%s", body)
	}
}

// RULE every environment variable the code reads is documented in docs/API.md.
// Reads go through os.Getenv or the envOr(key, default) helper in config.go.
func TestPolicyEnvVarsDocumented(t *testing.T) {
	re := regexp.MustCompile(`(?:os\.Getenv|envOr)\("([A-Z0-9_]+)"`)
	docs := read(t, "docs/API.md")
	seen := map[string]bool{}
	for _, f := range goFiles(t) {
		b, _ := os.ReadFile(f)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			seen[m[1]] = true
		}
	}
	var missing []string
	for name := range seen {
		if !strings.Contains(docs, "`"+name+"`") {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("env vars read by the code but absent from docs/API.md: %v", missing)
	}
}

// RULE every CLI flag is documented in README.md.
func TestPolicyFlagsDocumented(t *testing.T) {
	re := regexp.MustCompile(`flag\.(?:Bool|String|Int|Int64|Duration)\("([a-z0-9-]+)"`)
	readme := read(t, "README.md")
	var missing []string
	for _, m := range re.FindAllStringSubmatch(read(t, "main.go"), -1) {
		if !strings.Contains(readme, "--"+m[1]) {
			missing = append(missing, "--"+m[1])
		}
	}
	if len(missing) > 0 {
		t.Fatalf("flags absent from README.md: %v", missing)
	}
}

// RULE migrations are numbered contiguously from 0001 and never renamed.
func TestPolicyMigrationsContiguous(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(repo, "migrations", "*.sql"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no migrations")
	}
	for i, f := range files {
		base := filepath.Base(f)
		n, err := strconv.Atoi(base[:4])
		if err != nil || n != i+1 || base[4] != '_' {
			t.Fatalf("migration %q breaks the NNNN_name.sql sequence at position %d", base, i+1)
		}
	}
}

// RULE every ADR referenced in the README exists, and every ADR file is in the index.
func TestPolicyADRsIndexed(t *testing.T) {
	index := read(t, "docs/adr/README.md")
	files, _ := filepath.Glob(filepath.Join(repo, "docs", "adr", "0*.md"))
	for _, f := range files {
		if !strings.Contains(index, filepath.Base(f)) {
			t.Fatalf("ADR %s missing from docs/adr/README.md", filepath.Base(f))
		}
	}
	for _, m := range regexp.MustCompile(`ADR-(\d{4})`).FindAllStringSubmatch(read(t, "README.md"), -1) {
		if g, _ := filepath.Glob(filepath.Join(repo, "docs", "adr", m[1]+"-*.md")); len(g) == 0 {
			t.Fatalf("README cites ADR-%s but no docs/adr/%s-*.md exists", m[1], m[1])
		}
	}
}

// RULE no retry loops around backend chat calls: a failing backend is not retried, the
// router moves to the next candidate (retries live only in the judge, with backoff).
func TestPolicyRouterDoesNotRetrySameBackend(t *testing.T) {
	be := &fake{name: "ollama", local: true, err: errors.New("down")}
	srv := router.NewServer("fast-m", "quality-m", be)
	_, err := srv.Chat("hi", 0)
	if err == nil {
		t.Fatal("expected failure")
	}
	// fast then quality on the same backend = 2 calls, never more
	if be.calls != 2 {
		t.Fatalf("backend called %d times, want 2 (one per candidate, no retries)", be.calls)
	}
}

// ---- minimal fake backend ----

type fake struct {
	name  string
	local bool
	text  string
	err   error
	calls int
}

func (f *fake) Name() string {
	if f.name == "" {
		return "ollama"
	}
	return f.name
}
func (f *fake) Local() bool { return f.local || f.name == "" }
func (f *fake) Generate(ctx context.Context, model string, msgs []router.Message, maxTokens int) (string, router.Usage, error) {
	f.calls++
	if f.err != nil {
		return "", router.Usage{}, f.err
	}
	return f.text, router.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}, nil
}
func (f *fake) Stream(ctx context.Context, model string, msgs []router.Message, maxTokens int, emit func(string)) (router.Usage, error) {
	f.calls++
	if f.err != nil {
		return router.Usage{}, f.err
	}
	emit(f.text)
	return router.Usage{TotalTokens: 2}, nil
}
func (f *fake) Health(ctx context.Context) error { return nil }

// RULE privacy extends to MCP telemetry: mcp.tool spans carry the tool name and
// outcome only — a prompt sent via route_test_request appears in no span, even
// when the model echoes it back into the tool result.
func TestPolicyMCPToolSpansRedacted(t *testing.T) {
	canary := "CANARY-mcp-policy-do-not-store"
	rs := router.NewServer("fast-m", "quality-m", &echoGen{})
	msrv := mcp.NewServer(rs)
	var got []string
	msrv.SpanSink = func(traceID, spanID, parentID, name, attrs string) {
		if name != "mcp.tool" {
			t.Fatalf("MCP sink got span %q, want only mcp.tool", name)
		}
		got = append(got, attrs)
	}
	raw, status := msrv.HandleOne([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"route_test_request","arguments":{"prompt":"remember ` + canary + `"}}}`))
	if status != 200 {
		t.Fatalf("status %d: %s", status, raw)
	}
	if !strings.Contains(string(raw), canary) {
		t.Fatalf("fixture is weak: tool result should echo the prompt, got %s", raw)
	}
	if len(got) == 0 {
		t.Fatal("route_test_request emitted no mcp.tool span")
	}
	for _, a := range got {
		if strings.Contains(a, canary) {
			t.Fatalf("prompt leaked into mcp.tool span: %s", a)
		}
		if !strings.Contains(a, "route_test_request") {
			t.Fatalf("mcp.tool span must name the tool: %s", a)
		}
	}
}

// echoGen is a backend that repeats the prompt, so redaction tests prove the
// fence even against an echoing model.

type echoGen struct{}

func (g *echoGen) Name() string { return "ollama" }
func (g *echoGen) Local() bool  { return true }
func (g *echoGen) Generate(ctx context.Context, model string, msgs []router.Message, maxTokens int) (string, router.Usage, error) {
	out := "saw:"
	for _, m := range msgs {
		out += " " + m.Content
	}
	return out, router.Usage{TotalTokens: 9}, nil
}
func (g *echoGen) Stream(ctx context.Context, model string, msgs []router.Message, maxTokens int, emit func(string)) (router.Usage, error) {
	s, u, err := g.Generate(ctx, model, msgs, maxTokens)
	emit(s)
	return u, err
}
func (g *echoGen) Health(ctx context.Context) error { return nil }

// RULE stored conversations stay private by construction: messages die with
// their thread (ON DELETE CASCADE), idle threads have a 90-day purge, and the
// exception to "prompts are never stored" is disclosed in PRIVACY.md.
func TestPolicyConversationsPrivate(t *testing.T) {
	mig := read(t, "migrations/0005_conversations.sql")
	for _, must := range []string{"ON DELETE CASCADE", "90 days", "CREATE TABLE IF NOT EXISTS conversations", "CREATE TABLE IF NOT EXISTS messages"} {
		if !strings.Contains(mig, must) {
			t.Errorf("0005_conversations.sql missing %q", must)
		}
	}
	priv := read(t, "PRIVACY.md")
	for _, must := range []string{"conversations", "90", "DELETE /v1/conversations/{id}"} {
		if !strings.Contains(priv, must) {
			t.Errorf("PRIVACY.md must disclose %q", must)
		}
	}
}

// RULE the rules document only cites tests that exist: every `TestX` named in docs/RULES.md
// must be defined somewhere in the module, so an enforcement column can never go stale.
func TestPolicyRulesCiteRealTests(t *testing.T) {
	rules := read(t, "docs/RULES.md")
	cited := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]+`).FindAllString(rules, -1) {
		cited[m] = true
	}
	defined := map[string]bool{}
	err := filepath.WalkDir(repo, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, "_test.go") {
			b, _ := os.ReadFile(p)
			for _, m := range regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`).FindAllStringSubmatch(string(b), -1) {
				defined[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for name := range cited {
		if !defined[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("docs/RULES.md cites tests that do not exist: %v", missing)
	}
}

// RULE the console is built from the Tower design system: colours are --tower-* tokens
// declared once in the :root block of tower.css, never hex literals in JS, HTML or the
// rest of the stylesheet (docs/RULES.md §12, docs/tower-design-system.html).
func TestPolicyConsoleColoursAreTokens(t *testing.T) {
	hex := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
	for _, rel := range []string{"console/static/app.js", "console/static/index.html"} {
		for _, m := range hex.FindAllString(read(t, rel), -1) {
			if !strings.HasPrefix(m, "#/") { // hash routes are fine
				t.Errorf("%s: hex colour %q — use a --tower-* token", rel, m)
			}
		}
	}
	css := read(t, "console/static/tower.css")
	root := regexp.MustCompile(`(?s):root\s*\{.*?\}`)
	outside := root.ReplaceAllString(css, "")
	for _, m := range hex.FindAllString(outside, -1) {
		t.Errorf("tower.css: hex colour %q outside :root — add a token", m)
	}
}

// RULE every tower-* class the console references exists in tower.css. A typo'd class
// passes gofmt, vet, the Go tests and the browser — only the screen breaks.
func TestPolicyConsoleClassesExist(t *testing.T) {
	css := read(t, "console/static/tower.css")
	cls := regexp.MustCompile(`tower-[a-z0-9-]+`)
	seen := map[string]bool{}
	for _, rel := range []string{"console/static/app.js", "console/static/index.html"} {
		for _, m := range cls.FindAllString(read(t, rel), -1) {
			seen[m] = true
		}
	}
	var missing []string
	for name := range seen {
		if !strings.Contains(css, name) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("tower-* names used by the console but absent from tower.css: %v", missing)
	}
	if len(seen) < 40 {
		t.Fatalf("only %d tower-* names found — the scan is broken, not the console", len(seen))
	}
}

// ---- presence-reporting fake: healthy backend with a partial pulled set ----

type presenceFake struct {
	fake
	pulled map[string]bool
}

func (f *presenceFake) PulledModels(ctx context.Context) (map[string]bool, error) {
	return f.pulled, nil
}

// RULE Track J: a request for a model that is not on disk is a loud 503 in the
// one error shape — never a 502 surprise at request time, never a silent skip.
func TestPolicyModelNotPulledIsOneErrorShape(t *testing.T) {
	pb := &presenceFake{fake: fake{local: true, text: "hi"},
		pulled: map[string]bool{"fast-m": true}}
	srv := router.NewServer("fast-m", "quality-m", pb)
	prober := router.NewHealthProber(srv.Metrics, pb)
	prober.ProbeOnce(context.Background())
	srv.Prober = prober
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"quality-m","messages":[{"role":"user","content":"hi"}]}`)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("explicit unpulled model: status %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	var generic map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &generic); err != nil {
		t.Fatalf("not JSON: %s", rec.Body.String())
	}
	e, _ := generic["error"].(map[string]any)
	if e == nil || e["code"] != "model_not_pulled" || e["message"] == "" || e["trace_id"] == "" {
		t.Fatalf("error shape violated: %s", rec.Body.String())
	}
	if pb.calls != 0 {
		t.Fatalf("unpulled model attempted %d times", pb.calls)
	}
}

// RULE AGENTS.md is the one agent instruction file; every per-tool file points to it, and it
// cites the rule book and the design system (AGENTS.md → "Which tool reads what").
func TestPolicyAgentFilesPointHere(t *testing.T) {
	agents := read(t, "AGENTS.md")
	for _, must := range []string{"docs/RULES.md", "docs/tower-design-system.html", "go test -count=1 ./policy/"} {
		if !strings.Contains(agents, must) {
			t.Errorf("AGENTS.md no longer mentions %q", must)
		}
	}
	for _, rel := range []string{"CLAUDE.md", "GEMINI.md", "opencode.json", ".cursor/rules/agents.mdc", ".github/copilot-instructions.md"} {
		if !strings.Contains(read(t, rel), "AGENTS.md") {
			t.Errorf("%s does not point at AGENTS.md", rel)
		}
	}
	if strings.Contains(read(t, "CLAUDE.md"), "## ") {
		t.Errorf("CLAUDE.md has its own sections — it must stay a pointer (@AGENTS.md), not a second rule file")
	}
}
