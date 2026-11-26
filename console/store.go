// Package console serves the Tower web UI and the JSON it reads: recent requests and
// overview stats derived from spans, eval history, and workflows. Reads only, plus two
// explicit actions (run eval, start/resume a workflow) that main.go injects.
package console

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"tower/audit"
	"tower/evals"
	"tower/gpu"
	"tower/tracker"
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
	// AnsweredBy is "router" when no model ran: a question about the gateway itself.
	AnsweredBy string `json:"answered_by,omitempty"`
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

// SeriesPoint is one bucket of the Metrics time-series charts. It is a read over
// the same spans the trace pages use; there is no separate metric store (see
// docs/CONSOLE-REDESIGN.md and the "wave 4" decision there).
type SeriesPoint struct {
	At        time.Time `json:"at"`
	Requests  int       `json:"requests"`
	Errors    int       `json:"errors"`
	P50S      float64   `json:"p50_s"`
	P99S      float64   `json:"p99_s"`
	TokensIn  int64     `json:"tokens_in"`
	TokensOut int64     `json:"tokens_out"`
}

type EvalRun struct {
	ID     int    `json:"id"`
	Golden string `json:"golden_version"`
	Judge  string `json:"judge_model"`
	// JudgePrompt is the prompt version that produced the scores ("" for runs recorded
	// before it was stored). Two runs on the same model but different prompt versions
	// are not comparable, which is why drift reports it separately.
	JudgePrompt string  `json:"judge_prompt"`
	Model       string  `json:"model"`
	Score       float64 `json:"score"`
	// Judged is how many pairs the judge actually scored. A run with zero judged pairs
	// (every pair a retrieval miss) has no faithfulness measurement; the page must say
	// "unmeasured", not paint a 0.000 red.
	Judged    int       `json:"judged"`
	Total     int       `json:"total"`
	StartedAt time.Time `json:"started_at"`
	CreatedAt time.Time `json:"created_at"`
	// Status is running / complete / failed. Only complete runs carry a trustworthy score:
	// an interrupted suite keeps its pairs as evidence but its score is stale, so the page
	// says "incomplete" instead of painting whatever number was last written.
	Status string `json:"status"`
	// GPU is what the card did between StartedAt and CreatedAt. Absent when the
	// prober recorded nothing in the window: "no samples" is not "a quiet GPU".
	GPU *gpu.Summary `json:"gpu,omitempty"`
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
	// Trajectory counters, derived in one place (AnnotateTrajectory) so the page and
	// `--trajectory` cannot disagree about what a run did.
	Retries int  `json:"retries"`
	Clean   bool `json:"clean"`
	// Grade is the stored rubric score for the final answer, when one was taken. It is
	// quality, not faithfulness - see evals/rubric.go.
	Grade *WorkflowGrade `json:"grade,omitempty"`
	// Outcome-based verification (docs/WORKFLOW-EVAL-RESEARCH.md): a workflow is
	// 'done' only when its verifier passes, not when the agent claims success.
	Verifier       string     `json:"verifier,omitempty"`
	Verified       *bool      `json:"verified,omitempty"`
	VerifierOutput string     `json:"verifier_output,omitempty"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"`
}

type WorkflowGrade struct {
	Judge    string    `json:"judge"`
	Prompt   string    `json:"prompt"`
	Score    int       `json:"score"`
	Reason   string    `json:"reason"`
	GradedAt time.Time `json:"graded_at"`
}

// AnnotateTrajectory fills Retries and Clean. Clean = completed, every step done, no step
// retried: orchestration reliability, not answer quality (the toy workflow has no ground
// truth for its content).
func AnnotateTrajectory(w *Workflow) {
	w.Retries = 0
	allDone := len(w.Steps) > 0
	for _, s := range w.Steps {
		if s.Attempts > 1 {
			w.Retries += s.Attempts - 1
		}
		if s.Status != "done" {
			allDone = false
		}
	}
	w.Clean = w.Status == "done" && allDone && w.Retries == 0
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
	// Activity reads the Track S transition trail (append-only; writers live
	// in main.go call sites, never in this package).
	Activity(ctx context.Context, entity, id string, limit int) ([]audit.LoggedEntry, error)
	// GPUSamples is Track U's history: local GPU utilisation and VRAM,
	// newest-first. A nil slice (no rows) is not an error.
	GPUSamples(ctx context.Context, since time.Time, limit int) ([]gpu.Sample, error)
	// MetricSeries aggregates spans into fixed buckets between since and until:
	// requests, errors, p50/p99 latency and tokens. Empty buckets are returned
	// as zeros so a chart has no holes. Read-only; bounded by the caller.
	MetricSeries(ctx context.Context, since, until time.Time, step time.Duration) ([]SeriesPoint, error)
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
	// GPU is Track U's sample reader. gpu.Rows is a distinct named type from
	// evals.Rows, so it needs its own queryer; main.go supplies gpuAdapter.
	GPU gpu.Queryer
	// Begin, when non-nil, provides transactional multi-statement writes. It is
	// optional: a store built without a connection (tests) leaves it nil and
	// AppendMessage runs its statements sequentially, as it did before.
	Begin Beginner
}

// Tx is the small transaction surface AppendMessage needs. It deliberately
// mirrors Execer so a transaction can stand in for a plain execer without
// changing the call sites.
type Tx interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Beginner opens a transaction. Implemented by pgAdapter (main.go) over pgx.
type Beginner interface {
	Begin(ctx context.Context) (Tx, error)
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
	// The page shows recent requests, so both scans are bounded to 24 hours: the
	// span name alone matches every request ever made, and without a floor the
	// planner scans and detoasts JSONB far past the point any row can appear
	// (audit 2026-11-18 A2). migrations/0016 adds (name, started_at) for it.
	rows, err := s.spans(ctx,
		`SELECT trace_id, span_id, parent_id, name, started_at, attrs::text FROM spans
		 WHERE started_at > now() - interval '24 hours'
		   AND trace_id IN (
		     SELECT trace_id FROM spans
		      WHERE name = 'route.decide' AND started_at > now() - interval '24 hours'
		      ORDER BY started_at DESC LIMIT $1)
		   AND name IN ('route.decide', 'model.generate', 'router.answer')
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
		case "router.answer":
			// A gateway question: the router answered from labels, so there is no
			// latency and no token count. The row still names the model the turn was
			// routed to, and says who answered.
			req.Model = str(r.attrs, "model")
			req.Tier = str(r.attrs, "tier")
			req.Reason = str(r.attrs, "reason")
			req.AnsweredBy = str(r.attrs, "answered_by")
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

// EvalRuns returns the recent runs of one golden version. The GPU summary rides
// along in the same query (a lateral aggregate over the run's own window) so the
// page does not issue one query per run to answer "was the card busy?".
func (s SQLStore) EvalRuns(ctx context.Context, golden string, limit int) ([]EvalRun, error) {
	rows, err := s.Query.Query(ctx,
		`SELECT r.id, r.golden_version, r.judge_model, r.judge_prompt, r.model, r.score, r.started_at, r.created_at, r.status, r.total_pairs,
		        (SELECT count(*) FROM eval_pair_scores p
		          WHERE p.run_id = r.id AND p.faithfulness IS NOT NULL)::int AS judged,
		        g.samples, coalesce(g.util_mean,0), coalesce(g.util_peak,0),
		        coalesce(g.mem_peak,0), coalesce(g.mem_total,0), coalesce(g.swaps,0),
		        coalesce(g.max_temp,0), coalesce(g.throttled,0)
		 FROM eval_runs r
		 LEFT JOIN LATERAL (
		   SELECT count(*)::int AS samples, avg(util_pct) AS util_mean, max(util_pct) AS util_peak,
		          max(mem_used) AS mem_peak, max(mem_total) AS mem_total,
		          max(swaps_total) - min(swaps_total) AS swaps,
		          max(temp_c) AS max_temp, count(*) FILTER (WHERE throttled) AS throttled
		   FROM gpu_samples WHERE ts >= r.started_at AND ts <= coalesce(r.finished_at, now())
		 ) g ON true
		 WHERE r.golden_version = $1 ORDER BY r.created_at DESC, r.id DESC LIMIT $2`,
		golden, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvalRun
	for rows.Next() {
		var r EvalRun
		var samples int
		var utilMean, utilPeak float64
		var memPeak, memTotal, swaps int64
		var maxTemp float64
		var throttled int
		if err := rows.Scan(&r.ID, &r.Golden, &r.Judge, &r.JudgePrompt, &r.Model, &r.Score, &r.StartedAt, &r.CreatedAt, &r.Status, &r.Total,
			&r.Judged, &samples, &utilMean, &utilPeak, &memPeak, &memTotal, &swaps, &maxTemp, &throttled); err != nil {
			return nil, err
		}
		if samples > 0 {
			r.GPU = &gpu.Summary{
				From: r.StartedAt, To: r.CreatedAt, Samples: samples,
				UtilMean: utilMean, UtilPeak: utilPeak,
				MemPeakBytes: memPeak, MemTotalBytes: memTotal, Swaps: swaps,
				MaxTempC: maxTemp, ThrottledSamples: throttled,
			}
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
		`SELECT id, type, status, input, created_at, verifier, verified, verifier_output, verified_at FROM workflows ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var out []Workflow
	idx := map[string]int{}
	for rows.Next() {
		var w Workflow
		var verified sql.NullBool
		var verifiedAt sql.NullTime
		if err := rows.Scan(&w.ID, &w.Type, &w.Status, &w.Input, &w.CreatedAt, &w.Verifier, &verified, &w.VerifierOutput, &verifiedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if verified.Valid {
			v := verified.Bool
			w.Verified = &v
		}
		if verifiedAt.Valid {
			t := verifiedAt.Time
			w.VerifiedAt = &t
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
	if err := srows.Err(); err != nil {
		return nil, err
	}
	// Grades are optional: a workflow that was never graded has no row, which is not a
	// zero. The query is separate so the steps join stays unchanged.
	grows, err := s.Query.Query(ctx,
		`SELECT workflow_id, judge, prompt, score, reason, graded_at FROM workflow_grades WHERE workflow_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer grows.Close()
	for grows.Next() {
		var wid string
		var g WorkflowGrade
		if err := grows.Scan(&wid, &g.Judge, &g.Prompt, &g.Score, &g.Reason, &g.GradedAt); err != nil {
			return nil, err
		}
		if i, ok := idx[wid]; ok {
			out[i].Grade = &g
		}
	}
	if err := grows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		AnnotateTrajectory(&out[i])
	}
	return out, nil
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
// message names an untitled thread (first 60 chars). The UPDATE that bumps
// updated_at and the INSERT of the message must land together: otherwise a
// failure between them leaves a thread whose clock moved but whose message is
// missing, and the cockpit shows a reply with no row. They run in one
// transaction when a Beginner is wired (main.go), and sequentially otherwise.
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
	run := func(exec evals.Execer) error {
		tag, err := exec.Exec(ctx,
			`UPDATE conversations SET updated_at = now() WHERE id = $1`, convID)
		if err != nil {
			return err
		}
		if tag == 0 {
			return ErrConvNotFound
		}
		if _, err := exec.Exec(ctx,
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
			_, _ = exec.Exec(ctx,
				`UPDATE conversations SET title = $2 WHERE id = $1 AND (title IS NULL OR title = '')`, convID, title)
		}
		return nil
	}
	if s.Begin != nil {
		tx, err := s.Begin.Begin(ctx)
		if err != nil {
			return err
		}
		// Rollback is a no-op after Commit; keep it as the failure path.
		defer func() { _ = tx.Rollback(ctx) }()
		if err := run(tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	return run(s.Exec)
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

// Activity delegates to the audit trail (Track S); the console only reads.
func (s SQLStore) Activity(ctx context.Context, entity, id string, limit int) ([]audit.LoggedEntry, error) {
	return audit.SQLStore{Query: s.Query}.History(ctx, entity, id, limit)
}

// GPUSamples delegates to gpu.SQLStore so the SQL for gpu_samples lives in one
// package. GPU is nil in tests that only exercise spans, so guard it: a nil
// queryer is "no history", never a panic on a page load.
func (s SQLStore) GPUSamples(ctx context.Context, since time.Time, limit int) ([]gpu.Sample, error) {
	if s.GPU == nil {
		return nil, nil
	}
	return gpu.SQLStore{Query: s.GPU}.Series(ctx, since, limit)
}

// MetricSeries aggregates spans into buckets. Two queries' worth of facts come
// from one row set: route.decide counts a request, model.generate carries the
// latency, the error flag and the token counts. percentile_cont gives the tail
// (p50/p99) rather than a mean, which is what the Tail at Scale argument says a
// latency chart must show.
//
// Buckets with no rows are filled with zeros here, not in SQL, so the chart has
// no holes and an idle minute is visibly idle.
func (s SQLStore) MetricSeries(ctx context.Context, since, until time.Time, step time.Duration) ([]SeriesPoint, error) {
	if step <= 0 {
		step = time.Minute
	}
	if !until.After(since) {
		return []SeriesPoint{}, nil
	}
	rows, err := s.Query.Query(ctx, `
		SELECT date_bin(make_interval(secs => $1::int), started_at, TIMESTAMPTZ '1970-01-01') AS bucket,
		       count(*) FILTER (WHERE name = 'route.decide')::int AS requests,
		       count(*) FILTER (WHERE name = 'model.generate' AND attrs ? 'error')::int AS errors,
		       coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY nullif((attrs->>'latency_s'), '')::float8), 0) AS p50,
		       coalesce(percentile_cont(0.99) WITHIN GROUP (ORDER BY nullif((attrs->>'latency_s'), '')::float8), 0) AS p99,
		       coalesce(sum(CASE WHEN name = 'model.generate' THEN nullif((attrs->>'prompt_tokens'), '')::bigint END), 0) AS tokens_in,
		       coalesce(sum(CASE WHEN name = 'model.generate' THEN nullif((attrs->>'completion_tokens'), '')::bigint END), 0) AS tokens_out
		FROM spans
		WHERE started_at >= $2 AND started_at < $3
		GROUP BY 1 ORDER BY 1`,
		int64(step/time.Second), since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byBucket := map[int64]SeriesPoint{}
	for rows.Next() {
		var p SeriesPoint
		if err := rows.Scan(&p.At, &p.Requests, &p.Errors, &p.P50S, &p.P99S, &p.TokensIn, &p.TokensOut); err != nil {
			return nil, err
		}
		byBucket[p.At.Unix()] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Align to the same epoch grid date_bin uses, so lookup keys match.
	start := since.Truncate(step)
	out := make([]SeriesPoint, 0, int(until.Sub(start)/step)+1)
	for t := start; !t.After(until); t = t.Add(step) {
		p, ok := byBucket[t.Unix()]
		if !ok {
			p = SeriesPoint{At: t}
		}
		out = append(out, p)
	}
	return out, nil
}
