package tracker

import (
	"context"
	"time"
)

// WorkflowRun is a stored trajectory snapshot for one workflow run.
type WorkflowRun struct {
	ID         int64
	WorkflowID string
	Status     string
	Steps      int
	Done       int
	Attempts   int
	Retries    int
	Clean      bool
	Grade      *int
	Judge      string
	PromptVer  string
	GradedAt   *time.Time
	CreatedAt  time.Time
}

// RunStore is the minimal persistence surface RecordWorkflowRun needs.
type RunStore interface {
	Execer
	Queryer
}

// RecordWorkflowRun persists a Trajectory as a workflow_runs row and returns its id.
// Step-level detail is optional; pass nil to store the run summary only.
func RecordWorkflowRun(ctx context.Context, store RunStore, t Trajectory, steps []StepScore) (int64, error) {
	var grade *int
	var gradedAt *time.Time
	row, err := store.Query(ctx,
		`INSERT INTO workflow_runs (workflow_id, status, steps, done, attempts, retries, clean, grade, judge, prompt_version, graded_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
		t.WorkflowID, t.Status, t.Steps, t.Done, t.Attempts, t.Retries, t.Clean,
		grade, "", "", gradedAt)
	if err != nil {
		return 0, err
	}
	defer row.Close()
	if !row.Next() {
		if err := row.Err(); err != nil {
			return 0, err
		}
		return 0, context.Canceled
	}
	var id int64
	if err := row.Scan(&id); err != nil {
		return 0, err
	}
	for _, s := range steps {
		if _, err := store.Exec(ctx,
			`INSERT INTO workflow_step_scores (run_id, step_name, status, attempts, latency_s, tool_calls)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			id, s.Name, s.Status, s.Attempts, s.LatencyS, s.ToolCalls); err != nil {
			return id, err
		}
	}
	return id, nil
}

// StepScore is one step's contribution to a stored run.
type StepScore struct {
	Name      string
	Status    string
	Attempts  int
	LatencyS  float64
	ToolCalls int
}
