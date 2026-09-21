-- 0006 benchmark: which model answered, and what each answer cost.
-- eval_runs.model is the model under test ('' = runs that score the frozen
-- golden answers, i.e. all history before Track L). eval_pair_scores carries
-- per-answer generation latency and completion tokens (both 0 for golden runs).
ALTER TABLE eval_runs ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE eval_pair_scores ADD COLUMN latency_s REAL NOT NULL DEFAULT 0;
ALTER TABLE eval_pair_scores ADD COLUMN tokens INT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS eval_runs_model ON eval_runs (golden_version, model, created_at DESC);
