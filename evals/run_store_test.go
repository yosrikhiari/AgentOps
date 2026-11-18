package evals

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// runFake is a small in-memory store for the run lifecycle: header, incremental pairs,
// resume. It answers the four SQL shapes the code uses and nothing else, so a change in
// the real statements shows up as a test failure rather than a silent mismatch.
type runFake struct {
	runs   []fakeRun
	pairs  map[int][]StoredPair
	nextID int
}

type fakeRun struct {
	id           int
	golden       string
	judge        string
	model        string
	prompt       string
	score        float64
	status       string
	startedAt    time.Time
	finishedAt   time.Time
	hasFinished  bool
	questionSeen map[string]bool
}

type fakeRow struct{ id int }

func (r fakeRow) Scan(dest ...any) error {
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

func (f *runFake) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	switch {
	case strings.Contains(sql, "UPDATE eval_runs SET score"):
		id, _ := args[0].(int)
		score, _ := args[1].(float64)
		for i := range f.runs {
			if f.runs[i].id == id {
				f.runs[i].score = score
				f.runs[i].status = "complete"
				f.runs[i].finishedAt = time.Now()
				f.runs[i].hasFinished = true
			}
		}
		return 1, nil
	case strings.Contains(sql, "UPDATE eval_runs SET status"):
		id, _ := args[0].(int)
		for i := range f.runs {
			if f.runs[i].id == id && f.runs[i].status != "complete" {
				f.runs[i].status = "failed"
				f.runs[i].finishedAt = time.Now()
				f.runs[i].hasFinished = true
			}
		}
		return 1, nil
	case strings.Contains(sql, "INSERT INTO eval_pair_scores"):
		id, _ := args[0].(int)
		q, _ := args[1].(string)
		var faith float64
		if v, ok := args[2].(float64); ok {
			faith = v
		}
		var p, r float64
		if v, ok := args[3].(float64); ok {
			p = v
		}
		if v, ok := args[4].(float64); ok {
			r = v
		}
		miss, _ := args[5].(bool)
		claims, _ := args[8].(int)
		// upsert
		for i := range f.pairs[id] {
			if f.pairs[id][i].Question == q {
				f.pairs[id][i] = StoredPair{Question: q, Faithfulness: faith, Precision: p, Recall: r, RetrievalMiss: miss, Claims: claims}
				return 1, nil
			}
		}
		f.pairs[id] = append(f.pairs[id], StoredPair{Question: q, Faithfulness: faith, Precision: p, Recall: r, RetrievalMiss: miss, Claims: claims})
		return 1, nil
	}
	return 0, fmt.Errorf("unexpected exec: %s", sql)
}

func (f *runFake) QueryRow(_ context.Context, sql string, args ...any) Row {
	if !strings.Contains(sql, "INSERT INTO eval_runs") {
		return fakeRow{}
	}
	f.nextID++
	golden, _ := args[0].(string)
	judge, _ := args[1].(string)
	model, _ := args[2].(string)
	prompt, _ := args[3].(string)
	started, _ := args[4].(time.Time)
	f.runs = append(f.runs, fakeRun{id: f.nextID, golden: golden, judge: judge, model: model, prompt: prompt,
		status: "running", startedAt: started, questionSeen: map[string]bool{}})
	return fakeRow{id: f.nextID}
}

func (f *runFake) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	switch {
	case strings.Contains(sql, "status <> 'complete'"):
		golden, _ := args[0].(string)
		judge, _ := args[1].(string)
		model, _ := args[2].(string)
		prompt, _ := args[3].(string)
		for i := len(f.runs) - 1; i >= 0; i-- {
			r := f.runs[i]
			if r.golden == golden && r.judge == judge && r.model == model && r.prompt == prompt && r.status != "complete" {
				return &sliceRows{rows: [][]any{{r.id}}}, nil
			}
		}
		return &sliceRows{}, nil
	case strings.Contains(sql, "FROM eval_pair_scores") && strings.Contains(sql, "claims"):
		id, _ := args[0].(int)
		var rows [][]any
		for _, p := range f.pairs[id] {
			var faith any
			if !p.RetrievalMiss {
				faith = p.Faithfulness
			}
			rows = append(rows, []any{p.Question, faith, p.Precision, p.Recall, p.RetrievalMiss, p.Claims})
		}
		return &sliceRows{rows: rows}, nil
	}
	return &sliceRows{}, fmt.Errorf("unexpected query: %s", sql)
}

type sliceRows struct {
	rows [][]any
	i    int
}

func (r *sliceRows) Next() bool { return r.i < len(r.rows) }
func (r *sliceRows) Close()     {}
func (r *sliceRows) Err() error { return nil }
func (r *sliceRows) Scan(dest ...any) error {
	row := r.rows[r.i]
	r.i++
	if len(dest) != len(row) {
		return fmt.Errorf("arity %d vs %d", len(dest), len(row))
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			*p = row[i].(string)
		case *int:
			*p = row[i].(int)
		case *bool:
			*p = row[i].(bool)
		case **float64:
			if row[i] == nil {
				*p = nil
				continue
			}
			v := row[i].(float64)
			*p = &v
		case *float64:
			*p = row[i].(float64)
		case *time.Time:
			*p = row[i].(time.Time)
		default:
			return fmt.Errorf("unsupported dest %T", d)
		}
	}
	return nil
}

// TestStartFinishRun: a new run is 'running' until it is sealed, and only then does it
// carry a score. A crash before FinishRun leaves a row drift will ignore.
func TestStartFinishRun(t *testing.T) {
	ctx := context.Background()
	db := &runFake{pairs: map[int][]StoredPair{}}
	run, err := StartOrResumeRun(ctx, db, "v3", "phi4-mini", "", "faithfulness-v2", time.Unix(1000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if run.Resumed || run.ID == 0 || db.runs[0].status != "running" {
		t.Fatalf("start = %+v, stored %+v", run, db.runs[0])
	}
	if err := AddPair(ctx, db, run.ID, PairResult{Question: "q1", Faithfulness: 0.5, Precision: 0.2, Recall: 1, Claims: []ClaimResult{{Claim: "a"}, {Claim: "b"}}}); err != nil {
		t.Fatal(err)
	}
	if err := FinishRun(ctx, db, run.ID, 0.5); err != nil {
		t.Fatal(err)
	}
	if db.runs[0].status != "complete" || db.runs[0].score != 0.5 || !db.runs[0].hasFinished {
		t.Fatalf("finish = %+v", db.runs[0])
	}
}

// TestResumeInterruptedRun: the second attempt picks up the unfinished run with the same
// golden set, judge and prompt, and gets back what the first attempt already judged.
func TestResumeInterruptedRun(t *testing.T) {
	ctx := context.Background()
	db := &runFake{pairs: map[int][]StoredPair{}}
	first, err := StartOrResumeRun(ctx, db, "v3", "phi4-mini", "", "faithfulness-v2", time.Unix(1000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := AddPair(ctx, db, first.ID, PairResult{Question: "q1", Faithfulness: 1, Precision: 1, Recall: 1, Claims: []ClaimResult{{Claim: "a"}}}); err != nil {
		t.Fatal(err)
	}
	// Crash: no FinishRun. The next attempt resumes the same row and sees q1.
	second, err := StartOrResumeRun(ctx, db, "v3", "phi4-mini", "", "faithfulness-v2", time.Unix(2000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !second.Resumed || second.ID != first.ID {
		t.Fatalf("resume = %+v, first = %+v", second, first)
	}
	if len(second.Pairs) != 1 || second.Pairs[0].Question != "q1" || second.Pairs[0].Faithfulness != 1 {
		t.Fatalf("stored pairs = %+v", second.Pairs)
	}
	if len(db.runs) != 1 {
		t.Fatalf("resume created a second row: %+v", db.runs)
	}
	// A different prompt version is a different run: never mixed.
	third, err := StartOrResumeRun(ctx, db, "v3", "phi4-mini", "", "faithfulness-v3", time.Unix(3000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if third.Resumed || third.ID == first.ID {
		t.Fatalf("prompt change must not resume the old row: %+v", third)
	}
	// A completed run is never resumed.
	if err := FinishRun(ctx, db, first.ID, 1); err != nil {
		t.Fatal(err)
	}
	fourth, err := StartOrResumeRun(ctx, db, "v3", "phi4-mini", "", "faithfulness-v2", time.Unix(4000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Resumed || fourth.ID == first.ID {
		t.Fatalf("complete run resumed: %+v", fourth)
	}
}

// TestFailRunKeepsItResumable: a judge failure marks the run failed, not complete, so the
// next attempt resumes instead of re-judging the pairs already stored.
func TestFailRunKeepsItResumable(t *testing.T) {
	ctx := context.Background()
	db := &runFake{pairs: map[int][]StoredPair{}}
	run, err := StartOrResumeRun(ctx, db, "v3", "j", "", "p", time.Unix(1000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := FailRun(ctx, db, run.ID); err != nil {
		t.Fatal(err)
	}
	if db.runs[0].status != "failed" {
		t.Fatalf("status = %q", db.runs[0].status)
	}
	again, err := StartOrResumeRun(ctx, db, "v3", "j", "", "p", time.Unix(2000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !again.Resumed || again.ID != run.ID {
		t.Fatalf("failed run must be resumable: %+v", again)
	}
}

// TestDriftAndGateIgnoreIncompleteRuns pins the SQL contract: an interrupted suite is
// evidence about pairs, never a score in the window.
func TestDriftAndGateIgnoreIncompleteRuns(t *testing.T) {
	db := &sqlCaptureDB{}
	if _, err := DriftReportFor(context.Background(), db, "v3", 0.7); err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseGate(context.Background(), db, "v3", 1, 0.7); err != nil {
		t.Fatal(err)
	}
	if len(db.queries) != 2 {
		t.Fatalf("queries = %v", db.queries)
	}
	for _, q := range db.queries {
		if !strings.Contains(q, "status = 'complete'") {
			t.Fatalf("query does not exclude incomplete runs: %s", q)
		}
	}
	// Track L runs (model <> '') are a different question: the headline faithfulness is
	// the frozen golden answers, and a 1B model's run must not become "the number".
	if !strings.Contains(db.queries[0], "model = ''") {
		t.Fatalf("drift mixes model runs into the golden window: %s", db.queries[0])
	}
}

type sqlCaptureDB struct{ queries []string }

func (s *sqlCaptureDB) Query(_ context.Context, sql string, _ ...any) (Rows, error) {
	s.queries = append(s.queries, sql)
	return &sliceRows{}, nil
}
