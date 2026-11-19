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
      // Style goes through CSSOM, never a style="" attribute: the console ships a
      // strict CSP (no 'unsafe-inline'), and the browser blocks attribute styles.
      else if (k === 'style') for (const decl of String(v).split(';')) {
        const i = decl.indexOf(':');
        if (i > 0) el.style.setProperty(decl.slice(0, i).trim(), decl.slice(i + 1).trim());
      }
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
  // One duration formatter for every page: 1.35 s / 828 ms / <1 ms. "point" and
  // a bare "0 ms" are gone (redesign guide, shared shell).
  function duration(secs) {
    if (secs == null) return '–';
    const s = Number(secs) || 0;
    if (s >= 1) return s.toFixed(2) + ' s';
    const ms = Math.round(s * 1000);
    if (ms >= 1000) return (ms / 1000).toFixed(2) + ' s';
    if (ms >= 1) return ms + ' ms';
    return '<1 ms';
  }
  const fmt = {
    ms: duration,
    dur: duration,
    tokens: n => Number(n || 0).toLocaleString(),
    pct: v => v == null ? '–' : (Number(v) * 100).toFixed(1) + '%',
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
    document.getElementById('strip-version').textContent = ov.version ? 'tower ' + ov.version : '';
    const sd = document.getElementById('side-dot');
    if (sd) { sd.style.background = dot.style.background; sd.style.boxShadow = dot.style.boxShadow; }
    const sr = document.getElementById('side-router');
    if (sr) {
      sr.textContent = allUp ? 'router live' : 'backend down';
      // The rail footer is the dot alone; the words live in a tooltip.
      const item = sr.closest('.tower-pulse-item');
      if (item) item.title = sr.textContent;
    }
    const sv = document.getElementById('side-version'); if (sv) sv.textContent = ov.version ? 'tower ' + ov.version : '';
  }
  const pageTitles = { overview: 'Overview', traces: 'Traces', evals: 'Evals & drift', benchmarks: 'Benchmarks', workflows: 'Workflows', cockpit: 'Cockpit', gpu: 'GPU', metrics: 'Metrics', models: 'Models' };

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
      h('tbody', null, reqs.map(r => h('tr', { class: 'tower-row-link', onclick: () => { location.hash = '#/traces/' + r.trace_id; } },
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
    const healthBody = backends.length
      ? h('table', { class: 'tower-table' },
          h('tbody', null,
            backends.map(b => h('tr', null,
              h('td', { class: 'tower-mono' }, b.name),
              h('td', null, chip(b.local ? 'local' : 'cloud')),
              h('td', null, b.up ? pill('healthy', 'healthy') : pill('critical', 'down')),
              h('td', { class: 'tower-mono', title: b.error || '' }, (b.error || '').slice(0, 40))))))
      : h('div', { class: 'tower-empty' }, 'No backends registered');
    const health = panel('Backend health', 'Probed every 15 s - a down local backend makes chat fail over',
      h('div', null, healthBody,
        h('div', { class: 'tower-section-head', style: 'margin:10px 0 0' },
          h('span', { class: 'tower-panel-sub' }, 'Per-model detail lives on Models.'),
          h('a', { class: 'tower-link', href: '#/models' }, 'Models →'))));
    const typing = document.activeElement && content.contains(document.activeElement) && document.activeElement.tagName === 'INPUT';
    if (!typing) {
      content.replaceChildren(metrics, h('div', { class: 'tower-grid-2' }, traffic, health),
        panel('Recent requests', 'Newest first - click a row to open its trace', requestsTable(reqs.requests)));
    }
    pollTimer = setTimeout(() => { if (location.hash.startsWith('#/overview') || location.hash === '' || location.hash === '#/') pageOverview(signal, '', true); }, 5000);
  }

  // ---------- models page: what this box can serve, and what it has served ----------
  function modelRows(ov) {
    const since = ov.since_start || {};
    const byModel = (ov.traffic && ov.traffic.by_model) || {};
    const backends = ov.backends || [];
    return (ov.models || []).map(m => {
      const b = backends.find(x => x.name === m.backend);
      const local = b ? !!b.local : null;
      const s = since[m.model] || {};
      return { m, local, s, lastHour: byModel[m.model] || 0 };
    });
  }

  // One line chart for every time series on the page: axis with units, time
  // labels, one or two series, current dot. Same hand-rolled SVG approach as the
  // GPU charts, tokens only.
  function seriesChart(points, defs, opts) {
    opts = opts || {};
    const fmtV = opts.fmt || (v => String(Math.round(v)));
    const w = 620, hgt = 150, padL = 54, padR = 10, padT = 12, padB = 24;
    const NS = 'http://www.w3.org/2000/svg';
    const el = (t, a) => { const n = document.createElementNS(NS, t); for (const k in a) n.setAttribute(k, a[k]); return n; };
    const svg = el('svg', { viewBox: '0 0 ' + w + ' ' + hgt, class: 'tower-series' });
    const dataMax = Math.max(0, ...points.flatMap(p => defs.map(d => Number(p[d.key]) || 0)));
    const max = Math.max(opts.minMax || 1, dataMax);
    const x = i => padL + i * (w - padL - padR) / Math.max(1, points.length - 1);
    const y = v => padT + (1 - Math.min(1, Math.max(0, v / max))) * (hgt - padT - padB);
    [0, 0.5, 1].forEach(f => {
      svg.append(el('line', { x1: padL, x2: w - padR, y1: y(max * f), y2: y(max * f), stroke: 'var(--tower-border-soft)' }));
      const t = el('text', { x: padL - 8, y: y(max * f) + 3, 'text-anchor': 'end', fill: 'var(--tower-ink-faint)', 'font-size': 10 });
      t.textContent = fmtV(max * f); svg.append(t);
    });
    defs.forEach(d => {
      const pts = points.map((p, i) => x(i).toFixed(1) + ',' + y(Number(p[d.key]) || 0).toFixed(1));
      svg.append(el('polyline', { points: pts.join(' '), fill: 'none', stroke: d.color, 'stroke-width': 2, 'vector-effect': 'non-scaling-stroke' }));
      const last = points.length - 1;
      svg.append(el('circle', { cx: x(last), cy: y(Number(points[last][d.key]) || 0), r: 3, fill: d.color }));
    });
    if (points.length) {
      const t0 = el('text', { x: padL, y: hgt - 6, fill: 'var(--tower-ink-faint)', 'font-size': 10 });
      t0.textContent = fmt.time(points[0].at); svg.append(t0);
      const t1 = el('text', { x: w - padR, y: hgt - 6, 'text-anchor': 'end', fill: 'var(--tower-ink-faint)', 'font-size': 10 });
      t1.textContent = fmt.time(points[points.length - 1].at); svg.append(t1);
    }
    return svg;
  }

  function chartPanel(title, sub, pts, defs, opts) {
    opts = opts || {};
    const legend = defs.length > 1 ? ' · ' + defs.map(d => d.label).join(' / ') : '';
    return panel(title, sub + legend,
      pts.length ? seriesChart(pts, defs, opts)
        : h('div', { class: 'tower-empty' }, 'No traffic in this window.'));
  }

  const modelTest = { q: '', busy: false, result: null, error: '' };

  async function pageModels(signal, _sub, polling) {
    if (!polling) content.replaceChildren(skeleton(5));
    let ov;
    try { ov = await api('/v1/overview', null, signal); }
    catch (e) { if (e.name === 'AbortError') return; if (!polling) content.replaceChildren(errorBox('Could not load models: ' + e.message, () => route())); return; }
    updateStrip(ov);

    const rows = modelRows(ov);
    const backends = ov.backends || [];
    const localN = rows.filter(r => r.local === true).length;
    const cloudN = rows.filter(r => r.local === false).length;
    const upN = rows.filter(r => r.m.up !== false).length;
    const since = ov.since_start || {};
    const totalReq = Object.values(since).reduce((n, x) => n + (Number(x.requests) || 0), 0);
    const totalTok = Object.values(since).reduce((n, x) => n + (Number(x.tokens) || 0), 0);
    const totalErr = Object.values(since).reduce((n, x) => n + (Number(x.errors) || 0), 0);

    const metrics = h('div', { class: 'tower-metric-row' },
      metric('Models served', String(rows.length), 'registered with the router'),
      metric('Reachable now', upN + ' / ' + rows.length, upN === rows.length ? 'every backend answered' : 'check Backends below'),
      metric('Local / cloud', localN + ' / ' + cloudN, 'cloud is fallback only'),
      metric('Since start', String(totalReq), totalTok.toLocaleString() + ' tokens · ' + totalErr + ' errors'));

    const served = rows.length
      ? h('table', { class: 'tower-table' },
        h('thead', null, h('tr', null,
          ...['model', 'tier', 'backend', 'reach', 'last hour', 'requests', 'errors', 'tokens'].map(t => h('th', null, t)))),
        h('tbody', null, rows.map(r => h('tr', null,
          h('td', { class: 'tower-mono' }, r.m.model),
          h('td', null, chip(r.m.tier || 'unknown')),
          h('td', null, chip((r.m.backend || 'unknown') + (r.local === null ? '' : r.local ? ' · local' : ' · cloud'))),
          h('td', null, r.m.up === false ? pill('critical', 'down') : pill('healthy', 'up')),
          h('td', { class: 'tower-mono' }, String(r.lastHour)),
          h('td', { class: 'tower-mono' }, String(r.s.requests || 0)),
          h('td', { class: 'tower-mono' }, String(r.s.errors || 0)),
          h('td', { class: 'tower-mono' }, Number(r.s.tokens || 0).toLocaleString())))))
      : h('div', { class: 'tower-empty' },
        'No models are registered. Set FAST_MODEL / QUALITY_MODEL, or list extra local models in OLLAMA_MODELS, then restart.');

    const backendTable = backends.length
      ? h('table', { class: 'tower-table' },
        h('thead', null, h('tr', null, h('th', null, 'backend'), h('th', null, 'where'), h('th', null, 'health'), h('th', null, 'last error'))),
        h('tbody', null, backends.map(b => h('tr', null,
          h('td', { class: 'tower-mono' }, b.name),
          h('td', null, chip(b.local ? 'local' : 'cloud')),
          h('td', null, b.up ? pill('healthy', 'healthy') : pill('critical', 'down')),
          h('td', { class: 'tower-mono', title: b.error || '' }, (b.error || '').slice(0, 48))))))
      : h('div', { class: 'tower-empty' }, 'No backends registered.');

    // Route test: one real prompt through the router State lives outside the
    // render so a 5 s poll cannot wipe the answer, and a poll never replaces the
    // page while the field is focused or a test is in flight.
    const rtInput = h('input', {
      class: 'tower-input', placeholder: 'Route test: type a prompt…',
      'aria-label': 'Route test prompt', value: modelTest.q,
    });
    rtInput.addEventListener('input', () => { modelTest.q = rtInput.value; });
    const rtOut = h('div', { class: 'tower-panel-sub' }, '');
    const rtBtn = h('button', { class: 'tower-btn secondary', type: 'button' }, 'Route test…');
    function showRouteResult() {
      if (modelTest.error) { rtOut.replaceChildren(errorBox(modelTest.error)); return; }
      if (!modelTest.result) { rtOut.textContent = 'The answer, the model it picked and why appear here.'; return; }
      const r = modelTest.result;
      rtOut.replaceChildren(chip(r.model || '?'), chip(r.backend || '?'), chip(r.reason || '?'),
        h('span', { class: 'tower-mono' }, fmt.dur(r.ms / 1000)),
        h('div', { class: 'tower-panel-sub' }, (r.text || '').slice(0, 160)));
    }
    rtBtn.disabled = modelTest.busy;
    rtBtn.addEventListener('click', async () => {
      const q = rtInput.value.trim();
      if (!q || modelTest.busy) return;
      modelTest.busy = true; modelTest.error = ''; rtBtn.disabled = true;
      rtOut.textContent = 'Routing…';
      const t0 = performance.now();
      try {
        const r = await api('/v1/chat/completions', {
          method: 'POST', body: JSON.stringify({ messages: [{ role: 'user', content: q }], max_tokens: 32 }),
        });
        modelTest.result = { model: r.model, backend: r.backend, reason: r.reason, text: r.text, ms: performance.now() - t0 };
      } catch (e) {
        modelTest.error = 'Route test failed: ' + e.message;
      }
      modelTest.busy = false; rtBtn.disabled = false;
      showRouteResult();
    });
    showRouteResult();

    const typing = polling && (modelTest.busy ||
      (document.activeElement && content.contains(document.activeElement) && document.activeElement.tagName === 'INPUT'));
    if (!typing) {
      content.replaceChildren(metrics,
        panel('Models', 'One row per served model; counters since process start, last hour from spans', served),
        h('div', { class: 'tower-note' }, chip('cloud'), ' Cloud tiers are fallback only and are never used for sensitive prompts.'),
        h('div', { class: 'tower-grid-2' },
          panel('Backends', 'Probed every 15 s', backendTable),
          panel('Route test', 'Sends one prompt through the router', h('div', null,
            h('div', { class: 'tower-form-row' }, rtInput, rtBtn), rtOut))));
      showRouteResult();
    }

    pollTimer = setTimeout(() => { if (location.hash.startsWith('#/models')) pageModels(signal, '', true); }, 5000);
  }

  let metricsRange = '1h';
  async function pageMetrics(signal, _sub, polling) {
    if (!polling) content.replaceChildren(skeleton(5));
    let data, ov;
    try {
      [data, ov] = await Promise.all([
        api('/v1/metrics/series?window=' + metricsRange, null, signal),
        api('/v1/overview', null, signal),
      ]);
    } catch (e) { if (e.name === 'AbortError') return; if (!polling) content.replaceChildren(errorBox('Could not load metrics: ' + e.message, () => route())); return; }
    updateStrip(ov);

    const pts = data.points || [];
    const label = data.window || metricsRange;
    const sum = k => pts.reduce((n, p) => n + (Number(p[k]) || 0), 0);
    const req = sum('requests'), err = sum('errors'), tin = sum('tokens_in'), tout = sum('tokens_out');
    // The newest bucket is partial (the current minute): show the latest bucket
    // that actually measured a model call, or the KPI reads '<1 ms' during idle.
    const last = pts.slice().reverse().find(x => (Number(x.p50_s) || 0) > 0) || (pts.length ? pts[pts.length - 1] : {});
    const since = ov.since_start || {};
    const totalReq = Object.values(since).reduce((n, x) => n + (Number(x.requests) || 0), 0);
    const rate = req > 0 ? err / req : null;

    const metrics = h('div', { class: 'tower-metric-row' },
      metric('Requests', String(req), label + ' · ' + totalReq + ' since start'),
      metric('Error rate', rate === null ? '–' : (rate * 100).toFixed(2) + '%', err + ' errors in ' + label, rate !== null && rate >= 0.05 ? 'down' : ''),
      metric('p50 / p99', fmt.dur(last.p50_s), 'p99 ' + fmt.dur(last.p99_s) + ' · latest measured bucket'),
      metric('Tokens', (tin + tout).toLocaleString(), tin.toLocaleString() + ' in · ' + tout.toLocaleString() + ' out'));

    const rangeSeg = h('div', { class: 'tower-seg' },
      ...[['1h', '1 h'], ['6h', '6 h'], ['24h', '24 h']].map(([k, text]) =>
        h('button', {
          class: metricsRange === k ? 'on' : '',
          onclick: () => { if (metricsRange === k) return; metricsRange = k; clearTimeout(pollTimer); pageMetrics(signal, '', true); },
        }, text)));
    const prom = h('a', { class: 'tower-btn ghost', href: '/metrics', target: '_blank', rel: 'noopener' }, 'Prometheus text');

    content.replaceChildren(metrics,
      h('div', { class: 'tower-form-row', style: 'justify-content:flex-end' }, prom, rangeSeg),
      h('div', { class: 'tower-grid-2' },
        chartPanel('Requests', 'per bucket · ' + label, pts,
          [{ key: 'requests', color: 'var(--tower-ink-muted)' }], { fmt: v => String(Math.round(v)), minMax: 2 }),
        chartPanel('Latency', 'p50 / p99 per bucket · ' + label, pts,
          [{ key: 'p50_s', color: 'var(--tower-amber)', label: 'p50' }, { key: 'p99_s', color: 'var(--tower-red)', label: 'p99' }],
          { fmt: v => v > 0 ? fmt.dur(v) : '0' }),
        chartPanel('Errors', 'per bucket · ' + label, pts,
          [{ key: 'errors', color: 'var(--tower-red)' }], { fmt: v => String(Math.round(v)), minMax: 2 }),
        chartPanel('Tokens', 'in + out per bucket · ' + label, pts,
          [{ key: 'tokens_in', color: 'var(--tower-ink-faint)', label: 'in' }, { key: 'tokens_out', color: 'var(--tower-amber)', label: 'out' }],
          { fmt: v => Math.round(v).toLocaleString() })));

    pollTimer = setTimeout(() => { if (location.hash.startsWith('#/metrics')) pageMetrics(signal, '', true); }, 5000);
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
        h('div', { class: 'tower-trace-title' }, h('span', { class: 'tower-mono' }, data.trace_id), ' ', chip(kind), data.gpu_ok ? chip('GPU ' + Math.round(data.gpu.util_pct) + '% · ' + gpuGB(data.gpu.mem_used_bytes)) : null, failed ? pill('critical', 'failed') : pill('healthy', 'ok')),
        h('div', { class: 'tower-panel-sub' },
          spans.length + ' spans · ' + fmt.ms(total / 1000) + ' end to end',
          rows.length ? ' · started ' + fmt.date(rows[0].span.started_at) : '',
          decide ? ' · ' + (decide.attrs.reason || '') : '',
          decide && decide.attrs.sensitive ? ' · sensitive' : '')),
      h('div', { class: 'tower-form-row' },
        h('button', { class: 'tower-btn ghost', onclick: () => { navigator.clipboard && navigator.clipboard.writeText(data.trace_id); toast('trace id copied'); } }, 'Copy id'),
        h('a', { class: 'tower-btn ghost', href: '/v1/traces/' + encodeURIComponent(data.trace_id), target: '_blank' }, 'JSON')));

    // waterfall
    const ruler = h('div', { class: 'tower-wf-ruler' },
      [0, 25, 50, 75, 100].map(px => h('span', null, fmt.dur((total * px / 100) / 1000))));
    const wf = h('div', { class: 'tower-waterfall' }, ...rows.map(r => {
      const left = (r.start / total) * 100, width = Math.max(2, (r.dur / total) * 100);
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
      // A failed span is the first thing an operator needs: open it, do not hide it.
      if (attrs.error) detail.classList.add('open');
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
    return h('div', null, head, h('h4', { class: 'tower-sub-h' }, 'Timeline'), ruler, wf, h('h4', { class: 'tower-sub-h' }, 'Spans'), h('div', { class: 'tower-trace' }, ...steps),
      gen && gen.attrs.fallback_from ? h('div', { class: 'tower-note' }, 'Fallback: ' + gen.attrs.fallback_from.join(', ') + ' failed → served by ' + gen.attrs.model) : null);
  }

  async function pageTraces(signal, id) {
    let filter = 'all';
    const input = h('input', { class: 'tower-input', placeholder: 'Search or paste a trace id...', value: id || '', 'aria-label': 'Trace id' });
    const go = () => { const v = input.value.trim(); if (v) location.hash = '#/traces/' + v; };
    input.addEventListener('keydown', e => { if (e.key === 'Enter') go(); });
    const filters = h('div', { class: 'tower-filters' });
    const listBody = h('div', { class: 'tower-trace-list' }, skeleton(6));
    const detail = h('div', null, h('div', { class: 'tower-empty' }, 'Pick a trace on the left, or paste an id above.'));
    // One master-detail surface: the old separate "Trace inspector" card is gone;
    // the search box lives above the list it filters (redesign guide, Traces).
    const listPanel = h('section', { class: 'tower-panel tower-master' },
      h('div', { class: 'tower-panel-head' },
        h('div', null, h('h3', null, 'Traces'), h('div', { class: 'tower-panel-sub' }, 'Newest first - click to inspect'))),
      h('div', { class: 'tower-form-row', style: 'margin-bottom:10px' }, input,
        h('button', { class: 'tower-btn primary', onclick: go }, 'Inspect')),
      filters, listBody);
    const detailPanel = h('section', { class: 'tower-panel tower-detail' }, detail);
    content.replaceChildren(h('div', { class: 'tower-grid-traces' }, listPanel, detailPanel));
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
    // A run with zero judged pairs has no measurement. Plotting it as 0.0 made
    // "nothing was asked of the judge" look like a failure (redesign guide).
    const list = runs.filter(r => r.judged !== 0 && r.status !== 'running' && r.status !== 'failed').slice().reverse();
    const w = 560, hgt = 140, pad = 24;
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', `0 0 ${w} ${hgt}`); svg.setAttribute('width', '100%'); svg.style.display = 'block';
    const x = i => pad + (list.length === 1 ? (w - 2 * pad) / 2 : i * (w - 2 * pad) / (list.length - 1));
    const y = v => hgt - pad - v * (hgt - 2 * pad);
    const grid = document.createElementNS('http://www.w3.org/2000/svg', 'g');
    // Even ticks; the threshold is its own labelled line, not a tick.
    [0, 0.25, 0.5, 0.75, 1].forEach(v => {
      const l = document.createElementNS('http://www.w3.org/2000/svg', 'line');
      l.setAttribute('x1', pad); l.setAttribute('x2', w - pad); l.setAttribute('y1', y(v)); l.setAttribute('y2', y(v));
      l.setAttribute('stroke', 'var(--tower-border-soft)');
      grid.append(l);
      const t = document.createElementNS('http://www.w3.org/2000/svg', 'text');
      t.setAttribute('x', 2); t.setAttribute('y', y(v) + 4); t.setAttribute('fill', 'var(--tower-ink-faint)'); t.setAttribute('font-size', '10'); t.textContent = v.toFixed(2);
      grid.append(t);
    });
    const thr = document.createElementNS('http://www.w3.org/2000/svg', 'line');
    thr.setAttribute('x1', pad); thr.setAttribute('x2', w - pad); thr.setAttribute('y1', y(0.7)); thr.setAttribute('y2', y(0.7));
    thr.setAttribute('stroke', 'var(--tower-amber-dim)'); thr.setAttribute('stroke-dasharray', '4 3');
    grid.append(thr);
    const lbl = document.createElementNS('http://www.w3.org/2000/svg', 'text');
    lbl.setAttribute('x', w - pad); lbl.setAttribute('y', y(0.7) - 4); lbl.setAttribute('text-anchor', 'end');
    lbl.setAttribute('fill', 'var(--tower-ink-faint)'); lbl.setAttribute('font-size', '10'); lbl.textContent = 'threshold 0.70';
    grid.append(lbl);
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
      metric('Delta vs previous', drift.runs > 1 ? (drift.delta >= 0 ? '+' : '') + drift.delta.toFixed(3) : '–',
        drift.judge_changed ? 'judge changed — not corpus drift'
          : drift.prompt_changed ? 'judge prompt changed — not corpus drift'
            : (drift.judge_now || '')),
      metric('Runs (' + data.golden_version + ')', runs.length, runs.length ? 'last ' + fmt.date(runs[0].created_at) : ''),
      metric('Suite status', status.running ? 'running' : (status.error ? 'failed' : 'idle'), status.error || (status.finished_at && status.finished_at !== '0001-01-01T00:00:00Z' ? 'finished ' + fmt.date(status.finished_at) : ''), status.error ? 'down' : ''));
    const unmeasured = runs.filter(r => r.judged === 0 || r.status === 'running' || r.status === 'failed').length;
    const chartable = runs.length - unmeasured;
    const history = panel('Score history',
      'Scored runs of golden ' + data.golden_version + '; dashed line = threshold 0.70' + (unmeasured ? ' - ' + unmeasured + ' unmeasured' : ''),
      chartable > 0 ? scoreChart(runs) : h('div', { class: 'tower-empty' }, runs.length ? 'No scored runs yet - all ' + unmeasured + ' are unmeasured.' : 'No runs yet - start one.'), runBtn);
    const worst = drift.worst_cases || [];
    const misses = drift.misses_now || 0;
    const worstPanel = panel('Worst cases', 'Lowest faithfulness in the latest run' + (misses ? ' · ' + misses + ' retrieval miss(es) excluded from the average' : ''),
      worst.length ? h('table', { class: 'tower-table' }, h('thead', null, h('tr', null, h('th', null, 'question'), h('th', null, 'reason'), h('th', null, 'faith'), h('th', null, 'P@5'), h('th', null, 'R@5'))),
        h('tbody', null, worst.map(wc => h('tr', null,
          h('td', null, wc.question),
          h('td', null, wc.retrieval_miss ? chip('retrieval miss') : (wc.faithfulness === 0 ? chip('refuted') : chip('scored'))),
          h('td', { class: 'tower-mono' }, wc.retrieval_miss || wc.faithfulness == null ? '–' : fmt.score(wc.faithfulness)),
          h('td', { class: 'tower-mono' }, fmt.score(wc.precision)),
          h('td', { class: 'tower-mono' }, fmt.score(wc.recall))))))
        : h('div', { class: 'tower-empty' }, 'Nothing scored yet'));
    const table = panel('Runs', null, h('table', { class: 'tower-table' },
      h('thead', null, h('tr', null, ...['run', 'when', 'judge', 'score', 'gpu'].map(t => h('th', null, t)))),
      h('tbody', null, runs.map((r, i) => h('tr', null,
        h('td', { class: 'tower-mono' }, r.id),
        h('td', { class: 'tower-mono' }, fmt.date(r.created_at)),
        // A DOM node must never be concatenated into a string: `' ' + chip(x)`
        // renders "[object HTMLSpanElement]" (redesign guide, Evals).
        h('td', null, chip(r.judge_model), r.judge_prompt ? chip(r.judge_prompt) : null,
          runs[i + 1] && runs[i + 1].judge_model !== r.judge_model ? chip('judge changed') : null),
        h('td', null, r.status && r.status !== 'complete' ? chip('incomplete')
          : (r.judged === 0 ? chip('unmeasured') : (r.score >= (drift.threshold ?? 0.7) ? pill('healthy', fmt.score(r.score)) : pill('critical', fmt.score(r.score))))),
        h('td', null, r.gpu
          ? [chip('GPU ' + Math.round(r.gpu.util_mean_pct) + '%'),
             r.gpu.swaps > 0 ? chip(r.gpu.swaps + ' swap' + (r.gpu.swaps === 1 ? '' : 's')) : null,
             r.gpu.throttled_samples > 0 ? chip('throttled ' + r.gpu.throttled_samples + '/' + r.gpu.samples) : null]
          : h('span', { class: 'tower-muted' }, '-')))))));
    content.replaceChildren(metrics, h('div', { class: 'tower-grid-2' }, history, worstPanel), table);
    if (status.running) pollTimer = setTimeout(() => { if (location.hash.startsWith('#/evals')) pageEvals(signal); }, 5000);
  }

  function cmpRow(label, pct, value, cls) {
    return h('div', { class: 'tower-cmp-row' },
      h('span', null, label),
      h('div', { class: 'tower-cmp-track' },
        h('i', { class: 'tower-cmp-fill ' + (cls || ''), style: 'width:' + Math.max(2, Math.min(100, pct)) + '%' })),
      h('span', { class: 'tower-mono' }, value));
  }

  async function pageBenchmarks(signal) {
    content.replaceChildren(skeleton(6));
    let data;
    try { data = await api('/v1/benchmarks', null, signal); }
    catch (e) { if (e.name === 'AbortError') return; content.replaceChildren(errorBox('Could not load benchmarks: ' + e.message, () => route())); return; }
    const c = data.comparison || {};
    const verdict = c.verdict || {};
    const models = c.models || [], scenarios = c.scenarios || [], history = data.history || [];

    const metrics = h('div', { class: 'tower-metric-row cols-3' },
      metric('Judge', c.judge || '—', c.comparable ? 'same judge across compared runs' : 'not comparable'),
      metric('Models compared', String(models.length), models.length ? models.map(m => m.model).join(' · ') : ''),
      metric('Scenarios', String(scenarios.length), 'split by routing reason'));

    // A verdict is a conclusion with a reason, not a KPI: "tied" alone is not
    // actionable. The banner says why and what to do (redesign guide).
    const banner = h('div', { class: 'tower-banner' },
      h('b', null, 'Verdict: ' + (verdict.winner || 'tied') + '. '),
      verdict.reason || c.note || (c.comparable ? '' : 'The runs are not comparable: different judges.'));

    const maxLat = Math.max(0.001, ...models.map(m => Number(m.p50_latency_s) || 0));
    const maxTok = Math.max(1, ...models.map(m => Number(m.tokens_per_answer) || 0));
    const bestFaith = Math.max(0, ...models.map(m => Number(m.faithfulness) || 0));
    const bestLat = Math.min(...models.map(m => Number(m.p50_latency_s) || 0));
    const bestTok = Math.min(...models.map(m => Number(m.tokens_per_answer) || 0));
    const cards = models.length
      ? h('div', { class: 'tower-cmp' }, models.map(m => {
        const win = m.model === verdict.winner;
        return h('div', { class: 'tower-cmp-card' + (win ? ' win' : '') },
          h('div', { class: 'tower-cmp-head' }, h('b', null, m.model), win ? chip('winner') : null),
          h('div', { class: 'tower-panel-sub' }, m.scored + ' scored · ' + m.misses + ' misses'),
          cmpRow('faithfulness', (Number(m.faithfulness) || 0) * 100, fmt.score(m.faithfulness),
            Number(m.faithfulness) === bestFaith && models.length > 1 ? 'a' : ''),
          cmpRow('p50 latency', ((Number(m.p50_latency_s) || 0) / maxLat) * 100, fmt.ms(m.p50_latency_s),
            models.length > 1 && Number(m.p50_latency_s) === maxLat ? 'r' : (models.length > 1 && Number(m.p50_latency_s) === bestLat ? 'a' : '')),
          cmpRow('tokens / answer', ((Number(m.tokens_per_answer) || 0) / maxTok) * 100,
            String(Math.round(Number(m.tokens_per_answer) || 0)), models.length > 1 && Number(m.tokens_per_answer) === bestTok ? 'a' : ''));
      }))
      : h('div', { class: 'tower-empty' }, 'No scored model runs yet. Run one per model: go run . --score --score-model <name> (same golden version, same judge).');

    const scenCards = scenarios.length
      ? h('div', { class: 'tower-cmp' }, scenarios.map(sc => {
        const total = Math.max(0, ...sc.models.map(m => Number(m.n) || 0));
        const tooFew = total < 10;
        return h('div', { class: 'tower-cmp-card' },
          h('div', { class: 'tower-cmp-head' }, h('b', null, sc.scenario),
            tooFew ? pill('degraded', 'too few') : chip('n=' + total)),
          h('div', { class: 'tower-panel-sub' }, tooFew ? 'n=' + total + ' is too few to decide' : (sc.winner ? 'winner ' + sc.winner : (sc.reason || 'no verdict'))),
          ...sc.models.map(m => cmpRow(m.model, (Number(m.faithfulness) || 0) * 100,
            fmt.score(m.faithfulness) + ' · n=' + m.n, m.model === sc.winner ? 'a' : '')));
      }))
      : h('div', { class: 'tower-empty' }, 'No scenario split yet.');

    const hist = h('details', { class: 'tower-insp-more' }, h('summary', null, 'Model runs · ' + history.length),
      history.length ? h('table', { class: 'tower-table' },
        h('thead', null, h('tr', null, ...['run', 'model', 'judge', 'score', 'when'].map(t => h('th', null, t)))),
        h('tbody', null, history.map(r => h('tr', null,
          h('td', { class: 'tower-mono' }, r.id),
          h('td', null, chip(r.model)),
          h('td', null, chip(r.judge)),
          h('td', { class: 'tower-mono' }, fmt.score(r.score)),
          h('td', { class: 'tower-mono' }, fmt.date(r.created_at))))))
        : h('div', { class: 'tower-empty' }, 'No model runs recorded yet'));

    content.replaceChildren(metrics, banner,
      panel('Model comparison', 'Latest scored run per model; misses excluded from every average. Amber = better, red = slower', cards),
      panel('Scenarios', 'Split by the routing reason each question produced', scenCards),
      hist);
  }

  function stepper(w) {
    const nodes = [];
    w.steps.forEach((st, i) => {
      if (i) nodes.push(h('span', { class: 'tower-step-line' }));
      const cls = st.status === 'done' ? 'done' : st.status === 'running' ? 'running' : st.status === 'failed' ? 'failed' : 'pending';
      nodes.push(h('i', {
        class: 'tower-step-dot ' + cls,
        title: st.name + ' — ' + st.status + (st.attempts > 1 ? ' (' + st.attempts + ' attempts)' : ''),
      }));
    });
    return h('span', { class: 'tower-step', title: w.clean ? 'no retries' : w.retries + ' retries' }, ...nodes);
  }

  async function pageWorkflows(signal) {
    const input = h('input', { class: 'tower-input', placeholder: 'question for the Researcher → Drafter → Reviewer agent', value: 'what does tower do?' });
    const startBtn = h('button', { class: 'tower-btn primary' }, 'Start workflow');
    const listBody = h('div', null, skeleton(5));
    let filter = 'all';
    let cache = [];

    const filterBar = h('div', { class: 'tower-seg', style: 'margin-bottom:10px' },
      ...[['all', 'All'], ['running', 'Running'], ['failed', 'Failed'], ['done', 'Done']].map(([k, text]) =>
        h('button', { class: filter === k ? 'on' : '', onclick: () => { filter = k; filterBar.querySelectorAll('button').forEach(b => b.classList.toggle('on', b.textContent === text)); render(); } }, text)));

    content.replaceChildren(
      panel('Durable workflows', 'Each step is a Postgres row; kill the process mid-step and resume the same id — done steps are never re-run.',
        h('div', { class: 'tower-form-row' }, input, startBtn)),
      panel('Workflows', 'Newest first; repeated inputs are grouped. A stepper is one dot per step: green done, amber running, red failed, faint pending. Quality is a 1-5 rubric on the final answer (--workflow-grade), not groundedness.',
        h('div', null, filterBar, listBody)));

    function runRow(w) {
      const failed = w.steps.find(s => s.status === 'failed');
      const resume = h('button', { class: 'tower-btn primary', type: 'button' }, 'Resume');
      resume.addEventListener('click', async () => {
        resume.disabled = true;
        try { await api('/v1/workflows/' + w.id + '/resume', { method: 'POST' }); toast('Resuming ' + w.id); } catch (e) { toast(e.message, true); }
        setTimeout(refresh, 800);
      });
      return h('tr', null,
        h('td', { class: 'tower-mono', title: fmt.date(w.created_at) }, fmt.ago(w.created_at)),
        h('td', null, h('a', { class: 'tower-link tower-mono', href: '#/traces/' + w.id }, fmt.short(w.id))),
        h('td', null, w.input.length > 60 ? w.input.slice(0, 60) + '…' : w.input),
        h('td', null, stepper(w)),
        h('td', null, w.grade ? chip(w.grade.score + '/5') : h('span', { class: 'tower-muted' }, '—')),
        h('td', null, w.status === 'done' ? pill('healthy', 'done')
          : w.status === 'failed' ? pill('critical', failed ? 'failed at ' + failed.name : 'failed')
          : pill('degraded', w.status)),
        h('td', null, w.status === 'failed' ? resume : null));
    }

    function render() {
      const shown = cache.filter(w => filter === 'all' || w.status === filter);
      if (!shown.length) {
        listBody.replaceChildren(h('div', { class: 'tower-empty' }, cache.length ? 'Nothing matches this filter.' : 'No workflows yet'));
        return;
      }
      // Group by input: one visible row per distinct question, with the others
      // tucked behind a ×N chip so a repeated question cannot fill the page.
      const groups = [];
      const byInput = new Map();
      shown.forEach(w => {
        if (!byInput.has(w.input)) { byInput.set(w.input, []); groups.push(byInput.get(w.input)); }
        byInput.get(w.input).push(w);
      });
      const rows = [];
      groups.forEach(g => {
        const first = runRow(g[0]);
        if (g.length > 1) {
          const extras = g.slice(1).map(w => { const tr = runRow(w); tr.hidden = true; return tr; });
          const cell = first.querySelector('td:nth-child(3)');
          const toggle = h('button', { class: 'tower-chip tower-chip-btn', type: 'button', style: 'margin-left:6px' }, '×' + g.length);
          toggle.addEventListener('click', () => {
            const open = extras[0].hidden;
            extras.forEach(tr => { tr.hidden = !open; });
          });
          cell.append(toggle);
          rows.push(first, ...extras);
        } else {
          rows.push(first);
        }
      });
      listBody.replaceChildren(h('table', { class: 'tower-table' },
        h('thead', null, h('tr', null, ...['when', 'id', 'input', 'steps', 'quality', 'status', ''].map(t => h('th', null, t)))),
        h('tbody', null, rows)));
    }

    async function refresh() {
      let data;
      try { data = await api('/v1/workflows?limit=20', null, signal); }
      catch (e) { if (e.name === 'AbortError') return; listBody.replaceChildren(errorBox('Could not load workflows: ' + e.message, refresh)); return; }
      cache = data.workflows || [];
      render();
      clearTimeout(pollTimer);
      if (cache.some(w => w.status === 'running')) pollTimer = setTimeout(() => { if (location.hash.startsWith('#/workflows')) refresh(); }, 4000);
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

  // ---------- GPU page (Track U) ----------
  // Two charts, one table, and one honesty rule: when the prober cannot read the
  // driver the page says so and draws nothing. A flat 0% line would be a lie.
  function gpuPct(v) { return v == null ? '–' : Number(v).toFixed(0) + '%'; }
  function gpuGB(b) { return b == null ? '–' : (Number(b) / (1 << 30)).toFixed(2) + ' GB'; }

  // gpuChart is deliberately the same hand-rolled-SVG idea as trafficChart: no
  // library, one path, tokens only, y clamped to max so a bad value cannot
  // escape the box.
  // Hand-rolled SVG, tokens only. Every chart now has a scale, units, time
  // labels and a current-value dot; VRAM can draw its capacity and fill, and a
  // temperature chart can draw the throttle threshold (redesign guide, GPU).
  function gpuChart(points, key, max, opts) {
    opts = opts || {};
    const fmtV = opts.fmt || (v => String(Math.round(v)));
    const w = opts.vw || 760, hgt = 150, padL = 54, padR = 12, padT = 12, padB = 24;
    const NS = 'http://www.w3.org/2000/svg';
    const el = (t, a) => { const n = document.createElementNS(NS, t); for (const k in a) n.setAttribute(k, a[k]); return n; };
    const svg = el('svg', { viewBox: '0 0 ' + w + ' ' + hgt, class: 'tower-gpu-chart' });
    if (!points.length || !max) return svg;
    const plotW = w - padL - padR, plotH = hgt - padT - padB;
    const x = i => padL + i * plotW / Math.max(1, points.length - 1);
    const y = v => padT + (1 - Math.min(1, Math.max(0, v / max))) * plotH;

    [0, 0.5, 1].forEach(f => {
      svg.append(el('line', { x1: padL, x2: w - padR, y1: y(max * f), y2: y(max * f), stroke: 'var(--tower-border-soft)' }));
      const t = el('text', { x: padL - 8, y: y(max * f) + 3, 'text-anchor': 'end', fill: 'var(--tower-ink-faint)', 'font-size': 10 });
      t.textContent = fmtV(max * f); svg.append(t);
    });
    if (opts.cap) {
      svg.append(el('line', { x1: padL, x2: w - padR, y1: y(opts.cap), y2: y(opts.cap), stroke: 'var(--tower-ink-faint)', 'stroke-dasharray': '4 3' }));
    }
    if (opts.threshold != null) {
      svg.append(el('line', { x1: padL, x2: w - padR, y1: y(opts.threshold), y2: y(opts.threshold), stroke: 'var(--tower-amber-dim)', 'stroke-dasharray': '4 3' }));
      const t = el('text', { x: w - padR, y: y(opts.threshold) - 4, 'text-anchor': 'end', fill: 'var(--tower-ink-faint)', 'font-size': 10 });
      t.textContent = opts.thresholdLabel || 'threshold'; svg.append(t);
    }

    const pts = points.map((p, i) => x(i).toFixed(1) + ',' + y(Number(p[key]) || 0).toFixed(1));
    if (opts.area) {
      svg.append(el('polygon', { points: padL + ',' + y(0) + ' ' + pts.join(' ') + ' ' + (padL + plotW) + ',' + y(0), fill: 'var(--tower-amber-dim)', opacity: '.5' }));
    }
    svg.append(el('polyline', { points: pts.join(' '), fill: 'none', stroke: 'var(--tower-amber)', 'stroke-width': 2, 'vector-effect': 'non-scaling-stroke' }));

    const last = points[points.length - 1];
    const lv = Number(last[key]) || 0;
    svg.append(el('circle', { cx: x(points.length - 1), cy: y(lv), r: 3.5, fill: 'var(--tower-amber)' }));
    const vt = el('text', { x: x(points.length - 1) - 6, y: Math.max(10, y(lv) - 8), 'text-anchor': 'end', fill: 'var(--tower-ink)', 'font-size': 11, 'font-family': 'var(--tower-font-mono)' });
    vt.textContent = fmtV(lv); svg.append(vt);

    const t0 = el('text', { x: padL, y: hgt - 6, fill: 'var(--tower-ink-faint)', 'font-size': 10 });
    t0.textContent = fmt.time(points[0].at); svg.append(t0);
    const t1 = el('text', { x: w - padR, y: hgt - 6, 'text-anchor': 'end', fill: 'var(--tower-ink-faint)', 'font-size': 10 });
    t1.textContent = fmt.time(last.at); svg.append(t1);
    return svg;
  }

  // residentTable takes the snapshot's error as well as its list: with the
  // driver up and Ollama unreachable the list is empty and err says why, so
  // "nothing resident" would claim an idle GPU we were never able to observe.
  function residentTable(list, err) {
    if (!list.length) {
      return h('div', { class: 'tower-empty' }, err
        ? 'Resident models unknown — ' + err
        : 'Nothing resident. Ollama unloads a model once its keep-alive expires.');
    }
    return h('table', { class: 'tower-table' },
      h('thead', null, h('tr', null, ...['model', 'VRAM', 'context', 'expires'].map(t => h('th', null, t)))),
      h('tbody', null, list.map(m => h('tr', null,
        h('td', null, chip(m.name)),
        h('td', { class: 'tower-mono' }, gpuGB(m.size_vram_bytes)),
        h('td', { class: 'tower-mono' }, m.context_length || '–'),
        h('td', { class: 'tower-mono' }, fmt.date(m.expires_at))))));
  }

  let gpuRange = '6h';
  async function pageGPU(signal, _sub, polling) {
    if (!polling) content.replaceChildren(skeleton(6));
    let data;
    try { data = await api('/v1/gpu?since=' + gpuRange, null, signal); }
    catch (e) { if (e.name === 'AbortError') return; content.replaceChildren(errorBox('Could not load GPU: ' + e.message, () => route())); return; }
    const now = data.now || {};
    if (!now.available) {
      // No driver: one tall empty state and no charts at all. Zeroed charts
      // would invent a reading the prober never took.
      content.replaceChildren(panel('GPU', 'What local inference does to this machine',
        h('div', { class: 'tower-empty tower-empty-tall' },
          h('div', { class: 'tower-empty-title' }, 'No GPU telemetry'),
          now.error || 'the prober is not running',
          h('div', { class: 'tower-empty-actions' },
            h('span', { class: 'tower-panel-sub' }, 'Start the server on a machine with an NVIDIA driver, or set GPU_SMI to the nvidia-smi path.')))));
    } else {
      const s = now.sample || {};
      const hist = (data.history || []).slice().reverse(); // the store returns newest-first
      const histErr = data.history_error || '';
      const histEmpty = msg => h('div', { class: 'tower-empty' },
        histErr ? 'History unavailable - ' + histErr : msg);

      const metrics = h('div', { class: 'tower-metric-row' },
        metric('Utilisation', gpuPct(s.util_pct), s.name || ''),
        metric('VRAM', gpuGB(s.mem_used_bytes) + ' / ' + gpuGB(s.mem_total_bytes),
          gpuPct(s.mem_total_bytes ? s.mem_used_bytes * 100 / s.mem_total_bytes : null)),
        metric('Power', s.power_w == null ? '-' : Number(s.power_w).toFixed(0) + ' W', 'driver reading'),
        metric('Temperature', s.temp_c == null ? '-' : Number(s.temp_c).toFixed(0) + ' C', fmt.time(now.at)));

      const rangeSeg = h('div', { class: 'tower-seg' },
        ...[['1h', '1 h'], ['6h', '6 h'], ['24h', '24 h']].map(([k, label]) =>
          h('button', {
            class: gpuRange === k ? 'on' : '',
            onclick: () => { if (gpuRange === k) return; gpuRange = k; clearTimeout(pollTimer); pageGPU(signal, '', true); },
          }, label)));

      const swaps = now.swaps_last_10m || 0;
      const swapCount = swaps + ' model swap' + (swaps === 1 ? '' : 's');
      const residentSub = now.error
        ? 'Ollama unreachable - ' + swapCount + ' observed in the last 10 minutes'
        : swapCount + ' in the last 10 minutes';

      const caps = [
        panel('Utilisation', 'One sample per poll - last ' + gpuRange,
          hist.length ? gpuChart(hist, 'util_pct', 100, { fmt: v => v.toFixed(0) + '%' })
            : histEmpty('No history yet - the first sample lands within one poll interval')),
        panel('Temperature', 'Driver reading - dashed line = 85 C throttle threshold',
          hist.length ? gpuChart(hist, 'temp_c', 100, { fmt: v => v.toFixed(0) + ' C', threshold: 85, thresholdLabel: '85 C' })
            : histEmpty('No history yet')),
        panel('Power', 'Driver reading',
          hist.length ? gpuChart(hist, 'power_w', Math.max(50, ...hist.map(p => Number(p.power_w) || 0)) * 1.15, { fmt: v => v.toFixed(0) + ' W' })
            : histEmpty('No history yet')),
      ];
      const right = [
        panel('VRAM used', 'Dashed line = the ' + gpuGB(s.mem_total_bytes) + ' this card has',
          hist.length ? gpuChart(hist, 'mem_used_bytes', s.mem_total_bytes, { fmt: gpuGB, area: true, cap: s.mem_total_bytes, vw: 420 })
            : histEmpty('No history yet')),
        panel('Resident models', residentSub, residentTable(now.resident || [], now.error)),
      ];

      const nodes = [metrics,
        h('div', { class: 'tower-form-row', style: 'justify-content:flex-end' }, rangeSeg),
        h('div', { class: 'tower-gpu-cols' },
          h('div', { class: 'tower-gpu-col' }, ...caps),
          h('div', { class: 'tower-gpu-col' }, ...right))];
      if (swaps > 2) {
        nodes.push(h('div', { class: 'tower-note' },
          'Ollama swapped models ' + swaps + ' times in the last 10 minutes. Every swap unloads one model to load another, so a run that straddles one is not a fair speed comparison - see docs/VRAM.md.'));
      }
      if (s.throttled) {
        nodes.splice(1, 0, h('div', { class: 'tower-note' },
          pill('degraded', 'throttled'),
          " The driver has cut this card's clocks for heat. Answers are unaffected, but work timed now is slower than the same work on a cool card, so do not compare this run with a quiet one."));
      }
      content.replaceChildren(...nodes);
    }
    pollTimer = setTimeout(() => { if (location.hash.startsWith('#/gpu')) pageGPU(signal, '', true); }, 5000);
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
  // Cockpit: one conversation surface. The gateway stays stateless, so history
  // is client-held and resent each turn (ADR-0010), trimmed by a token budget.
  // "Run as workflow" posts the last input to the toy runner; both paths pin
  // their trace amber on the rail.
  const coHistBudget = 4000;
  function coEstTokens(s) { return Math.ceil(String(s).length / 4); }
  function coHistTokens(ms) { return ms.reduce((n, m) => n + coEstTokens(m.content), 0); }
  let coHist = [];        // [{role, content, model, backend, reason, trace_id, tokens, at}]
  let coDropped = 0;      // messages trimmed for the token budget
  let coPromise = false;  // a send is in flight
  let coPin = null;       // trace_id of the latest own send
  let coConvId = null;    // active stored thread, or null for a fresh one
  let coConvTitle = '';   // its title, so the panel head can say which thread
  let coLastInput = '';
  let coWorkflow = null;  // id of a workflow started from this input
  async function pageCockpit(signal) {
    content.classList.add('is-cockpit');
    const mainEl = document.querySelector('.tower-main');
    if (mainEl) mainEl.classList.add('is-cockpit');

    // ---- conversation column ----
    const ta = h('textarea', {
      class: 'tower-chat-input', rows: '2', 'aria-label': 'Message the router',
      placeholder: 'Ask a question, or paste the clause you are working on…',
    });
    const sendBtn = h('button', { class: 'tower-send', type: 'button', 'aria-label': 'Send message', disabled: 'disabled' }, '\u2191');
    const wfBtn = h('button', { class: 'tower-btn ghost', type: 'button' }, 'Run as workflow');
    const tokensHint = h('span', { class: 'tower-chat-tokens tower-mono' }, '~0 tokens');
    const errSlot = h('div', null);
    const log = h('div', { class: 'tower-chat-log' });
    const titleEl = h('h2', { class: 'tower-chat-title' }, 'New thread');
    const savedEl = h('span', { class: 'tower-chat-saved' }, '');

    const convList = h('div', { class: 'tower-conv' }, skeleton(2));
    const threadsBtn = h('button', { class: 'tower-btn ghost', type: 'button', 'aria-haspopup': 'true' }, 'Threads \u25be');
    const popNew = h('button', { class: 'tower-btn secondary', type: 'button' }, '\uff0b New thread');
    const pop = h('div', { class: 'tower-thread-pop' }, popNew, convList);
    const threadWrap = h('div', { class: 'tower-thread-wrap' }, threadsBtn, pop);
    const newBtn = h('button', { class: 'tower-btn ghost tower-btn-amber', type: 'button' }, '\uff0b New');

    const listBody = h('div', null, skeleton(5));
    const dot = h('span', { class: 'tower-live-dot' });
    const label = h('span', null, '');

    const composer = h('div', { class: 'tower-chat-composer' }, ta,
      h('div', { class: 'tower-chat-actions' }, wfBtn,
        h('span', { class: 'tower-spacer' }), tokensHint, sendBtn));
    const head = h('div', { class: 'tower-chat-head' },
      h('div', { class: 'tower-chat-heading' }, titleEl, savedEl),
      h('div', { class: 'tower-form-row' }, threadWrap, newBtn));
    const scroller = h('div', { class: 'tower-chat-scroll' },
      h('div', { class: 'tower-chat-col' }, log, errSlot));
    const mainc = h('section', { class: 'tower-cockpit-main' }, head, scroller,
      h('div', { class: 'tower-chat-dock' }, h('div', { class: 'tower-chat-col' }, composer)));

    // ---- inspector ----
    const inspStats = h('div', { class: 'tower-stat-grid' }, skeleton(2));
    const inspSpans = h('div', null, skeleton(2));
    const inspRuns = h('div', null, skeleton(2));
    const inspSub = h('div', { class: 'tower-panel-sub' }, 'Every calculation in this thread');
    const runsLabel = h('div', { class: 'tower-insp-label' }, 'Runs');
    const insp = h('aside', { class: 'tower-insp' },
      h('div', { class: 'tower-insp-head' }, h('h3', null, 'Inspector'), inspSub),
      inspStats,
      h('div', { class: 'tower-insp-label' }, 'Time by span (all runs)'),
      inspSpans,
      runsLabel,
      inspRuns,
      h('details', { class: 'tower-insp-more' }, h('summary', null, 'Live spans'),
        h('div', null,
          h('div', { class: 'tower-mono tower-muted', style: 'margin-bottom:8px;display:flex;gap:8px;align-items:center' }, dot, label),
          listBody)));

    content.replaceChildren(h('div', { class: 'tower-workbench tower-workbench-full' }, mainc, insp));

    // The one shared formatter (see fmt.dur): 1.35 s / 828 ms / <1 ms.
    const dur = fmt.dur;

    const coTraceCache = new Map();
    let inspSeq = 0;

    function traceRows(id) {
      const t = coTraceCache.get(id);
      if (!t) return null;
      return buildTimeline(t.spans || []).rows;
    }
    function runFor(id) {
      const rows = traceRows(id);
      if (!rows) return null;
      const total = rows.reduce((a, r) => a + r.dur / 1000, 0);
      const gen = rows.filter(r => r.span.name === 'model.generate').reduce((a, r) => a + r.dur / 1000, 0);
      return { rows, total, gen, spans: (coTraceCache.get(id).spans || []).length };
    }

    function stat(v, caption) {
      return h('div', { class: 'tower-stat' }, h('b', null, String(v)), h('small', null, caption));
    }
    function spanRow(name, secs, max) {
      return h('div', { class: 'tower-span-row' },
        h('span', null, name),
        h('div', { class: 'tower-span-bar' }, h('i', { style: 'width:' + Math.max(3, (secs / max) * 100) + '%' })),
        h('span', { class: 'tower-mono' }, dur(secs)));
    }
    function spanChip(id) {
      const r = runFor(id);
      return h('button', { class: 'tower-chip tower-chip-spans', type: 'button', 'data-spans': id },
        r ? '\u25cf ' + r.spans + ' spans \u00b7 ' + dur(r.total) : '\u25cf spans \u00b7 \u2026');
    }
    function focusRun(id) {
      const el = document.getElementById('run-' + id);
      if (!el) return;
      el.scrollIntoView({ block: 'nearest' });
      // "latest" marks the newest run permanently; "flash" is the transient
      // answer to a click, so the two never fight over the same border.
      el.classList.add('flash');
      setTimeout(() => el.classList.remove('flash'), 1400);
    }
    log.addEventListener('click', e => {
      const b = e.target.closest('[data-spans]');
      if (b) focusRun(b.dataset.spans);
    });

    function refreshFooters() {
      log.querySelectorAll('[data-spans]').forEach(el => {
        const r = runFor(el.dataset.spans);
        if (r) el.textContent = '\u25cf ' + r.spans + ' spans \u00b7 ' + dur(r.total);
      });
    }

    async function renderInspector() {
      const me = ++inspSeq;
      const answers = coHist.map((m, i) => ({ m, i }))
        .filter(x => x.m.role === 'assistant' && x.m.trace_id);
      const missing = answers.filter(x => !coTraceCache.has(x.m.trace_id));
      await Promise.all(missing.map(async x => {
        try { coTraceCache.set(x.m.trace_id, await api('/v1/traces/' + encodeURIComponent(x.m.trace_id))); }
        catch (e) { coTraceCache.set(x.m.trace_id, null); }
      }));
      if (me !== inspSeq || !insp.isConnected) return;
      refreshFooters();

      const runs = answers.map(x => ({ x, r: runFor(x.m.trace_id) })).filter(o => o.r);
      const totals = runs.map(o => o.r.total).sort((a, b) => a - b);
      const sorted = totals.slice();
      const median = sorted.length
        ? (sorted.length % 2 ? sorted[(sorted.length - 1) / 2] : (sorted[sorted.length / 2 - 1] + sorted[sorted.length / 2]) / 2)
        : null;
      const compute = sorted.reduce((a, b) => a + b, 0);
      const inTok = coHist.reduce((n, m) => n + (Number(m.prompt_tokens) || 0), 0);
      const outTok = coHist.reduce((n, m) => n + (Number(m.completion_tokens) || 0), 0);
      const genTime = runs.reduce((a, o) => a + o.r.gen, 0);
      const tps = genTime > 0 ? outTok / genTime : null;
      const tokValue = inTok + outTok;
      const tokCaption = inTok > 0 ? inTok + ' in \u00b7 ' + outTok + ' out' : 'output tokens';

      inspStats.replaceChildren(
        stat(coHist.length, 'messages'),
        stat(tokValue, tokCaption),
        stat(runs.length ? dur(compute) : '\u2013', 'total compute'),
        stat(median != null ? dur(median) : '\u2013', 'p50 latency'),
        stat(tps != null ? tps.toFixed(1) : '\u2013', 'tokens / s'),
        stat('\u2013', 'avg faithfulness'));

      const agg = {};
      runs.forEach(o => o.r.rows.forEach(row => {
        const secs = row.dur / 1000;
        agg[row.span.name] = (agg[row.span.name] || 0) + secs;
      }));
      const max = Math.max(0.001, ...Object.values(agg));
      inspSpans.replaceChildren(...(Object.keys(agg).length
        ? Object.entries(agg).sort((a, b) => b[1] - a[1]).map(([n, s]) => spanRow(n, s, max))
        : [h('div', { class: 'tower-empty' }, 'No runs yet - send a message and its spans appear here.')]));

      runsLabel.textContent = 'Runs \u00b7 ' + runs.length;
      inspRuns.replaceChildren(...(runs.length
        ? runs.slice().reverse().map((o, k) => {
          const r = o.r;
          const rmax = Math.max(0.001, ...r.rows.map(x => x.dur / 1000));
          const m = o.x.m;
          const toks = Number(m.prompt_tokens) > 0
            ? (m.prompt_tokens + '\u2192' + (Number(m.completion_tokens) || 0) + ' tok')
            : ((Number(m.completion_tokens) || Number(m.tokens) || 0) + ' tok');
          const meta = [m.model || '', toks].filter(Boolean).join(' \u00b7 ');
          const card = h('div', { id: 'run-' + m.trace_id, class: 'tower-run-card' + (k === 0 ? ' latest' : '') },
            h('div', { class: 'tower-run-head' },
              h('span', { class: 'tower-mono tower-run-id' }, fmt.short(m.trace_id)),
              h('span', { class: 'tower-mono tower-muted' }, fmt.time(m.at))),
            h('div', { class: 'tower-run-meta' }, meta),
            ...r.rows.map(row => spanRow(row.span.name, row.dur / 1000, rmax)));
          card.addEventListener('click', () => {
            const el = document.getElementById('co-m' + o.x.i);
            if (!el) return;
            el.scrollIntoView({ block: 'center' });
            el.classList.add('latest');
            setTimeout(() => el.classList.remove('latest'), 1400);
          });
          return card;
        })
        : [h('div', { class: 'tower-empty' }, 'Nothing measured yet.')]));
    }

    function autosize() {
      ta.style.height = 'auto';
      ta.style.height = Math.min(180, Math.max(46, ta.scrollHeight)) + 'px';
    }
    function syncComposer() {
      const v = ta.value;
      tokensHint.textContent = '~' + (v.trim() ? Math.max(1, Math.ceil(v.length / 4)) : 0) + ' tokens';
      sendBtn.disabled = coPromise || !v.trim();
      wfBtn.disabled = coPromise || !coLastInput;
    }
    function emptyState() {
      const prompts = [
        'Summarise what this gateway does in three bullets',
        'Which model would handle a long contract clause, and why?',
        'Draft a release checklist for this week',
      ];
      return h('div', { class: 'tower-chat-empty' },
        h('div', { class: 'tower-chat-empty-title' }, 'What are we working on?'),
        h('div', { class: 'tower-panel-sub' }, 'Everything you run here is measured in the inspector on the right. History is resent each turn; prompts are never stored.'),
        h('div', { class: 'tower-chat-suggest' }, prompts.map(p => {
          const b = h('button', { class: 'tower-chip tower-chip-btn', type: 'button' }, p);
          b.addEventListener('click', () => { ta.value = p; autosize(); syncComposer(); ta.focus(); });
          return b;
        })));
    }
    function msgEl(m, i) {
      if (m.role === 'user') {
        return h('div', { id: 'co-m' + i, class: 'tower-chat-msg user', title: m.at ? fmt.date(m.at) : '' }, m.content);
      }
      const bits = [];
      if (m.model) bits.push(chip(m.model));
      if (m.backend) bits.push(chip(m.backend));
      const t = Number(m.tokens) || ((Number(m.prompt_tokens) || 0) + (Number(m.completion_tokens) || 0));
      if (t) bits.push(h('span', { class: 'tower-chat-tok' }, t + ' tok'));
      if (m.trace_id) bits.push(spanChip(m.trace_id));
      if (m.trace_id) bits.push(h('a', { class: 'tower-link tower-mono', href: '#/traces/' + m.trace_id }, fmt.short(m.trace_id)));
      return h('div', { id: 'co-m' + i, class: 'tower-chat-msg assistant' },
        h('div', { class: 'tower-chat-body' }, m.content),
        bits.length ? h('div', { class: 'tower-chat-meta' }, ...bits) : null);
    }
    function pendingEl() {
      return h('div', { class: 'tower-chat-msg assistant pending' },
        h('span', { class: 'tower-typing' }, h('i'), h('i'), h('i')));
    }
    function renderChat() {
      const t = coConvTitle || (coHist.find(m => m.role === 'user') || {}).content || 'New thread';
      titleEl.textContent = t;
      titleEl.title = t;
      savedEl.textContent = coConvId ? 'saved' : 'not saved yet';
      inspSub.textContent = 'Every calculation in \u201c' + t + '\u201d';

      const nodes = coHist.length ? coHist.map((m, i) => msgEl(m, i)) : [emptyState()];
      if (coPromise) nodes.push(pendingEl());
      if (coWorkflow) nodes.push(h('div', { class: 'tower-chat-msg assistant' },
        h('div', { class: 'tower-chat-body' }, 'Started on the same input - it runs as a durable ticket.'),
        h('div', { class: 'tower-chat-meta' }, chip('workflow'),
          h('a', { class: 'tower-link tower-mono', href: '#/traces/' + coWorkflow }, coWorkflow))));
      log.replaceChildren(...nodes);
      scroller.scrollTop = scroller.scrollHeight;
      syncComposer();
      renderInspector();
    }

    async function send() {
      const text = ta.value.trim();
      if (!text || coPromise) return;
      ta.value = ''; autosize(); errSlot.replaceChildren();
      const mine = { role: 'user', content: text, at: new Date().toISOString() };
      coHist.push(mine);
      const wire = coHist.map(m => ({ role: m.role, content: m.content }));
      coDropped = 0;
      while (wire.length > 1 && coHistTokens(wire) > coHistBudget) { wire.shift(); coDropped++; }
      coLastInput = text; coPromise = true;
      syncComposer(); renderChat();
      const ctl = new AbortController();
      let convId = coConvId;
      if (!convId) {
        try { convId = (await api('/v1/conversations', { method: 'POST', body: JSON.stringify({ model: '' }) }, ctl.signal)).id; }
        catch (e) { convId = null; }
      }
      try {
        const r = await api('/v1/chat/completions', { method: 'POST', body: JSON.stringify({ messages: wire }) }, ctl.signal);
        const pt = r.usage ? r.usage.prompt_tokens : 0;
        const ct = r.usage ? r.usage.completion_tokens : 0;
        coHist.push({
          role: 'assistant', content: r.text, model: r.model, backend: r.backend, reason: r.reason,
          trace_id: r.trace_id, tokens: (r.usage ? r.usage.total_tokens : 0),
          prompt_tokens: pt, completion_tokens: ct, at: new Date().toISOString(),
        });
        coPin = r.trace_id;
        if (convId) {
          coConvId = convId;
          if (!coConvTitle) coConvTitle = text.slice(0, 48);
          recordTurn(convId, { role: 'user', content: text, trace_id: r.trace_id }).then(() =>
            recordTurn(convId, {
              role: 'assistant', content: r.text, model: r.model, trace_id: r.trace_id,
              prompt_tokens: pt, completion_tokens: ct,
            }).then(() => { if (convList.isConnected) loadConvs(); }));
        }
      } catch (e) {
        if (e.name !== 'AbortError') {
          if (coHist[coHist.length - 1] === mine) coHist.pop();
          ta.value = text; autosize();
          errSlot.replaceChildren(errorBox('Send failed: ' + e.message, send));
        }
      } finally {
        coPromise = false;
        if (log.isConnected) { renderChat(); }
      }
    }
    async function runWorkflow() {
      if (!coLastInput || coPromise) return;
      wfBtn.disabled = true;
      try {
        const r = await api('/v1/workflows', { method: 'POST', body: JSON.stringify({ input: coLastInput }) });
        toast('Workflow ' + r.id + ' started');
        coWorkflow = r.id; renderChat();
      } catch (e) { toast(e.message, true); }
      if (wfBtn.isConnected) wfBtn.disabled = false;
    }
    async function recordTurn(convId, m) {
      try { await api('/v1/conversations/' + convId + '/messages', { method: 'POST', body: JSON.stringify(m) }); }
      catch (e) { toast('Thread not stored: ' + e.message, true); }
    }
    async function loadConvs() {
      let data;
      try { data = await api('/v1/conversations?limit=50'); }
      catch (e) { convList.replaceChildren(errorBox('Threads unavailable: ' + e.message, loadConvs)); return; }
      const convs = data.conversations || [];
      convList.replaceChildren(...(convs.length ? convs.map(c => {
        const del = h('button', { class: 'tower-conv-del', type: 'button', title: 'delete thread', 'aria-label': 'Delete thread' }, '\u00d7');
        del.addEventListener('click', ev => { ev.stopPropagation(); deleteConv(c.id); });
        const el = h('div', { class: 'tower-conv-item' + (c.id === coConvId ? ' active' : '') },
          h('div', { class: 'tower-conv-title' }, c.title || '(untitled)'),
          h('div', { class: 'tower-conv-meta' }, chip(c.messages + ' msgs'), h('span', null, fmt.date(c.updated_at)), del));
        el.addEventListener('click', () => openConv(c.id, c.title));
        return el;
      }) : [h('div', { class: 'tower-empty' }, 'No threads yet - send one.')]));
    }
    async function openConv(id, title) {
      if (coPromise) return;
      let data;
      try { data = await api('/v1/conversations/' + id + '/messages?limit=500'); }
      catch (e) { toast(e.message, true); return; }
      coConvId = id; coConvTitle = title || ''; coDropped = 0; coWorkflow = null;
      pop.classList.remove('open');
      const msgs = data.messages || [];
      coHist = msgs.map(m => ({
        role: m.role, content: m.content, model: m.model, trace_id: m.trace_id,
        tokens: (m.prompt_tokens || 0) + (m.completion_tokens || 0),
        prompt_tokens: m.prompt_tokens || 0, completion_tokens: m.completion_tokens || 0,
        at: m.created_at,
      }));
      const users = coHist.filter(m => m.role === 'user');
      coLastInput = users.length ? users[users.length - 1].content : '';
      const traces = msgs.map(m => m.trace_id).filter(t => t);
      coPin = traces.length ? traces[traces.length - 1] : null;
      errSlot.replaceChildren(); syncComposer(); renderChat(); loadConvs(); ta.focus();
    }
    async function newThread() {
      if (coPromise) return;
      // Already on an empty new thread: do not create a second empty row.
      if (coConvId && !coHist.length) { pop.classList.remove('open'); ta.focus(); return; }
      coConvId = null; coConvTitle = ''; coHist = []; coDropped = 0; coPin = null; coLastInput = ''; coWorkflow = null;
      pop.classList.remove('open');
      errSlot.replaceChildren(); syncComposer(); renderChat(); loadConvs(); ta.focus();
      // Threads live server-side, so a new one is persisted now: it must appear in
      // the Threads list and the previous one must keep its runs (audit checklist).
      try {
        const c = await api('/v1/conversations', { method: 'POST', body: JSON.stringify({ model: '' }) });
        if (!coConvId && !coHist.length && log.isConnected) { coConvId = c.id; renderChat(); loadConvs(); }
      } catch (e) { /* drafting still works without storage */ }
    }
    async function deleteConv(id) {
      try { await api('/v1/conversations/' + id, { method: 'DELETE' }); }
      catch (e) { toast(e.message, true); return; }
      toast('Thread deleted');
      if (id === coConvId) newThread();
      else loadConvs();
    }
    ta.addEventListener('input', () => { autosize(); syncComposer(); });
    ta.addEventListener('keydown', e => {
      if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); send(); }
    });
    sendBtn.addEventListener('click', send);
    wfBtn.addEventListener('click', runWorkflow);
    newBtn.addEventListener('click', newThread);
    popNew.addEventListener('click', newThread);
    threadsBtn.addEventListener('click', e => { e.stopPropagation(); pop.classList.toggle('open'); });
    const closePop = e => {
      if (!threadWrap.isConnected) { document.removeEventListener('click', closePop); return; }
      if (!threadWrap.contains(e.target)) pop.classList.remove('open');
    };
    document.addEventListener('click', closePop);
    pop.addEventListener('keydown', e => { if (e.key === 'Escape') pop.classList.remove('open'); });

    const feed = () => { railCleanup = mountRail(listBody,
      (nt, ns) => { label.textContent = nt + ' traces \u00b7 ' + ns + ' spans'; },
      () => coPin,
      msg => listBody.replaceChildren(errorBox(msg, feed))); };
    feed();
    loadConvs();
    renderChat();
  }

  // ---------- router ----------
  const pages = { overview: pageOverview, traces: pageTraces, evals: pageEvals, benchmarks: pageBenchmarks, workflows: pageWorkflows, cockpit: pageCockpit, gpu: pageGPU, metrics: pageMetrics, models: pageModels };
  function route() {
    content.classList.remove('is-cockpit');
    const mainEl = document.querySelector('.tower-main');
    if (mainEl) mainEl.classList.remove('is-cockpit');
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
