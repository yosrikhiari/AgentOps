package gpu

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeExec struct {
	sql  string
	args []any
}

func (f *fakeExec) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	f.sql, f.args = sql, args
	return 1, nil
}

type fakeQueryer struct{ rows Rows }

func (f *fakeQueryer) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return f.rows, nil
}

type oneRow struct {
	scan func(dest ...any) error
	done bool
}

func (o *oneRow) Next() bool {
	if o.done {
		return false
	}
	o.done = true
	return true
}

func (o *oneRow) Scan(dest ...any) error { return o.scan(dest...) }
func (o *oneRow) Err() error             { return nil }
func (o *oneRow) Close()                 {}

func TestInsertSample(t *testing.T) {
	ex := &fakeExec{}
	s := SQLStore{Exec: ex}
	if err := s.InsertSample(context.Background(), Sample{
		At: time.Unix(0, 0).UTC(), Name: "GTX 1650", UtilPct: 39,
		MemUsedBytes: 77 << 20, MemTotalBytes: 4096 << 20,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ex.sql, "INSERT INTO gpu_samples") {
		t.Fatalf("sql = %s", ex.sql)
	}
	if len(ex.args) != 7 {
		t.Fatalf("args = %d, want 7", len(ex.args))
	}
}

// TestSeriesMapsRows is the regression test for the bug Track S shipped: a
// query naming a column the migration did not create 502'd in production.
func TestSeriesMapsRows(t *testing.T) {
	ts := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	rows := &oneRow{scan: func(dest ...any) error {
		*dest[0].(*time.Time) = ts
		*dest[1].(*string) = "GTX 1650"
		*dest[2].(*float64) = 39
		*dest[3].(*int64) = 77 << 20
		*dest[4].(*int64) = 4096 << 20
		*dest[5].(**float64) = nil
		*dest[6].(**float64) = nil
		return nil
	}}
	got, err := SQLStore{Query: &fakeQueryer{rows: rows}}.Series(context.Background(), ts, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UtilPct != 39 || got[0].At != ts {
		t.Fatalf("got %+v", got)
	}
}

// TestPurgeUsesTypedInterval guards the interval bug Track S shipped: building
// the window by casting a string ('7 days') is both slow and wrong for
// sub-day durations. Purge must use make_interval(secs => $1).
func TestPurgeUsesTypedInterval(t *testing.T) {
	ex := &fakeExec{}
	if _, err := (SQLStore{Exec: ex}).Purge(context.Background(), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ex.sql, "make_interval(secs =>") {
		t.Fatalf("sql = %s", ex.sql)
	}
	if strings.Contains(ex.sql, "'::interval'") || strings.Contains(ex.sql, "|| ' days'") {
		t.Fatalf("string-cast interval: %s", ex.sql)
	}
	if len(ex.args) != 1 {
		t.Fatalf("args = %d, want 1", len(ex.args))
	}
}
