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

## Wave 3 — Benchmarks and Workflows (shipped in the working tree)

Benchmarks:

- [x] Two side-by-side model cards with bars; the better faithfulness, latency and
      token values are amber, the slowest latency is red, the winner carries a badge.
- [x] The “tied” KPI became a verdict banner that says why (“n < 100 → route on
      cost”) instead of leaving a conclusion in a metric tile.
- [x] Scenario cards (one per routing reason) with absolute 0–1 bars; a scenario with
      fewer than 10 pairs is labelled “too few” rather than given a winner.
- [x] Model-run history collapsed at the bottom.

Workflows:

- [x] Repeated inputs collapse under one row with a `×N` chip that expands the
      hidden runs (verified: 9 identical inputs → 3 visible rows, 11 after expand).
- [x] Compact stepper: one dot per step, green done / amber running / red failed /
      faint pending, with the attempts and retry count in the tooltip.
- [x] Status filter (All / Running / Failed / Done).
- [x] The empty Trajectory column is gone; “clean” is the stepper tooltip.
- [x] Resume appears only on failed rows, as the primary amber action; timestamps are
      relative with the full date in a tooltip.

Verified in Chromium: verdict banner, 2 model cards + 3 scenario cards, 3 amber and
1 red bar, history collapsed; 4 filters, a ×9 chip, 3 visible / 8 hidden rows, 2 rows
under the Failed filter, 11 after expanding, no console errors.

## Wave 4 — Metrics and Models (shipped)

**Decision (market + papers, 2026-11-19): aggregate spans on read; no rollup table,
no TSDB.** Monarch and Gorilla pre-aggregate because ingestion volume forces it
(2.2 TB/s, 12 M points/s); Tower writes ~3 spans per request. Dapper's sampling is a
high-throughput tool, not a need here. So the charts read the existing rows through
one bounded query. Measured on the local box: **p50 2.5 ms, p95 21 ms** for the 24 h
window (20 runs), against a recorded rollup trigger of 300 ms. The hand-check matched
exactly (9 requests in the hour from SQL = 9 from the API).

- [x] `GET /v1/metrics/series?window=1h|6h|24h`: buckets of 1 m / 5 m / 15 m with
      requests, errors, **p50/p99 (`percentile_cont`, never a mean)**, input/output
      tokens; closed window set, server-derived step, zeros for empty buckets.
- [x] Metrics page is the time-series page: requests, latency (p50 amber / p99 red),
      errors, tokens; 1 h / 6 h / 24 h switch; Prometheus text moved to the header;
      duplicated Overview/Models tables removed.
- [x] KPI strip cut to four; “since start” lives in the caption.
- [x] Models: four registry KPIs; one merged table (model, tier, `backend · local`,
      reach, last hour, requests, errors, tokens); Backends as a short health list;
      cloud “never for sensitive prompts” note.
- [x] “Route test…” sends one prompt through the router and shows the model, backend,
      reason and latency, surviving a poll re-render.

Verified in Chromium: 4 charts, 3 range buttons, 6 h switch refetches, no duplicate
tables, 4 KPI cards, 3 route-test chips, prompt/result survive a 5 s poll, zero
console errors.

## Accessibility (shipped, `audit/a11y`)

- [x] Skip link as the first tab stop; `.tower-skip` sits off-screen until it is
      focused and targets `<main id="content" tabindex="-1">`. The skip hash
      (`#content`) is ignored by the router once a page has rendered, so
      skipping does not rebuild the page or restart its poll.
- [x] A route change rewrites `document.title` (`Traces · Tower`), moves focus to
      `#page-title`, and leaves exactly one `aria-current="page"` on the rail.
      First paint and a re-route to the same page do not steal the caret.
- [x] One narrow live region (`#announcer`) replaces the `aria-live` on the
      content column, which re-read the whole page on every five-second poll.
      Filter chips announce their result count; the toast is `aria-atomic` and
      flips to `assertive` for failures.
- [x] Toggles state themselves: `aria-pressed` on the trace chips, the workflow
      status filter and both range switches; `aria-expanded` plus a spoken name
      on the `×N` group chip, whose visible text is a count, not a control name.
- [x] Field and icon fixes: the workflow question input has an `aria-label`, and
      all nine rail SVGs are `aria-hidden` behind the text that names them.
- [x] Documented with its demos in `docs/tower-design-system.html#a11y`, and
      proved by 32 headless-browser assertions with zero console errors.

## Deferred or blocked on a decision

- [?] Overview range switch (30 req / 1 h / 24 h) — needs `since` on `/v1/requests`.
- [?] Light mode — console is dark-only by contract; tokens would allow it later.
- [?] Per-chat faithfulness — not stored anywhere; needs eval scores on the trace
      (data-model change).
- [?] Provider chip after reload — the conversations API stores `model` but not
      `backend`.

## Verification used for waves 0–1

Headless Chromium against the running app (Playwright), asserting layout boxes,
absence of the bug strings (`[object HTMLSpanElement]`, `point`), footer/chip
consistency, median vs sum, thread persistence across reload, and zero console
errors; screenshots kept outside the repo. `node --check`, 244 Go tests, 19 policies,
and the CI `go`/`policy`/`schema`/`docker` jobs must be green for every wave.
