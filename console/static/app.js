/* Tower console — hand-written, no framework. One hash route per page; every page owns an
   AbortController so navigating away cancels its fetches; 5xx get one retry. */
(function () {
  'use strict';

  const content = document.getElementById('content');
  const toastEl = document.getElementById('toast');
  let pageCtl = null;       // AbortController of the current page
  let pollTimer = null;

  // ---------- tiny DOM helper ----------
  function h(tag, attrs, ...children) {
    const el = document.createElement(tag);
    if (attrs) for (const [k, v] of Object.entries(attrs)) {
      if (k === 'class') el.className = v;
      else if (k === 'onclick') el.addEventListener('click', v);
      else if (k === 'html') el.innerHTML = v;
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
  }

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
          r.fallback && r.fallback.length ? chip('fallback') : null)))));
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
        panel('Recent requests', 'Newest first, from the span store', requestsTable(reqs.requests)));
    }
    pollTimer = setTimeout(() => { if (location.hash.startsWith('#/overview') || location.hash === '' || location.hash === '#/') pageOverview(signal, '', true); }, 5000);
  }

  function traceView(data) {
    const spans = data.spans || [];
    const byId = {}; spans.forEach(s => byId[s.span_id] = s);
    const steps = spans.map((s, i) => {
      let attrs = {}; try { attrs = JSON.parse(s.attrs || '{}'); } catch (e) { }
      const failed = attrs.error;
      const detail = h('div', { class: 'tower-trace-detail' },
        h('div', { class: 'tower-kv' }, ...Object.entries(attrs).flatMap(([k, v]) => [h('span', { class: 'k' }, k), h('span', { class: 'v' }, typeof v === 'object' ? JSON.stringify(v) : String(v))]),
          h('span', { class: 'k' }, 'span_id'), h('span', { class: 'v' }, s.span_id),
          h('span', { class: 'k' }, 'parent_id'), h('span', { class: 'v' }, s.parent_id || '(root)')));
      const body = h('div', { class: 'tower-trace-body', onclick: () => detail.classList.toggle('open') },
        h('div', { class: 'tower-trace-top' },
          h('span', { class: 'tower-trace-name' }, s.name),
          h('span', { class: 'tower-trace-meta' },
            attrs.latency_s != null ? h('span', { class: 'tower-mono' }, fmt.ms(attrs.latency_s)) : null,
            attrs.model ? h('span', { class: 'tower-mono' }, attrs.model) : null,
            attrs.backend ? chip(attrs.backend) : null,
            attrs.sensitive ? chip('sensitive') : null,
            failed ? pill('critical', 'failed') : null,
            h('span', null, '▾'))),
        detail);
      return h('div', { class: 'tower-trace-step ' + (failed ? 'active' : 'done') }, h('span', { class: 'tower-trace-dot' }), body);
    });
    return h('div', { class: 'tower-trace' }, ...steps);
  }

  async function pageTraces(signal, id) {
    const input = h('input', { class: 'tower-input', placeholder: 'trace id or workflow id', value: id || '' });
    const go = () => { const v = input.value.trim(); if (v) location.hash = '#/traces/' + v; };
    input.addEventListener('keydown', e => { if (e.key === 'Enter') go(); });
    const form = h('div', { class: 'tower-form-row' }, input, h('button', { class: 'tower-btn primary', onclick: go }, 'Inspect'));
    const result = h('div', null);
    content.replaceChildren(panel('Trace inspector', 'A router chat has 3 chained spans; a workflow has one span per step. Prompts are redacted.', h('div', null, form, h('div', { style: 'height:14px' }), result)));
    if (!id) { result.replaceChildren(h('div', { class: 'tower-empty' }, 'Paste a trace id, or click a request on the overview.')); return; }
    result.replaceChildren(skeleton(3));
    try {
      const data = await api('/v1/traces/' + encodeURIComponent(id), null, signal);
      result.replaceChildren(h('div', { class: 'tower-panel-sub', style: 'margin-bottom:10px' }, 'trace ', h('span', { class: 'tower-mono' }, id), ' · ', data.spans.length + ' spans'), traceView(data));
    } catch (e) {
      if (e.name === 'AbortError') return;
      result.replaceChildren(errorBox(e.code === 'trace_not_found' ? 'No spans for that id.' : 'Could not load trace: ' + e.message));
    }
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
    const worstPanel = panel('Worst cases', 'Lowest faithfulness in the latest run',
      worst.length ? h('table', { class: 'tower-table' }, h('thead', null, h('tr', null, h('th', null, 'question'), h('th', null, 'faith'), h('th', null, 'P@5'), h('th', null, 'R@5'))),
        h('tbody', null, worst.map(wc => h('tr', null, h('td', null, wc.question), h('td', { class: 'tower-mono' }, fmt.score(wc.faithfulness)), h('td', { class: 'tower-mono' }, fmt.score(wc.precision)), h('td', { class: 'tower-mono' }, fmt.score(wc.recall))))))
        : h('div', { class: 'tower-empty' }, 'Nothing scored yet'));
    const table = panel('Runs', null, h('table', { class: 'tower-table' },
      h('thead', null, h('tr', null, ...['run', 'when', 'judge', 'score'].map(t => h('th', null, t)))),
      h('tbody', null, runs.map(r => h('tr', null, h('td', { class: 'tower-mono' }, r.id), h('td', { class: 'tower-mono' }, fmt.date(r.created_at)), h('td', null, chip(r.judge_model)), h('td', null, r.score >= (drift.threshold ?? 0.7) ? pill('healthy', fmt.score(r.score)) : pill('critical', fmt.score(r.score))))))));
    content.replaceChildren(metrics, h('div', { class: 'tower-grid-2' }, history, worstPanel), table);
    if (status.running) pollTimer = setTimeout(() => { if (location.hash.startsWith('#/evals')) pageEvals(signal); }, 5000);
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

  // ---------- router ----------
  const pages = { overview: pageOverview, traces: pageTraces, evals: pageEvals, workflows: pageWorkflows };
  function route() {
    if (pageCtl) pageCtl.abort();
    clearTimeout(pollTimer);
    pageCtl = new AbortController();
    const parts = location.hash.replace(/^#\/?/, '').split('/');
    const name = pages[parts[0]] ? parts[0] : 'overview';
    document.querySelectorAll('.tower-rail-btn').forEach(b => b.dataset.active = String(b.dataset.page === name));
    pages[name](pageCtl.signal, parts.slice(1).join('/'));
  }
  document.querySelectorAll('.tower-rail-btn').forEach(b => b.addEventListener('click', () => { location.hash = '#/' + b.dataset.page; }));
  window.addEventListener('hashchange', route);
  route();
  // keep the strip fresh even off the overview page
  setInterval(async () => { try { updateStrip(await api('/v1/overview')); } catch (e) { /* strip is best-effort */ } }, 15000);
})();
