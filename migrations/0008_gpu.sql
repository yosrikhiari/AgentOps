-- Track U: local GPU telemetry. One row per poll (~10 s by default), pruned by
-- GPU_HISTORY_KEEP. Memory is bytes; the reader converted nvidia-smi's MiB
-- once so every reader of this table agrees.
CREATE TABLE IF NOT EXISTS gpu_samples (
    id           BIGSERIAL PRIMARY KEY,
    ts           TIMESTAMPTZ NOT NULL,
    gpu_name     TEXT NOT NULL,
    util_pct     DOUBLE PRECISION NOT NULL,
    mem_used     BIGINT NOT NULL,
    mem_total    BIGINT NOT NULL,
    power_w      DOUBLE PRECISION,
    temp_c       DOUBLE PRECISION
);

CREATE INDEX IF NOT EXISTS gpu_samples_ts_idx ON gpu_samples (ts DESC);
