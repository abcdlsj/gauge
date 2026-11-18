'use strict';

/* ---------- helpers ---------- */

// The desktop shell appends ?shell=desktop and gets a sidebar layout instead of
// the top navigation used by the browser build.
if (new URLSearchParams(location.search).get('shell') === 'desktop') {
  document.documentElement.dataset.shell = 'desktop';
}

const $ = (sel, root = document) => root.querySelector(sel);
const esc = (v) =>
  String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

const isCurrency = (unit) => /^[A-Z]{3}$/.test(unit || '');
const isDesktop = () => document.documentElement.dataset.shell === 'desktop';

// toneFor maps a remaining percentage onto the one colour language the panel uses.
function toneFor(pct) {
  if (pct <= 5) return 'danger';
  if (pct < 25) return 'warning';
  return 'ok';
}

function fmtNumber(v, unit) {
  if (unit === '%') return v.toLocaleString('en-US', { maximumFractionDigits: 1 });
  if (isCurrency(unit)) return v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  if (Math.abs(v) >= 1000) return v.toLocaleString('en-US', { maximumFractionDigits: 0 });
  return v.toLocaleString('en-US', { maximumFractionDigits: 2 });
}

function fmtDuration(seconds) {
  seconds = Math.max(0, Math.round(seconds));
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}

function refreshTickers() {
  const now = Date.now();
  document.querySelectorAll('[data-ago]').forEach((el) => {
    const ms = now - new Date(el.dataset.ago).getTime();
    el.textContent = Number.isNaN(ms) ? '—' : ms < 5000 ? 'just now' : `${fmtDuration(ms / 1000)} ago`;
  });
  document.querySelectorAll('[data-in]').forEach((el) => {
    const ms = new Date(el.dataset.in).getTime() - now;
    el.textContent = Number.isNaN(ms) ? '—' : ms <= 0 ? 'due' : fmtDuration(ms / 1000);

    // Countdowns can escalate while the page sits open.
    const warnUnder = Number(el.dataset.warnUnder || 0);
    const urgentUnder = Number(el.dataset.urgentUnder || 0);
    if (warnUnder) el.classList.toggle('soon', ms > 0 && ms <= warnUnder && !(urgentUnder && ms <= urgentUnder));
    if (urgentUnder) el.classList.toggle('urgent', ms > 0 && ms <= urgentUnder);
  });
  document.querySelectorAll('[data-duration]').forEach((el) => {
    el.textContent = fmtDuration(Number(el.dataset.duration) / 1000);
  });
}

let toastTimer = null;
function toast(message) {
  const el = $('#toast');
  el.textContent = message;
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, 2600);
}

async function api(path, options = {}) {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...options });
  const text = await res.text();
  let body = null;
  if (text) {
    try { body = JSON.parse(text); } catch { body = text; }
  }
  if (!res.ok) {
    const err = new Error((body && body.error) || res.statusText || `HTTP ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return body;
}

/* ---------- theme ---------- */

const THEMES = ['auto', 'light', 'dark'];

function applyTheme(theme) {
  const root = document.documentElement;
  if (theme === 'auto') delete root.dataset.theme;
  else root.dataset.theme = theme;
  localStorage.setItem('gauge-theme', theme);
}

function cycleTheme() {
  const current = localStorage.getItem('gauge-theme') || 'auto';
  const next = THEMES[(THEMES.indexOf(current) + 1) % THEMES.length];
  applyTheme(next);
  toast(`Theme: ${next}`);
}

const themeParam = new URLSearchParams(location.search).get('theme');
applyTheme(themeParam && THEMES.includes(themeParam) ? themeParam : localStorage.getItem('gauge-theme') || 'auto');
$('#theme').addEventListener('click', cycleTheme);

/* ---------- state ---------- */

let overview = null;

async function load() {
  try {
    overview = await api('/api/overview');
    everLoaded = true;
    $('#banner').hidden = true;
    render();
  } catch (err) {
    const banner = $('#banner');
    banner.textContent = `Cannot reach the panel API: ${err.message}`;
    banner.hidden = false;
  }
}

/* ---------- views ---------- */

// Routes: #/overview (the default; #/usage is the old name), #/source/<name>
// for one source, #/activity.
const SOURCE_ORDER = ['antigravity', 'commandcode', 'opendesign', 'colab', 'google-flow', 'gcp-monitoring', 'gcp-billing'];

function route() {
  const path = location.hash.replace(/^#\/?/, '');
  if (path === 'activity') return { view: 'activity' };
  const m = path.match(/^source\/(.+)$/);
  if (m) return { view: 'source', source: decodeURIComponent(m[1]) };
  return { view: 'usage' };
}

// sourceTabs lists the sources worth a tab: those with readings, or with a
// run that needs attention. Sources that were never set up stay out.
function sourceTabs() {
  if (!overview) return [];
  const withData = new Set((overview.quotas || []).map((q) => q.provider));
  const runs = new Map((overview.runs || []).map((r) => [r.provider, r]));
  return (overview.providers || [])
    .filter((p) => withData.has(p.name) || (runs.get(p.name) && !['unconfigured', 'ok', 'running'].includes(runs.get(p.name).status)))
    .sort((x, y) => (SOURCE_ORDER.indexOf(x.name) + 1 || 99) - (SOURCE_ORDER.indexOf(y.name) + 1 || 99));
}

function renderNav() {
  const nav = $('#nav');
  if (!nav || isDesktop()) return;
  const r = route();
  const link = (href, label, active) => `<a href="${href}"${active ? ' aria-current="page"' : ''}>${esc(label)}</a>`;
  nav.innerHTML = [
    link('#/overview', 'Overview', r.view === 'usage'),
    ...sourceTabs().map((p) => link(`#/source/${encodeURIComponent(p.name)}`, p.title, r.view === 'source' && r.source === p.name)),
    link('#/activity', 'Activity', r.view === 'activity'),
  ].join('');
}

function showView() {
  const r = route();
  for (const name of ['usage', 'source', 'activity']) {
    const el = $(`#view-${name}`);
    if (el) el.hidden = name !== r.view;
  }
  document.querySelectorAll('.dock-nav a[data-view]').forEach((a) => {
    if (a.dataset.view === r.view) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  });
  const title = document.querySelector('.board-title');
  if (title) title.textContent = r.view === 'activity' ? 'Activity' : 'Usage';
  renderNav();
  if (r.view === 'activity') renderActivity();
  if (r.view === 'source') renderSource(r.source);
  window.scrollTo(0, 0);
}

window.addEventListener('hashchange', showView);

/* ---------- render ---------- */

let firstRender = true;
let everLoaded = false;

function render() {
  if (!overview) return;
  if (firstRender) {
    // A restored webview can reopen mid-page; always start at the top.
    window.scrollTo(0, 0);
    firstRender = false;
  }
  renderAccounts();
  renderNav();
  renderUsage();
  const r = route();
  if (r.view === 'source') renderSource(r.source);
  renderActivity();
  renderSync();
  refreshTickers();
}

// renderAccounts shows one avatar per signed-in identity. The ring encodes the
// plan: multi-colour for Ultra, blue/green for Pro, grey otherwise.
function renderAccounts() {
  const accounts = overview.accounts || [];
  const root = $('#accounts');
  if (!accounts.length) {
    root.innerHTML = '';
    return;
  }
  root.innerHTML = accounts.map((a) => {
    const tier = a.tier || '';
    const kind = /ultra/i.test(tier) ? 'ultra' : /pro/i.test(tier) ? 'pro' : 'free';
    const initial = esc((a.name || a.email || '?').trim().charAt(0).toUpperCase());
    const face = a.picture
      ? `<img src="/api/avatar/${a.slot}" alt="${esc(a.name || a.email)}" loading="eager" onerror="this.replaceWith(Object.assign(document.createElement('span'),{className:'avatar-initial',textContent:'${initial}'}))" />`
      : `<span class="avatar-initial">${initial}</span>`;
    return `
      <span class="account" title="${esc((a.name ? a.name + ' · ' : '') + (a.email || ''))}">
        <span class="avatar" data-tier="${kind}">${face}</span>
        ${tier ? `<span class="account-tier">${esc(tier)}</span>` : ''}
      </span>`;
  }).join('');
}

function accountTag(email) {
  if (!email) return '';
  const short = email.split('@')[0];
  return `<span class="account-tag">${esc(short)}</span>`;
}

function renderProviders() {
  const runs = new Map((overview.runs || []).map((r) => [r.provider, r]));
  const counts = new Map();
  for (const q of overview.quotas || []) counts.set(q.provider, (counts.get(q.provider) || 0) + 1);

  $('#providers').innerHTML = (overview.providers || []).map((p) => {
    const run = runs.get(p.name);
    const status = run ? run.status : 'idle';
    const word = status === 'ok' ? (counts.get(p.name) ? 'live' : 'ok') : labelForStatus(status).toLowerCase();
    const next = run && run.next_run
      ? `<span>next <b data-in="${run.next_run}"></b></span>`
      : '<span>queued</span>';
    return `
      <div class="pcard">
        <div class="pcard-head">
          <span class="dot ${esc(status)}"></span>
          <span class="name">${esc(p.title)}</span>
          <span class="pstate ${esc(status)}">${esc(word)}</span>
        </div>
        <div class="pcard-meta"><span>every ${fmtDuration(p.interval_seconds)}</span>${next}</div>
      </div>`;
  }).join('');
}

function renderUsage() {
  const quotas = overview.quotas || [];
  const runs = new Map((overview.runs || []).map((r) => [r.provider, r]));
  const root = $('#view-usage');

  // The overview shows what the menu bar shows; source tabs show everything.
  const hidden = new Set(overview.hidden_cards || []);
  const byProvider = new Map();
  for (const q of quotas) {
    if (hidden.has(`${q.provider}/${q.metric}`)) continue;
    if (!byProvider.has(q.provider)) byProvider.set(q.provider, []);
    byProvider.get(q.provider).push(q);
  }

  const blocks = [];
  const compact = [];

  const ordered = (overview.providers || []).slice()
    .sort((x, y) => (SOURCE_ORDER.indexOf(x.name) + 1 || 99) - (SOURCE_ORDER.indexOf(y.name) + 1 || 99));
  for (const p of ordered) {
    const items = (byProvider.get(p.name) || []).slice().sort(compareSnapshots);
    const run = runs.get(p.name);

    if (isDesktop() && p.name !== 'antigravity') {
      if (!items.length) {
        const status = run ? run.status : 'idle';
        if (status === 'ok' || status === 'idle' || status === 'running') continue;
        compact.push(`<div class="compact-card notice"><span class="who">${esc(p.title)}</span>${esc(trim((run && run.error) || 'no readings yet', 90))}</div>`);
      } else {
        for (const sn of items) {
          if (sn.kind === 'note') {
            if (sn.note) compact.push(`<div class="compact-card"><span class="who">${esc(providerLabel(p, sn))}</span><div class="foot">${esc(sn.note)}</div></div>`);
            continue;
          }
          compact.push(renderCompactCard(p, sn));
        }
      }
      continue;
    }

    if (!items.length) {
      const status = run ? run.status : 'idle';
      if (status === 'ok' || status === 'idle' || status === 'running') continue;
      blocks.push(`
        <section class="group" id="group-${esc(p.name)}" data-title="${esc(p.title)}">
          ${groupHead(p, 0)}
          <article class="card notice" data-state="${esc(status)}">
            <div class="card-top">
              <span class="card-metric">status</span>
              <span class="chip ${esc(status)}">${esc(labelForStatus(status))}</span>
            </div>
            <p class="notice-text" title="${esc(run && run.error ? run.error : '')}">${esc(trim(run && run.error ? run.error : 'no readings yet', 260))}</p>
          </article>
        </section>`);
      continue;
    }

    const notes = items.filter((i) => i.kind === 'note');
    const cards = items.filter((i) => i.kind !== 'note');
    blocks.push(`
      <section class="group" id="group-${esc(p.name)}" data-title="${esc(p.title)}">
        ${groupHead(p, cards.length)}
        ${notes.map(renderGroupNote).join('')}
        <div class="grid">${cards.map(renderCard).join('')}</div>
      </section>`);
  }

  const side = [];
  if (compact.length) {
    side.push(`
      <section class="group">
        <div class="group-head"><h2>Other collectors</h2></div>
        <div class="compact-grid">${compact.join('')}</div>
      </section>`);
  }

  if (isDesktop() && everLoaded) {
    const events = (overview.events || []).slice(0, 5);
    const body = events.length
      ? events.map((e) => `
          <li class="${esc(e.level)}">
            <time data-ago="${e.time}"></time>
            <span class="who">${esc((overview.providers || []).find((p) => p.name === e.provider)?.title || e.provider)}</span>
            <span class="msg">${esc(e.message)}</span>
          </li>`).join('')
      : '<li class="empty">No state changes recorded yet.</li>';
    blocks.push(`
      <section class="group">
        <div class="group-head"><h2>Recent activity</h2></div>
        <div class="panel compact-log"><ul class="log">${body}</ul></div>
      </section>`);
  }

  if (isDesktop()) {
    root.innerHTML = blocks.length
      ? `<div class="col-main">${blocks.join('')}</div><div class="col-side">${side.join('')}</div>`
      : everLoaded
        ? `<div class="empty"><b>No snapshots yet</b>Collectors are running; readings appear after the first successful poll.</div>`
        : skeletonUsage();
    return;
  }

  root.innerHTML = blocks.concat(side).length
    ? blocks.concat(side).join('')
    : everLoaded
      ? `<div class="empty"><b>No snapshots yet</b>Collectors are running; readings appear after the first successful poll.</div>`
      : skeletonUsage();
}

// Lowest remaining ratio first; informational cards sink to the end.
function compareSnapshots(a, b) {
  const rank = (s) => (s.kind === 'note' || s.metric.startsWith('other') ? 1 : 0);
  if (rank(a) !== rank(b)) return rank(a) - rank(b);
  const ratio = (s) => (s.total > 0 ? s.remaining / s.total : 2);
  return ratio(a) - ratio(b);
}

// renderCompactCard is the dense one-line card used for the small providers.
function providerLabel(provider, sn) {
  return sn.metric === 'available_accelerators' ? `${provider.title} · accelerators` : provider.title;
}

// skeletonUsage holds the layout while the first payload is in flight.
function skeletonUsage() {
  const card = `
    <div class="card skeleton">
      <span class="sk-line" style="width:42%"></span>
      <span class="sk-line" style="width:68%;height:26px"></span>
      <span class="sk-block"></span>
      <span class="sk-line" style="width:52%"></span>
    </div>`;
  return `
    <section class="group">
      <div class="group-head"><h2><span class="dot"></span>&nbsp;</h2></div>
      <div class="grid">${card}${card}${card}</div>
    </section>`;
}

function renderCompactCard(provider, sn) {
  const total = sn.total || 0;
  const pct = total > 0 ? (sn.remaining / total) * 100 : null;
  const tone = pct !== null ? toneFor(pct) : (sn.status === 'error' || sn.status === 'expired_auth' ? 'danger' : '');
  const unit = sn.unit === '%' ? '%' : sn.unit || '';
  const reset = sn.reset_at ? `resets in <b data-in="${sn.reset_at}" data-warn-under="3600000" data-urgent-under="900000"></b>` : '';
  return `
    <div class="compact-card">
      <span class="who">${esc(provider.title)}</span>
      <div class="big">
        <b class="${tone === 'ok' ? '' : tone}">${fmtNumber(sn.remaining, sn.unit)}</b>
        <span>${esc(unit)}</span>
      </div>
      <div class="foot">${reset || `<b data-ago="${sn.updated_at}"></b>`}</div>
    </div>`;
}

function groupHead(provider, count) {
  const run = (overview.runs || []).find((r) => r.provider === provider.name);
  const status = run ? run.status : 'idle';
  const title = isDesktop()
    ? esc(provider.title)
    : `<a href="#/source/${encodeURIComponent(provider.name)}">${esc(provider.title)}</a>`;
  return `
    <div class="group-head">
      <h2><span class="dot ${esc(status)}"></span>${title}</h2>
    </div>`;
}

// settingsSection maps a provider onto its page in settings.html.
function settingsSection(name) {
  return name.startsWith('gcp-') ? 'gcp' : name;
}

// renderSource is one source's tab: its state, every card it produced
// (hidden ones included) and the actions that apply to it.
function renderSource(name) {
  const root = $('#view-source');
  if (!root || !overview) return;
  const p = (overview.providers || []).find((x) => x.name === name);
  if (!p) {
    root.innerHTML = `<div class="empty"><b>Unknown source</b>${esc(name)} is not collected by this panel.</div>`;
    return;
  }
  const run = (overview.runs || []).find((r) => r.provider === name);
  const status = run ? run.status : 'idle';
  const items = (overview.quotas || []).filter((q) => q.provider === name).sort(compareSnapshots);
  const notes = items.filter((i) => i.kind === 'note');
  const cards = items.filter((i) => i.kind !== 'note');
  const meta = [
    `every ${fmtDuration(p.interval_seconds)}`,
    run && run.last_run ? `last <b data-ago="${run.last_run}"></b>` : 'not run yet',
    run && run.next_run ? `next <b data-in="${run.next_run}"></b>` : '',
  ].filter(Boolean).join(' · ');
  const problem = run && run.error && status !== 'ok'
    ? `<article class="card notice" data-state="${esc(status)}">
         <div class="card-top"><span class="card-metric">last run</span><span class="chip ${esc(status)}">${esc(labelForStatus(status))}</span></div>
         <p class="notice-text">${esc(trim(run.error, 400))}</p>
       </article>`
    : '';
  root.innerHTML = `
    <section class="group">
      <div class="group-head source-head">
        <h2><span class="dot ${esc(status)}"></span>${esc(p.title)}</h2>
        <span class="hint">${meta}</span>
        <span class="source-actions">
          <a class="btn" href="/settings.html#${encodeURIComponent(settingsSection(name))}">Settings</a>
          <button class="btn" type="button" data-provider="${esc(name)}">Refresh</button>
        </span>
      </div>
      ${problem ? `<div class="grid">${problem}</div>` : ''}
      ${notes.map(renderGroupNote).join('')}
      ${cards.length ? `<div class="grid">${cards.map(renderCard).join('')}</div>` : problem ? '' : '<div class="empty"><b>No readings yet</b>They appear after the first successful poll.</div>'}
    </section>`;
  refreshTickers();
}

// renderGroupNote folds plan/tier information into a single line above the grid.
function renderGroupNote(sn) {
  return `<p class="group-note"><span class="chip ok">${esc(sn.unit || sn.label)}</span>${esc(sn.note || '')}</p>`;
}

function renderCard(sn) {
  if (sn.kind === 'note') return renderNoteCard(sn);
  if (sn.series && sn.series.length > 1) return renderSeriesCard(sn);

  const total = sn.total || 0;
  const pct = total > 0 ? Math.max(0, Math.min(100, (sn.remaining / total) * 100)) : 0;
  const tone = sn.status === 'error' || sn.status === 'expired_auth' ? 'danger'
    : total > 0 ? toneFor(pct) : 'ok';

  const unitLabel = sn.unit === '%'
    ? '% left'
    : total > 0
      ? `/ ${fmtNumber(total, sn.unit)} ${sn.unit}`.trim()
      : sn.unit;

  const chip = sn.status && sn.status !== 'ok'
    ? `<span class="chip ${esc(sn.status)}">${esc(labelForStatus(sn.status))}</span>`
    : '';

  const reset = sn.reset_at
    ? `<span>resets in <b data-in="${sn.reset_at}" data-warn-under="3600000" data-urgent-under="900000"></b></span>`
    : total > 0 ? `<span>${pct.toFixed(1)}% remaining</span>` : '<span>metered</span>';

  return `
    <article class="card" data-state="${esc(sn.status || 'ok')}">
      <div class="card-top">
        <span class="card-metric">${esc(sn.metric)}</span>
        ${chip}
      </div>
      <h3 class="card-title" title="${esc(sn.label)}">${accountTag(sn.account)}${esc(sn.label)}</h3>
      <div class="value">
        <span class="num ${tone === 'ok' ? '' : tone}">${fmtNumber(sn.remaining, sn.unit)}</span>
        <span class="unit">${esc(unitLabel)}</span>
      </div>
      ${total > 0 ? pixels(pct, tone) : ''}
      <div class="card-foot">
        ${reset}
        <span><b data-ago="${sn.updated_at}"></b></span>
      </div>
    </article>`;
}

// renderSeriesCard shows one bar per quota window (5-hour, weekly) in one card.
function renderSeriesCard(sn) {
  const rows = sn.series.map((s) => {
    const unit = s.unit || '%';
    const pct = s.total > 0 ? Math.max(0, Math.min(100, (s.remaining / s.total) * 100)) : 0;
    const tone = unit === '%' ? toneFor(pct) : 'ok';
    const tail = s.reset_at
      ? `<b data-in="${s.reset_at}" data-warn-under="3600000" data-urgent-under="900000"></b>`
      : `${pct.toFixed(pct < 10 ? 1 : 0)}%`;
    return `
      <div class="series-row">
        <div class="series-head">
          <span class="series-label">${esc(s.label || 'current')}</span>
          <span class="series-pct ${tone === 'ok' ? '' : tone}">${fmtNumber(s.remaining, unit)}<i>${unit === '%' ? '%' : esc(unit)}</i></span>
          <span class="series-reset">${tail}</span>
        </div>
        ${pixels(pct, tone, 24)}
      </div>`;
  }).join('');

  const chip = sn.status && sn.status !== 'ok'
    ? `<span class="chip ${esc(sn.status)}">${esc(labelForStatus(sn.status))}</span>` : '';

  return `
    <article class="card" data-state="${esc(sn.status || 'ok')}">
      <div class="card-top">
        <span class="card-metric">${esc(sn.metric)}</span>
        ${chip}
      </div>
      <h3 class="card-title" title="${esc(sn.label)}">${accountTag(sn.account)}${esc(sn.label)}</h3>
      <div class="series">${rows}</div>
      <div class="card-foot right">
        <span><b data-ago="${sn.updated_at}"></b></span>
      </div>
    </article>`;
}

// pixels renders the segmented panel bar used across the dashboard.
function pixels(pct, tone, segments = 24) {
  const lit = Math.round((pct / 100) * segments);
  let html = '';
  for (let i = 0; i < segments; i++) {
    html += `<i class="${i < lit ? 'on' : ''}"></i>`;
  }
  return `<div class="pixels" data-tone="${esc(tone)}" role="img" aria-label="${pct.toFixed(1)}% remaining">${html}</div>`;
}

function renderNoteCard(sn) {
  return `
    <article class="card">
      <div class="card-top"><span class="card-metric">${esc(sn.metric)}</span></div>
      <h3 class="card-title">${esc(sn.label)}</h3>
      <p class="note-value">${esc(sn.unit || '—')}</p>
      <p class="notice-text">${esc(sn.note || '')}</p>
      <div class="card-foot right"><span><b data-ago="${sn.updated_at}"></b></span></div>
    </article>`;
}

function renderActivity() {
  if (!overview) return;
  const runs = overview.runs || [];
  const providers = new Map((overview.providers || []).map((p) => [p.name, p]));

  $('#runs').innerHTML = runs.length ? runs.map((r) => {
    const meta = providers.get(r.provider);
    const title = meta ? meta.title : r.provider;
    const cadence = meta ? `every ${fmtDuration(meta.interval_seconds)}` : '';
    const last = r.last_run ? `last <b data-ago="${r.last_run}"></b>` : 'never';
    const next = r.next_run ? `next <b data-in="${r.next_run}"></b>` : '';
    return `
      <div class="run">
        <span class="dot ${esc(r.status)}"></span>
        <span class="run-name">${esc(title)}</span>
        <span class="chip ${esc(r.status)}">${esc(labelForStatus(r.status))}</span>
        <span class="run-meta">${esc(cadence)} · ${last}${next ? ' · ' + next : ''}</span>
        <span class="run-dur">${r.duration_ms ? r.duration_ms + 'ms' : ''}</span>
        ${r.error ? `<span class="run-error" title="${esc(r.error)}">${esc(trim(r.error, 160))}</span>` : ''}
      </div>`;
  }).join('') : '<p class="hint">No collector has run yet.</p>';

  const events = overview.events || [];
  $('#log').innerHTML = events.length ? events.slice(0, 40).map((e) => `
    <li class="${esc(e.level)}">
      <time data-ago="${e.time}"></time>
      <span class="who">${esc((providers.get(e.provider) || {}).title || e.provider)}</span>
      <span class="msg">${esc(e.message)}</span>
    </li>`).join('') : '<li class="empty">No state changes recorded yet.</li>';
}

function renderSync() {
  const times = (overview.quotas || []).map((q) => new Date(q.updated_at).getTime()).filter((n) => !Number.isNaN(n));
  const latest = times.length ? Math.max(...times) : null;
  $('#sync').innerHTML = latest ? `synced <b data-ago="${new Date(latest).toISOString()}"></b>` : 'no data yet';

  const footer = $('#footer-meta');
  if (!footer) return;
  const uptime = overview.started_at
    ? fmtDuration((Date.now() - new Date(overview.started_at).getTime()) / 1000)
    : '—';
  footer.textContent = `uptime ${uptime} · ${(overview.providers || []).length} collectors`;
}

function labelForStatus(status) {
  return {
    ok: 'ok',
    warning: 'low',
    error: 'error',
    expired_auth: 'auth expired',
    unconfigured: 'setup',
    running: 'running',
    idle: 'idle',
  }[status] || status;
}

function trim(text, max) {
  text = String(text).replace(/\s+/g, ' ').trim();
  return text.length > max ? text.slice(0, max - 1) + '…' : text;
}

/* ---------- actions ---------- */

async function refreshProvider(name, button) {
  if (button) button.disabled = true;
  try {
    const run = await api(`/api/refresh/${encodeURIComponent(name)}`, { method: 'POST' });
    toast(run && run.status && run.status !== 'ok' ? `${name}: ${run.error || labelForStatus(run.status)}` : `${name}: refreshed`);
  } catch (err) {
    toast(err.status === 429 ? `${name}: cooling down, try again in a minute` : `${name}: ${err.message}`);
  } finally {
    if (button) button.disabled = false;
    await load();
  }
}

async function refreshAll(button) {
  button.disabled = true;
  const label = button.textContent;
  button.textContent = 'Refreshing…';
  try {
    const res = await api('/api/refresh-all', { method: 'POST' });
    const runs = (res && res.runs) || [];
    const failed = runs.filter((r) => r.status && r.status !== 'ok' && r.status !== 'running');
    const skipped = runs.filter((r) => r.status === 'running');
    if (failed.length) toast(`${failed.length} collector(s) need attention`);
    else if (skipped.length) toast(`${skipped.length} collector(s) cooling down`);
    else toast('All collectors refreshed');
  } catch (err) {
    toast(`Refresh failed: ${err.message}`);
  } finally {
    button.disabled = false;
    button.textContent = label;
    await load();
  }
}

$('#refresh-all').addEventListener('click', (e) => refreshAll(e.currentTarget));
for (const id of ['#view-usage', '#view-source']) {
  const el = $(id);
  if (el) el.addEventListener('click', (e) => {
    const btn = e.target.closest('button[data-provider]');
    if (btn) refreshProvider(btn.dataset.provider, btn);
  });
}

/* ---------- boot ---------- */

showView();
load();
setInterval(load, 20000);
setInterval(refreshTickers, 1000);
document.addEventListener('visibilitychange', () => { if (!document.hidden) load(); });
