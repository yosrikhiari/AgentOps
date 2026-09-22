package audit

// Track S — append-only transition trail (TDD RED: package does not exist).
// Arbitrated descope: eval history is already append-only and resume UPDATEs
// are operational state — the real gap is the transition trail, so the trail
// is all this package records. No free text anywhere: events are typed
// constructors, so a prompt structurally cannot enter the log.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentops/evals"
)

func TestTrailOrder(t *testing.T) {
	m := NewMemStore()
	ctx := context.Background()
	for _, e := range []Entry{
		WorkflowCreated("wf1", "toy"),
		WorkflowStepDone("wf1", "researcher", 1),
		WorkflowCompleted("wf1"),
	} {
		if err := m.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.History(ctx, "workflow", "wf1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Event != "created" || got[2].Event != "completed" {
		t.Fatalf("trail out of order: %+v", got)
	}
}

func TestUnknownEventRejected(t *testing.T) {
	m := NewMemStore()
	if err := m.Append(context.Background(), Entry{Entity: "workflow", ID: "w", Event: "frobnicate"}); err == nil {
		t.Fatal("unknown event must fail loudly, not write a row")
	}
}

func TestEvalEventsRoundTrip(t *testing.T) {
	m := NewMemStore()
	ctx := context.Background()
	if err := m.Append(ctx, EvalStarted("v3", "fast-m", 72)); err != nil {
		t.Fatal(err)
	}
	if err := m.Append(ctx, EvalFinished(9, 0.972, false)); err != nil {
		t.Fatal(err)
	}
	got, err := m.History(ctx, "eval_run", "9", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Detail["score"] != "0.972" {
		t.Fatalf("eval trail wrong: %+v", got)
	}
}

func TestHistoryLimit(t *testing.T) {
	m := NewMemStore()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := m.Append(ctx, WorkflowStepDone("w", "drafter", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.History(ctx, "workflow", "w", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Detail["attempts"] != "5" {
		t.Fatalf("want newest 2 ending at attempt 5: %+v", got)
	}
}

// Regression: History selected created_at while migration 0007 names the
// column ts — the endpoint 502'd live. The stub proves the scan mapping;
// the text pin below guards the column list against schema drift.
type stubRows struct {
	rows [][]any
	i    int
}

func (r *stubRows) Next() bool { return r.i < len(r.rows) }
func (r *stubRows) Close()     {}
func (r *stubRows) Err() error { return nil }
func (r *stubRows) Scan(dest ...any) error {
	row := r.rows[r.i]
	r.i++
	if len(dest) != len(row) {
		return errors.New("scan mismatch")
	}
	for i := range row {
		switch d := dest[i].(type) {
		case *int64:
			*d = row[i].(int64)
		case *string:
			*d = row[i].(string)
		case *time.Time:
			ts, err := time.Parse(time.RFC3339, row[i].(string))
			if err != nil {
				return err
			}
			*d = ts
		default:
			return errors.New("scan mismatch")
		}
	}
	return nil
}

type stubQueryer struct {
	sql  string
	rows *stubRows
}

func (s *stubQueryer) Query(ctx context.Context, sql string, args ...any) (evals.Rows, error) {
	s.sql = sql
	return s.rows, nil
}

func TestSQLHistoryMapsRows(t *testing.T) {
	q := &stubQueryer{rows: &stubRows{rows: [][]any{
		{int64(1), "workflow", "wf1", "created", `{"type":"toy"}`, "2026-09-22T00:00:00Z"},
	}}}
	s := SQLStore{Query: q}
	got, err := s.History(context.Background(), "workflow", "wf1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Seq != 1 || got[0].At == "" || got[0].Detail["type"] != "toy" {
		t.Fatalf("mapping wrong: %+v", got)
	}
	if !strings.Contains(q.sql, "ts FROM audit_log") || strings.Contains(q.sql, "created_at") {
		t.Fatalf("column list drifted from migration 0007: %q", q.sql)
	}
}
