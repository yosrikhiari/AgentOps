# pgvector

Vectors live in the same Postgres the tracker already uses, so there is no
extra container to operate and backups cover everything at once. The `chunks`
table stores the text, its source document id, and an `embedding` column of
type `vector(768)` filled by the `nomic-embed-text` model through Ollama.

Similarity search uses cosine distance with an HNSW index built with `m=16`
and `ef_construction=128`, which balances recall against index build time for a
small corpus. Keyword search uses Postgres full-text search on the same rows,
and the two rankings are fused with reciprocal rank fusion.

pgvector was picked over OpenSearch and Qdrant for solo use because the corpus
is tiny — tens of documents and hundreds of chunks — where a two-node search
cluster would be pure overhead. The hybrid weights proven earlier (BM25 0.6,
vector 0.4, RRF k=60) stay portable if the store ever changes.
