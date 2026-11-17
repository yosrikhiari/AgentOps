package evals

// Track T — release gate (TDD RED: ReleaseGate does not exist yet).
// Arbitrated scope: local pre-tag step (CI has no GPU/Postgres), golden-answer
// runs only, release-time threshold, sliding last-k window.

import (
	"context"
	"strings"
	"testing"
	"time"
)

type gateRows struct {
	rows [][]any
	i    int
}

func (r *gateRows) Next() bool { return r.i < len(r.rows) }
func (r *gateRows) Close()     {}
func (r *gateRows) Err() error { return nil }
func (r *gateRows) Scan(dest ...any) error {
	row := r.rows[r.i]
	r.i++
	if len(dest) != len(row) {
		return errGateScan
	}
	for i := range row {
		switch d := dest[i].(type) {
		case *int:
			*d = row[i].(int)
		case *string:
			*d = row[i].(string)
		case *float64:
			*d = row[i].(float64)
		case *time.Time:
			*d = row[i].(time.Time)
		default:
			return errGateScan
		}
	}
	return nil
}

type gateScanError string

func (e gateScanError) Error() string { return string(e) }

var errGateScan = gateScanError("scan mismatch")

type gateDB struct {
	sql  string
	rows *gateRows
}

func (d *gateDB) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	d.sql = sql
	return d.rows, nil
}

func greenRun(id int) []any {
	start := time.Date(2026, 11, 16, 10, id, 0, 0, time.UTC)
	return []any{id, "qwen3:8b", "faithfulness-v2", 0.95, start, start.Add(4 * time.Minute)}
}

// runWithPrompt builds one gate row with an explicit judge prompt version.
func runWithPrompt(id int, prompt string) []any {
	start := time.Date(2026, 11, 16, 10, id, 0, 0, time.UTC)
	return []any{id, "qwen3:8b", prompt, 0.95, start, start.Add(4 * time.Minute)}
}

// TestGateRunCarriesItsWindow: the gate report is only half useful without the
// window each run occupied - that window is what lets the caller ask the GPU
// store what the card was doing during it.
func TestGateRunCarriesItsWindow(t *testing.T) {
	db := &gateDB{rows: &gateRows{rows: [][]any{greenRun(1)}}}
	res, err := ReleaseGate(context.Background(), db, "v3", 1, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Runs) != 1 {
		t.Fatalf("runs = %+v", res.Runs)
	}
	r := res.Runs[0]
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() || !r.FinishedAt.After(r.StartedAt) {
		t.Fatalf("window = %v .. %v", r.StartedAt, r.FinishedAt)
	}
}

func TestReleaseGatePass(t *testing.T) {
	db := &gateDB{rows: &gateRows{rows: [][]any{greenRun(3), greenRun(2), greenRun(1)}}}
	res, err := ReleaseGate(context.Background(), db, "v3", 3, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass {
		t.Fatalf("three green runs must pass: %+v", res)
	}
	if !strings.Contains(res.Report, "3/3") {
		t.Fatalf("report must show the window: %q", res.Report)
	}
}

func TestReleaseGateFailsBelowThreshold(t *testing.T) {
	db := &gateDB{rows: &gateRows{rows: [][]any{greenRun(3), {2, "qwen3:8b", "faithfulness-v2", 0.41, time.Date(2026, 11, 16, 10, 2, 0, 0, time.UTC), time.Date(2026, 11, 16, 10, 6, 0, 0, time.UTC)}, greenRun(1)}}}
	res, err := ReleaseGate(context.Background(), db, "v3", 3, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass {
		t.Fatal("a sub-threshold run in the window must fail the gate")
	}
	if !strings.Contains(res.Report, "2") || !strings.Contains(res.Report, "0.41") {
		t.Fatalf("report must name the failing run and score: %q", res.Report)
	}
}

func TestReleaseGateNeedsKRuns(t *testing.T) {
	db := &gateDB{rows: &gateRows{rows: [][]any{greenRun(1)}}}
	res, err := ReleaseGate(context.Background(), db, "v3", 3, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass {
		t.Fatal("one run must not satisfy k=3")
	}
}

func TestReleaseGateJudgeChangedInvalidates(t *testing.T) {
	db := &gateDB{rows: &gateRows{rows: [][]any{greenRun(3), greenRun(2), {1, "other-judge", "faithfulness-v2", 0.95, time.Date(2026, 11, 16, 10, 1, 0, 0, time.UTC), time.Date(2026, 11, 16, 10, 5, 0, 0, time.UTC)}}}}
	res, err := ReleaseGate(context.Background(), db, "v3", 3, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass {
		t.Fatal("mixed judges must invalidate the window")
	}
}

// TestReleaseGatePromptChangedInvalidates: the same model asked a different question is a
// different judge. A window that straddles a prompt change cannot say anything about the
// tree under test, and the report must name the versions instead of failing silently.
func TestReleaseGatePromptChangedInvalidates(t *testing.T) {
	db := &gateDB{rows: &gateRows{rows: [][]any{greenRun(3), greenRun(2), runWithPrompt(1, "faithfulness-v1")}}}
	res, err := ReleaseGate(context.Background(), db, "v3", 3, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass {
		t.Fatal("a prompt change across the window must invalidate it")
	}
	if !strings.Contains(res.Report, "prompt changed") ||
		!strings.Contains(res.Report, "faithfulness-v1") {
		t.Fatalf("report does not name the prompt change: %q", res.Report)
	}
	// An empty version (a run recorded before the column existed) is its own value: it
	// must not compare equal to a real version.
	db = &gateDB{rows: &gateRows{rows: [][]any{greenRun(3), greenRun(2), runWithPrompt(1, "")}}}
	res, err = ReleaseGate(context.Background(), db, "v3", 3, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass || !strings.Contains(res.Report, "unknown-prompt") {
		t.Fatalf("unknown prompt must not match a version: %+v", res)
	}
}

// TestReleaseGateIgnoresUnjudgedRuns: a run whose pairs were all retrieval misses has no
// faithfulness measurement at all. It must never enter the window - otherwise an empty
// corpus produces a 0.000 "score" that blocks releases and alarms an operator about a
// model that was never judged.
func TestReleaseGateIgnoresUnjudgedRuns(t *testing.T) {
	db := &gateDB{rows: &gateRows{rows: [][]any{greenRun(1)}}}
	if _, err := ReleaseGate(context.Background(), db, "v3", 1, 0.7); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(db.sql, "p.faithfulness IS NOT NULL") {
		t.Fatalf("the gate must exclude runs with nothing judged: %q", db.sql)
	}
}

func TestReleaseGateIgnoresModelRuns(t *testing.T) {
	db := &gateDB{rows: &gateRows{rows: [][]any{greenRun(3), greenRun(2), greenRun(1)}}}
	if _, err := ReleaseGate(context.Background(), db, "v3", 3, 0.7); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(db.sql, "model = ''") {
		t.Fatalf("per-model comparison runs must never enter the window: %q", db.sql)
	}
}
