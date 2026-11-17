-- A long eval run must survive the judge dying.
--
-- Until now the run header and every pair row were one transaction: a crash at pair 66 of
-- 72 wrote nothing, and there was no way to resume. On a 4 GB card with a 3.8B judge that
-- is the common case, not the rare one - two attempts died at 66/72 and 58/72.
--
-- Now the header is written first (status 'running'), each pair is stored as it is judged,
-- and 'complete' is set only at the end. Drift and the release gate read complete runs
-- only, so an interrupted run is visible evidence, never a result. Resuming matches the
-- newest unfinished run for the same golden set, judge and prompt version and skips the
-- pairs already stored.
--
-- 'complete' is the default so every existing row keeps its meaning.
ALTER TABLE eval_runs ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'complete';
ALTER TABLE eval_runs ADD COLUMN IF NOT EXISTS finished_at timestamp with time zone;

-- Old rows are complete by definition; their finish time was the row's creation time.
UPDATE eval_runs SET finished_at = created_at WHERE finished_at IS NULL;

-- How many claims the scorer split out of the answer. Stored so a resumed run can report
-- "no claims" separately from "all claims refuted" for pairs judged by an earlier process.
ALTER TABLE eval_pair_scores ADD COLUMN IF NOT EXISTS claims integer NOT NULL DEFAULT 0;
