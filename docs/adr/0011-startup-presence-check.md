# ADR-0011 — Startup presence check (Track J)

**Status:** accepted (2026-09-21) · **Context:** the gateway advertised every
configured model as servable, but configured is not pulled. Two live findings
forced it: the 7B tier 502'd for weeks while the pull retried (§1 hold), and
the E2E sweep caught `C:\Users\…\.ollama` — Ollama's own `OLLAMA_MODELS`
(models *directory*) — advertised as a tier-`local` model, because our
extra-models variable shares the name.

**Decision.**

1. **Presence is probed, not assumed.** `OllamaClient.PulledModels` decodes
   `GET /api/tags` (full name plus `:latest`-stripped); the health prober
   refreshes the set every 15 s tick. Only positive knowledge of absence
   filters — unknown presence (no prober, backend down, cloud backend) never
   removes a ref, so offline tests and cloud fallbacks behave exactly as before.
2. **Missing reads as 503, not 502.** An explicit request for an unpulled model,
   or an auto-tier whose model is missing, returns `model_not_pulled` naming the
   model and the fix (`run: ollama pull X`); the 403 sensitive-cloud refusal
   still wins on explicit cloud. `/v1/models` shows `up:false` plus
   `reason:"not_pulled"`. Gaps log once per model at boot and on change.
3. **Paths are never models.** `OLLAMA_MODELS` entries containing `/` or `\`
   are skipped with a log line — the variable collision stays harmless even
   where Ollama's own env leaks in.

**Alternatives rejected.** *Probe at request time* — a tags round-trip on the
chat path breaks the overhead budget. *Fall back to the other tier when the
classified model is missing* — silently serves a complex prompt on the fast
model with the wrong reason; a loud 503 keeps the routing story honest.
*Renaming `OLLAMA_MODELS`* — churns the documented env for a case the skip
already defuses; revisit if a second collision appears.

**Consequences.** `router.Backend` untouched (presence is an optional
`PresenceReporter` interface); `Plan()` signature untouched (filtering lives in
`completeWithTrace`); `TestPresenceSkipsUnpulled` + policy row bind the fence.
