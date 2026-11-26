# Tower — operations guide

This is the operator's manual: backup, retention, TLS, upgrades and the
failure modes that recur. For the HTTP surface and every environment
variable see [`API.md`](API.md); for the rules the build enforces see
[`RULES.md`](RULES.md). Nothing here requires editing Go.

## What you are running

One binary (`tower`, built from this repo) plus a Postgres instance with
the `pgvector` extension. The binary serves the OpenAI-shaped gateway on
`ADDR` (default `127.0.0.1:8080`) and the console on the same port at
`/`. Ollama is expected on `OLLAMA_URL` (default
`http://localhost:11434`) and is probed at startup; a missing driver or
Ollama does not stop the gateway, it just reports `gpu_up 0` / no models.

The smallest sane deployment is the one in `docker-compose.yml`:

```
docker compose up -d postgres
docker compose run --rm tower --migrate   # once, or after every upgrade
docker compose --profile app up -d tower
```

`--migrate` applies the files in `migrations/` in name order and records
each one in `schema_migrations`, so it is safe to run repeatedly and safe
to run against a database that is already current.

## Backup

Back up the Postgres database, not the container. The only state that
cannot be rebuilt from the repo is in Postgres:

- `spans`, `route_decisions`, `audit_log` — traffic and policy history
- `conversations`, `messages` — stored chat threads (retention 90 days)
- `eval_runs`, `eval_pair_scores` — faithfulness history
- `workflows`, `steps`, `workflow_grades` — durable workflow state
- `api_keys` — issued keys (hashes; the plaintext was shown once at
  creation and is not recoverable)
- `gpu_samples` — GPU history (re-derivable, low value)

`pg_dump` against the running database is the supported backup:

```
pg_dump -h localhost -p 5432 -U tower -Fc tower > tower-$(date +%F).dump
```

Restore with `pg_restore -d tower tower-YYYY-MM-DD.dump`. If you run the
provided compose file, the data lives in the named volume `tower-pgdata`;
backing up the volume itself is fine, but a logical dump is portable
across Postgres versions and is what you want for an upgrade.

What you do **not** need to back up: the binary (rebuild from git), the
Ollama models (re-pull), `evals/corpus/cache/` (re-embed with
`--ingest`), or anything under `lessons/`.

## Retention

Retention is a sweep, not a promise, and it runs at boot and every 24
hours. Four independent windows, each set by an environment variable and
clamped to at least one hour so a typo cannot empty a table:

| Table | Variable | Default |
|---|---|---|
| `spans` | `SPAN_KEEP` | `720h` (30 days) |
| `route_decisions` | `DECISION_KEEP` | `2160h` (90 days) |
| `audit_log` | `AUDIT_KEEP` | `8760h` (365 days) |
| `gpu_samples` | `GPU_HISTORY_KEEP` | `168h` (7 days) |
| conversations | — | 90 days, hard-coded in the purge query |

Unparseable values fall back to the default. Conversations older than 90
days are purged at boot; a thread can be erased on demand with
`DELETE /v1/conversations/{id}`. A user who asks for their data to be
removed gets that endpoint, not a ticket.

The sweep logs one line per table at boot:
`retention (boot): dropped N row(s) from {audit_log,spans,route_decisions}`.

## TLS and exposure

Tower does not terminate TLS. It is a local appliance and binds to
loopback by default (`ADDR=127.0.0.1:8080`); publishing it is an
explicit act (`ADDR=0.0.0.0:8080`). The console is loopback-only unless
`CONSOLE_REMOTE=true`, because that is where prompts are readable and
GPU work is started. Put the whole thing behind a reverse proxy (nginx,
Caddy, Traefik) for TLS, and leave `CONSOLE_REMOTE` false unless the
proxy is the only path in.

The gateway and the console are unauthenticated unless
`REQUIRE_API_KEY=true`, in which case every request must carry
`Authorization: Bearer <key>`. Keys are issued with `--create-key` and
shown once. Do not expose `/metrics` or the console to the public
internet even with keys: they are not part of the authenticated surface.

## Upgrading

1. `git pull` (or rebuild the image).
2. `docker compose run --rm tower --migrate` — or `go run . --migrate`
   outside Docker. This is idempotent and safe to run on a live database;
   migrations are additive (`CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF
   NOT EXISTS`, `ADD CONSTRAINT` only when absent).
3. Restart the binary / container. The boot sweep runs on the new
   process.

Read `CHANGELOG.md` before upgrading across more than one release: the
`## Unreleased` section and the dated sections below it list operator-
visible changes (renamed env vars, retired models, schema changes). The
most common breakage is a retired model name — Groq retired
`llama-3.1-8b-instant` in 2026 and the default is now
`openai/gpt-oss-20b`; set `GROQ_JUDGE_MODEL` / `CLOUD_MODEL` to
something your account can reach (`GET /openai/v1/models`).

Do not run two versions of the binary against the same database at the
same time: the migration tracker is not locked, and a concurrent
`--migrate` from two processes can race on `schema_migrations`.

## Hosting behind a reverse proxy (Kong / Cloudflare / nginx)

When the gateway is hosted outside the premises, put it behind a reverse proxy and
keep these rules so streaming and long generations survive:

- **Do not buffer SSE.** The streaming endpoint sends `X-Accel-Buffering: no`; make sure the
  proxy honours it (nginx: `proxy_buffering off;` for the `/v1/chat/completions` location,
  or set `proxy_set_header X-Accel-Buffering no;`). Cloudflare passes it through by default.
- **Raise idle/read timeouts.** Cold loads and evictions can take 30–120 s; a 30 s proxy
  idle timeout will kill a generation mid-flight. Set `proxy_read_timeout` (nginx) or the
  equivalent to at least 300 s, matching `REQUEST_TIMEOUT`.
- **The server emits a 30 s SSE heartbeat** (`: ping`) during gaps, which resets most idle
  timers; do not strip comment lines in the proxy.
- **Enforce auth at the edge.** Set `REQUIRE_API_KEY=true` and issue virtual keys
  (`API_KEYS`), or terminate auth in Kong/Cloudflare and forward `X-API-Key`. The gateway
  already hashes keys and applies per-key RPM/token budgets.
- **Rate limit at the edge** in addition to the in-process limiter (which is per-process and
  resets on restart).
- **TLS termination** at the proxy; the gateway listens on plain HTTP by default (`ADDR`).

## Troubleshooting

**"backend ollama is reachable" but no models answer.** Check
`OLLAMA_MODELS` if a client names a model explicitly — only registered
models are accepted, and an unregistered name answers `unknown_model`.
`OLLAMA_URL` must be reachable from where the binary runs, not from your
laptop; inside Docker that is `http://host.docker.internal:11434`.

**GPU page is empty / `gpu_up 0`.** The prober shells out to
`GPU_SMI` (default `nvidia-smi`) and Ollama's `/api/ps`. On a box
without a working driver every `gpu_*` series reports `gpu_up 0` and no
utilisation is published — there is no fabricated zero. Set `GPU_SMI` to
the full path if it is not on `PATH` inside the container.

**Ollama model entry "looks like a path, not a model — skipping".**
`OLLAMA_MODELS` is comma-separated *model names*, not a directory. Fix
the value.

**A migration fails at boot of a new version.** Migrations are not run
at boot — run `--migrate` explicitly. If `--migrate` itself fails, read
the error: a `CHECK` constraint (migration 0017) or a `NOT NULL` can
fail on existing data that violates it. Fix the data or restore from a
dump, then re-run; do not edit the migration file to make it pass.

**The console is blank or returns 403 from a remote browser.**
`CONSOLE_REMOTE` defaults to false. Set it to `true` only when the
console is reached through a container port mapping or a reverse proxy,
and only together with a host-only port binding (see `docker-compose.yml`).

**Requests return 401.** `REQUIRE_API_KEY=true` is set and the request
has no `Authorization: Bearer <key>` header, or the key was revoked /
expired. Issue a new one with `--create-key`.

**A failed request shows no trace.** Traces are written asynchronously;
the cockpit re-reads after the writer settles. If the process died
mid-flush, the span is gone — check the audit log and the process logs.
Spans are also purged by `SPAN_KEEP`; a trace older than 30 days is
expected to be gone.

**Eval runs never finish.** `EVAL_TIMEOUT` (default `90m`) and GPU
contention: a 3.8 B judge on a 4 GB card leaves little room for the
model under test. Interrupted runs are kept as evidence with status
`failed` and are resumable; only `complete` runs feed drift and the
release gate. See `docs/VRAM.md`.

**Shutdown hangs.** In-flight requests drain up to `REQUEST_TIMEOUT`
(default 300 s). `docker stop -t 120` is the safe container stop; a
harder kill loses queued spans.

## Files an operator touches

| File | Purpose |
|---|---|
| `docker-compose.yml` | postgres + tower, ports, env |
| `.env` (optional) | values `docker-compose.yml` reads via `${VAR:-default}` |
| `migrations/` | schema; apply with `--migrate`, never hand-edit applied files |
| `evals/golden/*.jsonl` | golden sets for `--score` and drift |
| `docs/API.md` | full HTTP and env reference |
| `docs/RULES.md` | the rules the build enforces |
| `docs/VRAM.md` | GPU memory budget and model choices |
| `CHANGELOG.md` | operator-visible changes per release |
