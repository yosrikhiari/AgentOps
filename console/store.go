// Package console serves the Tower web UI and the JSON it reads: recent requests and
// overview stats derived from spans, eval history, and workflows. Reads only, plus two
// explicit actions (run eval, start/resume a workflow) that main.go injects.
package console

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"agentops/evals"
)

// Request is one router chat as the UI shows it, assembled from its route.decide and
// model.generate spans.
type Request struct {
	TraceID   string    `json:"trace_id"`
	At        time.Time `json:"at"`
	Model     string    `json:"model"`
	Backend   string    `json:"backend"`
	Tier      string    `json:"tier"`
	Reason    string    `json:"reason"`
	Sensitive bool      `json:"sensitive"`
	LatencyS  float64   `json:"latency_s"`
	Tokens    int       `json:"tokens"`
	Fallback  []string  `json:"fallback,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Overview is the KPI header plus per-model traffic for the last hour.
type Overview struct {
	RequestsLastHour   int            `json:"requests_last_hour"`
	RequestsLastMinute int            `json:"requests_last_minute"`
	ErrorsLastHour     int            `json:"errors_last_hour"`
	P50LatencyS        float64        `json:"p50_latency_s"`
	P99LatencyS        float64        `json:"p99_latency_s"`
	TokensLastHour     int            `json:"tokens_last_hour"`
	ByModel            map[string]int `json:"by_model"`
}

type EvalRun struct {
	ID        int       `json:"id"`
	Golden    string    `json:"golden_version"`
	Judge     string    `json:"judge_model"`
	Score     float64   `json:"score"`
	CreatedAt time.Time `json:"created_at"`
}

type Step struct {
	Seq      int    `json:"seq"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Attempts int    `json:"attempts"`
	Output   string `json:"output_snippet"`
}

type Workflow struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	Input     string    `json:"input"`
	CreatedAt time.Time `json:"created_at"`
	Steps     []Step    `json:"steps"`
}

// Store is what the handlers read. SQLStore is the real one over evals.Queryer.
type Store interface {
	RecentRequests(ctx context.Context, limit int) ([]Request, error)
	Overview(ctx context.Context) (Overview, error)
	EvalRuns(ctx context.Context, golden string, limit int) ([]EvalRun, error)
	Workflows(ctx context.Context, limit int) ([]Workflow, error)
}

type SQLStore struct {
	Query evals.Queryer
}

type spanRow struct {
	traceID, spanID, parentID, name string
	startedAt                       time.Time
	attrs                           map[string]any
}

func (s SQLStore) spans(ctx context.Context, sql string, args ...any) ([]spanRow, error) {
	rows, err := s.Query.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []spanRow
	for rows.Next() {
		var r spanRow
		var raw string
		if err := rows.Scan(&r.traceID, &r.spanID, &r.parentID, &r.name, &r.startedAt, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &r.attrs)
		out = append(out, r)
	}
	return out, rows.Err()
}

func str(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func num(m map[string]any, k string) float64 {
	v, _ := m[k].(float64)
	return v
}

func strs(m map[string]any, k string) []string {
	arr, _ := m[k].([]any)
	out := make([]string, 0, len(arr))
	for _, a := range arr {
		if s, ok := a.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// RecentRequests returns the newest router chats first. Pre-Track-B spans (no latency or
// backend attrs) still render with what they have.
func (s SQLStore) RecentRequests(ctx context.Context, limit int) ([]Request, error) {
	rows, err := s.spans(ctx,
		`SELECT trace_id, span_id, parent_id, name, started_at, attrs::text FROM spans
		 WHERE trace_id IN (SELECT trace_id FROM spans WHERE name = 'route.decide' ORDER BY started_at DESC LIMIT $1)
		   AND name IN ('route.decide', 'model.generate')
		 ORDER BY started_at DESC`, limit)
	if err != nil {
		return nil, err
	}
	byTrace := map[string]*Request{}
	var order []string
	for _, r := range rows {
		req, ok := byTrace[r.traceID]
		if !ok {
			req = &Request{TraceID: r.traceID, At: r.startedAt}
			byTrace[r.traceID] = req
			order = append(order, r.traceID)
		}
		if r.startedAt.Before(req.At) {
			req.At = r.startedAt
		}
		switch r.name {
		case "route.decide":
			req.Tier = str(r.attrs, "tier")
			req.Reason = str(r.attrs, "reason")
			req.Sensitive, _ = r.attrs["sensitive"].(bool)
		case "model.generate":
			req.Model = str(r.attrs, "model")
			req.Backend = str(r.attrs, "backend")
			req.LatencyS = num(r.attrs, "latency_s")
			req.Tokens = int(num(r.attrs, "prompt_tokens") + num(r.attrs, "completion_tokens"))
			req.Fallback = strs(r.attrs, "fallback_from")
			req.Error = str(r.attrs, "error")
		}
	}
	out := make([]Request, 0, len(order))
	for _, id := range order {
		out = append(out, *byTrace[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p*float64(len(sorted)-1) + 0.5)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Overview aggregates the last hour of model.generate spans.
func (s SQLStore) Overview(ctx context.Context) (Overview, error) {
	rows, err := s.spans(ctx,
		`SELECT trace_id, span_id, parent_id, name, started_at, attrs::text FROM spans
		 WHERE name = 'model.generate' AND started_at > now() - interval '1 hour' ORDER BY started_at`)
	if err != nil {
		return Overview{}, err
	}
	ov := Overview{ByModel: map[string]int{}}
	var lat []float64
	cutoff := time.Now().Add(-time.Minute)
	for _, r := range rows {
		if str(r.attrs, "error") != "" {
			ov.ErrorsLastHour++
			continue
		}
		ov.RequestsLastHour++
		if r.startedAt.After(cutoff) {
			ov.RequestsLastMinute++
		}
		if l := num(r.attrs, "latency_s"); l > 0 {
			lat = append(lat, l)
		}
		ov.TokensLastHour += int(num(r.attrs, "prompt_tokens") + num(r.attrs, "completion_tokens"))
		if m := str(r.attrs, "model"); m != "" {
			ov.ByModel[m]++
		}
	}
	sort.Float64s(lat)
	ov.P50LatencyS = percentile(lat, 0.5)
	ov.P99LatencyS = percentile(lat, 0.99)
	return ov, nil
}

func (s SQLStore) EvalRuns(ctx context.Context, golden string, limit int) ([]EvalRun, error) {
	rows, err := s.Query.Query(ctx,
		`SELECT id, golden_version, judge_model, score, created_at FROM eval_runs WHERE golden_version = $1 ORDER BY created_at DESC, id DESC LIMIT $2`,
		golden, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvalRun
	for rows.Next() {
		var r EvalRun
		if err := rows.Scan(&r.ID, &r.Golden, &r.Judge, &r.Score, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s SQLStore) Workflows(ctx context.Context, limit int) ([]Workflow, error) {
	rows, err := s.Query.Query(ctx,
		`SELECT id, type, status, input, created_at FROM workflows ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var out []Workflow
	idx := map[string]int{}
	for rows.Next() {
		var w Workflow
		if err := rows.Scan(&w.ID, &w.Type, &w.Status, &w.Input, &w.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		w.Steps = []Step{}
		idx[w.ID] = len(out)
		out = append(out, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(out))
	for _, w := range out {
		ids = append(ids, w.ID)
	}
	srows, err := s.Query.Query(ctx,
		`SELECT workflow_id, seq, name, status, attempts, left(output, 160) FROM steps WHERE workflow_id = ANY($1) ORDER BY workflow_id, seq`, ids)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var wid string
		var st Step
		if err := srows.Scan(&wid, &st.Seq, &st.Name, &st.Status, &st.Attempts, &st.Output); err != nil {
			return nil, err
		}
		if i, ok := idx[wid]; ok {
			out[i].Steps = append(out[i].Steps, st)
		}
	}
	return out, srows.Err()
}
