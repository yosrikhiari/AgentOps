-- 0005 conversations: stored chat threads for the cockpit shelf.
-- Prompts ARE stored here (unlike gateway spans) — see PRIVACY.md: purpose is
-- conversation continuity, retention is 90 days (see purge query below), and
-- DELETE /v1/conversations/{id} erases a thread on demand.
CREATE TABLE IF NOT EXISTS conversations (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS messages (
  id BIGSERIAL PRIMARY KEY,
  conv_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  role TEXT NOT NULL,
  content TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  trace_id TEXT NOT NULL DEFAULT '',
  prompt_tokens INT NOT NULL DEFAULT 0,
  completion_tokens INT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS messages_conv_id ON messages(conv_id, id);

-- Retention purge (run at server boot, best-effort; also safe to cron):
-- DELETE FROM conversations WHERE updated_at < now() - interval '90 days';
