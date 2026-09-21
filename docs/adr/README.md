# Architecture decision records

One page each, written once the decision was real. Numbering follows the plan.

| # | Decision |
|---|---|
| [0001](0001-monorepo-single-binary.md) | One module, one binary, packages behind tiny DB interfaces |
| [0002](0002-ollama-first.md) | Ollama first, vLLM later |
| [0003](0003-postgres-native-durability.md) | Postgres-native durable steps, not DBOS/Temporal |
| [0004](0004-pgvector.md) | pgvector, not a separate vector store |
| [0005](0005-eval-runs-plus-pair-scores.md) | Per-question eval rows for drift and worst cases |
| [0006](0006-scheduler-is-a-flag.md) | The scheduler is a flag, not a service |
| [0007](0007-gateway-backends-and-fail-closed.md) | Backend abstraction and the fail-closed rule |
| [0008](0008-console-without-a-framework.md) | Tower console: embedded static files, no framework |
| [0009](0009-generation-params-and-client-refs.md) | v1.1: generation parameters, client references, extra local models |
| [0010](0010-client-held-conversation-and-gated-workflows.md) | Cockpit: client-held conversation; capability-gated workflows with approval (proposed) |
| [0011](0011-startup-presence-check.md) | Track J: presence over assumption — unpulled models read 503, paths never register |
| [0012](0012-static-advisor.md) | Track K: static VRAM/fit report with provenance-bound rumors, no runtime role |
