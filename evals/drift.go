package evals

import (
	"context"
	"sort"
)

type WorstCase struct {
	Question      string   `json:"question"`
	Faithfulness  *float64 `json:"faithfulness"`
	Precision     float64  `json:"precision"`
	Recall        float64  `json:"recall"`
	RetrievalMiss bool     `json:"retrieval_miss"`
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
	MissesNow     int         `json:"misses_now"`
	MissesThen    int         `json:"misses_then"`
	WorstCases    []WorstCase `json:"worst_cases"`
}

func RecordRun(ctx context.Context, exec Execer, qr Querier, goldenVersion, judgeModel, model string, score float64, pairs []PairResult) (int, error) {
	var id int
	row := qr.QueryRow(ctx,
		`INSERT INTO eval_runs (golden_version, judge_model, model, score) VALUES ($1, $2, $3, $4) RETURNING id`,
		goldenVersion, judgeModel, model, score)
	if err := row.Scan(&id); err != nil {
		return 0, err
	}
	for _, p := range pairs {
		q := p.Question
		if len([]rune(q)) > 500 {
			q = string([]rune(q)[:500])
		}
		var f any
		if !p.RetrievalMiss {
			f = p.Faithfulness // miss → NULL, never 0.0
		}
		if _, err := exec.Exec(ctx,
			`INSERT INTO eval_pair_scores (run_id, question, faithfulness, precision, recall, retrieval_miss, latency_s, tokens) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (run_id, question) DO UPDATE SET faithfulness = EXCLUDED.faithfulness, precision = EXCLUDED.precision, recall = EXCLUDED.recall, retrieval_miss = EXCLUDED.retrieval_miss, latency_s = EXCLUDED.latency_s, tokens = EXCLUDED.tokens`,
			id, q, f, p.Precision, p.Recall, p.RetrievalMiss, p.LatencyS, p.Tokens); err != nil {
			return id, err
		}
	}
	return id, nil
}

type runRow struct {
	id     int
	judge  string
	model  string
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
		`SELECT question, faithfulness, precision, recall, retrieval_miss FROM eval_pair_scores WHERE run_id = $1 ORDER BY retrieval_miss DESC, faithfulness ASC NULLS FIRST LIMIT 3`,
		runs[0].id)
	if err != nil {
		return rep, err
	}
	defer wrows.Close()
	for wrows.Next() {
		var w WorstCase
		if err := wrows.Scan(&w.Question, &w.Faithfulness, &w.Precision, &w.Recall, &w.RetrievalMiss); err != nil {
			return rep, err
		}
		rep.WorstCases = append(rep.WorstCases, w)
	}
	if err := wrows.Err(); err != nil {
		return rep, err
	}
	sort.Slice(rep.WorstCases, func(i, j int) bool {
		if rep.WorstCases[i].RetrievalMiss != rep.WorstCases[j].RetrievalMiss {
			return rep.WorstCases[i].RetrievalMiss
		}
		if rep.WorstCases[i].Faithfulness == nil {
			return true
		}
		if rep.WorstCases[j].Faithfulness == nil {
			return false
		}
		return *rep.WorstCases[i].Faithfulness < *rep.WorstCases[j].Faithfulness
	})
	rep.MissesNow = countMisses(ctx, db, runs[0].id)
	if len(runs) == 2 {
		rep.MissesThen = countMisses(ctx, db, runs[1].id)
	}
	return rep, nil
}

func countMisses(ctx context.Context, db Queryer, runID int) int {
	rows, err := db.Query(ctx,
		`SELECT COUNT(*) FROM eval_pair_scores WHERE run_id = $1 AND retrieval_miss`,
		runID)
	if err != nil {
		return 0
	}
	defer rows.Close()
	var n int
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0
		}
	}
	return n
}
