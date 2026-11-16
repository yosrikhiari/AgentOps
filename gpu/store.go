package gpu

import (
	"context"
	"time"
)

// The three interfaces below are the repo's established shape (tracker doubles
// them). main.go's gpuAdapter satisfies them; this package never imports pgx.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
}

type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

type Queryer interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

type SQLStore struct {
	Exec  Execer
	Query Queryer
}

func (s SQLStore) InsertSample(ctx context.Context, x Sample) error {
	_, err := s.Exec.Exec(ctx,
		`INSERT INTO gpu_samples (ts, gpu_name, util_pct, mem_used, mem_total, power_w, temp_c)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		x.At, x.Name, x.UtilPct, x.MemUsedBytes, x.MemTotalBytes, x.PowerW, x.TempC)
	return err
}

// Series returns samples newest-first. since bounds the window; limit bounds
// the rows so a week-long window cannot return five figures to a browser.
func (s SQLStore) Series(ctx context.Context, since time.Time, limit int) ([]Sample, error) {
	rows, err := s.Query.Query(ctx,
		`SELECT ts, gpu_name, util_pct, mem_used, mem_total, power_w, temp_c
		 FROM gpu_samples WHERE ts >= $1 ORDER BY ts DESC LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var x Sample
		if err := rows.Scan(&x.At, &x.Name, &x.UtilPct, &x.MemUsedBytes, &x.MemTotalBytes, &x.PowerW, &x.TempC); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Nearest returns the sample closest in time to at, within tolerance — the
// honest way to say "the GPU during this request" without claiming a
// per-request measurement that does not exist.
func (s SQLStore) Nearest(ctx context.Context, at time.Time, tolerance time.Duration) (Sample, bool, error) {
	rows, err := s.Query.Query(ctx,
		`SELECT ts, gpu_name, util_pct, mem_used, mem_total, power_w, temp_c
		 FROM gpu_samples WHERE ts BETWEEN $1 AND $2
		 ORDER BY abs(extract(epoch FROM ts - $3)) LIMIT 1`,
		at.Add(-tolerance), at.Add(tolerance), at)
	if err != nil {
		return Sample{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Sample{}, false, rows.Err()
	}
	var x Sample
	if err := rows.Scan(&x.At, &x.Name, &x.UtilPct, &x.MemUsedBytes, &x.MemTotalBytes, &x.PowerW, &x.TempC); err != nil {
		return Sample{}, false, err
	}
	return x, true, nil
}

// Purge drops samples older than keep. Called at boot and hourly.
func (s SQLStore) Purge(ctx context.Context, keep time.Duration) (int64, error) {
	return s.Exec.Exec(ctx,
		`DELETE FROM gpu_samples WHERE ts < now() - make_interval(secs => $1)`, keep.Seconds())
}
