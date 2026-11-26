-- Outcome-based verification (docs/WORKFLOW-EVAL-RESEARCH.md): a workflow is
-- 'done' only when its verifier passes, not when the agent claims success.
ALTER TABLE workflows ADD COLUMN IF NOT EXISTS verifier TEXT NOT NULL DEFAULT '';
ALTER TABLE workflows ADD COLUMN IF NOT EXISTS verified BOOLEAN;
ALTER TABLE workflows ADD COLUMN IF NOT EXISTS verifier_output TEXT NOT NULL DEFAULT '';
ALTER TABLE workflows ADD COLUMN IF NOT EXISTS verified_at TIMESTAMPTZ;