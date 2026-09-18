package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentops/evals"
)

type memStore struct {
	reqs []Request
	runs []EvalRun
	wfs  []Workflow
	err  error
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

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
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
