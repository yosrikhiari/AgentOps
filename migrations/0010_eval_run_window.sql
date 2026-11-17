-- When a run started, not just when it finished.
--
-- The gate report already knew each run's id and score, but a GPU window needs
-- both ends. `created_at` is the finish; `started_at` is written by RecordRun.
-- Old rows are backfilled to created_at - an instantaneous window, so a GPU
-- summary for them reports no samples instead of inventing one.
ALTER TABLE eval_runs ADD COLUMN IF NOT EXISTS started_at timestamptz;
UPDATE eval_runs SET started_at = created_at WHERE started_at IS NULL;
ALTER TABLE eval_runs ALTER COLUMN started_at SET NOT NULL;
ALTER TABLE eval_runs ALTER COLUMN started_at SET DEFAULT now();
