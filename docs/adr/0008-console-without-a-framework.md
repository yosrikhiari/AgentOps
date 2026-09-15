# ADR-0008 — Tower console: embedded static files, no framework

**Status:** accepted (2026-09-15, v1.0 Track C)

**Decision.** The web UI is three files (`index.html`, `tower.css` on the mockup's tokens, `app.js` ~350 lines) embedded with `embed.FS` and served at `/` by the same binary. Pages are hash routes; each owns an `AbortController`; 5xx get one retry; store failures render an inline error with Retry; polling refreshes data panels in place and never replaces a focused input. The overview is derived from spans (one source of truth), not a second metrics table.

**Consequences.** The product stays one `go build`; no Node toolchain, no bundle. Read-mostly by design — the only writes are *run eval* and *start/resume workflow*; key management stays on the CLI. Console endpoints are unauthenticated like `/metrics` (localhost tool; proxy it if exposed).
