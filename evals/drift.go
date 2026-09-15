package evals

import (
	"context"
	"sort"
)

type WorstCase struct {
	Question     string  `json:"question"`
	Faithfulness float64 `json:"faithfulness"`
	Precision    float64 `json:"precision"`
	Recall       float64 `json:"recall"`
}

type DriftReport struct {
	GoldenVersion string      `json:"golden_version"`
	Threshold     float64     `json:"threshold"`
	ScoreThen     float64     `json:"score_then"`
	ScoreNow      float64     `json:"score_now"`
	JudgeThen     string      `json:"judge_then"`
	JudgeNow      string      `json:"judge_now"`
	JudgeChanged  bool        `json:"judge_changed"`
	Delta         float64     `json:"delta"`
	Alert         bool        `json:"alert"`
	Runs          int         `json:"runs"`
	WorstCases    []WorstCase `json:"worst_cases"`
}

func RecordRun(ctx context.Context, exec Execer, qr Querier, goldenVersion, judgeModel string, score float64, pairs []PairResult) (int, error) {
	var id int
	row := qr.QueryRow(ctx,
		`INSERT INTO eval_runs (golden_version, judge_model, score) VALUES ($1, $2, $3) RETURNING id`,
		goldenVersion, judgeModel, score)
	if err := row.Scan(&id); err != nil {
		return 0, err
	}
	for _, p := range pairs {
		q := p.Question
		if len([]rune(q)) > 500 {
			q = string([]rune(q)[:500])
		}
		if _, err := exec.Exec(ctx,
			`INSERT INTO eval_pair_scores (run_id, question, faithfulness, precision, recall) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (run_id, question) DO UPDATE SET faithfulness = EXCLUDED.faithfulness, precision = EXCLUDED.precision, recall = EXCLUDED.recall`,
			id, q, p.Faithfulness, p.Precision, p.Recall); err != nil {
			return id, err
		}
	}
	return id, nil
}

type runRow struct {
	id     int
	judge  string
	score  float64
	golden string
}

func DriftReportFor(ctx context.Context, db Queryer, goldenVersion string, threshold float64) (DriftReport, error) {
	rep := DriftReport{GoldenVersion: goldenVersion, Threshold: threshold, WorstCases: []WorstCase{}}
	rows, err := db.Query(ctx,
		`SELECT id, golden_version, judge_model, score FROM eval_runs WHERE golden_version = $1 ORDER BY created_at DESC, id DESC LIMIT 2`,
		goldenVersion)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	var runs []runRow
	for rows.Next() {
		var r runRow
		if err := rows.Scan(&r.id, &r.golden, &r.judge, &r.score); err != nil {
			return rep, err
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}
	if len(runs) == 0 {
		return rep, nil
	}
	rep.Runs = len(runs)
	rep.ScoreNow = runs[0].score
	rep.ScoreThen = runs[0].score
	rep.JudgeNow = runs[0].judge
	rep.JudgeThen = runs[0].judge
	if len(runs) == 2 {
		rep.ScoreThen = runs[1].score
		rep.JudgeThen = runs[1].judge
	}
	// A delta measured across two different judges is not drift in the corpus or the
	// model under test — flag it so the reader does not chase a ghost.
	rep.JudgeChanged = rep.JudgeNow != rep.JudgeThen
	rep.Delta = rep.ScoreNow - rep.ScoreThen
	rep.Alert = rep.ScoreNow < threshold
	wrows, err := db.Query(ctx,
		`SELECT question, faithfulness, precision, recall FROM eval_pair_scores WHERE run_id = $1 ORDER BY faithfulness ASC LIMIT 3`,
		runs[0].id)
	if err != nil {
		return rep, err
	}
	defer wrows.Close()
	for wrows.Next() {
		var w WorstCase
		if err := wrows.Scan(&w.Question, &w.Faithfulness, &w.Precision, &w.Recall); err != nil {
			return rep, err
		}
		rep.WorstCases = append(rep.WorstCases, w)
	}
	if err := wrows.Err(); err != nil {
		return rep, err
	}
	sort.Slice(rep.WorstCases, func(i, j int) bool { return rep.WorstCases[i].Faithfulness < rep.WorstCases[j].Faithfulness })
	return rep, nil
}
