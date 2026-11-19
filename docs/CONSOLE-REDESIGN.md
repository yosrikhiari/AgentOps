# Console redesign — backlog and status

Source: the 2026-11-19 redesign guide (shared shell + nine pages). This file is the
tracked plan; `docs/tower-design-system.html` is the rendering contract and
`docs/PLAN.md` records the wider project state.

**Rules every item must keep** (they are enforced, not preferences):

- colours are `--tower-*` tokens (no hex outside `:root`); components are `tower-*`
  classes defined in `tower.css`; DOM is built with `h()`, never `innerHTML`;
- the CSP forbids inline `style=""` attributes — style goes through CSSOM;
- every page renders skeleton / live / empty-with-next-action / error-with-Retry;
- no API, router or data-model change without an explicit decision (noted below);
- a change is done when it has been checked in a real browser, `node --check`,
  `go test ./...` and `go test ./policy/` are green, and CI is green.

Status: `[x]` shipped · `[~]` partially shipped · `[ ]` queued · `[?]` needs a decision.

## Wave 0 — shell and Cockpit (shipped)

- [x] 56 px icon rail, warm neutral palette, serif page titles; contrast fixes
      (`ink-faint` 2.57:1 → 5.0:1, error red 4.08:1 → 5.5:1).
- [x] In-app **Metrics** and **Models** pages (replaced the `/metrics` and
      `/v1/models` external links).
- [x] Cockpit: full-height rail | conversation | 340 px inspector, docked composer,
      thread title header, threads popover, message footers with a span chip,
      median-of-runs p50, one duration formatter, live spans collapsed.

## Wave 1 — shared shell, Evals, Overview (shipped, `09c0f36`)

- [x] Content fills the viewport (no 1480 px cap; 1864 px at 1920).
- [x] Tooltips on all nine rail buttons; top-bar metrics 12 px and muted.
- [x] One shared duration formatter (`1.35 s` / `828 ms` / `<1 ms`) used everywhere.
- [x] **Evals bug:** Judge column printed `[object HTMLSpanElement]` (a DOM node was
      concatenated into a string). Fixed.
- [x] Evals: unmeasured/unfinished runs no longer plotted as 0.0; panel reports
      “N unmeasured”; even axis ticks; threshold labelled; worst cases gained a reason
      column with misses first; judge changes marked in the Runs table.
- [x] Overview: removed the table that duplicated Models/Metrics (one link now);
      Backend health lists backends only; request rows open their trace; errors are a
      red chip.

## Wave 2 — Traces and GPU (shipped)

Traces:

- [x] The separate “Trace inspector” card is gone; the search box sits above the
      list it filters (Enter inspects).
- [x] Fixed-height master–detail: the list and the detail scroll independently.
- [x] Waterfall offsets bars by real start time, with a time ruler above.
- [x] Minimum bar width (2%) and the duration beside every bar.
- [x] Clicking a span opens its attributes; an error span opens automatically and
      turns red.

GPU:

- [x] Y-axis, units, time labels and a current-value dot on every chart.
- [x] VRAM: capacity drawn as a dashed line, area filled under the curve.
- [x] Temperature and Power history charts; the 85 °C throttle threshold is drawn
      and labelled on the temperature chart.
- [x] Range switch (1 h / 6 h / 24 h) using the existing `/v1/gpu?since=`.
- [x] Resident models sit beside the VRAM chart, with memory per model.

Verified in Chromium: no inspector card, input inside the master panel, list
scrolls, ruler present, minimum bar 12.4 px, four charts, three range buttons, the
1 h switch refetches, an error trace auto-expands with a red bar, zero console
errors.

## Wave 3 — Benchmarks and Workflows (queued)

Benchmarks:

- [ ] Two side-by-side model cards with bars; better value amber; winner badge.
- [ ] Verdict banner explaining “n < 100 → route on cost”.
- [ ] Scenario cards as grouped bars; low-sample scenarios flagged.
- [ ] Slower latency in red; model history collapsed at the bottom.

Workflows:

- [ ] Group repeated inputs under one row with a ×N chip.
- [ ] Compact state stepper (colored by state) naming the failed step.
- [ ] Status filter (All / Running / Failed / Done).
- [ ] Drop the empty Trajectory column; fold “clean” into the stepper.
- [ ] Resume only on failed rows, as the primary amber action; relative time with a
      full-date tooltip.

## Wave 4 — Metrics and Models (queued, largest)

- [ ] Metrics becomes the time-series page (requests/min, p50+p99, errors, tokens)
      with a range switch; remove what duplicates Overview/Models.
- [ ] KPI strip limited to four cards; “since start” moves into captions.
- [ ] Models: four registry KPIs; merge the three model tables into one; backend shown
      inside each row; “Route test…” action; cloud “never for sensitive” note.
- [ ] Prometheus text link moves to the page header.
- [?] The time-series charts need historical span aggregation. Either add a
      `since`/`step` parameter to an existing console endpoint (API addition) or build
      them from `/v1/requests` only. Decision required before this wave.

## Deferred or blocked on a decision

- [?] Overview range switch (30 req / 1 h / 24 h) — needs `since` on `/v1/requests`.
- [?] Light mode — console is dark-only by contract; tokens would allow it later.
- [?] Per-chat faithfulness — not stored anywhere; needs eval scores on the trace
      (data-model change).
- [?] Provider chip after reload — the conversations API stores `model` but not
      `backend`.
- [ ] Accessibility bundle from the audit (skip link, focus/title on route,
      `aria-live` scope, Level-A fixes) — independent of this redesign.

## Verification used for waves 0–1

Headless Chromium against the running app (Playwright), asserting layout boxes,
absence of the bug strings (`[object HTMLSpanElement]`, `point`), footer/chip
consistency, median vs sum, thread persistence across reload, and zero console
errors; screenshots kept outside the repo. `node --check`, 244 Go tests, 19 policies,
and the CI `go`/`policy`/`schema`/`docker` jobs must be green for every wave.
