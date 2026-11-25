-- Trajectory eval storage (audit 2026-11 section 2.1): one row per workflow run,
-- plus one row per step, so a run's shape can be graded and compared later
-- without re-running it. Mirrors eval_runs/eval_pair_scores: the run is the
-- unit, the step scores are the detail.
CREATE TABLE IF NOT EXISTS workflow_runs (
  id BIGSERIAL PRIMARY KEY,
  workflow_id TEXT NOT NULL,
  status TEXT NOT NULL,
  steps INT NOT NULL DEFAULT 0,
  done INT NOT NULL DEFAULT 0,
  attempts INT NOT NULL DEFAULT 0,
  retries INT NOT NULL DEFAULT 0,
  clean BOOLEAN NOT NULL DEFAULT false,
  grade INT,                       -- 1-5 rubric, null until graded
  judge TEXT,
  prompt_version TEXT,
  graded_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS workflow_runs_workflow ON workflow_runs (workflow_id, created_at DESC);
CREATE INDEX IF NOT EXISTS workflow_runs_created ON workflow_runs (created_at DESC);

CREATE TABLE IF NOT EXISTS workflow_step_scores (
  id BIGSERIAL PRIMARY KEY,
  run_id BIGINT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
  step_name TEXT NOT NULL,
  status TEXT NOT NULL,
  attempts INT NOT NULL DEFAULT 0,
  latency_s DOUBLE PRECISION,
  tool_calls INT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS workflow_step_scores_run ON workflow_step_scores (run_id);