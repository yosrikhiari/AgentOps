-- Which judge prompt produced a run's scores.
--
-- The judge is a model plus a question. Changing the prompt moves the verdict boundary
-- even when the model name is identical, so drift and the release gate must see that
-- change and refuse to compare across it - the same discipline they already apply to a
-- judge swap (0012 is the GPU thermal feature; this is the eval-provenance one).
--
-- '' is deliberate: it means the run predates this column, so its prompt version is
-- unknown. Unknown is its own value and never compares equal to a real version.
ALTER TABLE eval_runs ADD COLUMN IF NOT EXISTS judge_prompt text NOT NULL DEFAULT '';
