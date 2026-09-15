# ADR-0004 — pgvector, not a separate vector store

**Status:** accepted (2026-09-13) · **Context:** corpus of 15 docs / ~25 chunks; Postgres already required for the tracker and evals.

**Decision.** `chunks.embedding vector(768)` (`nomic-embed-text` via Ollama), cosine `<=>`, HNSW `m=16, ef_construction=128`. Vector-only retrieval for now; the hybrid BM25 + RRF weights from earlier work stay parked.

**Consequences.** Zero extra containers, one backup. Ingest is a full sync (stale chunks pruned) so an edited doc cannot keep answering with old text.
