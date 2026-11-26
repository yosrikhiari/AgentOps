package console

import (
	"context"
	"database/sql"
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

	"tower/audit"
	"tower/evals"
	"tower/gpu"
)

type memStore struct {
	reqs    []Request
	runs    []EvalRun
	wfs     []Workflow
	err     error
	convs   map[string]*memConv
	bench   BenchmarkInput
	gpuHist []gpu.Sample
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

func (m *memStore) GPUSamples(ctx context.Context, since time.Time, limit int) ([]gpu.Sample, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.gpuHist, nil
}

func (m *memStore) MetricSeries(ctx context.Context, since, until time.Time, step time.Duration) ([]SeriesPoint, error) {
	if m.err != nil {
		return nil, m.err
	}
	return nil, nil
}

func (m *memStore) BenchmarkData(ctx context.Context, golden string) (BenchmarkInput, error) {
	if m.err != nil {
		return BenchmarkInput{}, m.err
	}
	out := m.bench
	out.Golden = golden
	return out, nil
}

func (m *memStore) Activity(ctx context.Context, entity, id string, limit int) ([]audit.LoggedEntry, error) {
	if m.err != nil {
		return nil, m.err
	}
	return []audit.LoggedEntry{}, nil
}

func TestActivityEndpoint(t *testing.T) {
	h := New(Deps{Store: &memStore{}})
	rec := do(h, "GET", "/v1/activity?entity=workflow", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"entries":[]`) {
		t.Fatalf("activity: %d %s", rec.Code, rec.Body.String())
	}
	hbad := New(Deps{Store: &memStore{err: errors.New("down")}})
	if rec := do(hbad, "GET", "/v1/activity", ""); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"code":"store_unavailable"`) {
		t.Fatalf("store down: %d %s", rec.Code, rec.Body.String())
	}
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
		StartWorkflow: func(ctx context.Context, input, verifier string) (string, error) { startedWith = input; return "wf1", nil },
		ResumeWork: func(ctx context.Context, id string) error {
			if id == "missing" {
				return ErrNotFound
			}
			return nil
		}})
	if rec := do(h, "POST", "/v1/workflows", `{"input":"  "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty input: %d", rec.Code)
	}
	if rec := do(h, "POST", "/v1/workflows", `{"input":"what is tower?"}`); rec.Code != http.StatusAccepted || startedWith != "what is tower?" || !strings.Contains(rec.Body.String(), "wf1") {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "POST", "/v1/workflows/missing/resume", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("resume missing: %d", rec.Code)
	}
	if rec := do(h, "POST", "/v1/workflows/wf1/resume", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("resume: %d", rec.Code)
	}
}

func TestAnnotateTrajectory(t *testing.T) {
	clean := Workflow{Status: "done", Steps: []Step{{Status: "done", Attempts: 1}, {Status: "done", Attempts: 1}}}
	AnnotateTrajectory(&clean)
	if !clean.Clean || clean.Retries != 0 {
		t.Fatalf("clean run = %+v", clean)
	}
	retried := Workflow{Status: "done", Steps: []Step{{Status: "done", Attempts: 1}, {Status: "done", Attempts: 3}}}
	AnnotateTrajectory(&retried)
	if retried.Clean || retried.Retries != 2 {
		t.Fatalf("retried run = %+v", retried)
	}
	running := Workflow{Status: "running", Steps: []Step{{Status: "done", Attempts: 1}, {Status: "pending"}}}
	AnnotateTrajectory(&running)
	if running.Clean {
		t.Fatalf("unfinished run = %+v", running)
	}
	// No steps is not a clean run: an empty trajectory proves nothing.
	empty := Workflow{Status: "done"}
	AnnotateTrajectory(&empty)
	if empty.Clean {
		t.Fatal("a workflow with no steps must not be clean")
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
		case *int64:
			*p = row[i].(int64)
		case *float64:
			*p = row[i].(float64)
		case *sql.NullBool:
			if row[i] == nil {
				p.Valid = false
			} else {
				p.Bool = row[i].(bool)
				p.Valid = true
			}
		case *sql.NullTime:
			if row[i] == nil {
				p.Valid = false
			} else {
				p.Time = row[i].(time.Time)
				p.Valid = true
			}
		case **float64:
			if row[i] == nil {
				*p = nil
				continue
			}
			v := row[i].(float64)
			*p = &v
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

// fakeGPUQueryer is fakeQueryer for gpu.Queryer: same method name, different
// return type, so one type cannot satisfy both.
type fakeGPUQueryer struct{ rows [][]any }

func (f fakeGPUQueryer) Query(ctx context.Context, sql string, args ...any) (gpu.Rows, error) {
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

// ---- Track U: /v1/gpu ----

// TestGPUEndpointLive: live snapshot from the prober, history from the store.
func TestGPUEndpointLive(t *testing.T) {
	now := time.Now().UTC()
	h := New(Deps{
		Store: &memStore{gpuHist: []gpu.Sample{{At: now, Name: "GTX 1650", UtilPct: 12}}},
		GPU: func() gpu.Snapshot {
			return gpu.Snapshot{At: now, Available: true, SwapsLast10m: 2,
				Sample:   gpu.Sample{Name: "GTX 1650", UtilPct: 39, MemUsedBytes: 77 << 20, MemTotalBytes: 4096 << 20},
				Resident: []gpu.Resident{{Name: "qwen2.5:3b-instruct", SizeVRAMBytes: 2 << 30}}}
		},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/gpu", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Now struct {
			Available bool `json:"available"`
			Sample    struct {
				UtilPct float64 `json:"util_pct"`
			} `json:"sample"`
			Resident []struct {
				Name string `json:"name"`
			} `json:"resident"`
			Swaps10m int `json:"swaps_last_10m"`
		} `json:"now"`
		History []struct {
			UtilPct float64 `json:"util_pct"`
		} `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Now.Available || got.Now.Sample.UtilPct != 39 || got.Now.Swaps10m != 2 || len(got.Now.Resident) != 1 {
		t.Fatalf("now = %+v", got.Now)
	}
	if len(got.History) != 1 || got.History[0].UtilPct != 12 {
		t.Fatalf("history = %+v", got.History)
	}
}

// TestGPUEndpointNoProber: no GPU is not an error and not a 500. The page must
// be able to tell "no driver" from "idle GPU", so available=false carries an
// explanation.
func TestGPUEndpointNoProber(t *testing.T) {
	h := New(Deps{Store: &memStore{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/gpu", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("body = %s", rec.Body)
	}
}

// TestGPUEndpointEmptyHistoryIsAList: gpu.Series returns a nil slice for no
// rows, which marshals as null. The page does history.map(...) — null throws.
func TestGPUEndpointEmptyHistoryIsAList(t *testing.T) {
	h := New(Deps{Store: &memStore{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/gpu", nil))
	if !strings.Contains(rec.Body.String(), `"history":[]`) {
		t.Fatalf("history is not an empty array: %s", rec.Body)
	}
}

// TestGPUEndpointKeepsNowWhenHistoryFails: the live reading comes from the
// prober, not the database. A dead Postgres must not hide the card it is meant
// to describe, so the read stays 200 with "now" and an explained empty chart -
// and history_error is what stops "no samples yet" looking like "store down".
func TestGPUEndpointKeepsNowWhenHistoryFails(t *testing.T) {
	h := New(Deps{
		Store: &memStore{err: errors.New("no database")},
		GPU: func() gpu.Snapshot {
			return gpu.Snapshot{At: time.Now().UTC(), Available: true,
				Sample: gpu.Sample{Name: "GTX 1650", UtilPct: 39}}
		},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/gpu", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Now struct {
			Available bool `json:"available"`
			Sample    struct {
				UtilPct float64 `json:"util_pct"`
			} `json:"sample"`
		} `json:"now"`
		History      []gpu.Sample `json:"history"`
		HistoryError string       `json:"history_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Now.Available || got.Now.Sample.UtilPct != 39 {
		t.Fatalf("now = %+v", got.Now)
	}
	if got.HistoryError == "" {
		t.Fatalf("store failure is not explained: %s", rec.Body)
	}
	if got.History == nil {
		t.Fatalf("history is null, not []: %s", rec.Body)
	}
}

// TestSQLStoreEvalRunsCarriesGPUWindow: the evals page needs each run's card
// story from one query, and a window with no samples must carry no summary - so
// the page shows nothing instead of a 0% GPU nobody measured.
func TestSQLStoreEvalRunsCarriesGPUWindow(t *testing.T) {
	start := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	end := start.Add(8 * time.Minute)
	q := fakeQueryer{rows: [][]any{
		{1, "v3", "qwen3:8b", "faithfulness-v2", "", 0.94, start, end, "complete", 72, 72, 41, 63.0, 88.0, int64(2 << 30), int64(4 << 30), int64(3), 84.0, 4},
		{2, "v3", "qwen3:8b", "", "", 0.95, start, end, "failed", 0, 0, 0, 0.0, 0.0, int64(0), int64(0), int64(0), 0.0, 0},
	}}
	got, err := SQLStore{Query: q}.EvalRuns(context.Background(), "v3", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("runs = %d", len(got))
	}
	if !got[0].StartedAt.Equal(start) || !got[0].CreatedAt.Equal(end) {
		t.Fatalf("window = %v..%v", got[0].StartedAt, got[0].CreatedAt)
	}
	if got[0].GPU == nil || got[0].GPU.Samples != 41 || got[0].GPU.Swaps != 3 ||
		got[0].GPU.UtilMean != 63 || got[0].GPU.MemPeakBytes != 2<<30 {
		t.Fatalf("gpu = %+v", got[0].GPU)
	}
	if got[0].GPU.MaxTempC != 84 || got[0].GPU.ThrottledSamples != 4 {
		t.Fatalf("a run that was throttled must say so: %+v", got[0].GPU)
	}
	if got[0].Judged != 72 || got[1].Judged != 0 {
		t.Fatalf("judged = %d/%d, want 72/0", got[0].Judged, got[1].Judged)
	}
	if got[0].JudgePrompt != "faithfulness-v2" || got[1].JudgePrompt != "" {
		t.Fatalf("judge prompt = %q/%q", got[0].JudgePrompt, got[1].JudgePrompt)
	}
	if got[0].Status != "complete" || got[1].Status != "failed" {
		t.Fatalf("status = %q/%q", got[0].Status, got[1].Status)
	}
	if got[1].GPU != nil {
		t.Fatalf("a run with no samples must not carry a summary: %+v", got[1].GPU)
	}
}

// TestSQLStoreGPUSamplesMapsEveryColumn: a fake store cannot catch a Scan that
// silently leaves mem_used, power_w or the swap counter at zero.
func TestSQLStoreGPUSamplesMapsEveryColumn(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	q := fakeGPUQueryer{rows: [][]any{{
		now, "GTX 1650", 39.0, int64(77 << 20), int64(4096 << 20), 12.5, 66.0, int64(4),
	}}}
	got, err := SQLStore{GPU: q}.GPUSamples(context.Background(), now.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d", len(got))
	}
	s := got[0]
	if !s.At.Equal(now) || s.Name != "GTX 1650" || s.UtilPct != 39 ||
		s.MemUsedBytes != 77<<20 || s.MemTotalBytes != 4096<<20 {
		t.Fatalf("sample = %+v", s)
	}
	if s.PowerW == nil || *s.PowerW != 12.5 || s.TempC == nil || *s.TempC != 66 {
		t.Fatalf("power/temp = %v/%v", s.PowerW, s.TempC)
	}
	if s.SwapsTotal != 4 {
		t.Fatalf("swaps_total = %d, want 4", s.SwapsTotal)
	}
}

// TestSQLStoreWorkflowsAttachesGrade: a graded workflow carries its score, an ungraded one
// carries nil - "no grade" and "graded 0" must not look alike.
func TestSQLStoreWorkflowsAttachesGrade(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	q := &wfQueryer{rows: map[string][][]any{
		"FROM workflows":  {{"w1", "toy", "done", "question", now, "", nil, "", nil}, {"w2", "toy", "done", "other", now, "", nil, "", nil}},
		"FROM steps":      {{"w1", 0, "researcher", "done", 1, "a"}, {"w1", 1, "drafter", "done", 1, "b"}, {"w1", 2, "reviewer", "done", 1, "c"}},
		"workflow_grades": {{"w1", "phi4-mini:3.8b", "answer-quality-v1", 4, "good", now}},
	}}
	got, err := SQLStore{Query: q}.Workflows(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("workflows = %d", len(got))
	}
	if !got[0].Clean || got[0].Retries != 0 {
		t.Fatalf("trajectory = %+v", got[0])
	}
	if got[0].Grade == nil || got[0].Grade.Score != 4 || got[0].Grade.Judge != "phi4-mini:3.8b" {
		t.Fatalf("grade = %+v", got[0].Grade)
	}
	if got[1].Grade != nil {
		t.Fatalf("ungraded workflow carries a grade: %+v", got[1].Grade)
	}
}

type wfQueryer struct{ rows map[string][][]any }

func (q *wfQueryer) Query(_ context.Context, sql string, _ ...any) (evals.Rows, error) {
	for key, rows := range q.rows {
		if strings.Contains(sql, key) {
			return &fakeRows{rows: rows}, nil
		}
	}
	return &fakeRows{}, nil
}
