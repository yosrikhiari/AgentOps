package evals

// Track T — release gate (TDD RED: ReleaseGate does not exist yet).
// Arbitrated scope: local pre-tag step (CI has no GPU/Postgres), golden-answer
// runs only, release-time threshold, sliding last-k window.

import (
	"context"
	"strings"
	"testing"
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

func greenRun(id int) []any { return []any{id, "qwen3:8b", 0.95} }

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
	db := &gateDB{rows: &gateRows{rows: [][]any{greenRun(3), {2, "qwen3:8b", 0.41}, greenRun(1)}}}
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
	db := &gateDB{rows: &gateRows{rows: [][]any{greenRun(3), greenRun(2), {1, "other-judge", 0.95}}}}
	res, err := ReleaseGate(context.Background(), db, "v3", 3, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass {
		t.Fatal("mixed judges must invalidate the window")
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
