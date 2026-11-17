package evals

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Track T — release gate. A version tag requires k consecutive green runs on
// the frozen golden set, judged by the release-time threshold (not the
// score-time one): the gate answers "does this tree meet today's bar".
//
// Scope, deliberately narrow:
//   - Golden-answer runs only (model = ”): per-model comparison runs never
//     enter the window — enforced in SQL, pinned by test.
//   - Identical judge across the window: a judge change invalidates (same rule
//     as drift's judge_changed). Recovery is explicit: run the suite until k
//     new-judge runs fill the window; the report says so.
//   - Fewer than k runs fails: no evidence is not evidence.
//   - Local pre-tag step: CI has neither the GPU nor the eval Postgres, so
//     the gate runs on the operator's box, not in the release job.
type GateRun struct {
	ID         int
	Judge      string
	Prompt     string
	Score      float64
	StartedAt  time.Time
	FinishedAt time.Time
}

type GateResult struct {
	Pass   bool
	Report string
	Runs   []GateRun
}

func ReleaseGate(ctx context.Context, db Queryer, golden string, k int, threshold float64) (GateResult, error) {
	var res GateResult
	if k <= 0 {
		k = 3
	}
	rows, err := db.Query(ctx,
		`SELECT id, judge_model, judge_prompt, score, started_at, created_at FROM eval_runs
		 WHERE golden_version = $1 AND model = ''
		   AND EXISTS (SELECT 1 FROM eval_pair_scores p WHERE p.run_id = eval_runs.id AND p.faithfulness IS NOT NULL)
		 ORDER BY created_at DESC, id DESC LIMIT $2`,
		golden, k)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var r GateRun
		if err := rows.Scan(&r.ID, &r.Judge, &r.Prompt, &r.Score, &r.StartedAt, &r.FinishedAt); err != nil {
			return res, err
		}
		res.Runs = append(res.Runs, r)
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "release gate %s k=%d threshold=%.2f: %d/%d runs\n", golden, k, threshold, len(res.Runs), k)
	for _, r := range res.Runs {
		fmt.Fprintf(&sb, "  run %d judge=%s prompt=%s score=%.3f\n", r.ID, r.Judge, r.Prompt, r.Score)
	}
	if len(res.Runs) < k {
		fmt.Fprintf(&sb, "FAIL: need %d runs, have %d — run the suite %d more time(s)", k, len(res.Runs), k-len(res.Runs))
		res.Report = sb.String()
		return res, nil
	}
	judge := res.Runs[0].Judge
	for _, r := range res.Runs[1:] {
		if r.Judge != judge {
			fmt.Fprintf(&sb, "FAIL: judge changed across the window (%s vs %s) — run the suite %d more time(s) on the new judge to re-baseline", judge, r.Judge, k)
			res.Report = sb.String()
			return res, nil
		}
	}
	// Same model, different prompt is still a different judge: the verdict boundary
	// moved, so a window that straddles the change is not evidence about this tree.
	prompt := res.Runs[0].Prompt
	for _, r := range res.Runs[1:] {
		if r.Prompt != prompt {
			fmt.Fprintf(&sb, "FAIL: judge prompt changed across the window (%s vs %s) — run the suite %d more time(s) on the new prompt to re-baseline", promptLabel(prompt), promptLabel(r.Prompt), k)
			res.Report = sb.String()
			return res, nil
		}
	}
	worst := res.Runs[0]
	for _, r := range res.Runs[1:] {
		if r.Score < worst.Score {
			worst = r
		}
	}
	if worst.Score < threshold {
		fmt.Fprintf(&sb, "FAIL: run %d scores %.3f below %.2f", worst.ID, worst.Score, threshold)
		res.Report = sb.String()
		return res, nil
	}
	fmt.Fprintf(&sb, "PASS: %d/%d green", len(res.Runs), k)
	res.Report = sb.String()
	res.Pass = true
	return res, nil
}

// promptLabel names an empty prompt version honestly: runs recorded before the column
// existed have no version, which is not "v1" and must not equal a real one.
func promptLabel(v string) string {
	if v == "" {
		return "unknown-prompt"
	}
	return v
}
