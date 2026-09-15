# pgvector

Vectors live in the same Postgres the tracker already uses, so there is no extra
container to operate and backups cover everything at once. The `chunks` table
stores the text, its source document id, a content hash, and an `embedding`
column of type `vector(768)` filled by the `nomic-embed-text` model through
Ollama's embed endpoint.

Similarity search uses cosine distance with pgvector's `<=>` operator and an HNSW
index built with `m=16` and `ef_construction=128`, which balances recall against
index build time for a small corpus. Search is vector-only; a hybrid mode with
Postgres full-text search and reciprocal rank fusion is a planned later step.

pgvector was picked over OpenSearch and Qdrant for solo use because the corpus is
tiny — fifteen documents and a few dozen chunks — where a two-node search cluster
would be pure overhead. The hybrid weights proven earlier (BM25 0.6, vector 0.4,
RRF k=60) stay portable if the store ever changes.
