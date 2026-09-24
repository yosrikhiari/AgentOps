# AgentOps — instructions for coding agents

Read this before changing anything. It is short on purpose; the detail lives in the documents it points to, and the rules are enforced by tests, not by hoping you read them.

## The rules

**`docs/RULES.md` is the rule book.** Every rule there says what, why, and how it is enforced. The enforcement is `policy/policy_test.go` — it runs in CI on every push and fails the build when a rule is broken. Run it locally before you finish:

```
go test -count=1 ./policy/
```

Full local gate (what CI runs): `gofmt -l . && go vet ./... && go test -race ./... && go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./... && go test -count=1 ./policy/`.

If you add a rule, follow `docs/RULES.md` §14: write the policy test first, watch it fail on a scratch breakage, then add the row.

## The console UI — use the Tower design system

Anything that renders in the console (`console/static/`) is built on **Tower**. Before adding or changing UI:

1. **Open `docs/tower-design-system.html`.** It links the shipped `console/static/tower.css`, so it shows exactly what runs: tokens, the shell, every primitive, the four feedback states, data display, both charts, the trace inspector, and the rules for extending. Each section has a spec table saying which class does what and where the console uses it.
2. **Reuse before inventing.** Panel, metric row, table, pill, chip, chip-button, input + form-row, empty (inline / tall), error box with Retry, skeleton, toast, note, key-value grid, waterfall, span tree already exist. If it is on the page, use it.
3. **Colours are `--tower-*` tokens**, declared once in `:root` of `tower.css`. No hex literal in `app.js`, `index.html`, or elsewhere in the stylesheet. A new colour is a new token with a name and a reason. (`TestPolicyConsoleColoursAreTokens`)
4. **A component is a `tower-*` class in `tower.css`**, in the matching `/* ===== */` block, defined before it is used. (`TestPolicyConsoleClassesExist`)
5. **State is a pill, a fact is a chip.** `pill(state, label)` for healthy / degraded / critical only; `chip(text)` for tier, backend, reason, model, kind. Never colour a chip. **One accent:** amber marks the thing to look at, nothing decorative.
6. **Every page renders four states** — skeleton, live, empty (with the next action), error (with Retry). Build with `h(tag, attrs, ...children)`; children are text or nodes. There is no `html:` attribute and no `innerHTML`.
7. **Add your example to `docs/tower-design-system.html`** in the same change, citing the function it is used in. The page is the contract.

The agreed backlog of UI/UX improvements, each with a live demo, is `docs/tower-enhancements.html` — if you are asked to improve the console, start there and cite the example number.

## Which tool reads what

This file is the only instruction file with content, and the only one in git besides the Copilot pointer. The other per-tool pointers below are local files, listed in `.gitignore`: create the one your tool needs. `TestPolicyAgentFilesPointHere` fails the build if a pointer that exists stops pointing here, or this file stops citing the rule book and the design system.

| Tool | Reads | Notes |
|---|---|---|
| OpenCode | `AGENTS.md` (native) · `opencode.json` → `instructions` | config lists it explicitly too, so a global OpenCode config cannot shadow it |
| Claude Code | `CLAUDE.md` → `@AGENTS.md` | import, not a copy |
| Codex CLI / Copilot coding agent | `AGENTS.md` (native) | |
| Cursor | `.cursor/rules/agents.mdc` (`alwaysApply`) | newer Cursor also reads `AGENTS.md` directly |
| GitHub Copilot (IDE chat) | `.github/copilot-instructions.md` | |
| Gemini CLI | `GEMINI.md` | |
| Anything else | point it at `AGENTS.md` | add a row here and a line to the policy test |

Local pointer contents: `CLAUDE.md` is `@AGENTS.md`; `GEMINI.md` says to read `AGENTS.md`; `opencode.json` is `{"instructions": ["AGENTS.md"]}`; `.cursor/rules/agents.mdc` is an `alwaysApply` rule pointing at `AGENTS.md`.

## Where things are

| Path | What |
|---|---|
| `main.go`, `config.go` | one binary, many modes (`--migrate`, `--mcp`, `--score`, …); config from env with defaults |
| `router/` | the OpenAI-shaped gateway: routing, backends, API keys, fail-closed, streaming |
| `tracker/` | durable workflows (Postgres rows, resume by id) and spans |
| `evals/` | corpus ingest, pgvector search, golden sets, judge, drift |
| `console/` | Tower web UI (`static/`) + its JSON endpoints |
| `mcp/` | JSON-RPC 2.0 over stdio, 5 tools |
| `policy/` | the executable rules |
| `docs/` | `RULES.md` · `API.md` (every endpoint and env var) · `adr/` · `VRAM.md` · `MARKET.md` · the two Tower pages |
| `lessons/` | plain-language HTML lessons on this repo (not analysed by the policies) |

## Things that bite

- Every env var you read must be documented in `docs/API.md` (`TestPolicyEnvVarsDocumented` scans `os.Getenv(` and `envOr(`); every CLI flag in `README.md`.
- Never put a credential-shaped literal anywhere — not in Go, compose, docs or lessons. SonarCloud analyses HTML and YAML too and fails the quality gate on a `user:password@` DSN. The local-dev DSN is assembled by `defaultDSN()` from `POSTGRES_*` parts for that reason.
- GitHub Actions in `ci.yml` are pinned to full commit SHAs with the release tag as a comment; Dependabot bumps them. Do not replace a SHA with a tag.
- Prompts never reach spans. Sensitive requests never reach a cloud backend. Both are policies; both have tests.
- `CHANGELOG.md` has an `## Unreleased` section; add a line for anything a user or operator would notice.

## Commit messages

The subject says what the change does for someone using or reading the code, in the imperative, under 72 characters: `Block a release when faithfulness drops below the gate`, not `Track T: eval release gates (flag + checklist + docs)`. Plan-track IDs, pass numbers and checklists go in the body. One logical change per commit.
