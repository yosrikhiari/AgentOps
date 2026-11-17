# Tier 0 — closing the four gaps the November market pass found

*Written 2026-11-16, after the market addendum in `docs/MARKET.md` §9. Four items, each
demoable, each closing a gap named there. One commit per task; tests first; no new direct
dependency; the whole local gate stays green without Postgres, Ollama or a GPU.*

## Why these four, in this order

| # | Item | Gap it closes | Evidence |
|---|---|---|---|
| 1 | Signed releases + SBOM + provenance + `--version --json` | "what happens when the tool is compromised" — nobody can verify what they downloaded | LiteLLM shipped a credential stealer in 1.82.7/1.82.8 and again in 1.107.0 (Mar + Oct 2026); 2,100–2,500+ orgs exposed |
| 2 | Anthropic Messages API ingress (`POST /v1/messages`) | coding agents speak Anthropic; both Ollama and LM Studio now serve that API | Ollama added Anthropic compatibility (Jan 2026) and `ollama launch` for Claude Code / Codex / OpenCode; LM Studio 0.4.0 does the same |
| 3 | MCP 2026-07-28 (`server/discover`, stateless HTTP, headers) | the server declares `2024-11-05`; the current spec has no `initialize` and requires discovery | `mcp/server.go:18`; spec 2026-07-28; registry ~25k servers |
| 4 | GPU ↔ eval correlation | the telemetry exists but answers no question yet | `gpu_samples` + `Nearest` shipped this week; no competitor reports GPU state at all |

Order is deliberate: 1 is the cheapest and most defensible; 2 unlocks the largest user
group; 3 is compatibility plus a distribution channel; 4 turns this week's work into a
sentence no competitor can write.

---

## Task 1 — release trust pack

**Status: shipped in `b547c91`.** Implemented as `--version-json` (a separate flag) rather than
`--version --json`; `--version` prints `agentops <version> (<commit>) built <date>`.

**Files:** `main.go`, `main_test.go`, `.github/workflows/ci.yml`, `README.md`, `CHANGELOG.md`

**What exists:** the `release` job in `ci.yml` already builds linux/windows/darwin **amd64**,
writes `SHA256SUMS`, and uploads with `softprops/action-gh-release`. There is no signing,
no SBOM, no provenance, and `--version` does not exist.

**Steps**

1. **`--version` flag**, test first. `main_test.go` gains `TestBuildInfoJSON` (offline):
   the payload has a non-empty `version`, a `go` field starting `go`, and `module` equal to
   the module path. A second test asserts `commit` falls back to `debug.ReadBuildInfo`'s
   `vcs.revision` when the ldflag is empty, and that the ldflag wins when set.
2. Implement `buildInfo()`/`buildInfoJSON()` in `main.go`: `version` (existing ldflag),
   `commit` (`-X main.commit`, else `vcs.revision`), `date` (`-X main.buildDate`, else
   `vcs.time`), `go` (`runtime.Version()`), `module`, `vcs_modified` when known. `--version`
   prints one line of JSON and exits 0 before any store/DSN work.
3. **Release job upgrade**: build the 6 targets (linux/darwin/windows × amd64/arm64) with
   `-trimpath -buildvcs=true` and `-X main.version/-X main.commit/-X main.buildDate`;
   `SHA256SUMS`; `cosign sign-blob --yes --bundle dist/SHA256SUMS.sigstore.json`; SBOM via
   `anchore/sbom-action` (SPDX JSON into `dist/`); provenance via `actions/attest`
   (`subject-path: dist/agentops-*`). New actions pinned to full commit SHAs with the tag in
   a comment, exactly like the existing ones (fetched from the GitHub API, not invented):
   - `sigstore/cosign-installer@756fe2d284c7292c4e9b62c8a28f1e5629c59532 # v4.1.3`
   - `anchore/sbom-action@66cbf4bc1f1c0d2edc94016e65bc221b6bb0ad6c # v0.24.3`
   - `actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6 # v4.2.2`
   Job permissions: `contents: write`, `id-token: write`, `attestations: write`. Nothing is
   downloaded with `curl | sh`, so `TestPolicyAgentConfigClean` stays green.
4. **README**: one `--version` row in the flag table (`TestPolicyFlagsDocumented` requires it)
   and a "Verify a release" block with the three commands: `sha256sum -c SHA256SUMS`,
   `cosign verify-blob --bundle … --certificate-identity-regexp
   'https://github.com/yosrikhiari/AgentOps/.github/workflows/ci.yml@refs/tags/v'` and
   `gh attestation verify … --repo yosrikhiari/AgentOps`.
5. `CHANGELOG.md` line under `## Unreleased`.

**Acceptance:** `go run . --version --json`-style output is valid JSON and contains the
commit; `go test ./ -run 'BuildInfo'` green; `go test ./policy/` green; the workflow is
syntactically valid YAML (`python -c` or a YAML parser). **Not verifiable locally:** the
signing itself — it needs a tag and GitHub's OIDC issuer. Say so in the commit body; the
first tag is the real proof.

---

## Task 2 — Anthropic Messages API ingress

**Status: text ingress shipped in `045785d`.** Tools and non-text blocks are rejected with
`400 invalid_request_error` (see 2b). Tests: shape, shared routing path, streaming order,
rejections, `x-api-key`, error envelope. Live: 502 `api_error` with no backend, 400 for
`tools`, OpenAI endpoint unchanged.

### Task 2b — tool passthrough (the reason this is not finished)

Anthropic's agentic clients (Claude Code, the SDKs' tool loops) always send `tools` and then
`tool_use`/`tool_result` blocks. Accepting a tool request and answering with plain text makes
the client hang, so v1 rejects it. Making it real needs, in this order:

1. `ChatRequest.Tools` + `Message.ToolCalls`/`ToolCallID` (OpenAI shape internally).
2. The Ollama backend: `tools` in `/api/chat`, `message.tool_calls` in the response and in
   stream chunks; the OpenAI backend: `tools`, `tool_choice`, `delta.tool_calls`.
3. Anthropic translation both ways: `tools[].input_schema` ⇄ `function.parameters`,
   assistant `tool_use` blocks ⇄ `tool_calls`, user `tool_result` blocks ⇄ `role:"tool"`
   messages, and the streaming `content_block_start(type:tool_use)` +
   `input_json_delta` accumulation (partial JSON per block index).
4. Tests per mapping plus a recorded two-turn tool loop against a fake backend, then one
   live loop against Ollama on the demo box.

Estimate 3-4 solo days. Until then the Anthropic endpoint is honest: text works, tools are a
400, and `docs/API.md` says so.

**Files:** `router/` (new translation file + tests), `main.go`, `docs/API.md`, `README.md`,
`CHANGELOG.md`

**What exists:** `POST /v1/chat/completions` (OpenAI shape, SSE streaming) and
`GET /v1/models`. No `/v1/messages`. Backends are Ollama and any OpenAI-style endpoint.

**Design decisions**

- `POST /v1/messages` accepts the Anthropic request shape (`model`, `max_tokens`,
  `messages[{role,content}]`, `system`, `stream`, `temperature`, `stop_sequences`, `tools`
  best-effort) and translates into the router's internal `ChatRequest`, so routing, keys,
  budgets, fail-closed and spans are unchanged. One code path, two wire formats.
- Auth accepts `x-api-key` as well as `Authorization: Bearer` (Claude Code sends the former).
- Response: non-streaming Anthropic `message` object (`id`, `type:"message"`, `role`,
  `content:[{type:"text",text}]`, `model`, `stop_reason`, `usage{input_tokens,output_tokens}`);
  streaming emits `message_start`, `content_block_start`, `content_block_delta`,
  `content_block_stop`, `message_delta`, `message_stop` with `event:` lines.
- Errors keep the repo's one shape? **No** — Anthropic clients expect
  `{"type":"error","error":{"type":…,"message":…}}`. Document the divergence in
  `docs/API.md`; add a policy exception only if a policy test demands it (it does not —
  `TestPolicyOneErrorShape` targets the router's own endpoints; do not weaken it).
- Malformed/unsupported fields are rejected with 400, not silently dropped, except
  `metadata`/`system` which are accepted and ignored.

**Steps:** failing httptest cases first (non-streaming translation, streaming event order,
`x-api-key` auth, tool-call passthrough best-effort), then the translator, then wire the
route in `main.go` and the console's `Routes()` if it belongs there (it does not — the
router owns it). Live check with a real Claude Code-shaped request against a fake backend.

**Acceptance:** offline tests green; `curl -s localhost:8080/v1/messages -H 'x-api-key: …'`
returns an Anthropic-shaped body from a stub backend; streaming produces the six events in
order; `docs/API.md` documents both shapes and the error divergence.

---

## Task 3 — MCP 2026-07-28

**Status: shipped in `7c396fc`.** Dual-era: `server/discover`, per-request version in
`params._meta` / `MCP-Protocol-Version`, `-32022` + supported list, `initialize` echoes the
requested version. Tasks extension and `Mcp-Method`/`Mcp-Name` headers deliberately out of
scope. Registry publishing is a manual `mcp-publisher` step documented in `mcp/README.md`.

**Files:** `mcp/server.go`, `mcp/server_test.go`, `mcp/README.md`, `docs/API.md`,
`CHANGELOG.md`, optionally `docs/tower-design-system.html` (no UI change expected)

**What exists:** `protocolVersion = "2024-11-05"`, `initialize` handshake, stateless
`POST /mcp` one-request-per-call, five tools, stdio mode. No `server/discover`, no version
negotiation, no `MCP-*` headers.

**Steps**

1. Tests first: `server/discover` returns the protocol version + tools capability;
   a request carrying `MCP-Protocol-Version: 2026-07-28` is accepted without `initialize`;
   an unknown version still falls back to the 2024 handshake (old clients keep working);
   `Mcp-Method`/`Mcp-Name` headers are honoured when present and never trusted over the
   JSON body.
2. Implement: `supportedVersions = {"2026-07-28","2025-11-25","2025-06-18","2025-03-26","2024-11-05"}`,
   negotiate on request, answer `server/discover`, keep `initialize` for older versions.
3. Do **not** implement the Tasks extension yet: the five tools are fast reads. Note it in
   `mcp/README.md` as the path for long-running workflows.
4. Registry: add a `server.json`-shaped manifest note in `mcp/README.md` and a one-command
   publish recipe (manual step, not CI).

**Acceptance:** old and new clients both pass the tests; `curl -s -X POST localhost:8080/mcp
-d '{"jsonrpc":"2.0","id":1,"method":"server/discover"}'` answers; `go test ./mcp/` green.

---

## Task 4 — GPU ↔ eval correlation

**Status: done.** A (`038dc0b`) persisted the swap counter; the window landed in
`347ec9f`: `0010_eval_run_window.sql` adds `eval_runs.started_at` (backfilled to
`created_at`), `RecordRun` writes it, `evals.GateRun` carries the window,
`--release-gate` prints one GPU line per run ("no samples (prober off)" when there are
none), and `GET /v1/evals/runs` enriches every run with a `gpu` summary from one lateral
aggregate over its own window - rendered in `#/evals` as `GPU 41%` / `3 swaps` chips, or
`-` when nothing was measured. Verified in Chrome with a fixture payload.

**Remaining:** none for this task. `--score` and `--release-gate` were exercised by unit
tests and code review, not live: both need Ollama and a reachable Postgres, and the Docker
engine on this box keeps dropping its pipe.

**Files:** `console/store.go`, `console/handler.go`, `console/console_test.go`,
`console/static/app.js`, `main.go`, `docs/API.md`, `CHANGELOG.md`,
`docs/tower-design-system.html`

**What exists:** `gpu_samples` history, `gpu.SQLStore.Nearest`, a trace chip, and an evals
page that shows scores but not the card.

**Steps**

1. Store: `GPUSummary(ctx, from, to) (gpu.Summary, error)` — mean/peak utilisation, peak
   VRAM, sample count, swap count in the window (swaps are a counter, so derive from the
   prober's `swaps_last_10m` only if timestamps allow; otherwise report samples only and
   say swaps are per-poll). Nil queryer ⇒ zero summary, never an error.
2. `EvalRun` gains `gpu` (summary) when a window is available; the evals endpoint fills it
   with one query per run window, and the console renders one chip: `GPU 41% · 2.1/4.0 GB`.
3. `--release-gate` and `--score` print a warning line when the run's window contains model
   swaps: *"3 model swaps during this run — faithfulness may not be comparable"*. No gate
   failure (evidence, not a veto).
4. Design-system example updated with the chip (AGENTS.md requires it in the same change).

**Acceptance:** fake-store test for the summary mapping and the "no samples" case (chip
absent, never "0%"); a run with swaps gets the warning; `go test ./console/ ./` green;
`#/evals` renders in the browser with a fixture payload.

---

## Definition of done (all four)

- `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test -count=1 ./...`,
  staticcheck, `go test -count=1 ./policy/`, `node --check console/static/app.js` — green.
- One CHANGELOG line per item; `docs/API.md` rows for the new endpoint/fields; README rows
  for new flags.
- `docs/MARKET.md` §9 marks each item as shipped when it lands.
- `-race` is CI-only here (no C compiler) — never claim a local run.
