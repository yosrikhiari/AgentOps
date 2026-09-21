ALTER TABLE eval_pair_scores ALTER COLUMN faithfulness DROP NOT NULL;
ALTER TABLE eval_pair_scores ADD COLUMN retrieval_miss BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS eval_pair_scores_miss ON eval_pair_scores (run_id, retrieval_miss);
