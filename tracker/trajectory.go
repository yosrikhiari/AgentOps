package tracker

// Trajectory grading: what a workflow run did, not only what it said.
//
// The toy researcher→drafter→reviewer workflow has no ground truth for its content, so
// this cannot score answer quality. What it can measure honestly is orchestration
// reliability: did every step land, and did any step need a second attempt? That is the
// metric a durable-workflow pitch stands or falls on, and it is the one a retry loop
// silently ruins.

import (
	"context"
	"errors"
	"fmt"
)

// ErrWorkflowNotFound is returned when the id names no workflow; an empty run is not a
// pass, and a typo should not read as "0 steps, clean".
var ErrWorkflowNotFound = errors.New("workflow not found")

// Trajectory is one workflow run's shape.
type Trajectory struct {
	WorkflowID string
	Status     string
	Steps      int
	Done       int
	Attempts   int
	Retries    int
	// Clean is the success predicate this workflow can support: completed, every step
	// done, no step retried. It measures orchestration reliability, not answer quality.
	Clean bool
}

func (t Trajectory) Summary() string {
	return fmt.Sprintf("%s: %d steps, %d done, %d attempts (%d retries), status=%s, clean=%v",
		t.WorkflowID, t.Steps, t.Done, t.Attempts, t.Retries, t.Status, t.Clean)
}

// TrajectoryFor reads one workflow's steps and derives the trajectory counters in a single
// query, so the console and the CLI cannot disagree about what a run did.
func TrajectoryFor(ctx context.Context, q Queryer, workflowID string) (Trajectory, error) {
	rows, err := q.Query(ctx,
		`SELECT w.id, w.status,
		        count(s.*)::int,
		        count(*) FILTER (WHERE s.status = 'done')::int,
		        coalesce(sum(s.attempts), 0)::int,
		        coalesce(sum(greatest(s.attempts - 1, 0)), 0)::int
		 FROM workflows w LEFT JOIN steps s ON s.workflow_id = w.id
		 WHERE w.id = $1
		 GROUP BY w.id, w.status`, workflowID)
	if err != nil {
		return Trajectory{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Trajectory{}, err
		}
		return Trajectory{}, ErrWorkflowNotFound
	}
	t := Trajectory{WorkflowID: workflowID}
	if err := rows.Scan(&t.WorkflowID, &t.Status, &t.Steps, &t.Done, &t.Attempts, &t.Retries); err != nil {
		return Trajectory{}, err
	}
	t.Clean = t.Status == StatusDone && t.Steps > 0 && t.Done == t.Steps && t.Retries == 0
	return t, nil
}

// PassK is tau-bench's reliability metric: with n independent trials and c clean ones, the
// unbiased estimator of "all k trials succeed" is C(c,k)/C(n,k). k=1 is the plain success
// rate; k>n has no estimate and returns ok=false rather than a fabricated zero.
func PassK(successes, runs, k int) (float64, bool) {
	if runs < 1 || k < 1 || k > runs || successes < 0 || successes > runs {
		return 0, false
	}
	return float64(choose(successes, k)) / float64(choose(runs, k)), true
}

func choose(n, k int) int {
	if k > n {
		return 0
	}
	if k > n-k {
		k = n - k
	}
	out := 1
	for i := 1; i <= k; i++ {
		out = out * (n - k + i) / i
	}
	return out
}
