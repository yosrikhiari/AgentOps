-- Eval progress: total pairs in the golden set, so the console can show N/M.
ALTER TABLE eval_runs ADD COLUMN IF NOT EXISTS total_pairs INT NOT NULL DEFAULT 0;