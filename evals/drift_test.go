package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

type driftMemDB struct {
	nextID int
	runs   []runRow
	pairs  map[int][]WorstCase
	execs  int
}

func (d *driftMemDB) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	d.execs++
	if len(args) >= 5 {
		if id, ok := args[0].(int); ok {
			q, _ := args[1].(string)
			var f *float64
			switch v := args[2].(type) {
			case float64:
				fv := v
				f = &fv
			case nil:
				f = nil // retrieval miss → NULL
			}
			p, _ := args[3].(float64)
			r, _ := args[4].(float64)
			miss := false
			if len(args) >= 6 {
				miss, _ = args[5].(bool)
			}
			d.pairs[id] = append(d.pairs[id], WorstCase{Question: q, Faithfulness: f, Precision: p, Recall: r, RetrievalMiss: miss})
			return 1, nil
		}
	}
	return 0, nil
}

type driftRow struct{ id int }

func (r driftRow) Scan(dest ...any) error {
	if len(dest) != 1 {
		return fmt.Errorf("want 1 dest")
	}
	p, ok := dest[0].(*int)
	if !ok {
		return fmt.Errorf("want *int")
	}
	*p = r.id
	return nil
}

func (d *driftMemDB) QueryRow(ctx context.Context, sql string, args ...any) Row {
	d.nextID++
	golden, _ := args[0].(string)
	judge, _ := args[1].(string)
	model, _ := args[2].(string)
	score, _ := args[3].(float64)
	d.runs = append([]runRow{{id: d.nextID, golden: golden, judge: judge, model: model, score: score}}, d.runs...)
	return driftRow{id: d.nextID}
}

type driftRows struct {
	rows [][]any
	i    int
}

func (r *driftRows) Next() bool { return r.i < len(r.rows) }
func (r *driftRows) Close()     {}
func (r *driftRows) Err() error { return nil }
func (r *driftRows) Scan(dest ...any) error {
	if r.i >= len(r.rows) {
		return fmt.Errorf("no row")
	}
	row := r.rows[r.i]
	r.i++
	if len(dest) != len(row) {
		return fmt.Errorf("arity")
	}
	for i := range dest {
		switch p := dest[i].(type) {
		case *int:
			*p = row[i].(int)
		case *string:
			*p = row[i].(string)
		case *float64:
			*p = row[i].(float64)
		case **float64:
			if row[i] == nil {
				*p = nil
			} else {
				fv := row[i].(float64)
				*p = &fv
			}
		case *bool:
			*p = row[i].(bool)
		default:
			return fmt.Errorf("type")
		}
	}
	return nil
}

func (d *driftMemDB) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	if len(args) == 1 {
		if _, ok := args[0].(string); ok {
			var rows [][]any
			for i, r := range d.runs {
				if i >= 2 {
					break
				}
				rows = append(rows, []any{r.id, r.golden, r.judge, r.score})
			}
			return &driftRows{rows: rows}, nil
		}
		if id, ok := args[0].(int); ok {
			if strings.Contains(sql, "COUNT(*)") {
				n := 0
				for _, w := range d.pairs[id] {
					if w.RetrievalMiss {
						n++
					}
				}
				return &driftRows{rows: [][]any{{n}}}, nil
			}
			var rows [][]any
			for _, w := range d.pairs[id] {
				var f any
				if w.Faithfulness != nil {
					f = *w.Faithfulness
				}
				rows = append(rows, []any{w.Question, f, w.Precision, w.Recall, w.RetrievalMiss})
			}
			return &driftRows{rows: rows}, nil
		}
	}
	return &driftRows{}, nil
}

func TestRecordAndDriftReport(t *testing.T) {
	ctx := context.Background()
	db := &driftMemDB{pairs: map[int][]WorstCase{}}
	pairs := []PairResult{
		{Question: "q1", Faithfulness: 0.2, Precision: 0.5, Recall: 1},
		{Question: "q2", Faithfulness: 0.9, Precision: 1, Recall: 1},
		{Question: "q-miss", Precision: 0, Recall: 0, RetrievalMiss: true},
	}
	id, err := RecordRun(ctx, db, db, "v1", "test-judge", "test-model", 0.55, time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC), pairs)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("want run id")
	}
	if db.runs[0].model != "test-model" {
		t.Fatalf("model under test not recorded: %+v", db.runs[0])
	}
	db.runs = append(db.runs, runRow{id: 99, golden: "v1", judge: "old", score: 0.95})
	rep, err := DriftReportFor(ctx, db, "v1", 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Runs != 2 {
		t.Fatalf("want 2 runs, got %+v", rep)
	}
	if rep.ScoreNow != 0.55 || rep.ScoreThen != 0.95 {
		t.Fatalf("bad scores %+v", rep)
	}
	if rep.Delta != rep.ScoreNow-rep.ScoreThen {
		t.Fatalf("bad delta %+v", rep)
	}
	if !rep.Alert {
		t.Fatal("0.55 < 0.7 should alert")
	}
	if rep.JudgeNow != "test-judge" || rep.JudgeThen != "old" || !rep.JudgeChanged {
		t.Fatalf("judge provenance missing: %+v", rep)
	}
	if len(rep.WorstCases) != 3 {
		t.Fatalf("want 3 worst cases, got %+v", rep.WorstCases)
	}
	if !rep.WorstCases[0].RetrievalMiss || rep.WorstCases[0].Faithfulness != nil {
		t.Fatalf("miss must sort first with NULL faithfulness: %+v", rep.WorstCases[0])
	}
	if rep.WorstCases[1].Question != "q1" {
		t.Fatalf("worst cases not sorted: %+v", rep.WorstCases)
	}
	if rep.MissesNow != 1 {
		t.Fatalf("want 1 miss now, got %+v", rep)
	}
	calm, err := DriftReportFor(ctx, &driftMemDB{pairs: map[int][]WorstCase{}}, "v1", 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if calm.Runs != 0 || calm.Alert {
		t.Fatalf("empty should not alert: %+v", calm)
	}
}

// TestDriftReportSurvivesALegacyNaN: rows written before the empty-retrieval
// fix carry precision = NaN (0/0). JSON cannot encode NaN, so one such row used
// to turn /v1/overview, /v1/evals/runs and /v1/drift/report into 502s. The read
// path must hand back a report that marshals, with the non-finite value read as
// the zero it should have been.
func TestDriftReportSurvivesALegacyNaN(t *testing.T) {
	db := &driftMemDB{
		runs:  []runRow{{id: 1, golden: "v1", judge: "j", score: math.NaN()}},
		pairs: map[int][]WorstCase{1: {{Question: "q", Faithfulness: ptr(math.NaN()), Precision: math.NaN(), Recall: 0}}},
	}
	rep, err := DriftReportFor(context.Background(), db, "v1", 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(rep); err != nil {
		t.Fatalf("report is not encodable: %v", err)
	}
	if rep.ScoreNow != 0 {
		t.Fatalf("score_now = %v, want 0 for a non-finite stored score", rep.ScoreNow)
	}
	if len(rep.WorstCases) != 1 {
		t.Fatalf("worst cases = %+v", rep.WorstCases)
	}
	if w := rep.WorstCases[0]; w.Precision != 0 || w.Faithfulness != nil {
		t.Fatalf("legacy NaN not sanitised: %+v", w)
	}
}

func ptr(f float64) *float64 { return &f }
