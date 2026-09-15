# ADR-0007 — Backend abstraction and the fail-closed rule

**Status:** accepted (2026-09-15, v1.0 Track B) · **Context:** the router must serve OpenAI-style clients and may have a cloud fallback; sensitive data must never leave the machine.

**Decision.** One `Backend` interface (`Generate / Stream / Health`, plus `Local()`); implementations for Ollama and any OpenAI-compatible API. Routing builds the whole candidate chain once in `Plan()`: the classified tier, then the other local tier, then cloud — and cloud is simply absent when the request is sensitive (header, body flag or keyword). An explicit cloud model on sensitive data is refused (403). A health prober lets the router skip fallbacks it already knows are down.

**Consequences.** The rule cannot be bypassed by a later code path because it shapes the plan, not the call. Fallbacks are visible in the response (`fallback`) and in the `model.generate` span. Virtual API keys (hash stored, RPM window, token budget) sit in front; the limiter is per process and usage is charged asynchronously — both documented trade-offs.
