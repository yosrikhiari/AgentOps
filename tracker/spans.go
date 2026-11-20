package tracker

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

type Span struct {
	TraceID   string    `json:"trace_id"`
	SpanID    string    `json:"span_id"`
	ParentID  string    `json:"parent_id"`
	Name      string    `json:"name"`
	StartedAt time.Time `json:"started_at"`
	Attrs     string    `json:"attrs"`
}

func RedactAttrs(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return "{}"
	}
	for _, k := range []string{"prompt", "input", "text", "output"} {
		delete(m, k)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(out)
}

// orderSpans returns the spans of one trace parents-before-children. started_at is the
// time the single writer thread persisted the span, and the OS clock is coarse enough
// that two spans emitted back-to-back share it to the microsecond — the SQL ORDER BY
// started_at, span_id then decided their order by a random id. Walking parent_id makes
// the order a fact rather than a coincidence, without a schema change.
func orderSpans(in []Span) []Span {
	if len(in) < 2 {
		return in
	}
	byID := make(map[string]*Span, len(in))
	for i := range in {
		byID[in[i].SpanID] = &in[i]
	}
	depth := make(map[string]int, len(in))
	var depthOf func(id string, guard int) int
	depthOf = func(id string, guard int) int {
		if d, ok := depth[id]; ok {
			return d
		}
		sp, ok := byID[id]
		if !ok || sp.ParentID == "" || guard > 64 { // unknown or cyclic parent: treat as root
			depth[id] = 0
			return 0
		}
		d := depthOf(sp.ParentID, guard+1) + 1
		depth[id] = d
		return d
	}
	out := append([]Span(nil), in...)
	for i := range out {
		depthOf(out[i].SpanID, 0)
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := depth[out[i].SpanID], depth[out[j].SpanID]
		if di != dj {
			return di < dj
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

func (s SQLStore) ListSpans(ctx context.Context, traceID string) ([]Span, error) {
	rows, err := s.Query.Query(ctx,
		`SELECT trace_id, span_id, parent_id, name, started_at, attrs::text FROM spans WHERE trace_id = $1 ORDER BY started_at, span_id`,
		traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Span
	for rows.Next() {
		var sp Span
		if err := rows.Scan(&sp.TraceID, &sp.SpanID, &sp.ParentID, &sp.Name, &sp.StartedAt, &sp.Attrs); err != nil {
			return nil, err
		}
		sp.Attrs = RedactAttrs(sp.Attrs)
		out = append(out, sp)
	}
	return orderSpans(out), rows.Err()
}

func (m *MemStore) ListSpans(ctx context.Context, traceID string) ([]Span, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Span
	for _, sp := range m.spanData {
		if sp.TraceID == traceID {
			sp.Attrs = RedactAttrs(sp.Attrs)
			out = append(out, sp)
		}
	}
	return orderSpans(out), nil
}
