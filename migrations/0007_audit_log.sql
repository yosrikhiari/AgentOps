-- 0007 audit_log: append-only transition trail (Track S). Writers never
-- UPDATE this table; readers resolve chains by (entity, entity_id, id).
-- Events are a closed set enforced in code (audit package); detail carries
-- ids, counts and scores only — never prompts.
CREATE TABLE IF NOT EXISTS audit_log (
  id BIGSERIAL PRIMARY KEY,
  ts TIMESTAMPTZ NOT NULL DEFAULT now(),
  entity TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  event TEXT NOT NULL,
  detail JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS audit_log_entity ON audit_log (entity, entity_id, id);
