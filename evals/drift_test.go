package evals

import (
	"context"
	"fmt"
	"testing"
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
			f, _ := args[2].(float64)
			p, _ := args[3].(float64)
			r, _ := args[4].(float64)
			d.pairs[id] = append(d.pairs[id], WorstCase{Question: q, Faithfulness: f, Precision: p, Recall: r})
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
	score, _ := args[2].(float64)
	d.runs = append([]runRow{{id: d.nextID, golden: golden, judge: judge, score: score}}, d.runs...)
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
			var rows [][]any
			for _, w := range d.pairs[id] {
				rows = append(rows, []any{w.Question, w.Faithfulness, w.Precision, w.Recall})
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
	}
	id, err := RecordRun(ctx, db, db, "v1", "test-judge", 0.55, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("want run id")
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
	if len(rep.WorstCases) != 2 || rep.WorstCases[0].Question != "q1" {
		t.Fatalf("worst cases not sorted: %+v", rep.WorstCases)
	}
	calm, err := DriftReportFor(ctx, &driftMemDB{pairs: map[int][]WorstCase{}}, "v1", 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if calm.Runs != 0 || calm.Alert {
		t.Fatalf("empty should not alert: %+v", calm)
	}
}
