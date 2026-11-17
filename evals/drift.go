package evals

import (
	"context"
	"math"
	"sort"
	"time"
)

type WorstCase struct {
	Question      string   `json:"question"`
	Faithfulness  *float64 `json:"faithfulness"`
	Precision     float64  `json:"precision"`
	Recall        float64  `json:"recall"`
	RetrievalMiss bool     `json:"retrieval_miss"`
}

type DriftReport struct {
	GoldenVersion string  `json:"golden_version"`
	Threshold     float64 `json:"threshold"`
	ScoreThen     float64 `json:"score_then"`
	ScoreNow      float64 `json:"score_now"`
	JudgeThen     string  `json:"judge_then"`
	JudgeNow      string  `json:"judge_now"`
	JudgeChanged  bool    `json:"judge_changed"`
	// PromptNow/PromptThen are the judge prompt versions. A prompt change moves the
	// verdict boundary, so it invalidates a comparison exactly like a judge swap; the
	// empty string is "recorded before the column existed", kept distinct from any
	// real version rather than guessed at.
	PromptNow     string      `json:"prompt_now"`
	PromptThen    string      `json:"prompt_then"`
	PromptChanged bool        `json:"prompt_changed"`
	Delta         float64     `json:"delta"`
	Alert         bool        `json:"alert"`
	Runs          int         `json:"runs"`
	MissesNow     int         `json:"misses_now"`
	MissesThen    int         `json:"misses_then"`
	WorstCases    []WorstCase `json:"worst_cases"`
}

// RunDB is the slice of the store a run needs: it writes pairs as they are judged and
// reads back what an interrupted attempt already stored.
type RunDB interface {
	Execer
	Querier
	Queryer
}

// StartOrResumeRun opens a run and returns it with the pairs already stored. An unfinished
// run for the same golden set, judge model and prompt version is resumed: on a 4 GB card a
// 72-pair suite can outlive the judge process, and re-judging 66 pairs to reach the last
// six wastes the very minutes that keep failing.
type StartedRun struct {
	ID      int
	Resumed bool
	Pairs   []StoredPair
}

// StoredPair is a pair read back from an earlier attempt. Only the fields the suite
// summary needs are kept; the claim text and justifications are already on disk.
type StoredPair struct {
	Question      string
	Faithfulness  float64
	Precision     float64
	Recall        float64
	RetrievalMiss bool
	Claims        int
}

// Unsolvable is the stored-pair form of Unsolvable(PairResult), same rule.
func (p StoredPair) Unsolvable() bool {
	return p.RetrievalMiss || (p.Claims > 0 && p.Faithfulness == 0)
}

func StartOrResumeRun(ctx context.Context, db RunDB, goldenVersion, judgeModel, model, judgePrompt string, startedAt time.Time) (StartedRun, error) {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	rows, err := db.Query(ctx,
		`SELECT id FROM eval_runs
		 WHERE golden_version = $1 AND judge_model = $2 AND model = $3 AND judge_prompt = $4
		   AND status <> 'complete'
		 ORDER BY id DESC LIMIT 1`, goldenVersion, judgeModel, model, judgePrompt)
	if err != nil {
		return StartedRun{}, err
	}
	var id int
	if rows.Next() {
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return StartedRun{}, err
		}
	}
	rows.Close()
	if id != 0 {
		pairs, err := LoadRunPairs(ctx, db, id)
		if err != nil {
			return StartedRun{}, err
		}
		return StartedRun{ID: id, Resumed: true, Pairs: pairs}, nil
	}
	row := db.QueryRow(ctx,
		`INSERT INTO eval_runs (golden_version, judge_model, model, judge_prompt, score, started_at, status)
		 VALUES ($1, $2, $3, $4, 0, $5, 'running') RETURNING id`,
		goldenVersion, judgeModel, model, judgePrompt, startedAt)
	if err := row.Scan(&id); err != nil {
		return StartedRun{}, err
	}
	return StartedRun{ID: id}, nil
}

// LoadRunPairs reads what an earlier attempt already judged.
func LoadRunPairs(ctx context.Context, db Queryer, runID int) ([]StoredPair, error) {
	rows, err := db.Query(ctx,
		`SELECT question, faithfulness, precision, recall, retrieval_miss, claims
		 FROM eval_pair_scores WHERE run_id = $1 ORDER BY question`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredPair
	for rows.Next() {
		var p StoredPair
		var f *float64
		if err := rows.Scan(&p.Question, &f, &p.Precision, &p.Recall, &p.RetrievalMiss, &p.Claims); err != nil {
			return nil, err
		}
		if f != nil {
			p.Faithfulness = finiteOrZero(*f)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddPair stores one judged pair immediately. A crash after this call keeps the evidence.
func AddPair(ctx context.Context, exec Execer, runID int, p PairResult) error {
	q := p.Question
	if len([]rune(q)) > 500 {
		q = string([]rune(q)[:500])
	}
	var f any
	if !p.RetrievalMiss {
		f = p.Faithfulness // miss → NULL, never 0.0
	}
	_, err := exec.Exec(ctx,
		`INSERT INTO eval_pair_scores (run_id, question, faithfulness, precision, recall, retrieval_miss, latency_s, tokens, claims) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (run_id, question) DO UPDATE SET faithfulness = EXCLUDED.faithfulness, precision = EXCLUDED.precision, recall = EXCLUDED.recall, retrieval_miss = EXCLUDED.retrieval_miss, latency_s = EXCLUDED.latency_s, tokens = EXCLUDED.tokens, claims = EXCLUDED.claims`,
		runID, q, f, p.Precision, p.Recall, p.RetrievalMiss, p.LatencyS, p.Tokens, len(p.Claims))
	return err
}

// FinishRun seals the run: the score becomes readable and drift and the gate may use it.
func FinishRun(ctx context.Context, exec Execer, runID int, score float64) error {
	_, err := exec.Exec(ctx,
		`UPDATE eval_runs SET score = $2, status = 'complete', finished_at = now() WHERE id = $1`, runID, score)
	return err
}

// FailRun records why a run stopped. It stays resumable: 'failed' is not 'complete'.
func FailRun(ctx context.Context, exec Execer, runID int) error {
	_, err := exec.Exec(ctx,
		`UPDATE eval_runs SET status = 'failed', finished_at = now() WHERE id = $1 AND status <> 'complete'`, runID)
	return err
}

// RecordRun writes a whole run in one go. Kept for tests and for any caller that already
// has every pair in hand.
func RecordRun(ctx context.Context, db RunDB, goldenVersion, judgeModel, model, judgePrompt string, score float64, startedAt time.Time, pairs []PairResult) (int, error) {
	run, err := StartOrResumeRun(ctx, db, goldenVersion, judgeModel, model, judgePrompt, startedAt)
	if err != nil {
		return 0, err
	}
	for _, p := range pairs {
		if err := AddPair(ctx, db, run.ID, p); err != nil {
			return run.ID, err
		}
	}
	return run.ID, FinishRun(ctx, db, run.ID, score)
}

type runRow struct {
	id     int
	judge  string
	prompt string
	model  string
	score  float64
	golden string
}

func DriftReportFor(ctx context.Context, db Queryer, goldenVersion string, threshold float64) (DriftReport, error) {
	rep := DriftReport{GoldenVersion: goldenVersion, Threshold: threshold, WorstCases: []WorstCase{}}
	rows, err := db.Query(ctx,
		`SELECT id, golden_version, judge_model, judge_prompt, score FROM eval_runs
		 WHERE golden_version = $1 AND status = 'complete'
		   AND EXISTS (SELECT 1 FROM eval_pair_scores p WHERE p.run_id = eval_runs.id AND p.faithfulness IS NOT NULL)
		 ORDER BY created_at DESC, id DESC LIMIT 2`,
		goldenVersion)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	var runs []runRow
	for rows.Next() {
		var r runRow
		if err := rows.Scan(&r.id, &r.golden, &r.judge, &r.prompt, &r.score); err != nil {
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
	rep.ScoreNow = finiteOrZero(runs[0].score)
	rep.ScoreThen = rep.ScoreNow
	rep.JudgeNow = runs[0].judge
	rep.JudgeThen = runs[0].judge
	rep.PromptNow = runs[0].prompt
	rep.PromptThen = runs[0].prompt
	if len(runs) == 2 {
		rep.ScoreThen = finiteOrZero(runs[1].score)
		rep.JudgeThen = runs[1].judge
		rep.PromptThen = runs[1].prompt
	}
	// A delta measured across two different judges is not drift in the corpus or the
	// model under test — flag it so the reader does not chase a ghost. A prompt change
	// is the same kind of break: the judge is the same model with a different question.
	rep.JudgeChanged = rep.JudgeNow != rep.JudgeThen
	rep.PromptChanged = rep.PromptNow != rep.PromptThen
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
		// Rows written before PrecisionRecall guarded empty retrieval can carry
		// NaN. JSON cannot encode it, so one legacy row would take down every
		// report that embeds it; read a non-finite metric as the zero it should
		// have been, and a non-finite faithfulness as "not scored".
		w.Precision = finiteOrZero(w.Precision)
		w.Recall = finiteOrZero(w.Recall)
		if w.Faithfulness != nil && !isFinite(*w.Faithfulness) {
			w.Faithfulness = nil
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

// isFinite reports whether v can be encoded as a JSON number.
func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// finiteOrZero keeps NaN and ±Inf out of everything this package returns.
// encoding/json rejects them, and a stored metric must never be able to turn a
// report into a 502.
func finiteOrZero(v float64) float64 {
	if !isFinite(v) {
		return 0
	}
	return v
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
