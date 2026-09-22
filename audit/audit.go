// Package audit is Track S: an append-only transition trail. Eval history is
// already append-only and tracker status UPDATEs are operational state — the
// gap this closes is the trail between states: what was created, resumed,
// completed or failed, and when. Writers never UPDATE; readers resolve chains
// by (entity, id, time). Events are typed constructors with fixed fields, so
// a prompt structurally cannot enter the log (no free text anywhere).
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"agentops/evals"
)

// Events. Unknown strings are rejected at write time — a typo'd event fails
// loudly in dev, never silently writes a row nobody queries.
const (
	EventCreated   = "created"
	EventResumed   = "resumed"
	EventStepDone  = "step_done"
	EventCompleted = "completed"
	EventFailed    = "failed"
	EventStarted   = "started"
	EventFinished  = "finished"
)

var validEvents = map[string]bool{
	EventCreated: true, EventResumed: true, EventStepDone: true,
	EventCompleted: true, EventFailed: true, EventStarted: true,
	EventFinished: true,
}

type Entry struct {
	Entity string            `json:"entity"`
	ID     string            `json:"id"`
	Event  string            `json:"event"`
	Detail map[string]string `json:"detail"`
}

func WorkflowCreated(id, wtype string) Entry {
	return Entry{Entity: "workflow", ID: id, Event: EventCreated, Detail: map[string]string{"type": wtype}}
}

func WorkflowResumed(id string) Entry {
	return Entry{Entity: "workflow", ID: id, Event: EventResumed, Detail: map[string]string{}}
}

func WorkflowStepDone(id, step string, attempts int) Entry {
	return Entry{Entity: "workflow", ID: id, Event: EventStepDone,
		Detail: map[string]string{"step": step, "attempts": strconv.Itoa(attempts)}}
}

func WorkflowCompleted(id string) Entry {
	return Entry{Entity: "workflow", ID: id, Event: EventCompleted, Detail: map[string]string{}}
}

func WorkflowFailed(id, code string) Entry {
	return Entry{Entity: "workflow", ID: id, Event: EventFailed, Detail: map[string]string{"code": code}}
}

func EvalStarted(golden, model string, pairs int) Entry {
	return Entry{Entity: "eval_run", ID: golden, Event: EventStarted,
		Detail: map[string]string{"golden": golden, "model": model, "pairs": strconv.Itoa(pairs)}}
}

func EvalFinished(runID int, score float64, alert bool) Entry {
	return Entry{Entity: "eval_run", ID: strconv.Itoa(runID), Event: EventFinished,
		Detail: map[string]string{"score": strconv.FormatFloat(score, 'f', 3, 64), "alert": strconv.FormatBool(alert)}}
}

// Store appends entries and reads chains. SQLStore is the Postgres one;
// MemStore is the fake for tests (and for callers that run without a DB).
type Store interface {
	Append(ctx context.Context, e Entry) error
	History(ctx context.Context, entity, id string, limit int) ([]LoggedEntry, error)
}

type LoggedEntry struct {
	Entry
	Seq int64  `json:"seq"`
	At  string `json:"at"`
}

type MemStore struct {
	rows []LoggedEntry
	seq  int64
}

func NewMemStore() *MemStore { return &MemStore{} }

func (m *MemStore) Append(ctx context.Context, e Entry) error {
	if !validEvents[e.Event] {
		return fmt.Errorf("audit: unknown event %q", e.Event)
	}
	m.seq++
	m.rows = append(m.rows, LoggedEntry{Entry: e, Seq: m.seq})
	return nil
}

func (m *MemStore) History(ctx context.Context, entity, id string, limit int) ([]LoggedEntry, error) {
	var out []LoggedEntry
	for _, r := range m.rows {
		if r.Entity == entity && (id == "" || r.ID == id) {
			out = append(out, r)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

type SQLStore struct {
	Exec  evals.Execer
	Query evals.Queryer
}

func (s SQLStore) Append(ctx context.Context, e Entry) error {
	if !validEvents[e.Event] {
		return fmt.Errorf("audit: unknown event %q", e.Event)
	}
	raw, err := json.Marshal(e.Detail)
	if err != nil {
		return err
	}
	_, err = s.Exec.Exec(ctx,
		`INSERT INTO audit_log (entity, entity_id, event, detail) VALUES ($1, $2, $3, $4)`,
		e.Entity, e.ID, e.Event, string(raw))
	return err
}

func (s SQLStore) History(ctx context.Context, entity, id string, limit int) ([]LoggedEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.Query.Query(ctx,
		`SELECT id, entity, entity_id, event, detail::text, ts FROM audit_log WHERE entity = $1 AND ($2 = '' OR entity_id = $2) ORDER BY id LIMIT $3`,
		entity, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LoggedEntry
	for rows.Next() {
		var l LoggedEntry
		var detail string
		var ts time.Time
		if err := rows.Scan(&l.Seq, &l.Entity, &l.ID, &l.Event, &detail, &ts); err != nil {
			return nil, err
		}
		l.At = ts.Format(time.RFC3339)
		l.Detail = map[string]string{}
		_ = json.Unmarshal([]byte(detail), &l.Detail)
		if !validEvents[l.Event] {
			continue
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
