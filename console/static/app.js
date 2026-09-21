/* Tower console — hand-written, no framework. One hash route per page; every page owns an
   AbortController so navigating away cancels its fetches; 5xx get one retry. */
(function () {
  'use strict';

  const content = document.getElementById('content');
  const toastEl = document.getElementById('toast');
  let pageCtl = null;       // AbortController of the current page
  let pollTimer = null;
  let railCleanup = null;   // live rail feed hookup; one rail mounted at a time

  // ---------- tiny DOM helper ----------
  function h(tag, attrs, ...children) {
    const el = document.createElement(tag);
    if (attrs) for (const [k, v] of Object.entries(attrs)) {
      if (k === 'class') el.className = v;
      else if (k === 'onclick') el.addEventListener('click', v);
      else if (v !== null && v !== undefined) el.setAttribute(k, v);
    }
    for (const c of children.flat()) {
      if (c === null || c === undefined) continue;
      el.append(c instanceof Node ? c : document.createTextNode(String(c)));
    }
    return el;
  }

  function toast(msg, isErr) {
    toastEl.textContent = msg;
    toastEl.className = 'tower-toast' + (isErr ? ' err' : '');
    toastEl.hidden = false;
    clearTimeout(toast.t);
    toast.t = setTimeout(() => { toastEl.hidden = true; }, 4000);
  }

  // ---------- fetch with abort + one retry on 5xx ----------
  async function api(path, opts, signal) {
    for (let attempt = 0; attempt < 2; attempt++) {
      const res = await fetch(path, Object.assign({ signal, headers: { 'Content-Type': 'application/json' } }, opts || {}));
      if (res.status >= 500 && attempt === 0) { await new Promise(r => setTimeout(r, 400)); continue; }
      let body = null;
      try { body = await res.json(); } catch (e) { body = null; }
      if (!res.ok) {
        const err = new Error((body && body.error && body.error.message) || ('HTTP ' + res.status));
        err.code = body && body.error && body.error.code; err.status = res.status; throw err;
      }
      if (body == null) {
        const err = new Error('empty response body');
        err.status = res.status; throw err;
      }
      return body;
    }
  }

  // ---------- formatting ----------
  const fmt = {
    ms: s => s == null ? '–' : (s < 1 ? Math.round(s * 1000) + ' ms' : s.toFixed(2) + ' s'),
    score: v => v == null ? '–' : Number(v).toFixed(3),
    time: iso => { const d = new Date(iso); return isNaN(d) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }); },
    date: iso => { const d = new Date(iso); return isNaN(d) ? '' : d.toLocaleString(); },
    short: id => (id || '').slice(0, 8),
  };

  function pill(state, label) { return h('span', { class: 'tower-pill ' + state }, label); }
  function chip(text) { return h('span', { class: 'tower-chip' }, text); }
  function errorBox(msg, retry) {
    return h('div', { class: 'tower-error' }, h('span', null, msg), retry ? h('button', { class: 'tower-btn secondary', onclick: retry }, 'Retry') : null);
  }
  function skeleton(rows) {
    return h('div', { style: 'display:flex;flex-direction:column;gap:10px' }, Array.from({ length: rows || 4 }, () => h('div', { class: 'tower-skeleton' })));
  }
  function metric(label, value, delta, deltaClass) {
    return h('div', { class: 'tower-metric' },
      h('div', { class: 'tower-metric-label' }, label),
      h('div', { class: 'tower-metric-value' }, value),
      delta ? h('div', { class: 'tower-metric-delta ' + (deltaClass || '') }, delta) : null);
  }
  function panel(title, sub, body, actions) {
    return h('section', { class: 'tower-panel' },
      h('div', { class: 'tower-panel-head' },
        h('div', null, h('h3', null, title), sub ? h('div', { class: 'tower-panel-sub' }, sub) : null),
        actions || null),
      body);
  }

  // ---------- status strip ----------
  function updateStrip(ov) {
    const dot = document.getElementById('strip-dot');
    const backends = ov.backends || [];
    const allUp = backends.length > 0 && backends.every(b => b.up);
    dot.style.background = allUp ? 'var(--tower-green)' : 'var(--tower-red)';
    dot.style.boxShadow = '0 0 0 3px ' + (allUp ? 'var(--tower-green-dim)' : 'var(--tower-red-dim)');
    document.getElementById('strip-router').textContent = allUp ? 'router live' : 'backend down';
    document.getElementById('strip-models').textContent = (ov.models || []).length;
    document.getElementById('strip-faith').textContent = ov.drift && ov.drift.runs ? fmt.score(ov.drift.score_now) : '–';
    document.getElementById('strip-p50').textContent = fmt.ms(ov.traffic && ov.traffic.p50_latency_s);
    document.getElementById('strip-version').textContent = ov.version ? 'agentops ' + ov.version : '';
    const sd = document.getElementById('side-dot');
    if (sd) { sd.style.background = dot.style.background; sd.style.boxShadow = dot.style.boxShadow; }
    const sr = document.getElementById('side-router'); if (sr) sr.textContent = allUp ? 'router live' : 'backend down';
    const sv = document.getElementById('side-version'); if (sv) sv.textContent = ov.version ? 'agentops ' + ov.version : '';
  }
  const pageTitles = { overview: 'Overview', traces: 'Traces', evals: 'Evals & drift', benchmarks: 'Benchmarks', workflows: 'Workflows', cockpit: 'Cockpit' };

  // ---------- traffic chart (hand-rolled SVG bars) ----------
  function trafficChart(reqs) {
    const n = 30, w = 560, hgt = 120;
    const list = reqs.slice(0, n).reverse();
    const maxLat = Math.max(0.5, ...list.map(r => r.latency_s || 0));
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', `0 0 ${w} ${hgt}`);
    svg.setAttribute('width', '100%');
    svg.style.display = 'block';
    const barW = w / n - 3;
    list.forEach((r, i) => {
      const height = r.error ? 12 : Math.max(8, (r.latency_s || 0.05) / maxLat * (hgt - 10));
      const rect = document.createElementNS('http://www.w3.org/2000/svg', 'rect');
      rect.setAttribute('x', i * (w / n) + 1); rect.setAttribute('y', hgt - height);
      rect.setAttribute('width', barW); rect.setAttribute('height', height); rect.setAttribute('rx', 2);
      rect.setAttribute('fill', r.error ? 'var(--tower-red)' : r.tier === 'quality' || r.tier === 'cloud' ? 'var(--tower-amber)' : 'var(--tower-ink-faint)');
      rect.setAttribute('class', 'tower-bar');
      const title = document.createElementNS('http://www.w3.org/2000/svg', 'title');
      title.textContent = `${fmt.time(r.at)} · ${r.model || '?'} · ${fmt.ms(r.latency_s)} · ${r.reason || ''}${r.error ? ' · ERROR' : ''}`;
      rect.append(title);
      rect.addEventListener('click', () => { location.hash = '#/traces/' + r.trace_id; });
      svg.append(rect);
    });
    return svg;
  }

  function requestsTable(reqs) {
    if (!reqs.length) return h('div', { class: 'tower-empty' }, 'No requests yet. Send one: curl localhost:8080/v1/chat/completions -d \'{"prompt":"hi"}\'');
    return h('table', { class: 'tower-table' },
      h('thead', null, h('tr', null, ...['time', 'trace', 'model', 'backend', 'reason', 'latency', 'tokens', ''].map(t => h('th', null, t)))),
      h('tbody', null, reqs.map(r => h('tr', null,
        h('td', { class: 'tower-mono' }, fmt.time(r.at)),
        h('td', null, h('a', { class: 'tower-link tower-mono', href: '#/traces/' + r.trace_id }, fmt.short(r.trace_id))),
        h('td', null, r.model || '–'),
        h('td', { class: 'tower-mono' }, r.backend || '–'),
        h('td', null, chip(r.reason || '–')),
        h('td', { class: 'tower-mono' }, fmt.ms(r.latency_s)),
        h('td', { class: 'tower-mono' }, r.tokens || 0),
        h('td', null,
          r.error ? pill('critical', 'error') : null,
          r.sensitive ? chip('sensitive') : null,
          r.fallback && r.fallback.length ? chip('fallback') : null,
          r.agent_role ? chip(r.agent_role) : null)))));
  }

  // ---------- per-model totals (the five Grafana panels, Tower-native) ----------
  // Grafana drew these from Prometheus (rate() over counters + histogram_quantile);
  // Tower draws the same information from spans: last-hour counts from
  // /v1/overview traffic.by_model, exact cumulative errors/tokens from the
  // since_start process counters, and p50/p99 from the last-30 request sample
  // (labelled as such — a sample percentile, not a histogram quantile).
  function pct50(sorted) { return sorted.length ? sorted[Math.round(0.5 * (sorted.length - 1))] : null; }
  function pct99(sorted) { return sorted.length ? sorted[Math.round(0.99 * (sorted.length - 1))] : null; }

  function modelsTable(ov, reqs) {
    const t = ov.traffic || {};
    const byHour = t.by_model || {};
    const since = ov.since_start || {};
    const names = Array.from(new Set([...Object.keys(byHour), ...Object.keys(since)])).sort();
    if (!names.length) return h('div', { class: 'tower-empty' }, 'No model traffic yet. Send one: curl localhost:8080/v1/chat/completions -d \'{"prompt":"hi"}\'');
    const latByModel = {};
    (reqs || []).forEach(r => {
      if (r.error || !(r.latency_s > 0)) return;
      (latByModel[r.model || '?'] = latByModel[r.model || '?'] || []).push(r.latency_s);
    });
    Object.values(latByModel).forEach(l => l.sort((a, b) => a - b));
    return h('table', { class: 'tower-table' },
      h('thead', null, h('tr', null, ...['model', 'last hour', 'since start', 'p50 · last 30', 'p99 · last 30', 'tokens', 'errors'].map(x => h('th', null, x)))),
      h('tbody', null, names.map(name => {
        const s = since[name] || { requests: 0, errors: 0, tokens: 0 };
        const denom = (s.requests || 0) + (s.errors || 0);
        const rate = denom ? (s.errors || 0) / denom : null;
        const lat = latByModel[name] || [];
        return h('tr', null,
          h('td', null, name),
          h('td', { class: 'tower-mono' }, byHour[name] || 0),
          h('td', { class: 'tower-mono' }, s.requests || 0),
          h('td', { class: 'tower-mono' }, lat.length ? fmt.ms(pct50(lat)) : '–'),
          h('td', { class: 'tower-mono' }, lat.length ? fmt.ms(pct99(lat)) : '–'),
          h('td', { class: 'tower-mono' }, s.tokens || 0),
          h('td', null,
            (s.errors || 0) === 0 ? pill('healthy', 'clean') :
              rate !== null && rate < 0.05 ? pill('degraded', (rate * 100).toFixed(1) + '%') :
                pill('critical', rate !== null ? (rate * 100).toFixed(1) + '%' : String(s.errors))));
      })));
  }

  // ---------- pages ----------
  async function pageOverview(signal, _sub, polling) {
    if (!polling) { content.replaceChildren(skeleton(6)); }
    let ov, reqs;
    try {
      [ov, reqs] = await Promise.all([api('/v1/overview', null, signal), api('/v1/requests?limit=30', null, signal)]);
    } catch (e) { if (e.name === 'AbortError') return; if (!polling) content.replaceChildren(errorBox('Could not load overview: ' + e.message, () => route())); return; }
    updateStrip(ov);
    const t = ov.traffic || {};
    const drift = ov.drift || {};
    const deltaTxt = drift.runs > 1 ? (drift.delta >= 0 ? '↑ ' : '↓ ') + Math.abs(drift.delta).toFixed(3) + ' vs previous run' : (drift.runs === 1 ? 'first run' : 'no runs yet');
    const metrics = h('div', { class: 'tower-metric-row' },
      metric('Requests / last hour', t.requests_last_hour || 0, (t.requests_last_minute || 0) + ' in the last minute'),
      metric('p50 latency', fmt.ms(t.p50_latency_s), 'p99 ' + fmt.ms(t.p99_latency_s)),
      metric('Faithfulness (' + (ov.golden_version || '–') + ')', drift.runs ? fmt.score(drift.score_now) : '–', deltaTxt, drift.runs > 1 ? (drift.delta >= 0 ? 'up' : 'down') : ''),
      metric('Judge cost today', '$' + Number(ov.judge_cost_usd || 0).toFixed(2), 'local judge + Groq free tier'));
    const legend = h('div', { class: 'tower-chart-legend' },
      h('span', { class: 'tower-legend-item' }, h('span', { class: 'tower-legend-swatch', style: 'background:var(--tower-amber)' }), 'quality / cloud'),
      h('span', { class: 'tower-legend-item' }, h('span', { class: 'tower-legend-swatch', style: 'background:var(--tower-ink-faint)' }), 'fast'),
      h('span', { class: 'tower-legend-item' }, h('span', { class: 'tower-legend-swatch', style: 'background:var(--tower-red)' }), 'error'));
    const traffic = panel('Routing traffic — last ' + Math.min(30, reqs.requests.length) + ' requests', 'Bar height = latency; click a bar to open its trace',
      reqs.requests.length ? trafficChart(reqs.requests) : h('div', { class: 'tower-empty' }, 'No traffic in the span store yet'), legend);
    const backends = (ov.backends || []);
    const health = panel('Backend health', 'Probed every 15 s',
      backends.length ? h('table', { class: 'tower-table' }, h('tbody', null,
        backends.map(b => h('tr', null,
          h('td', { class: 'tower-mono' }, b.name),
          h('td', null, chip(b.local ? 'local' : 'cloud')),
          h('td', null, b.up ? pill('healthy', 'healthy') : pill('critical', 'down')),
          h('td', { class: 'tower-mono', title: b.error || '' }, (b.error || '').slice(0, 40)))),
        (ov.models || []).map(m => h('tr', null,
          h('td', { class: 'tower-mono' }, m.model),
          h('td', null, chip(m.tier)),
          h('td', null, m.up === false ? pill('critical', 'backend down') : pill('healthy', m.backend)),
          h('td', null)))))
        : h('div', { class: 'tower-empty' }, 'No backends registered'));
    const typing = document.activeElement && content.contains(document.activeElement) && document.activeElement.tagName === 'INPUT';
    if (!typing) {
      content.replaceChildren(metrics, h('div', { class: 'tower-grid-2' }, traffic, health),
        panel('Models — requests, errors and tokens', 'Last hour from spans · since start from the process counters · p50/p99 from the last 30 requests', modelsTable(ov, reqs.requests || [])),
        panel('Recent requests', 'Newest first, from the span store', requestsTable(reqs.requests)));
    }
    pollTimer = setTimeout(() => { if (location.hash.startsWith('#/overview') || location.hash === '' || location.hash === '#/') pageOverview(signal, '', true); }, 5000);
  }

  // ---------- traces page: list + waterfall + span tree ----------
  const CURL = 'curl localhost:8080/v1/chat/completions -d ' + "'" + '{"prompt":"hi"}' + "'";
  function parseAttrs(s) { try { return JSON.parse(s || '{}'); } catch (e) { return {}; } }
  fmt.ago = iso => {
    const d = new Date(iso); if (isNaN(d)) return '';
    const s = Math.max(0, (Date.now() - d.getTime()) / 1000);
    if (s < 60) return Math.round(s) + ' s ago';
    if (s < 3600) return Math.round(s / 60) + ' min ago';
    if (s < 86400) return Math.round(s / 3600) + ' h ago';
    return Math.round(s / 86400) + ' d ago';
  };

  // Build the span tree (children under parents, unknown parents at the root) and a
  // timeline: offset from the first span, and a duration where the span carries one
  // (model.generate has latency_s; other spans get the gap to the next span).
  function buildTimeline(spans) {
    // Spans are emitted when their work completes, so started_at is really the end. A span
    // with latency_s starts latency before it; a span without one is a point in time.
    const sorted = spans.slice().sort((a, b) => new Date(a.started_at) - new Date(b.started_at));
    let rows = sorted.map(s => {
      const attrs = parseAttrs(s.attrs);
      const end = new Date(s.started_at).getTime();
      const dur = attrs.latency_s != null ? Number(attrs.latency_s) * 1000 : 0;
      return { span: s, attrs, end: isNaN(end) ? 0 : end, dur: isNaN(dur) ? 0 : Math.max(0, dur), depth: 0 };
    });
    const t0 = rows.length ? Math.min(...rows.map(r => r.end - r.dur)) : 0;
    rows.forEach(r => { r.start = Math.max(0, r.end - r.dur - t0); });
    const byId = {}; rows.forEach(r => byId[r.span.span_id] = r);
    rows.forEach(r => { let p = r.span.parent_id, d = 0, guard = 0; while (p && byId[p] && guard++ < 32) { d++; p = byId[p].span.parent_id; } r.depth = d; });
    // tree order: parents before children, siblings by completion time
    rows.sort((a, b) => a.depth - b.depth || a.end - b.end);
    const total = Math.max(1, ...rows.map(r => r.start + r.dur));
    return { rows, total };
  }

  function traceKind(spans) {
    const names = new Set(spans.map(s => s.name));
    if (names.has('route.decide')) return 'chat';
    if (names.has('researcher') || names.has('drafter') || names.has('reviewer')) return 'workflow';
    return 'trace';
  }

  function traceView(data) {
    const spans = data.spans || [];
    const { rows, total } = buildTimeline(spans);
    const kind = traceKind(spans);
    const decide = rows.find(r => r.span.name === 'route.decide');
    const gen = rows.find(r => r.span.name === 'model.generate');
    const failed = rows.some(r => r.attrs.error);
    const head = h('div', { class: 'tower-trace-head' },
      h('div', null,
        h('div', { class: 'tower-trace-title' }, h('span', { class: 'tower-mono' }, data.trace_id), ' ', chip(kind), failed ? pill('critical', 'failed') : pill('healthy', 'ok')),
        h('div', { class: 'tower-panel-sub' },
          spans.length + ' spans · ' + fmt.ms(total / 1000) + ' end to end',
          rows.length ? ' · started ' + fmt.date(rows[0].span.started_at) : '',
          decide ? ' · ' + (decide.attrs.reason || '') : '',
          decide && decide.attrs.sensitive ? ' · sensitive' : '')),
      h('div', { class: 'tower-form-row' },
        h('button', { class: 'tower-btn ghost', onclick: () => { navigator.clipboard && navigator.clipboard.writeText(data.trace_id); toast('trace id copied'); } }, 'Copy id'),
        h('a', { class: 'tower-btn ghost', href: '/v1/traces/' + encodeURIComponent(data.trace_id), target: '_blank' }, 'JSON')));

    // waterfall
    const wf = h('div', { class: 'tower-waterfall' }, ...rows.map(r => {
      const left = (r.start / total) * 100, width = Math.max(0.6, (r.dur / total) * 100);
      const color = r.attrs.error ? 'var(--tower-red)' : r.span.name === 'model.generate' ? 'var(--tower-amber)' : r.span.name.startsWith('route') || r.span.name.startsWith('router') ? 'var(--tower-ink-faint)' : 'var(--tower-green)';
      return h('div', { class: 'tower-wf-row' },
        h('div', { class: 'tower-wf-name', style: 'padding-left:' + (r.depth * 14) + 'px' }, r.span.name),
        h('div', { class: 'tower-wf-track' },
          h('div', { class: 'tower-wf-bar', style: `left:${left}%;width:${width}%;background:${color}`, title: fmt.ms(r.dur / 1000) })),
        h('div', { class: 'tower-wf-dur tower-mono' }, r.dur ? fmt.ms(r.dur / 1000) : '–'));
    }));

    // span tree with expandable attributes
    const steps = rows.map(r => {
      const s = r.span, attrs = r.attrs;
      const detail = h('div', { class: 'tower-trace-detail' },
        h('div', { class: 'tower-kv' }, ...Object.entries(attrs).flatMap(([k, v]) => [h('span', { class: 'k' }, k), h('span', { class: 'v' }, typeof v === 'object' ? JSON.stringify(v) : String(v))]),
          h('span', { class: 'k' }, 'started_at'), h('span', { class: 'v' }, fmt.date(s.started_at)),
          h('span', { class: 'k' }, 'span_id'), h('span', { class: 'v' }, s.span_id),
          h('span', { class: 'k' }, 'parent_id'), h('span', { class: 'v' }, s.parent_id || '(root)')));
      const body = h('div', { class: 'tower-trace-body', onclick: () => detail.classList.toggle('open') },
        h('div', { class: 'tower-trace-top' },
          h('span', { class: 'tower-trace-name' }, s.name),
          h('span', { class: 'tower-trace-meta' },
            r.dur ? h('span', { class: 'tower-mono' }, fmt.ms(r.dur / 1000)) : null,
            attrs.model ? h('span', { class: 'tower-mono' }, attrs.model) : null,
            attrs.backend ? chip(attrs.backend) : null,
            attrs.tier ? chip(attrs.tier) : null,
            attrs.fallback_from ? chip('fallback') : null,
            attrs.sensitive ? chip('sensitive') : null,
            attrs.agent_role ? chip(attrs.agent_role) : null,
            attrs.error ? pill('critical', 'failed') : null,
            h('span', null, '▾'))),
        detail);
      return h('div', { class: 'tower-trace-step ' + (attrs.error ? 'active' : 'done'), style: 'margin-left:' + (11 + r.depth * 14) + 'px' }, h('span', { class: 'tower-trace-dot' }), body);
    });
    return h('div', null, head, h('h4', { class: 'tower-sub-h' }, 'Timeline'), wf, h('h4', { class: 'tower-sub-h' }, 'Spans'), h('div', { class: 'tower-trace' }, ...steps),
      gen && gen.attrs.fallback_from ? h('div', { class: 'tower-note' }, 'Fallback: ' + gen.attrs.fallback_from.join(', ') + ' failed → served by ' + gen.attrs.model) : null);
  }

  async function pageTraces(signal, id) {
    let filter = 'all';
    const input = h('input', { class: 'tower-input', placeholder: 'trace id or workflow id', value: id || '' });
    const go = () => { const v = input.value.trim(); if (v) location.hash = '#/traces/' + v; };
    input.addEventListener('keydown', e => { if (e.key === 'Enter') go(); });
    const filters = h('div', { class: 'tower-filters' });
    const listBody = h('div', { class: 'tower-trace-list' }, skeleton(6));
    const detail = h('div', null);
    const listPanel = h('section', { class: 'tower-panel' },
      h('div', { class: 'tower-panel-head' }, h('div', null, h('h3', null, 'Recent traces'), h('div', { class: 'tower-panel-sub' }, 'Router chats and workflows, newest first')), null),
      filters, listBody);
    const detailPanel = h('section', { class: 'tower-panel' }, detail);
    content.replaceChildren(
      panel('Trace inspector', 'Every request is a tree of spans with trace_id / span_id / parent_id. Prompts are never stored.',
        h('div', { class: 'tower-form-row' }, input, h('button', { class: 'tower-btn primary', onclick: go }, 'Inspect'))),
      h('div', { class: 'tower-grid-traces' }, listPanel, detailPanel));

    let items = [];
    function renderFilters() {
      filters.replaceChildren(...[['all', 'All'], ['chat', 'Chats'], ['workflow', 'Workflows'], ['error', 'Errors'], ['sensitive', 'Sensitive'], ['fallback', 'Fallbacks']].map(([k, label]) =>
        h('button', { class: 'tower-chip tower-chip-btn' + (filter === k ? ' on' : ''), onclick: () => { filter = k; renderList(); renderFilters(); } }, label)));
    }
    function renderList() {
      const shown = items.filter(it => filter === 'all' || (filter === 'chat' && it.kind === 'chat') || (filter === 'workflow' && it.kind === 'workflow') || (filter === 'error' && it.error) || (filter === 'sensitive' && it.sensitive) || (filter === 'fallback' && it.fallback));
      if (!shown.length) { listBody.replaceChildren(h('div', { class: 'tower-empty' }, items.length ? 'Nothing matches this filter.' : 'No traces yet — send a request: curl localhost:8080/v1/chat/completions -d \'{"prompt":"hi"}\'')); return; }
      listBody.replaceChildren(...shown.map(it => h('div', { class: 'tower-trace-item' + (it.id === id ? ' selected' : ''), onclick: () => { location.hash = '#/traces/' + it.id; } },
        h('div', { class: 'tower-trace-item-top' },
          h('span', null, chip(it.kind), ' ', h('span', { class: 'tower-mono' }, fmt.short(it.id))),
          h('span', { class: 'tower-mono tower-muted' }, fmt.ago(it.at))),
        h('div', { class: 'tower-trace-item-sub' }, it.label),
        h('div', { class: 'tower-trace-item-meta' },
          it.latency != null ? h('span', { class: 'tower-mono' }, fmt.ms(it.latency)) : null,
          it.error ? pill('critical', 'error') : null,
          it.sensitive ? chip('sensitive') : null,
          it.fallback ? chip('fallback') : null,
          it.role ? chip(it.role) : null,
          it.status ? (it.status === 'done' ? pill('healthy', 'done') : pill('degraded', it.status)) : null))));
    }
    async function loadList() {
      try {
        const [reqs, wfs] = await Promise.all([api('/v1/requests?limit=50', null, signal), api('/v1/workflows?limit=20', null, signal)]);
        const a = (reqs.requests || []).map(r => ({ id: r.trace_id, kind: 'chat', at: r.at, label: (r.model || '–') + ' · ' + (r.reason || ''), latency: r.latency_s, error: !!r.error, sensitive: r.sensitive, fallback: !!(r.fallback && r.fallback.length), role: r.agent_role }));
        const b = (wfs.workflows || []).map(w => ({ id: w.id, kind: 'workflow', at: w.created_at, label: w.input, status: w.status, error: false }));
        items = a.concat(b).sort((x, y) => new Date(y.at) - new Date(x.at));
        renderList();
        if (!id && items.length) { id = items[0].id; history.replaceState(null, '', '#/traces/' + id); renderList(); loadDetail(true); }
      } catch (e) { if (e.name === 'AbortError') return; listBody.replaceChildren(errorBox('Could not load traces: ' + e.message, loadList)); }
    }
    async function loadDetail(auto) {
      if (!id) { detail.replaceChildren(h('div', { class: 'tower-empty tower-empty-tall' }, h('div', { class: 'tower-empty-title' }, 'No traces yet'), 'Send one request and it will appear here.',
        h('div', { class: 'tower-empty-actions' }, h('button', { class: 'tower-btn secondary', onclick: () => { navigator.clipboard && navigator.clipboard.writeText(CURL); toast('curl command copied'); } }, 'Copy a curl')))); return; }
      detail.replaceChildren(skeleton(5));
      try {
        const view = traceView(await api('/v1/traces/' + encodeURIComponent(id), null, signal));
        if (auto) view.prepend(h('div', { class: 'tower-panel-sub', style: 'margin-bottom:10px' }, 'Showing the latest trace — pick another on the left.'));
        detail.replaceChildren(view);
      }
      catch (e) { if (e.name === 'AbortError') return; detail.replaceChildren(errorBox(e.code === 'trace_not_found' ? 'No spans for ' + id + '.' : 'Could not load trace: ' + e.message, loadDetail)); }
    }
    renderFilters();
    loadList();
    loadDetail();
  }

  function scoreChart(runs) {
    const list = runs.slice().reverse();
    const w = 560, hgt = 140, pad = 24;
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', `0 0 ${w} ${hgt}`); svg.setAttribute('width', '100%'); svg.style.display = 'block';
    const x = i => pad + (list.length === 1 ? (w - 2 * pad) / 2 : i * (w - 2 * pad) / (list.length - 1));
    const y = v => hgt - pad - v * (hgt - 2 * pad);
    const grid = document.createElementNS('http://www.w3.org/2000/svg', 'g');
    [0, 0.5, 0.7, 1].forEach(v => {
      const l = document.createElementNS('http://www.w3.org/2000/svg', 'line');
      l.setAttribute('x1', pad); l.setAttribute('x2', w - pad); l.setAttribute('y1', y(v)); l.setAttribute('y2', y(v));
      l.setAttribute('stroke', v === 0.7 ? 'var(--tower-amber-dim)' : 'var(--tower-border-soft)'); l.setAttribute('stroke-dasharray', v === 0.7 ? '4 3' : '');
      grid.append(l);
      const t = document.createElementNS('http://www.w3.org/2000/svg', 'text');
      t.setAttribute('x', 2); t.setAttribute('y', y(v) + 4); t.setAttribute('fill', 'var(--tower-ink-faint)'); t.setAttribute('font-size', '10'); t.textContent = v.toFixed(1);
      grid.append(t);
    });
    svg.append(grid);
    if (list.length > 1) {
      const p = document.createElementNS('http://www.w3.org/2000/svg', 'polyline');
      p.setAttribute('points', list.map((r, i) => `${x(i)},${y(r.score)}`).join(' '));
      p.setAttribute('fill', 'none'); p.setAttribute('stroke', 'var(--tower-amber)'); p.setAttribute('stroke-width', '2');
      svg.append(p);
    }
    list.forEach((r, i) => {
      const c = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
      c.setAttribute('cx', x(i)); c.setAttribute('cy', y(r.score)); c.setAttribute('r', 4);
      c.setAttribute('fill', r.score >= 0.7 ? 'var(--tower-green)' : 'var(--tower-red)');
      const t = document.createElementNS('http://www.w3.org/2000/svg', 'title');
      t.textContent = `run ${r.id} · ${fmt.date(r.created_at)} · ${r.judge_model} · ${fmt.score(r.score)}`;
      c.append(t); svg.append(c);
    });
    return svg;
  }

  async function pageEvals(signal) {
    content.replaceChildren(skeleton(6));
    let data, status;
    try { [data, status] = await Promise.all([api('/v1/evals/runs?limit=50', null, signal), api('/v1/evals/status', null, signal)]); }
    catch (e) { if (e.name === 'AbortError') return; content.replaceChildren(errorBox('Could not load evals: ' + e.message, () => route())); return; }
    const runs = data.runs || [], drift = data.drift || {};
    const runBtn = h('button', { class: 'tower-btn primary' }, status.running ? 'Running…' : 'Run eval suite');
    runBtn.disabled = !!status.running;
    runBtn.addEventListener('click', async () => {
      runBtn.disabled = true; runBtn.textContent = 'Starting…';
      try { await api('/v1/evals/run', { method: 'POST', body: JSON.stringify({ golden_version: data.golden_version }) }); toast('Eval suite started on ' + data.golden_version); }
      catch (e) { toast(e.message, true); }
      pageEvals(signal);
    });
    const metrics = h('div', { class: 'tower-metric-row' },
      metric('Latest faithfulness', drift.runs ? fmt.score(drift.score_now) : '–', drift.alert ? 'ALERT: below ' + drift.threshold : 'threshold ' + (drift.threshold ?? 0.7), drift.alert ? 'down' : 'up'),
      metric('Delta vs previous', drift.runs > 1 ? (drift.delta >= 0 ? '+' : '') + drift.delta.toFixed(3) : '–', drift.judge_changed ? 'judge changed — not corpus drift' : (drift.judge_now || '')),
      metric('Runs (' + data.golden_version + ')', runs.length, runs.length ? 'last ' + fmt.date(runs[0].created_at) : ''),
      metric('Suite status', status.running ? 'running' : (status.error ? 'failed' : 'idle'), status.error || (status.finished_at && status.finished_at !== '0001-01-01T00:00:00Z' ? 'finished ' + fmt.date(status.finished_at) : ''), status.error ? 'down' : ''));
    const history = panel('Score history', 'Every run of golden ' + data.golden_version + '; dashed line = alert threshold',
      runs.length ? scoreChart(runs) : h('div', { class: 'tower-empty' }, 'No runs yet — start one.'), runBtn);
    const worst = drift.worst_cases || [];
    const misses = drift.misses_now || 0;
    const worstPanel = panel('Worst cases', 'Lowest faithfulness in the latest run' + (misses ? ' · ' + misses + ' retrieval miss(es) excluded from the average' : ''),
      worst.length ? h('table', { class: 'tower-table' }, h('thead', null, h('tr', null, h('th', null, 'question'), h('th', null, 'faith'), h('th', null, 'P@5'), h('th', null, 'R@5'))),
        h('tbody', null, worst.map(wc => h('tr', null,
          h('td', null, wc.question, wc.retrieval_miss ? ' ' : null, wc.retrieval_miss ? chip('retrieval miss') : null),
          h('td', { class: 'tower-mono' }, wc.retrieval_miss || wc.faithfulness == null ? '–' : fmt.score(wc.faithfulness)),
          h('td', { class: 'tower-mono' }, fmt.score(wc.precision)),
          h('td', { class: 'tower-mono' }, fmt.score(wc.recall))))))
        : h('div', { class: 'tower-empty' }, 'Nothing scored yet'));
    const table = panel('Runs', null, h('table', { class: 'tower-table' },
      h('thead', null, h('tr', null, ...['run', 'when', 'judge', 'score'].map(t => h('th', null, t)))),
      h('tbody', null, runs.map(r => h('tr', null, h('td', { class: 'tower-mono' }, r.id), h('td', { class: 'tower-mono' }, fmt.date(r.created_at)), h('td', null, chip(r.judge_model)), h('td', null, r.score >= (drift.threshold ?? 0.7) ? pill('healthy', fmt.score(r.score)) : pill('critical', fmt.score(r.score))))))));
    content.replaceChildren(metrics, h('div', { class: 'tower-grid-2' }, history, worstPanel), table);
    if (status.running) pollTimer = setTimeout(() => { if (location.hash.startsWith('#/evals')) pageEvals(signal); }, 5000);
  }

  async function pageBenchmarks(signal) {
    content.replaceChildren(skeleton(6));
    let data;
    try { data = await api('/v1/benchmarks', null, signal); }
    catch (e) { if (e.name === 'AbortError') return; content.replaceChildren(errorBox('Could not load benchmarks: ' + e.message, () => route())); return; }
    const c = data.comparison || {};
    const verdict = c.verdict || {};
    const models = c.models || [], scenarios = c.scenarios || [], history = data.history || [];
    const metrics = h('div', { class: 'tower-metric-row' },
      metric('Verdict (' + (data.golden_version || '–') + ')', verdict.winner || 'tied', verdict.reason || c.note || ''),
      metric('Judge', c.judge || '–', c.comparable ? 'same judge across compared runs' : 'not comparable'),
      metric('Models compared', models.length, models.length ? models.map(m => m.model).join(' · ') : ''),
      metric('Scenarios', scenarios.length, 'split by routing reason'));
    const matrix = panel('Models — faithfulness, latency, tokens', 'Latest scored run per model; misses excluded from every average',
      models.length ? h('table', { class: 'tower-table' },
        h('thead', null, h('tr', null, ...['model', 'faith', 'scored', 'misses', 'p50 latency', 'tokens/answer'].map(t => h('th', null, t)))),
        h('tbody', null, models.map(m => h('tr', null,
          h('td', null, m.model === verdict.winner ? chip(m.model) : m.model),
          h('td', { class: 'tower-mono' }, fmt.score(m.faithfulness)),
          h('td', { class: 'tower-mono' }, m.scored),
          h('td', { class: 'tower-mono' }, m.misses),
          h('td', { class: 'tower-mono' }, fmt.ms(m.p50_latency_s)),
          h('td', { class: 'tower-mono' }, Math.round(m.tokens_per_answer))))))
        : h('div', { class: 'tower-empty' }, 'No scored model runs yet. Run one per model: go run . --score --score-model <name> (same golden version, same judge).'));
    const scenPanels = scenarios.map(sc => {
      const rows = sc.models.map(m => h('tr', null,
        h('td', null, m.model === sc.winner ? chip(m.model) : m.model),
        h('td', { class: 'tower-mono' }, fmt.score(m.faithfulness)),
        h('td', { class: 'tower-mono' }, m.n)));
      const tbl = h('table', { class: 'tower-table' },
        h('thead', null, h('tr', null, ...['model', 'faith', 'n'].map(t => h('th', null, t)))),
        h('tbody', null, rows));
      return panel('Scenario: ' + sc.scenario, sc.winner ? 'winner ' + sc.winner + ' — ' + sc.reason : sc.reason, tbl);
    });
    const hist = panel('Model runs', 'Latest first; full history with golden-answer runs on the Evals page',
      history.length ? h('table', { class: 'tower-table' },
        h('thead', null, h('tr', null, ...['run', 'model', 'judge', 'score', 'when'].map(t => h('th', null, t)))),
        h('tbody', null, history.map(r => h('tr', null,
          h('td', { class: 'tower-mono' }, r.id),
          h('td', null, chip(r.model)),
          h('td', null, chip(r.judge)),
          h('td', { class: 'tower-mono' }, fmt.score(r.score)),
          h('td', { class: 'tower-mono' }, fmt.date(r.created_at))))))
        : h('div', { class: 'tower-empty' }, 'No model runs recorded yet'));
    content.replaceChildren(metrics, matrix, ...scenPanels, hist);
  }

  function stepPill(st) {
    const state = st.status === 'done' ? 'healthy' : st.status === 'running' ? 'degraded' : 'critical';
    return h('span', { class: 'tower-pill ' + state, title: st.output_snippet || '' }, st.name + (st.attempts > 1 ? ' ×' + st.attempts : ''));
  }

  async function pageWorkflows(signal) {
    const input = h('input', { class: 'tower-input', placeholder: 'question for the Researcher → Drafter → Reviewer agent', value: 'what does agentops do?' });
    const startBtn = h('button', { class: 'tower-btn primary' }, 'Start workflow');
    const listBody = h('div', null, skeleton(5));
    content.replaceChildren(
      panel('Durable workflows', 'Each step is a Postgres row; kill the process mid-step and resume the same id — done steps are never re-run.',
        h('div', { class: 'tower-form-row' }, input, startBtn)),
      panel('Workflows', 'Newest first, refreshes while something is running', listBody));

    async function refresh() {
      let data;
      try { data = await api('/v1/workflows?limit=20', null, signal); }
      catch (e) { if (e.name === 'AbortError') return; listBody.replaceChildren(errorBox('Could not load workflows: ' + e.message, refresh)); return; }
      const wfs = data.workflows || [];
      const rows = wfs.map(w => {
        const resume = h('button', { class: 'tower-btn secondary' }, 'Resume');
        resume.addEventListener('click', async () => {
          resume.disabled = true;
          try { await api('/v1/workflows/' + w.id + '/resume', { method: 'POST' }); toast('Resuming ' + w.id); } catch (e) { toast(e.message, true); }
          setTimeout(refresh, 800);
        });
        return h('tr', null,
          h('td', { class: 'tower-mono' }, fmt.date(w.created_at)),
          h('td', null, h('a', { class: 'tower-link tower-mono', href: '#/traces/' + w.id }, w.id)),
          h('td', null, w.input.length > 60 ? w.input.slice(0, 60) + '…' : w.input),
          h('td', null, h('div', { class: 'tower-steps' }, w.steps.map(stepPill))),
          h('td', null, w.status === 'done' ? pill('healthy', 'done') : pill('degraded', w.status)),
          h('td', null, w.status !== 'done' ? resume : null));
      });
      listBody.replaceChildren(wfs.length ? h('table', { class: 'tower-table' },
        h('thead', null, h('tr', null, ...['when', 'id', 'input', 'steps', 'status', ''].map(t => h('th', null, t)))),
        h('tbody', null, rows)) : h('div', { class: 'tower-empty' }, 'No workflows yet'));
      clearTimeout(pollTimer);
      if (wfs.some(w => w.status === 'running')) pollTimer = setTimeout(() => { if (location.hash.startsWith('#/workflows')) refresh(); }, 4000);
    }
    startBtn.addEventListener('click', async () => {
      startBtn.disabled = true;
      try { const r = await api('/v1/workflows', { method: 'POST', body: JSON.stringify({ input: input.value }) }); toast('Workflow ' + r.id + ' started'); }
      catch (e) { toast(e.message, true); }
      startBtn.disabled = false;
      setTimeout(refresh, 800);   // EnsureWorkflow runs in the background; give it a beat
    });
    refresh();
  }

  // ---------- cockpit (try+live merged; single surface) ----------

  // Tickets group a trace's spans into quest stages: healthy = stage landed,
  // degraded = next stage running, chip = stage still ahead. Error spans flag
  // the ticket critical. Newest ticket flashes once (amber, motion-safe).
  const liveStages = {
    chat: ['route.decide', 'model.generate', 'router.respond'],
    workflow: ['researcher', 'drafter', 'reviewer'],
    mcp: ['mcp.tool'],
  };
  function liveKind(names) {
    if (names.includes('model.generate')) return 'chat';
    if (names.includes('researcher') || names.includes('drafter') || names.includes('reviewer')) return 'workflow';
    return 'mcp';
  }
  // Shared live rail: subscribes to /v1/events, groups spans into tickets.
  // getPin() returns the trace_id to highlight as yours (cockpit) or null.
  // onFatal renders the error UI. Returns a cleanup that closes the feed.
  // Rail nodes only — the input subtree is never touched (focus-safe rule).
  function mountRail(listBody, setCounts, getPin, onFatal) {
    const seen = new Map(); // trace_id -> {order:[], info:{stage:{lat,err}}, model, at, pop}
    let lastTrace = null;
    function ticketEl(id, t) {
      const kind = liveKind(t.order);
      const stages = liveStages[kind] || [];
      const next = stages.find(s => !t.info[s]);
      const steps = stages.map(s => {
        const st = t.info[s];
        if (st) {
          const el = pill(st.err ? 'critical' : 'healthy', s);
          if (t.pop && s === stages[stages.length - 1]) el.classList.add('tower-live-pop');
          return el;
        }
        return s === next ? pill('degraded', s + ' …') : chip(s);
      });
      for (const n of t.order) if (!stages.includes(n)) steps.push(chip(n));
      t.pop = false; // pop plays once, on the render that completes the trace
      const maxLat = Math.max(0.01, ...stages.map(s => (t.info[s] && t.info[s].lat) || 0));
      const rows = stages.map(s => {
        const st = t.info[s];
        const bar = h('div', { class: 'tower-live-bar' + (st ? (st.err ? ' bad' : ' done') : (s === next ? ' run' : '')) });
        if (st) bar.style.width = Math.max(3, ((st.lat || 0.005) / maxLat) * 100) + '%';
        else if (s === next) bar.style.width = '30%';
        return h('div', { class: 'tower-live-row' }, h('span', null, s),
          h('div', { class: 'tower-live-track' }, bar),
          h('span', { class: 'tower-mono' }, st ? (st.lat != null ? fmt.ms(st.lat) : '–') : '…'));
      });
      const failed = Object.values(t.info).some(s => s.err);
      const pinned = getPin && getPin() === id;
      const head = [chip(kind), ' ',
        h('a', { class: 'tower-link tower-mono', href: '#/traces/' + id }, fmt.short(id))];
      if (t.model) head.push(' ', chip(t.model));
      if (pinned) head.push(' ', chip('yours'));
      if (failed) head.push(' ', pill('critical', 'error'));
      return h('div', { class: 'tower-ticket' + (id === lastTrace ? ' flash' : '') + (pinned ? ' tower-live-yours' : '') },
        h('div', { class: 'tower-ticket-top' }, h('span', null, ...head),
          h('span', { class: 'tower-mono tower-muted' }, fmt.time(t.at))),
        h('div', { class: 'tower-steps' }, ...steps),
        ...rows);
    }
    function render(emptyMsg) {
      const ids = [...seen.keys()].reverse().slice(0, 8);
      let spans = 0; seen.forEach(t => { spans += t.order.length; });
      setCounts(seen.size, spans);
      listBody.replaceChildren(...(ids.length ? ids.map(id => ticketEl(id, seen.get(id)))
        : [h('div', { class: 'tower-empty' }, emptyMsg || 'No spans yet — send a chat, run a workflow, or call an MCP tool.')]));
    }
    render();
    let src;
    try {
      src = new EventSource('/v1/events');
    } catch (e) { onFatal('Live feed unavailable: ' + e.message); return () => {}; }
    src.onmessage = ev => {
      try {
        const d = JSON.parse(ev.data);
        const a = (d.attrs && typeof d.attrs === 'object') ? d.attrs : {};
        let t = seen.get(d.trace_id);
        if (!t) { t = { order: [], info: {}, at: new Date().toISOString() }; seen.set(d.trace_id, t); }
        if (!t.info[d.name]) { t.info[d.name] = {}; t.order.push(d.name); }
        const st = t.info[d.name];
        if (typeof a.latency_s === 'number') st.lat = a.latency_s;
        if (a.error) st.err = true;
        if (typeof a.model === 'string' && a.model) t.model = a.model;
        t.at = new Date().toISOString();
        const stages = liveStages[liveKind(t.order)] || [];
        if (stages.length && stages.every(s => t.info[s])) t.pop = true;
        lastTrace = d.trace_id;
        render();
      } catch (e) { /* one bad frame never kills the rail */ }
    };
    src.onerror = () => { try { src.close(); } catch (e2) {} onFatal('Live feed dropped.'); };
    return () => { try { src.close(); } catch (e) {} };
  }
  // Cockpit: send + sight on one surface. History is client-held messages[]
  // capped by tokens (ADR-0010); the gateway stays stateless. Own sends pin
  // amber via their trace_id. "Run as workflow" posts the last input to the
  // existing toy runner — no backend change.
  const coHistBudget = 4000;
  function coEstTokens(s) { return Math.ceil(String(s).length / 4); }
  function coHistTokens(ms) { return ms.reduce((n, m) => n + coEstTokens(m.content), 0); }
  let coHist = [];        // [{role, content}] oldest-first
  let coDropped = 0;      // messages trimmed for the token budget
  let coPromise = null;
  let coPin = null;       // trace_id of the latest own send
  let coConvId = null;    // active stored thread, or null for a fresh one
  let coLastInput = '';
  let coLastAnswer = null;
  let coLastErr = '';
  async function pageCockpit(signal) {
    const input = h('input', { class: 'tower-input', placeholder: 'message the router — history resends each turn' });
    const sendBtn = h('button', { class: 'tower-btn primary' }, 'Send');
    const wfBtn = h('button', { class: 'tower-btn secondary' }, 'Run as workflow');
    const histNote = h('div', { class: 'tower-mono tower-muted', style: 'font-size:11px' }, '');
    const transcript = h('div', null);
    const answer = h('div', null, h('div', { class: 'tower-empty' }, 'Send a message to start a conversation.'));
    const dot = h('span', { class: 'tower-live-dot' });
    const label = h('span', null, '');
    const counts = h('div', { class: 'tower-mono tower-muted', style: 'margin-bottom:10px;display:flex;gap:8px;align-items:center' }, dot, label);
    const listBody = h('div', null, skeleton(5));
    const newBtn = h('button', { class: 'tower-btn secondary' }, '+ New thread');
    const convList = h('div', { class: 'tower-conv' }, skeleton(2));
    const side = h('div', { style: 'position:sticky;top:0;align-self:start' },
      panel('Threads', 'Stored server-side · 90-day retention.',
        h('div', null, newBtn, h('div', { style: 'margin-top:8px' }, convList))));
    const left = h('div', { style: 'position:sticky;top:0;align-self:start;display:flex;flex-direction:column;gap:16px' },
      panel('Send', 'Client-held history resends every turn; the gateway stays stateless.',
        h('div', null,
          h('div', { class: 'tower-form-row tower-live-sendcol' }, input,
            h('div', { class: 'tower-form-row' }, sendBtn, wfBtn)),
          histNote)),
      panel('Answer', null, h('div', null, transcript, answer)));
    content.replaceChildren(h('div', { class: 'tower-grid-cockpit' }, side, left,
      panel('Rail', 'Your ticket pins amber; everything lands live.',
        h('div', null, counts, listBody))));
    function renderTranscript() {
      transcript.replaceChildren(...coHist.slice(-6).map(m =>
        h('div', { class: 'tower-mono tower-muted', style: 'font-size:11.5px;margin-bottom:4px' },
          (m.role === 'user' ? 'you: ' : 'ai: ') + (m.content.length > 140 ? m.content.slice(0, 140) + '…' : m.content))));
      const n = coHistTokens(coHist);
      histNote.textContent = coHist.length ? ('history ' + coHist.length + ' msgs · ~' + n + ' tokens' + (coDropped ? ' · ' + coDropped + ' dropped' : '')) : '';
    }
    function renderAnswer(r) {
      answer.replaceChildren(
        h('div', null, r.text),
        h('div', { style: 'margin-top:8px;display:flex;gap:8px;flex-wrap:wrap' },
          chip(r.model), chip(r.reason), chip(r.backend),
          h('a', { class: 'tower-link tower-mono', href: '#/traces/' + r.trace_id }, r.trace_id)));
    }
    async function send() {
      const text = input.value.trim();
      if (!text || coPromise) return;
      let msgs = coHist.concat([{ role: 'user', content: text }]);
      while (msgs.length > 1 && coHistTokens(msgs) > coHistBudget) { msgs.shift(); coDropped++; }
      coErr(); sendBtn.disabled = true; wfBtn.disabled = true;
      answer.replaceChildren(skeleton(2));
      input.value = '';
      const ctl = new AbortController();
      let convId = coConvId;
      if (!convId) {
        // Storage is best-effort: a thread that cannot be stored still chats.
        try {
          convId = (await api('/v1/conversations', { method: 'POST', body: JSON.stringify({ model: '' }) }, ctl.signal)).id;
        } catch (e) { convId = null; }
      }
      const p = coPromise = (async () => {
        try {
          const r = await api('/v1/chat/completions', { method: 'POST', body: JSON.stringify({ messages: msgs }) }, ctl.signal);
          coHist = msgs.concat([{ role: 'assistant', content: r.text }]);
          coPin = r.trace_id;
          coLastInput = text;
          coLastAnswer = r; coLastErr = '';
          if (convId) {
            coConvId = convId;
            recordTurn(convId, { role: 'user', content: text, trace_id: r.trace_id }).then(() =>
              recordTurn(convId, {
                role: 'assistant', content: r.text, model: r.model, trace_id: r.trace_id,
                prompt_tokens: r.usage ? r.usage.prompt_tokens : 0,
                completion_tokens: r.usage ? r.usage.completion_tokens : 0,
              }).then(() => { if (convList.isConnected) loadConvs(); }));
          }
        } catch (e) { if (e.name !== 'AbortError') { coLastAnswer = null; coLastErr = e.message; } }
        finally { if (coPromise === p) coPromise = null; }
      })();
      await p;
      // The page may be gone (user navigated away): history waits on revisit.
      if (!answer.isConnected) return;
      if (coLastAnswer) renderAnswer(coLastAnswer);
      else answer.replaceChildren(errorBox('Send failed: ' + coLastErr, send));
      renderTranscript();
      sendBtn.disabled = false; wfBtn.disabled = false;
    }
    function coErr() { coLastAnswer = null; coLastErr = ''; }
    async function runWorkflow() {
      if (!coLastInput || coPromise) return;
      wfBtn.disabled = true;
      try {
        const r = await api('/v1/workflows', { method: 'POST', body: JSON.stringify({ input: coLastInput }) });
        toast('Workflow ' + r.id + ' started');
        answer.append(h('div', { style: 'margin-top:8px' }, chip('workflow'), ' ',
          h('a', { class: 'tower-link tower-mono', href: '#/traces/' + r.id }, r.id)));
      } catch (e) { toast(e.message, true); }
      if (wfBtn.isConnected) wfBtn.disabled = false;
    }
    async function recordTurn(convId, m) {
      // No page signal: storing must survive navigation, like the send itself.
      try { await api('/v1/conversations/' + convId + '/messages', { method: 'POST', body: JSON.stringify(m) }); }
      catch (e) { toast('Thread not stored: ' + e.message, true); }
    }
    async function loadConvs() {
      let data;
      try { data = await api('/v1/conversations?limit=50'); }
      catch (e) { convList.replaceChildren(errorBox('Threads unavailable: ' + e.message, loadConvs)); return; }
      const convs = data.conversations || [];
      convList.replaceChildren(...(convs.length ? convs.map(c => {
        const del = h('span', { class: 'tower-conv-del', title: 'delete thread' }, '×');
        del.addEventListener('click', ev => { ev.stopPropagation(); deleteConv(c.id); });
        const el = h('div', { class: 'tower-conv-item' + (c.id === coConvId ? ' active' : '') },
          h('div', { class: 'tower-conv-title' }, c.title || '(untitled)'),
          h('div', { class: 'tower-conv-meta' }, chip(c.messages + ' msgs'), h('span', null, fmt.date(c.updated_at)), del));
        el.addEventListener('click', () => openConv(c.id));
        return el;
      }) : [h('div', { class: 'tower-empty' }, 'No threads yet — send one.')]));
    }
    async function openConv(id) {
      if (coPromise) return;
      let data;
      try { data = await api('/v1/conversations/' + id + '/messages?limit=500'); }
      catch (e) { toast(e.message, true); return; }
      coConvId = id;
      coHist = (data.messages || []).map(m => ({ role: m.role, content: m.content }));
      coDropped = 0;
      const users = coHist.filter(m => m.role === 'user');
      coLastInput = users.length ? users[users.length - 1].content : '';
      const traces = (data.messages || []).map(m => m.trace_id).filter(t => t);
      coPin = traces.length ? traces[traces.length - 1] : null;
      coLastAnswer = null;
      answer.replaceChildren(h('div', { class: 'tower-empty' }, 'Thread loaded — continue below.'));
      renderTranscript();
      loadConvs();
      input.focus();
    }
    function newThread() {
      if (coPromise) return;
      coConvId = null; coHist = []; coDropped = 0; coPin = null;
      coLastInput = ''; coLastAnswer = null;
      answer.replaceChildren(h('div', { class: 'tower-empty' }, 'Send a message to start a conversation.'));
      renderTranscript();
      loadConvs();
      input.focus();
    }
    async function deleteConv(id) {
      try { await api('/v1/conversations/' + id, { method: 'DELETE' }); }
      catch (e) { toast(e.message, true); return; }
      toast('Thread deleted');
      if (id === coConvId) newThread();
      else loadConvs();
    }
    const feed = () => { railCleanup = mountRail(listBody,
      (nt, ns) => { label.textContent = nt + ' traces · ' + ns + ' spans on rail'; },
      () => coPin,
      msg => listBody.replaceChildren(errorBox(msg, feed))); };
    feed();
    loadConvs();
    renderTranscript();
    newBtn.addEventListener('click', newThread);
    if (coLastAnswer && answer.isConnected) renderAnswer(coLastAnswer);
    sendBtn.addEventListener('click', send);
    wfBtn.addEventListener('click', runWorkflow);
    input.addEventListener('keydown', e => { if (e.key === 'Enter') send(); });
  }

  // ---------- router ----------
  const pages = { overview: pageOverview, traces: pageTraces, evals: pageEvals, benchmarks: pageBenchmarks, workflows: pageWorkflows, cockpit: pageCockpit };
  function route() {
    if (railCleanup) { railCleanup(); railCleanup = null; }
    if (pageCtl) pageCtl.abort();
    clearTimeout(pollTimer);
    pageCtl = new AbortController();
    const parts = location.hash.replace(/^#\/?/, '').split('/');
    const name = pages[parts[0]] ? parts[0] : 'overview';
    document.querySelectorAll('.tower-nav-btn[data-page]').forEach(b => b.dataset.active = String(b.dataset.page === name));
    const pt = document.getElementById('page-title'); if (pt) pt.textContent = pageTitles[name] || 'Tower';
    if (name !== 'overview') api('/v1/overview', null, pageCtl.signal).then(updateStrip).catch(() => { /* strip is best-effort */ });
    pages[name](pageCtl.signal, parts.slice(1).join('/'));
  }
  document.querySelectorAll('.tower-nav-btn[data-page]').forEach(b => b.addEventListener('click', () => { location.hash = '#/' + b.dataset.page; }));
  window.addEventListener('hashchange', route);
  route();
  // keep the strip fresh even off the overview page
  setInterval(async () => { try { updateStrip(await api('/v1/overview')); } catch (e) { /* strip is best-effort */ } }, 15000);
})();
