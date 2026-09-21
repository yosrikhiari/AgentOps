package console

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentops/evals"
)

type memStore struct {
	reqs  []Request
	runs  []EvalRun
	wfs   []Workflow
	err   error
	convs map[string]*memConv
	bench BenchmarkInput
}

type memConv struct {
	Conversation
	msgs []Message
}

func (m *memStore) convsInit() {
	if m.convs == nil {
		m.convs = map[string]*memConv{}
	}
}

func (m *memStore) RecentRequests(ctx context.Context, limit int) ([]Request, error) {
	return m.reqs, m.err
}
func (m *memStore) Overview(ctx context.Context) (Overview, error) {
	return Overview{RequestsLastHour: len(m.reqs), ByModel: map[string]int{"m": len(m.reqs)}}, m.err
}
func (m *memStore) EvalRuns(ctx context.Context, golden string, limit int) ([]EvalRun, error) {
	return m.runs, m.err
}
func (m *memStore) Workflows(ctx context.Context, limit int) ([]Workflow, error) { return m.wfs, m.err }

func (m *memStore) BenchmarkData(ctx context.Context, golden string) (BenchmarkInput, error) {
	if m.err != nil {
		return BenchmarkInput{}, m.err
	}
	out := m.bench
	out.Golden = golden
	return out, nil
}

func (m *memStore) Conversations(ctx context.Context, limit int) ([]Conversation, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.convsInit()
	var out []Conversation
	for _, c := range m.convs {
		out = append(out, c.Conversation)
	}
	if out == nil {
		out = []Conversation{}
	}
	return out, nil
}

func (m *memStore) CreateConversation(ctx context.Context, title, model string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	m.convsInit()
	id := "mconv-" + strconv.Itoa(len(m.convs))
	m.convs[id] = &memConv{Conversation: Conversation{ID: id, Title: title, Model: model}}
	return id, nil
}

func (m *memStore) ConversationMessages(ctx context.Context, id string, limit int) ([]Message, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.convsInit()
	c, ok := m.convs[id]
	if !ok {
		return nil, ErrConvNotFound
	}
	out := c.msgs
	if out == nil {
		out = []Message{}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (m *memStore) AppendMessage(ctx context.Context, convID string, msg Message) error {
	if m.err != nil {
		return m.err
	}
	m.convsInit()
	c, ok := m.convs[convID]
	if !ok {
		return ErrConvNotFound
	}
	c.msgs = append(c.msgs, msg)
	if c.Title == "" && msg.Role == "user" {
		c.Title = msg.Content
	}
	return nil
}

func (m *memStore) DeleteConversation(ctx context.Context, id string) error {
	if m.err != nil {
		return m.err
	}
	m.convsInit()
	if _, ok := m.convs[id]; !ok {
		return ErrConvNotFound
	}
	delete(m.convs, id)
	return nil
}

func (m *memStore) PurgeConversations(ctx context.Context, days int) (int64, error) {
	return 0, m.err
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The 90-day purge died live with "unable to encode 90 into text format for
// text": ($1 || ' days') types the param as text while pgx hands it an int, so
// retention never ran. The interval must be built from a typed int parameter.
type captureExec struct {
	sql  string
	args []any
}

func (c *captureExec) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	c.sql, c.args = sql, args
	return 0, nil
}

func TestPurgeConversationsBuildsTypedInterval(t *testing.T) {
	ex := &captureExec{}
	s := SQLStore{Exec: ex}
	if _, err := s.PurgeConversations(context.Background(), 90); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if strings.Contains(ex.sql, "||") {
		t.Fatalf("param text-concatenated into interval (pgx int/text mismatch): %q", ex.sql)
	}
	if !strings.Contains(ex.sql, "make_interval") {
		t.Fatalf("expected a typed make_interval purge: %q", ex.sql)
	}
	if len(ex.args) != 1 || ex.args[0] != 90 {
		t.Fatalf("days not passed as one int param: %v", ex.args)
	}
}

// A value encoding/json cannot represent (NaN from a 0/0 metric) must never
// leave the console emitting headers with an empty body: the page would show
// nothing with no error to retry on. Loud 502 instead.
func TestWriteJSONNaNIs502(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]any{"score": math.NaN()})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %q)", rec.Code, rec.Body.String())
	}
	var generic map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &generic); err != nil || generic["error"] == nil {
		t.Fatalf("not a one-error-shape body: %q", rec.Body.String())
	}
}

func TestBenchmarksEndpoint(t *testing.T) {
	mk := func() *memStore {
		return &memStore{bench: BenchmarkInput{Golden: "v3", Judge: "qwen3:8b", Runs: []ModelRunData{
			{Model: "fast-m", Judge: "qwen3:8b", Pairs: []ScoredPair{{Question: "hi?", Faithfulness: 1, LatencyS: 0.5, Tokens: 10}}},
			{Model: "quality-m", Judge: "qwen3:8b", Pairs: []ScoredPair{{Question: "hi?", Faithfulness: 1, LatencyS: 0.9, Tokens: 20}}},
		}}}
	}
	h := New(Deps{Store: mk(), GoldenVersion: "v3"})
	rec := do(h, "GET", "/v1/benchmarks?golden=v3", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"comparable":true`) {
		t.Fatalf("benchmarks: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tied") {
		t.Fatalf("2 scored pairs must read tied: %s", rec.Body.String())
	}
	hbad := New(Deps{Store: &memStore{err: errors.New("down")}, GoldenVersion: "v3"})
	if rec := do(hbad, "GET", "/v1/benchmarks", ""); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"code":"store_unavailable"`) {
		t.Fatalf("store down: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConversationCRUD(t *testing.T) {
	h := New(Deps{Store: &memStore{}})
	rec := do(h, "POST", "/v1/conversations", `{"title":"","model":"fast-m"}`)
	var created map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id, _ := created["id"].(string)
	if rec.Code != 200 || id == "" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct{ body string }{
		{`{"role":"user","content":"my favourite colour is teal"}`},
		{`{"role":"assistant","content":"noted","model":"fast-m","prompt_tokens":10,"completion_tokens":2}`},
	} {
		if rec := do(h, "POST", "/v1/conversations/"+id+"/messages", tc.body); rec.Code != 200 {
			t.Fatalf("append %s: %d %s", tc.body, rec.Code, rec.Body.String())
		}
	}
	if rec := do(h, "POST", "/v1/conversations/"+id+"/messages", `{"role":"system","content":"x"}`); rec.Code != 400 {
		t.Fatalf("bad role: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "POST", "/v1/conversations/nope/messages", `{"role":"user","content":"x"}`); rec.Code != 404 {
		t.Fatalf("unknown thread append: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(h, "GET", "/v1/conversations/"+id+"/messages", "")
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	msgs, _ := got["messages"].([]any)
	if rec.Code != 200 || len(msgs) != 2 {
		t.Fatalf("messages: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(h, "GET", "/v1/conversations", "")
	var list map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	convs, _ := list["conversations"].([]any)
	if rec.Code != 200 || len(convs) != 1 || convs[0].(map[string]any)["title"] != "my favourite colour is teal" {
		t.Fatalf("list titles from first user message: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "DELETE", "/v1/conversations/"+id, ""); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "GET", "/v1/conversations/"+id+"/messages", ""); rec.Code != 404 {
		t.Fatalf("messages after delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "DELETE", "/v1/conversations/"+id, ""); rec.Code != 404 {
		t.Fatalf("second delete: %d %s", rec.Code, rec.Body.String())
	}
}

func TestStaticAndOverview(t *testing.T) {
	h := New(Deps{Store: &memStore{reqs: []Request{{TraceID: "t1", Model: "m"}}}, GoldenVersion: "v2", Version: "test"})
	rec := do(h, "GET", "/", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>Tower") {
		t.Fatalf("index: %d %.80s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "GET", "/static/tower.css", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "--tower-amber") {
		t.Fatalf("css: %d", rec.Code)
	}
	rec = do(h, "GET", "/v1/overview", "")
	var ov map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &ov)
	if rec.Code != 200 || ov["golden_version"] != "v2" || ov["version"] != "test" || ov["traffic"].(map[string]any)["requests_last_hour"].(float64) != 1 {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(h, "GET", "/v1/requests?limit=5", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"trace_id":"t1"`) {
		t.Fatalf("requests: %d %s", rec.Code, rec.Body.String())
	}
}

func TestStoreDownIsOneErrorShape(t *testing.T) {
	h := New(Deps{Store: &memStore{err: errors.New("pg down")}})
	for _, p := range []string{"/v1/overview", "/v1/requests", "/v1/evals/runs", "/v1/workflows"} {
		rec := do(h, "GET", p, "")
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"code":"store_unavailable"`) {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
}

// Only one eval run at a time: the second POST gets 409 while the first is in flight,
// and status reflects it; once done, a new run is accepted again.
func TestEvalRunSingleFlight(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Int32
	h := New(Deps{Store: &memStore{}, GoldenVersion: "v2", RunEval: func(ctx context.Context, golden string) error {
		started.Add(1)
		<-release
		return nil
	}})
	if rec := do(h, "POST", "/v1/evals/run", `{}`); rec.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "POST", "/v1/evals/run", `{}`); rec.Code != http.StatusConflict {
		t.Fatalf("second while running: %d", rec.Code)
	}
	if rec := do(h, "GET", "/v1/evals/status", ""); !strings.Contains(rec.Body.String(), `"running":true`) {
		t.Fatalf("status: %s", rec.Body.String())
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec := do(h, "GET", "/v1/evals/status", ""); strings.Contains(rec.Body.String(), `"running":false`) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	release = make(chan struct{})
	close(release)
	if rec := do(h, "POST", "/v1/evals/run", `{"golden_version":"v1"}`); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"v1"`) {
		t.Fatalf("after finish: %d %s", rec.Code, rec.Body.String())
	}
	if started.Load() < 1 {
		t.Fatal("runner never called")
	}
}

func TestWorkflowActions(t *testing.T) {
	h := New(Deps{Store: &memStore{}})
	if rec := do(h, "POST", "/v1/workflows", `{"input":"x"}`); rec.Code != http.StatusNotImplemented {
		t.Fatalf("unconfigured start: %d", rec.Code)
	}
	var startedWith string
	h = New(Deps{Store: &memStore{},
		StartWorkflow: func(ctx context.Context, input string) (string, error) { startedWith = input; return "wf1", nil },
		ResumeWork: func(ctx context.Context, id string) error {
			if id == "missing" {
				return ErrNotFound
			}
			return nil
		}})
	if rec := do(h, "POST", "/v1/workflows", `{"input":"  "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty input: %d", rec.Code)
	}
	if rec := do(h, "POST", "/v1/workflows", `{"input":"what is agentops?"}`); rec.Code != http.StatusAccepted || startedWith != "what is agentops?" || !strings.Contains(rec.Body.String(), "wf1") {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "POST", "/v1/workflows/missing/resume", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("resume missing: %d", rec.Code)
	}
	if rec := do(h, "POST", "/v1/workflows/wf1/resume", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("resume: %d", rec.Code)
	}
}

// ---- SQLStore grouping over fake rows ----

type fakeRows struct {
	rows [][]any
	i    int
}

func (r *fakeRows) Next() bool { r.i++; return r.i <= len(r.rows) }
func (r *fakeRows) Scan(dest ...any) error {
	row := r.rows[r.i-1]
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			*p = row[i].(string)
		case *time.Time:
			*p = row[i].(time.Time)
		case *int:
			*p = row[i].(int)
		case *float64:
			*p = row[i].(float64)
		}
	}
	return nil
}
func (r *fakeRows) Err() error { return nil }
func (r *fakeRows) Close()     {}

type fakeQueryer struct{ rows [][]any }

func (f fakeQueryer) Query(ctx context.Context, sql string, args ...any) (evals.Rows, error) {
	return &fakeRows{rows: f.rows}, nil
}

func TestRecentRequestsGroupsSpansPerTrace(t *testing.T) {
	t0 := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	q := fakeQueryer{rows: [][]any{
		{"t2", "s3", "", "route.decide", t0.Add(2 * time.Minute), `{"tier":"quality","reason":"long-or-complex-prompt","sensitive":true}`},
		{"t2", "s4", "s3", "model.generate", t0.Add(2*time.Minute + time.Second), `{"error":"all down","tried":["ollama/q"]}`},
		{"t1", "s1", "", "route.decide", t0, `{"tier":"fast","reason":"short-simple-prompt","sensitive":false,"agent_role":"critic","client_ref":"s1/t3/critic/2"}`},
		{"t1", "s2", "s1", "model.generate", t0.Add(time.Second), `{"model":"m","backend":"ollama","latency_s":0.42,"prompt_tokens":10,"completion_tokens":5,"fallback_from":["ollama/f"]}`},
	}}
	reqs, err := SQLStore{Query: q}.RecentRequests(context.Background(), 30)
	if err != nil || len(reqs) != 2 {
		t.Fatalf("got %d reqs err=%v", len(reqs), err)
	}
	if reqs[0].TraceID != "t2" || !reqs[0].Sensitive || reqs[0].Error != "all down" || reqs[0].Tier != "quality" {
		t.Fatalf("t2: %+v", reqs[0])
	}
	r1 := reqs[1]
	if r1.TraceID != "t1" || r1.Model != "m" || r1.Backend != "ollama" || r1.LatencyS != 0.42 || r1.Tokens != 15 || len(r1.Fallback) != 1 || r1.Reason != "short-simple-prompt" {
		t.Fatalf("t1: %+v", r1)
	}
	if r1.AgentRole != "critic" || r1.ClientRef != "s1/t3/critic/2" {
		t.Fatalf("t1 client ref not surfaced: %+v", r1)
	}
}
