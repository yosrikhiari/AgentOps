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
		SwapsTotal: 5,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ex.sql, "INSERT INTO gpu_samples") {
		t.Fatalf("sql = %s", ex.sql)
	}
	if len(ex.args) != 8 {
		t.Fatalf("args = %d, want 8 (seven readings plus the swap counter)", len(ex.args))
	}
	if got, _ := ex.args[7].(int64); got != 5 {
		t.Fatalf("swaps_total = %v, want 5", ex.args[7])
	}
}

// TestSummaryMapsAggregateRow: the summary is what turns a table of samples into
// the sentence "this run saw 3 swaps", so every column has to land.
func TestSummaryMapsAggregateRow(t *testing.T) {
	q := &fakeQueryer{rows: &oneRow{scan: func(dest ...any) error {
		*(dest[0].(*int)) = 12
		*(dest[1].(*float64)) = 41.5
		*(dest[2].(*float64)) = 99
		*(dest[3].(*int64)) = 2 << 30
		*(dest[4].(*int64)) = 4 << 30
		*(dest[5].(*int64)) = 3
		return nil
	}}}
	from := time.Unix(1000, 0).UTC()
	to := time.Unix(2000, 0).UTC()
	sum, err := SQLStore{Query: q}.Summary(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Samples != 12 || sum.UtilMean != 41.5 || sum.UtilPeak != 99 {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.MemPeakBytes != 2<<30 || sum.MemTotalBytes != 4<<30 || sum.Swaps != 3 {
		t.Fatalf("summary = %+v", sum)
	}
	if !sum.From.Equal(from) || !sum.To.Equal(to) {
		t.Fatalf("window = %v..%v", sum.From, sum.To)
	}
}

// TestSummaryWithoutSamplesIsNotAnError: a run scored while the prober was off
// has no GPU story. Zero samples must read as "unmeasured", never as an error and
// never as a 0% run.
func TestSummaryWithoutSamplesIsNotAnError(t *testing.T) {
	q := &fakeQueryer{rows: &oneRow{scan: func(dest ...any) error {
		*(dest[0].(*int)) = 0
		*(dest[1].(*float64)) = 0
		*(dest[2].(*float64)) = 0
		*(dest[3].(*int64)) = 0
		*(dest[4].(*int64)) = 0
		*(dest[5].(*int64)) = 0
		return nil
	}}}
	sum, err := SQLStore{Query: q}.Summary(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Samples != 0 || sum.Swaps != 0 || sum.UtilPeak != 0 {
		t.Fatalf("summary = %+v", sum)
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
