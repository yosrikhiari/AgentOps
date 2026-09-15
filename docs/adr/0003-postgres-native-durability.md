# ADR-0003 — Postgres-native durable steps, not DBOS/Temporal

**Status:** accepted (2026-09-14) · **Context:** the tracker must survive `kill -9` mid-workflow; DBOS has an official Go SDK; Temporal needs a cluster.

**Decision.** Durability = row status. `workflows(id, status, input)` + `steps(workflow_id, seq, name, status, output, attempts)` with `(workflow_id, seq)` as the idempotency key; `pending → running → done`; resume = re-run the same workflow id and skip `done` steps, reusing their stored output and the workflow's *original* input. Stdlib + pgx only.

**Consequences.** ~250 lines, fully testable with an in-memory `Store`, proven by `TestKillResume` and a live kill. Known limits (backlog): no lease for two concurrent resumers, no `failed` terminal state / attempt cap, tracker step spans use the workflow id as parent instead of a root span.
