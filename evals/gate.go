package evals

import (
	"context"
	"fmt"
	"strings"
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
	ID    int
	Judge string
	Score float64
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
		`SELECT id, judge_model, score FROM eval_runs WHERE golden_version = $1 AND model = '' ORDER BY created_at DESC, id DESC LIMIT $2`,
		golden, k)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var r GateRun
		if err := rows.Scan(&r.ID, &r.Judge, &r.Score); err != nil {
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
		fmt.Fprintf(&sb, "  run %d judge=%s score=%.3f\n", r.ID, r.Judge, r.Score)
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
