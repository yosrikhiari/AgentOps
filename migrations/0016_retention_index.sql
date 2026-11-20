-- 0016 retention index: the Requests page scans spans by name inside a 24-hour
-- window, and the overview/drift paths filter the same way. Without (name,
-- started_at) that filter reads every row in the window and detoasts JSONB for
-- each one; spans_started_at alone cannot help a name predicate.
--
-- Retention itself is not schema: the sweep (SPAN_KEEP, DECISION_KEEP,
-- AUDIT_KEEP) runs at boot and daily in main.go, so the table this index serves
-- is bounded by policy rather than growing forever.
CREATE INDEX IF NOT EXISTS spans_name_started ON spans (name, started_at DESC);
