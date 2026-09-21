// Package console serves the Tower web UI and the JSON it reads: recent requests and
// overview stats derived from spans, eval history, and workflows. Reads only, plus two
// explicit actions (run eval, start/resume a workflow) that main.go injects.
package console

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"agentops/evals"
	"agentops/tracker"
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
	// v1.1 client reference (ADR-0009): which agent of a multi-agent client made the call.
	AgentRole string `json:"agent_role,omitempty"`
	ClientRef string `json:"client_ref,omitempty"`
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
	Model     string    `json:"model"`
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
	// BenchmarkData feeds Track L's comparison: the latest scored run per
	// model for a golden version plus recent model-run history.
	BenchmarkData(ctx context.Context, golden string) (BenchmarkInput, error)
	Workflows(ctx context.Context, limit int) ([]Workflow, error)
	// Conversations is the cockpit shelf. Prompts ARE stored here (unlike span
	// attrs) — purpose-limited to thread continuity, 90-day retention, user
	// erasable. See PRIVACY.md.
	Conversations(ctx context.Context, limit int) ([]Conversation, error)
	CreateConversation(ctx context.Context, title, model string) (string, error)
	ConversationMessages(ctx context.Context, id string, limit int) ([]Message, error)
	AppendMessage(ctx context.Context, convID string, m Message) error
	DeleteConversation(ctx context.Context, id string) error
	PurgeConversations(ctx context.Context, olderThanDays int) (int64, error)
}

// Conversation is one stored thread on the cockpit shelf.
type Conversation struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Model     string    `json:"model"`
	Messages  int       `json:"messages"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Message is one stored turn. Content holds the prompt or answer verbatim.
type Message struct {
	ID               int64     `json:"id"`
	Role             string    `json:"role"`
	Content          string    `json:"content"`
	Model            string    `json:"model"`
	TraceID          string    `json:"trace_id"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	CreatedAt        time.Time `json:"created_at"`
}

type SQLStore struct {
	Query evals.Queryer
	Exec  evals.Execer
	Row   evals.Querier
}

// ErrConvNotFound answers 404: no thread with that id.
var ErrConvNotFound = errors.New("conversation not found")

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
			req.AgentRole = str(r.attrs, "agent_role")
			req.ClientRef = str(r.attrs, "client_ref")
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
		`SELECT id, golden_version, judge_model, model, score, created_at FROM eval_runs WHERE golden_version = $1 ORDER BY created_at DESC, id DESC LIMIT $2`,
		golden, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvalRun
	for rows.Next() {
		var r EvalRun
		if err := rows.Scan(&r.ID, &r.Golden, &r.Judge, &r.Model, &r.Score, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// BenchmarkData returns the latest scored run per model for a golden version
// (untagged golden-answer runs never compare) plus the recent model-run
// history for the page.
func (s SQLStore) BenchmarkData(ctx context.Context, golden string) (BenchmarkInput, error) {
	in := BenchmarkInput{Golden: golden}
	rows, err := s.Query.Query(ctx,
		`SELECT DISTINCT ON (model) id, model, judge_model FROM eval_runs WHERE golden_version = $1 AND model != '' ORDER BY model, created_at DESC, id DESC`,
		golden)
	if err != nil {
		return in, err
	}
	type runHead struct {
		id    int
		model string
		judge string
	}
	var heads []runHead
	for rows.Next() {
		var h runHead
		if err := rows.Scan(&h.id, &h.model, &h.judge); err != nil {
			rows.Close()
			return in, err
		}
		heads = append(heads, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}
	for _, h := range heads {
		rd := ModelRunData{Model: h.model, Judge: h.judge}
		prows, err := s.Query.Query(ctx,
			`SELECT question, faithfulness, latency_s, tokens, retrieval_miss FROM eval_pair_scores WHERE run_id = $1`,
			h.id)
		if err != nil {
			return in, err
		}
		for prows.Next() {
			var q string
			var f *float64
			var lat float64
			var tok int
			var miss bool
			if err := prows.Scan(&q, &f, &lat, &tok, &miss); err != nil {
				prows.Close()
				return in, err
			}
			if miss || f == nil {
				rd.Misses++
				continue
			}
			rd.Pairs = append(rd.Pairs, ScoredPair{Question: q, Faithfulness: *f, LatencyS: lat, Tokens: tok})
		}
		prows.Close()
		if err := prows.Err(); err != nil {
			return in, err
		}
		in.Runs = append(in.Runs, rd)
	}
	hrows, err := s.Query.Query(ctx,
		`SELECT id, model, judge_model, score, created_at FROM eval_runs WHERE golden_version = $1 AND model != '' ORDER BY created_at DESC, id DESC LIMIT 10`,
		golden)
	if err != nil {
		return in, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var h RunHistory
		var created time.Time
		if err := hrows.Scan(&h.ID, &h.Model, &h.Judge, &h.Score, &created); err != nil {
			return in, err
		}
		h.Created = created.Format(time.RFC3339)
		in.History = append(in.History, h)
	}
	return in, hrows.Err()
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

// Conversations lists threads newest-first with their message counts.
func (s SQLStore) Conversations(ctx context.Context, limit int) ([]Conversation, error) {
	rows, err := s.Query.Query(ctx,
		`SELECT c.id, c.title, c.model, COUNT(m.id), c.updated_at FROM conversations c
		 LEFT JOIN messages m ON m.conv_id = c.id
		 GROUP BY c.id ORDER BY c.updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.Model, &c.Messages, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if out == nil {
		out = []Conversation{}
	}
	return out, rows.Err()
}

// CreateConversation opens a thread. An empty title is filled from the first
// user message on append.
func (s SQLStore) CreateConversation(ctx context.Context, title, model string) (string, error) {
	if s.Exec == nil {
		return "", errors.New("conversation store not configured")
	}
	id := tracker.NewWorkflowID()
	if _, err := s.Exec.Exec(ctx,
		`INSERT INTO conversations (id, title, model) VALUES ($1, $2, $3)`, id, title, model); err != nil {
		return "", err
	}
	return id, nil
}

func (s SQLStore) ConversationMessages(ctx context.Context, id string, limit int) ([]Message, error) {
	rows, err := s.Query.Query(ctx,
		`SELECT id, role, content, model, trace_id, prompt_tokens, completion_tokens, created_at
		 FROM messages WHERE conv_id = $1 ORDER BY id ASC LIMIT $2`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.Model, &m.TraceID, &m.PromptTokens, &m.CompletionTokens, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if out == nil {
		// Distinguish empty thread from unknown id.
		var one int
		if err := s.Row.QueryRow(ctx, `SELECT 1 FROM conversations WHERE id = $1`, id).Scan(&one); err != nil {
			return nil, ErrConvNotFound
		}
		out = []Message{}
	}
	return out, rows.Err()
}

// AppendMessage stores one turn and refreshes the thread clock. The first user
// message names an untitled thread (first 60 chars).
func (s SQLStore) AppendMessage(ctx context.Context, convID string, m Message) error {
	if s.Exec == nil {
		return errors.New("conversation store not configured")
	}
	if m.PromptTokens < 0 {
		m.PromptTokens = 0
	}
	if m.CompletionTokens < 0 {
		m.CompletionTokens = 0
	}
	tag, err := s.Exec.Exec(ctx,
		`UPDATE conversations SET updated_at = now() WHERE id = $1`, convID)
	if err != nil {
		return err
	}
	if tag == 0 {
		return ErrConvNotFound
	}
	if _, err := s.Exec.Exec(ctx,
		`INSERT INTO messages (conv_id, role, content, model, trace_id, prompt_tokens, completion_tokens)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		convID, m.Role, m.Content, m.Model, m.TraceID, m.PromptTokens, m.CompletionTokens); err != nil {
		return err
	}
	if m.Role == "user" {
		title := m.Content
		if r := []rune(title); len(r) > 60 {
			title = string(r[:60]) + "…"
		}
		_, _ = s.Exec.Exec(ctx,
			`UPDATE conversations SET title = $2 WHERE id = $1 AND (title IS NULL OR title = '')`, convID, title)
	}
	return nil
}

// DeleteConversation erases a thread; messages follow by ON DELETE CASCADE.
func (s SQLStore) DeleteConversation(ctx context.Context, id string) error {
	if s.Exec == nil {
		return errors.New("conversation store not configured")
	}
	tag, err := s.Exec.Exec(ctx, `DELETE FROM conversations WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag == 0 {
		return ErrConvNotFound
	}
	return nil
}

// PurgeConversations deletes threads idle longer than days. Returns rows dropped.
func (s SQLStore) PurgeConversations(ctx context.Context, days int) (int64, error) {
	if s.Exec == nil {
		return 0, errors.New("conversation store not configured")
	}
	return s.Exec.Exec(ctx,
		`DELETE FROM conversations WHERE updated_at < now() - make_interval(days => $1)`, days)
}
