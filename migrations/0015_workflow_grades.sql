-- Quality grades for a workflow's final answer, separate from trajectory metrics.
--
-- A workflow can be "clean" (every step landed first try) and still answer badly. This
-- table stores one rubric grade per workflow, with the judge and rubric version, so a
-- later reader can tell a 5 from a model that no longer exists. It is NOT faithfulness:
-- the workflow does not persist its retrieved chunks, so there is no source to check the
-- answer against. The docs say so wherever the number is shown.
CREATE TABLE IF NOT EXISTS workflow_grades (
  workflow_id TEXT PRIMARY KEY REFERENCES workflows(id) ON DELETE CASCADE,
  judge       TEXT NOT NULL,
  prompt      TEXT NOT NULL,
  score       INT  NOT NULL,
  reason      TEXT NOT NULL DEFAULT '',
  graded_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
