CREATE TABLE IF NOT EXISTS eval_pair_scores (
  run_id INT NOT NULL REFERENCES eval_runs(id) ON DELETE CASCADE,
  question TEXT NOT NULL,
  faithfulness DOUBLE PRECISION NOT NULL,
  precision DOUBLE PRECISION NOT NULL DEFAULT 0,
  recall DOUBLE PRECISION NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, question)
);

CREATE INDEX IF NOT EXISTS eval_runs_golden_created ON eval_runs (golden_version, created_at DESC);
CREATE INDEX IF NOT EXISTS eval_pair_scores_run ON eval_pair_scores (run_id);
