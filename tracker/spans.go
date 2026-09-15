package tracker

import (
	"context"
	"encoding/json"
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
	return out, rows.Err()
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
	return out, nil
}
