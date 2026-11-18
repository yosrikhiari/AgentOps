package tracker

import (
	"context"
	"math"
	"testing"
)

type trajRow struct {
	id, status                     string
	steps, done, attempts, retries int
}

func (r trajRow) Scan(dest ...any) error {
	*dest[0].(*string) = r.id
	*dest[1].(*string) = r.status
	*dest[2].(*int) = r.steps
	*dest[3].(*int) = r.done
	*dest[4].(*int) = r.attempts
	*dest[5].(*int) = r.retries
	return nil
}

type trajRows struct {
	row  *trajRow
	seen bool
}

func (r *trajRows) Next() bool {
	if r.row == nil || r.seen {
		return false
	}
	r.seen = true
	return true
}
func (r *trajRows) Scan(dest ...any) error { return r.row.Scan(dest...) }
func (r *trajRows) Err() error             { return nil }
func (r *trajRows) Close()                 {}

type trajDB struct {
	rows  *trajRows
	query string
}

func (d *trajDB) Query(_ context.Context, sql string, _ ...any) (Rows, error) {
	d.query = sql
	return d.rows, nil
}

// TestTrajectoryCleanRequiresNoRetries: the success predicate is "completed, every step
// done, nothing retried". A run that finished on the second attempt is not clean - the
// retry is the thing durable workflows exist to remove.
func TestTrajectoryCleanRequiresNoRetries(t *testing.T) {
	db := &trajDB{rows: &trajRows{row: &trajRow{id: "w1", status: StatusDone, steps: 3, done: 3, attempts: 3}}}
	got, err := TrajectoryFor(context.Background(), db, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Clean || got.Retries != 0 {
		t.Fatalf("clean run = %+v", got)
	}
	if got.Summary() == "" {
		t.Fatal("empty summary")
	}

	retried := &trajDB{rows: &trajRows{row: &trajRow{id: "w2", status: StatusDone, steps: 3, done: 3, attempts: 5, retries: 2}}}
	got, err = TrajectoryFor(context.Background(), retried, "w2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Clean {
		t.Fatalf("a retried run must not be clean: %+v", got)
	}
	if got.Retries != 2 {
		t.Fatalf("retries = %d", got.Retries)
	}

	unfinished := &trajDB{rows: &trajRows{row: &trajRow{id: "w3", status: StatusRunning, steps: 3, done: 1, attempts: 1}}}
	got, err = TrajectoryFor(context.Background(), unfinished, "w3")
	if err != nil {
		t.Fatal(err)
	}
	if got.Clean {
		t.Fatalf("unfinished run must not be clean: %+v", got)
	}
}

// TestTrajectoryUnknownID: a typo must not read as "0 steps, clean".
func TestTrajectoryUnknownID(t *testing.T) {
	db := &trajDB{rows: &trajRows{}}
	if _, err := TrajectoryFor(context.Background(), db, "nope"); err != ErrWorkflowNotFound {
		t.Fatalf("err = %v, want ErrWorkflowNotFound", err)
	}
}

// TestPassK: k=1 is the success rate; k=2 with 2 of 3 clean is 1/3 (one pair of the three
// possible pairs is both-clean); k>n is undefined, never zero.
func TestPassK(t *testing.T) {
	cases := []struct {
		successes, runs, k int
		want               float64
		ok                 bool
	}{
		{2, 3, 1, 2.0 / 3.0, true},
		{2, 3, 2, 1.0 / 3.0, true},
		{3, 3, 3, 1, true},
		{2, 3, 3, 0, true}, // still defined: no 3-subset is all-clean
		{2, 3, 4, 0, false},
		{0, 3, 1, 0, true},
		{5, 3, 1, 0, false}, // more successes than runs is a caller bug
		{1, 1, 1, 1, true},
	}
	for _, c := range cases {
		got, ok := PassK(c.successes, c.runs, c.k)
		if ok != c.ok {
			t.Fatalf("PassK(%d,%d,%d) ok = %v, want %v", c.successes, c.runs, c.k, ok, c.ok)
		}
		if ok && math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("PassK(%d,%d,%d) = %v, want %v", c.successes, c.runs, c.k, got, c.want)
		}
	}
}

func TestChoose(t *testing.T) {
	if choose(5, 2) != 10 || choose(3, 3) != 1 || choose(3, 0) != 1 || choose(2, 5) != 0 {
		t.Fatalf("choose broken: %d %d %d %d", choose(5, 2), choose(3, 3), choose(3, 0), choose(2, 5))
	}
}
