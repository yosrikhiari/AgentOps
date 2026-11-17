-- Phase 0 of intent-aware routing: record the decision the router already makes.
--
-- Labels and model names only - no prompt, no answer - so this table is safe under the
-- same rule as spans. Phase 1 joins it to per-(query, model) outcomes to compute AIQ/PGR;
-- until then it answers "what did we choose, and why", and it is the audit trail for every
-- later policy that changes the answer.
--
-- planned is the policy's primary candidate; chosen is the model that actually answered
-- (empty when every candidate failed). They differ when a fallback ran, and a replay must
-- attribute the outcome to the model that produced it, not to the plan.
CREATE TABLE IF NOT EXISTS route_decisions (
  id BIGSERIAL PRIMARY KEY,
  trace_id TEXT NOT NULL,
  intent TEXT NOT NULL,
  intent_confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
  difficulty DOUBLE PRECISION NOT NULL DEFAULT 0,
  policy TEXT NOT NULL,
  tier TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  planned TEXT NOT NULL DEFAULT '',
  chosen TEXT NOT NULL DEFAULT '',
  fallback BOOLEAN NOT NULL DEFAULT false,
  failed BOOLEAN NOT NULL DEFAULT false,
  candidates TEXT[] NOT NULL DEFAULT '{}',
  sensitive BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS route_decisions_created ON route_decisions (created_at DESC);
CREATE INDEX IF NOT EXISTS route_decisions_intent ON route_decisions (intent, created_at DESC);
