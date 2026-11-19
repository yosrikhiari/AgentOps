package console

// Wave 4 of the redesign guide: the Metrics page reads buckets aggregated from
// spans. These tests pin the two things that can silently lie in a chart: the
// SQL must use percentile_cont (a mean would hide the tail), and empty buckets
// must come back as zeros rather than holes.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tower/evals"
)

type seriesRows struct {
	rows [][]any
	i    int
}

func (r *seriesRows) Next() bool { return r.i < len(r.rows) }
func (r *seriesRows) Close()     {}
func (r *seriesRows) Err() error { return nil }
func (r *seriesRows) Scan(dest ...any) error {
	row := r.rows[r.i]
	r.i++
	for i := range row {
		switch d := dest[i].(type) {
		case *time.Time:
			*d = row[i].(time.Time)
		case *int:
			*d = row[i].(int)
		case *float64:
			*d = row[i].(float64)
		case *int64:
			*d = row[i].(int64)
		}
	}
	return nil
}

type seriesDB struct {
	sql  string
	rows *seriesRows
}

func (d *seriesDB) Query(ctx context.Context, sql string, args ...any) (evals.Rows, error) {
	d.sql = sql
	return d.rows, nil
}

func TestMetricSeriesFillsEmptyBucketsAndUsesPercentiles(t *testing.T) {
	start := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	db := &seriesDB{rows: &seriesRows{rows: [][]any{
		{start, 1, 0, 0.5, 0.9, int64(10), int64(20)},
		{start.Add(2 * time.Minute), 1, 1, 2.0, 2.1, int64(5), int64(7)},
	}}}
	store := SQLStore{Query: db}
	got, err := store.MetricSeries(context.Background(), start, start.Add(3*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("points = %d, want 4 (start..until inclusive)", len(got))
	}
	if got[0].P50S != 0.5 || got[0].TokensOut != 20 {
		t.Fatalf("first bucket = %+v", got[0])
	}
	if got[1].Requests != 0 || got[1].P50S != 0 {
		t.Fatalf("empty bucket must be zero-filled, got %+v", got[1])
	}
	if got[2].Errors != 1 || got[2].P99S != 2.1 {
		t.Fatalf("third bucket = %+v", got[2])
	}
	if !strings.Contains(db.sql, "percentile_cont(0.5)") || !strings.Contains(db.sql, "percentile_cont(0.99)") {
		t.Fatalf("latency must be a percentile, not an average: %s", db.sql)
	}
	if !strings.Contains(db.sql, "date_bin") {
		t.Fatalf("buckets must be aligned in SQL: %s", db.sql)
	}
}

func TestMetricSeriesHandlerValidatesWindow(t *testing.T) {
	h := New(Deps{Store: &memStore{}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/metrics/series?window=2h", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown window = %d, want 400", rec.Code)
	}

	for window, step := range map[string]int{"1h": 60, "6h": 300, "24h": 900} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/metrics/series?window="+window, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", window, rec.Code, rec.Body.String())
		}
		var body struct {
			Window string `json:"window"`
			StepS  int    `json:"step_s"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Window != window || body.StepS != step {
			t.Fatalf("%s: window=%q step=%d", window, body.Window, body.StepS)
		}
	}
}
