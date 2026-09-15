# ADR-0006 — The scheduler is a flag, not a service

**Status:** accepted (2026-09-14)

**Decision.** `--schedule-evals 24h` loops the exact `scoreOnce` that `--score` runs, re-reading the golden file each tick, logging loudly on failure and surviving it. The console's *Run eval suite* calls the same function, single-flight.

**Consequences.** Zero new deps; any cron/systemd timer can drive it. Limits: no lease across replicas, and an eval run shares the GPU with live traffic (`EVAL_TIMEOUT`, `docs/VRAM.md`).
