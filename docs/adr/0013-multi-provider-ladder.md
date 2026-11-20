# ADR-0013 — Multi-provider LLM ladder and exhaustion handling

**Status:** accepted (2026-11-20) · **Context:** Tower supports one cloud backend (`GROQ_API_KEY`) and already fails over across the candidate chain built by `Plan()`. Free-tier cloud APIs have daily/minute quotas that, once exhausted, make every request fail for the rest of the window. The operator wants several free APIs tried in quality order, with local Ollama as the final rung.

**Decision.** A ranked, operator-declared ladder of cloud backends (best → worst) plus local Ollama as the last rung, with per-backend exhaustion state and cooldowns:

- **Registration:** `CLOUD_LADDER=name:key:url:model[,name:key:url:model,...]`, ordered best-first. When unset, the legacy `GROQ_API_KEY`/`CLOUD_*` variables form a one-rung ladder, so existing deployments are unchanged.
- **Exhaustion tracker:** in-memory `map[name]time.Time` (benched-until), mutex-guarded, exposed via `/metrics` (`router_backend_benched`, `router_backend_exhausted_total`) and the console. Resets on restart.
- **Classification of a failed rung:**
  - HTTP 429 / 402 / 403 / quota / rate-limit → bench the rung for the provider's reset window: per-minute (`Retry-After` / `retryDelay` / `X-RateLimit-Reset`, default 60s) or daily (until next midnight UTC, ≤24h). No retry — the rung is dead for the cooldown.
  - 5xx / timeout / connection error → bench for 60s (no retry; the router moves to the next candidate, per the no-retry policy rule).
- **Typed errors:** backends return an `ExhaustionError` (or `HTTPStatusError`) carrying status + `RetryAfter`, instead of a bare `fmt.Errorf`, so classification does not rely on string parsing. Error messages retain status/body for existing substring assertions.
- **Streaming:** no fallthrough after the first delta is emitted (a partial completion is not silently replaced); fallthrough only when no bytes were sent.
- **Sensitive requests:** bypass the ladder entirely — local only (fail-closed rule unchanged).
- **Non-sensitive requests:** the ladder is the primary decision — best available cloud first, then next best, then local. Fast/quality classification still drives the local fallbacks.
- **No early un-bench on success:** a benched rung stays benched until its cooldown elapses (predictable, no flapping).

**Consequences.** `Plan()` keeps building the full candidate chain (unchanged JSON contract); the execution loop skips benched rungs with a log line. Worst-case latency is `N × 10s` only on the first request after exhaustion; subsequent requests skip benched rungs instantly. Cooldowns are env-tunable (`EXHAUST_COOLDOWN_QUOTA`, `EXHAUST_COOLDOWN_TRANSIENT`) with the defaults above. `CLOUD_LADDER` contains secrets and is never logged. ADR-0007 (fail-closed) is preserved: sensitive data never leaves the box. Tests: tracker expiry/concurrency, failover table-driven, streaming no-fallthrough, sensitive bypass, ladder parsing, legacy fallback, env-var documentation policy. Implementation requires a worktree (shared files: `router/server.go`, `router/openai_backend.go`, `router/ollama.go`, `main.go`, `docs/API.md`).