package tracker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
)

var StepNames = []string{"researcher", "drafter", "reviewer"}

type Step struct {
	WorkflowID string
	Seq        int
	Name       string
	Status     string
	Output     string
	Attempts   int
}

// ErrTraceNotFound is returned by trace lookups when no span carries the trace id.
var ErrTraceNotFound = errors.New("trace not found")

type Store interface {
	// EnsureWorkflow creates the workflow + its pending steps if new, and returns the input
	// the workflow was *first* started with — on resume that wins over the caller's input.
	EnsureWorkflow(ctx context.Context, id, wtype, input string) (storedInput string, err error)
	ListSteps(ctx context.Context, id string) ([]Step, error)
	SetRunning(ctx context.Context, id string, seq int) error
	SetDone(ctx context.Context, id string, seq int, output string) error
	SetWorkflowStatus(ctx context.Context, id, status string) error
	EmitSpan(ctx context.Context, traceID, spanID, parentID, name, attrs string) error
	ListSpans(ctx context.Context, traceID string) ([]Span, error)
}

func NewWorkflowID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func newSpanID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
}

type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

type Queryer interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

type SQLStore struct {
	Exec  Execer
	Query Queryer
}

func (s SQLStore) EnsureWorkflow(ctx context.Context, id, wtype, input string) (string, error) {
	if _, err := s.Exec.Exec(ctx,
		`INSERT INTO workflows (id, type, status, input) VALUES ($1, $2, 'running', $3) ON CONFLICT (id) DO NOTHING`,
		id, wtype, input); err != nil {
		return "", err
	}
	for seq, name := range StepNames {
		if _, err := s.Exec.Exec(ctx,
			`INSERT INTO steps (workflow_id, seq, name, status) VALUES ($1, $2, $3, 'pending') ON CONFLICT (workflow_id, seq) DO NOTHING`,
			id, seq, name); err != nil {
			return "", err
		}
	}
	rows, err := s.Query.Query(ctx, `SELECT input FROM workflows WHERE id = $1`, id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	stored := input
	if rows.Next() {
		if err := rows.Scan(&stored); err != nil {
			return "", err
		}
	}
	return stored, rows.Err()
}

func (s SQLStore) ListSteps(ctx context.Context, id string) ([]Step, error) {
	rows, err := s.Query.Query(ctx,
		`SELECT workflow_id, seq, name, status, output, attempts FROM steps WHERE workflow_id = $1 ORDER BY seq`,
		id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Step
	for rows.Next() {
		var st Step
		if err := rows.Scan(&st.WorkflowID, &st.Seq, &st.Name, &st.Status, &st.Output, &st.Attempts); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s SQLStore) SetRunning(ctx context.Context, id string, seq int) error {
	_, err := s.Exec.Exec(ctx,
		`UPDATE steps SET status = 'running', attempts = attempts + 1 WHERE workflow_id = $1 AND seq = $2`,
		id, seq)
	return err
}

func (s SQLStore) SetDone(ctx context.Context, id string, seq int, output string) error {
	n, err := s.Exec.Exec(ctx,
		`UPDATE steps SET status = 'done', output = $3 WHERE workflow_id = $1 AND seq = $2`,
		id, seq, output)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("step %s/%d not found", id, seq)
	}
	return nil
}

func (s SQLStore) SetWorkflowStatus(ctx context.Context, id, status string) error {
	_, err := s.Exec.Exec(ctx, `UPDATE workflows SET status = $2 WHERE id = $1`, id, status)
	return err
}

func (s SQLStore) EmitSpan(ctx context.Context, traceID, spanID, parentID, name, attrs string) error {
	if spanID == "" {
		spanID = newSpanID()
	}
	if attrs == "" {
		attrs = "{}"
	}
	_, err := s.Exec.Exec(ctx,
		`INSERT INTO spans (trace_id, span_id, parent_id, name, attrs) VALUES ($1, $2, $3, $4, $5::jsonb) ON CONFLICT (trace_id, span_id) DO NOTHING`,
		traceID, spanID, parentID, name, attrs)
	return err
}

type MemStore struct {
	mu        sync.Mutex
	inputs    map[string]string
	wfStatus  map[string]string
	steps     map[string][]Step
	spans     int
	spanNames []string
	spanData  []Span
}

func NewMemStore() *MemStore {
	return &MemStore{inputs: map[string]string{}, wfStatus: map[string]string{}, steps: map[string][]Step{}}
}

func (m *MemStore) EnsureWorkflow(ctx context.Context, id, wtype, input string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.steps[id]; ok {
		return m.inputs[id], nil
	}
	m.inputs[id] = input
	m.wfStatus[id] = StatusRunning
	for seq, name := range StepNames {
		m.steps[id] = append(m.steps[id], Step{WorkflowID: id, Seq: seq, Name: name, Status: StatusPending})
	}
	return input, nil
}

func (m *MemStore) ListSteps(ctx context.Context, id string) ([]Step, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	steps, ok := m.steps[id]
	if !ok {
		return nil, fmt.Errorf("workflow %q not found", id)
	}
	out := append([]Step(nil), steps...)
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (m *MemStore) SetRunning(ctx context.Context, id string, seq int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	steps, ok := m.steps[id]
	if !ok || seq < 0 || seq >= len(steps) {
		return fmt.Errorf("step %s/%d not found", id, seq)
	}
	steps[seq].Status = StatusRunning
	steps[seq].Attempts++
	m.steps[id] = steps
	return nil
}

func (m *MemStore) SetDone(ctx context.Context, id string, seq int, output string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	steps, ok := m.steps[id]
	if !ok || seq < 0 || seq >= len(steps) {
		return fmt.Errorf("step %s/%d not found", id, seq)
	}
	steps[seq].Status = StatusDone
	steps[seq].Output = output
	m.steps[id] = steps
	return nil
}

func (m *MemStore) SetWorkflowStatus(ctx context.Context, id, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.steps[id]; !ok {
		return fmt.Errorf("workflow %q not found", id)
	}
	m.wfStatus[id] = status
	return nil
}

// WorkflowStatus is a test helper; the SQL store reads workflows.status directly.
func (m *MemStore) WorkflowStatus(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wfStatus[id]
}

func (m *MemStore) EmitSpan(ctx context.Context, traceID, spanID, parentID, name, attrs string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if spanID == "" {
		spanID = newSpanID()
	}
	m.spans++
	m.spanNames = append(m.spanNames, name)
	m.spanData = append(m.spanData, Span{TraceID: traceID, SpanID: spanID, ParentID: parentID, Name: name, Attrs: attrs})
	return nil
}

func (m *MemStore) SpanCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spans
}

type StepFunc func(ctx context.Context, input string) (string, error)

func spanAttrs(seq int, output string) string {
	snip := output
	if len(snip) > 200 {
		snip = snip[:200]
	}
	raw, _ := json.Marshal(map[string]any{"seq": seq, "output_snippet": snip})
	return string(raw)
}

func RunToy(ctx context.Context, store Store, workflowID, input string, researcher, drafter, reviewer StepFunc) (string, error) {
	if workflowID == "" {
		workflowID = NewWorkflowID()
	}
	storedInput, err := store.EnsureWorkflow(ctx, workflowID, "research-draft-review", input)
	if err != nil {
		return "", err
	}
	if storedInput != "" {
		input = storedInput
	}
	steps, err := store.ListSteps(ctx, workflowID)
	if err != nil {
		return "", err
	}
	if len(steps) != len(StepNames) {
		return "", fmt.Errorf("workflow %q has %d steps, want %d", workflowID, len(steps), len(StepNames))
	}
	fns := []StepFunc{researcher, drafter, reviewer}
	current := input
	for i, st := range steps {
		if st.Status == StatusDone {
			current = st.Output
			continue
		}
		if err := store.SetRunning(ctx, workflowID, st.Seq); err != nil {
			return "", err
		}
		out, err := fns[i](ctx, current)
		if err != nil {
			return "", err
		}
		if err := store.SetDone(ctx, workflowID, st.Seq, out); err != nil {
			return "", err
		}
		parent := ""
		if i > 0 {
			parent = workflowID
		}
		if err := store.EmitSpan(ctx, workflowID, "", parent, st.Name, spanAttrs(st.Seq, out)); err != nil {
			return "", err
		}
		current = out
	}
	if err := store.SetWorkflowStatus(ctx, workflowID, StatusDone); err != nil {
		return "", err
	}
	return current, nil
}
