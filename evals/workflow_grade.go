package evals

import (
	"context"
	"time"
)

// WorkflowGrade is one stored quality grade for a workflow's final answer.
type WorkflowGrade struct {
	WorkflowID string
	Judge      string
	Prompt     string
	Score      int
	Reason     string
	GradedAt   time.Time
}

// SaveWorkflowGrade upserts the latest grade for a workflow. One row per workflow: the
// question is "how good is this answer now", and history lives in the commit/eval logs.
func SaveWorkflowGrade(ctx context.Context, exec Execer, g WorkflowGrade) error {
	if g.GradedAt.IsZero() {
		g.GradedAt = time.Now().UTC()
	}
	_, err := exec.Exec(ctx,
		`INSERT INTO workflow_grades (workflow_id, judge, prompt, score, reason, graded_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (workflow_id) DO UPDATE SET judge = EXCLUDED.judge, prompt = EXCLUDED.prompt,
		   score = EXCLUDED.score, reason = EXCLUDED.reason, graded_at = EXCLUDED.graded_at`,
		g.WorkflowID, g.Judge, g.Prompt, g.Score, g.Reason, g.GradedAt)
	return err
}
