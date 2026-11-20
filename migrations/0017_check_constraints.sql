-- 0017 check constraints: keep invalid state out of the tables that the rest of
-- the code assumes are clean. Every value here is already enforced in Go (the
-- handler rejects bad roles, the eval runner only writes the three statuses, the
-- workflow tracker only writes the four); the constraint is the last line so a
-- direct psql or a future caller cannot put the tables in a state the readers
-- do not handle. Constraints are added only when absent so re-running Migrate
-- on an upgraded schema is a no-op.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'eval_runs_status_check') THEN
    ALTER TABLE eval_runs
      ADD CONSTRAINT eval_runs_status_check
      CHECK (status IN ('running', 'complete', 'failed'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'messages_role_check') THEN
    ALTER TABLE messages
      ADD CONSTRAINT messages_role_check
      CHECK (role IN ('user', 'assistant'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'messages_tokens_nonneg_check') THEN
    ALTER TABLE messages
      ADD CONSTRAINT messages_tokens_nonneg_check
      CHECK (prompt_tokens >= 0 AND completion_tokens >= 0);
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workflows_status_check') THEN
    ALTER TABLE workflows
      ADD CONSTRAINT workflows_status_check
      CHECK (status IN ('running', 'done', 'failed'));
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'steps_status_check') THEN
    ALTER TABLE steps
      ADD CONSTRAINT steps_status_check
      CHECK (status IN ('pending', 'running', 'done', 'failed'));
  END IF;
END $$;
