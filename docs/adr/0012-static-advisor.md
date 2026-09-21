# ADR-0012 — Static advisor (Track K)

**Status:** accepted (2026-09-21) · **Context:** Track L benchmarks models and
Track M routes on the evidence — but an operator should see the static picture
first: what each configured model costs in VRAM, whether the box can hold it,
and what the public claims about it, before spending GPU hours measuring.

**Decision.**

1. **Static table over live lookups.** `advisor/` holds size, quant, context
   note, license and VRAM estimates (docs/VRAM.md numbers) for the lineup;
   `go run . --advise` prints the table and exits. No runtime behaviour reads
   it — the advisor compares, the operator (or Track F's gate) promotes.
2. **Rumors carry provenance or do not ship.** Cached public scores need
   source + date + metric + value; `TestCachedRumorsCarryProvenance` fails the
   build otherwise. The table shipped empty: a same-day search found no
   Qwen2.5-3B/7B rank or score citable enough to freeze, and a fabricated
   number would poison every later comparison. The report says so out loud.
3. **Veto before measurement.** Anything over the 8 GB budget is flagged
   cannot-fit before it can enter Track L. Unknown names get "no static data",
   never a guessed verdict; unpulled models get "pull first" (Track J feeds K).

**Alternatives rejected.** *Scraping leaderboards at runtime* — network,
parsing, and rot on the operator's critical path for numbers that change
weekly. *VRAM auto-detection* — Ollama reports resident size, not fit for a
model under a context window; the static estimate plus the swap rule is the
honest answer on a one-GPU box.

**Consequences.** One flag (`--advise`, README row per policy), no env vars,
no endpoints, no console page. Track L consumes the veto list.
