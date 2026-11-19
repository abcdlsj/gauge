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
  if (!el) return;
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

/* ---------- brand & family DNA (shared with popover & settings) ---------- */

const CLOCK_ICON = '<svg class="clock" viewBox="0 0 16 16" width="11" height="11" aria-hidden="true">' +
  '<circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="1.5"/>' +
  '<path fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" d="M8 4.8V8l2.2 1.3"/></svg>';

const REFRESH_ICON = '<svg viewBox="0 0 20 20" width="13" height="13" aria-hidden="true">' +
  '<path fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" d="M16.2 8.4A6.4 6.4 0 1 0 16 12.6M16.2 3.6v4.8h-4.8"/></svg>';

const GEAR_ICON = '<svg viewBox="0 0 20 20" width="13" height="13" aria-hidden="true">' +
  '<path fill="currentColor" d="M10 6.9A3.1 3.1 0 1 0 10 13.1 3.1 3.1 0 0 0 10 6.9Zm7 3.1c0-.4 0-.7-.1-1l1.4-1.1-1.5-2.6-1.7.6a7 7 0 0 0-1.7-1L13.1 3H9.9l-.3 1.9a7 7 0 0 0-1.7 1l-1.7-.6-1.5 2.6L6.1 9a7.6 7.6 0 0 0 0 2L4.7 12.1l1.5 2.6 1.7-.6c.5.4 1.1.8 1.7 1l.3 1.9h3.2l.3-1.9c.6-.2 1.2-.6 1.7-1l1.7.6 1.5-2.6-1.4-1.1c.1-.3.1-.6.1-1Z"/></svg>';

const SOURCE_META = {
  google: {
    tint: 'var(--gemini)',
    icon: '<svg viewBox="0 0 18 18" width="13" height="13" aria-hidden="true"><path fill="currentColor" d="M9 1.5l1.95 5.55L16.5 9l-5.55 1.95L9 16.5l-1.95-5.55L1.5 9l5.55-1.95Z"/></svg>',
  },
  commandcode: {
    tint: 'var(--commandcode)',
    icon: '<svg viewBox="0 0 18 18" width="13" height="13" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" d="M4.2 5.8 7.8 9l-3.6 3.2M9.2 12.2h4.4"/></svg>',
  },
  opendesign: {
    tint: 'var(--opendesign)',
    icon: '<svg viewBox="0 0 18 18" width="13" height="13" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.65" stroke-linejoin="round" d="M9 2.6 15.2 9 9 15.4 2.8 9Z"/><circle cx="9" cy="9" r="1.45" fill="currentColor"/></svg>',
  },
  gcp: {
    tint: 'var(--gcp)',
    icon: '<svg viewBox="0 0 18 18" width="13" height="13" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" d="M5.4 13.5h7.3a2.7 2.7 0 0 0 .4-5.37 4.1 4.1 0 0 0-7.9-1.15 3.1 3.1 0 0 0 .2 6.52Z"/></svg>',
  },
};

const FAMILY_GLYPHS = {
  gemini: '<svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path fill="currentColor" d="M8 0l1.9 5.6L15.5 7.5 9.9 9.4 8 15l-1.9-5.6L.5 7.5l5.6-1.9Z"/></svg>',
  claude: '<svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><g stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M8 1.5v13M2.4 4.75l11.2 6.5M13.6 4.75l-11.2 6.5"/></g></svg>',
  commandcode: '<svg viewBox="0 0 18 18" width="12" height="12" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" d="M4.2 5.8 7.8 9l-3.6 3.2M9.2 12.2h4.4"/></svg>',
  opendesign: '<svg viewBox="0 0 18 18" width="12" height="12" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" d="M9 2.6 15.2 9 9 15.4 2.8 9Z"/><circle cx="9" cy="9" r="1.5" fill="currentColor"/></svg>',
  gcp: '<svg viewBox="0 0 18 18" width="12" height="12" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" d="M5.4 13.5h7.3a2.7 2.7 0 0 0 .4-5.37 4.1 4.1 0 0 0-7.9-1.15 3.1 3.1 0 0 0 .2 6.52Z"/></svg>',
};

function baseKind(id) {
  return String(id || '').replace(/-\d+$/, '');
}

function sourceMetaFor(id) {
  return SOURCE_META[baseKind(id)] || { tint: 'var(--accent)', icon: SOURCE_META.google.icon };
}

function cardStyleFor(sn) {
  const provider = baseKind(sn.provider || '');
  const metric = sn.metric || '';
  if (provider === 'antigravity') {
    if (metric.startsWith('claude')) return { tint: 'var(--claude)', glyph: FAMILY_GLYPHS.claude };
    if (metric.startsWith('gemini')) return { tint: 'var(--gemini)', glyph: FAMILY_GLYPHS.gemini };
    return { tint: 'var(--gemini)', glyph: '' };
  }
  if (provider === 'commandcode') return { tint: 'var(--commandcode)', glyph: FAMILY_GLYPHS.commandcode };
  if (provider === 'opendesign') return { tint: 'var(--opendesign)', glyph: FAMILY_GLYPHS.opendesign };
  if (provider.startsWith('gcp')) return { tint: 'var(--gcp)', glyph: FAMILY_GLYPHS.gcp };
  return { tint: 'var(--gemini)', glyph: '' };
}

function planClass(detail) {
  if (/ultra/i.test(detail || '')) return 'ultra';
  if (/pro/i.test(detail || '')) return 'pro';
  return 'free';
}

function renderGroupEmblem(g, size = 'sm') {
  const meta = sourceMetaFor(g.id);
  const kind = baseKind(g.id);
  if (kind === 'google' && (g.account_name || g.email || g.slot)) {
    const tier = planClass(g.detail);
    const initial = esc((g.account_name || g.email || 'G').trim().charAt(0).toUpperCase());
    const slot = g.slot || 1;
    const face = g.slot
      ? `<img src="/api/avatar/${slot}" alt="" loading="eager" onerror="this.remove()" />`
      : '';
    return `
      <span class="g-avatar g-avatar-${size}" data-tier="${tier}">
        <span class="g-avatar-inner"><span class="g-avatar-init">${initial}</span>${face}</span>
      </span>`;
  }
  return `<span class="g-badge g-badge-${size}" style="--tint:${meta.tint}">${meta.icon}</span>`;
}

/* ---------- theme ---------- */

const THEMES = ['auto', 'light', 'dark'];
const THEME_LABELS = { auto: 'Follow system', light: 'Light', dark: 'Dark' };
const THEME_ICONS = {
  auto: '<svg viewBox="0 0 20 20" width="15" height="15" aria-hidden="true">' +
    '<circle cx="10" cy="10" r="6.2" fill="none" stroke="currentColor" stroke-width="1.6"/>' +
    '<path d="M10 3.8a6.2 6.2 0 0 1 0 12.4Z" fill="currentColor"/></svg>',
  light: '<svg viewBox="0 0 20 20" width="15" height="15" aria-hidden="true">' +
    '<circle cx="10" cy="10" r="3.6" fill="currentColor"/>' +
    '<g stroke="currentColor" stroke-width="1.6" stroke-linecap="round">' +
    '<path d="M10 2.2v1.8M10 16v1.8M2.2 10h1.8M16 10h1.8M4.5 4.5l1.3 1.3M14.2 14.2l1.3 1.3M15.5 4.5l-1.3 1.3M5.8 14.2l-1.3 1.3"/>' +
    '</g></svg>',
  dark: '<svg viewBox="0 0 20 20" width="15" height="15" aria-hidden="true">' +
    '<path d="M10 3.2a6.8 6.8 0 1 0 6.8 6.8A5.4 5.4 0 0 1 10 3.2Z" fill="currentColor"/></svg>',
};

function paintThemeButton(theme) {
  const btn = $('#theme');
  if (!btn) return;
  btn.innerHTML = THEME_ICONS[theme] || THEME_ICONS.auto;
  btn.title = `Theme: ${THEME_LABELS[theme] || theme}`;
  btn.setAttribute('aria-label', btn.title);
}

function applyTheme(theme) {
  const root = document.documentElement;
  if (theme === 'auto') delete root.dataset.theme;
  else root.dataset.theme = theme;
  localStorage.setItem('gauge-theme', theme);
  paintThemeButton(theme);
}

function cycleTheme() {
  const current = localStorage.getItem('gauge-theme') || 'auto';
  const next = THEMES[(THEMES.indexOf(current) + 1) % THEMES.length];
  applyTheme(next);
  toast(`Theme: ${THEME_LABELS[next]}`);
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

function route() {
  const path = location.hash.replace(/^#\/?/, '');
  if (path === 'activity') return { view: 'activity' };
  const m = path.match(/^source\/(.+)$/);
  if (m) return { view: 'source', source: decodeURIComponent(m[1]) };
  return { view: 'usage' };
}

function runOf(provider) {
  return (overview.runs || []).find((r) => r.provider === provider);
}

const SEVERITY = ['expired_auth', 'error', 'warning', 'running', 'ok', 'unconfigured', 'idle'];

function groupStatus(g) {
  const states = g.providers.map((p) => (runOf(p) || {}).status || 'idle');
  return states.sort((a, b) => SEVERITY.indexOf(a) - SEVERITY.indexOf(b))[0] || 'idle';
}

function snapshotOf(it) {
  return (overview.quotas || []).find((q) => q.provider === it.provider && q.metric === it.metric);
}

function sourceTabs() {
  if (!overview) return [];
  return (overview.layout || []).filter((g) => g.items.length
    || g.providers.some((p) => ['error', 'expired_auth', 'warning'].includes((runOf(p) || {}).status)));
}

function hiddenSet() {
  const out = new Set();
  for (const g of overview.layout || []) {
    for (const it of g.items) if (g.hidden || it.hidden) out.add(it.id);
  }
  return out;
}

function groupHandle(g, items) {
  if (g.email) return g.email;
  for (const it of items || g.items || []) {
    const sn = snapshotOf(it);
    if (sn && sn.account) return sn.account.split('@')[0];
  }
  return '';
}

function groupIssueSummary(g) {
  const issues = g.providers
    .map((p) => {
      const r = runOf(p);
      if (!r || !['expired_auth', 'error'].includes(r.status)) return null;
      const meta = (overview.providers || []).find((x) => x.name === p);
      const name = (meta && meta.title ? meta.title : p).replace(/^Google\s+/i, '');
      return { status: r.status, text: r.status === 'expired_auth' ? `${name} expired` : `${name} error` };
    })
    .filter(Boolean);
  return issues[0] || null;
}

function renderNav() {
  const nav = $('#nav');
  if (!nav || isDesktop()) return;
  const r = route();
  nav.querySelectorAll('a[data-view]').forEach((a) => {
    const active = (a.dataset.view === 'usage' && (r.view === 'usage' || r.view === 'source'))
      || (a.dataset.view === r.view);
    if (active) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  });
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

// Desktop dock still has an #accounts container; index.html renders accounts
// inline on each subscription header instead.
function renderAccounts() {
  const root = $('#accounts');
  if (!root) return;
  const accounts = overview.accounts || [];
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

function renderUsage() {
  const quotas = overview.quotas || [];
  const runs = new Map((overview.runs || []).map((r) => [r.provider, r]));
  const root = $('#view-usage');

  if (!isDesktop()) {
    renderGroups(root);
    return;
  }

  const hidden = hiddenSet();
  const byProvider = new Map();
  for (const q of quotas) {
    if (hidden.has(`${q.provider}/${q.metric}`)) continue;
    if (!byProvider.has(q.provider)) byProvider.set(q.provider, []);
    byProvider.get(q.provider).push(q);
  }

  const blocks = [];
  const compact = [];

  for (const p of overview.providers || []) {
    const items = (byProvider.get(p.name) || []).slice().sort(compareSnapshots);
    const run = runs.get(p.name);

    if (p.name !== 'antigravity') {
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
          ${groupHead(p)}
          <article class="card notice" data-state="${esc(status)}">
            <div class="card-head">
              <span class="card-title">${esc(p.title)}</span>
              <span class="pill is-${esc(status)}">${esc(labelForStatus(status))}</span>
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
        ${groupHead(p)}
        ${notes.map(renderGroupNote).join('')}
        <div class="grid">${cards.map((sn) => renderCard(sn)).join('')}</div>
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

  if (everLoaded) {
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

  root.innerHTML = blocks.length
    ? `<div class="col-main">${blocks.join('')}</div><div class="col-side">${side.join('')}</div>`
    : everLoaded
      ? `<div class="empty"><b>No snapshots yet</b>Collectors are running; readings appear after the first successful poll.</div>`
      : skeletonUsage();
}

function compareSnapshots(a, b) {
  const rank = (s) => (s.kind === 'note' || s.metric.startsWith('other') ? 1 : 0);
  if (rank(a) !== rank(b)) return rank(a) - rank(b);
  const ratio = (s) => (s.total > 0 ? s.remaining / s.total : 2);
  return ratio(a) - ratio(b);
}

function providerLabel(provider, sn) {
  return sn.metric === 'available_accelerators' ? `${provider.title} · accelerators` : provider.title;
}

function skeletonUsage() {
  const card = `
    <div class="card skeleton">
      <span class="sk-line" style="width:45%"></span>
      <span class="sk-line" style="width:65%;height:22px"></span>
      <span class="sk-block"></span>
    </div>`;
  return `
    <section class="group">
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

function groupHead(provider) {
  const run = runOf(provider.name);
  const status = run ? run.status : 'idle';
  return `
    <div class="group-head">
      <h2><span class="dot ${esc(status)}"></span>${esc(provider.title)}</h2>
    </div>`;
}

function problemCards(g, onlyWithoutData) {
  return g.providers.map((p) => {
    const run = runOf(p);
    if (!run || !run.error || ['ok', 'running', 'unconfigured'].includes(run.status)) return '';
    if (onlyWithoutData && (overview.quotas || []).some((q) => q.provider === p)) return '';
    const title = ((overview.providers || []).find((x) => x.name === p) || {}).title || p;
    return `
      <article class="card notice" data-state="${esc(run.status)}">
        <div class="card-head">
          <span class="card-title"><span class="dot ${esc(run.status)}"></span>${esc(title)}</span>
          <span class="pill is-${esc(run.status)}">${esc(labelForStatus(run.status))}</span>
        </div>
        <p class="notice-text" title="${esc(run.error)}">${esc(trim(run.error, 220))}</p>
        <div class="card-foot">
          <a class="inline-link" href="/settings.html#${encodeURIComponent(g.id)}/connection">Configure →</a>
          ${run.last_run ? `<span><b data-ago="${run.last_run}"></b></span>` : ''}
        </div>
      </article>`;
  }).join('');
}

function groupNotes(g, providers) {
  return (overview.quotas || [])
    .filter((q) => q.kind === 'note' && providers.has(q.provider) && g.providers.includes(q.provider))
    .map(renderGroupNote).join('');
}

function cardFor(it, opts = {}) {
  const sn = snapshotOf(it);
  return sn ? renderCard({ ...sn, label: it.label }, { ...opts, hidden: !!it.hidden }) : '';
}

function renderEmptyUsage() {
  const runs = overview.runs || [];
  const hasSetup = runs.some((r) => r.status === 'unconfigured' || r.status === 'expired_auth' || r.status === 'error');
  const checking = !hasSetup && runs.some((r) => r.status === 'running' || r.status === 'idle');
  if (checking) {
    return '<div class="empty"><b>Checking local accounts…</b>Looking for IDE, CLI and browser sessions. Readings appear as soon as the first poll completes.</div>';
  }
  return `
    <div class="empty">
      <b>Connect a source</b>
      Add Google AI, Command Code, Open Design or Google Cloud in Settings to start tracking quota.
      <div class="empty-actions">
        <a class="btn primary" href="/settings.html#add">Connect a source</a>
        <a class="btn" href="/settings.html#general/network">Network settings</a>
      </div>
    </div>`;
}

// renderGroups is the browser overview: visible subscriptions in the user's
// order, each with its visible cards in a compact, unified layout.
function renderGroups(root) {
  const blocks = (overview.layout || []).filter((g) => !g.hidden).map((g) => {
    const items = g.items.filter((it) => !it.hidden && snapshotOf(it));
    const problems = problemCards(g, true);
    if (!items.length && !problems) return '';
    const handle = groupHandle(g, items);
    const issue = groupIssueSummary(g);
    const plan = g.detail
      ? `<span class="plan-pill ${planClass(g.detail)}">${esc(g.detail)}</span>`
      : '';
    return `
      <section class="group" id="group-${esc(g.id)}">
        <div class="group-head">
          <div class="group-id">
            ${renderGroupEmblem(g, 'sm')}
            <a class="group-title" href="#/source/${encodeURIComponent(g.id)}">${esc(g.title)}</a>
            ${plan}
            ${handle ? `<span class="group-handle">${esc(handle)}</span>` : ''}
            ${issue ? `<a class="pill is-${esc(issue.status)}" href="#/source/${encodeURIComponent(g.id)}">${esc(issue.text)}</a>` : ''}
          </div>
          <div class="group-tools">
            <button class="tool-btn" type="button" data-providers="${esc(g.providers.join(','))}" title="Refresh ${esc(g.title)}" aria-label="Refresh ${esc(g.title)}">${REFRESH_ICON}</button>
            <a class="tool-btn" href="/settings.html#${encodeURIComponent(g.id)}" title="${esc(g.title)} settings" aria-label="${esc(g.title)} settings">${GEAR_ICON}</a>
          </div>
        </div>
        ${groupNotes(g, new Set(items.map((it) => it.provider)))}
        <div class="grid">${problems}${items.map((it) => cardFor(it)).join('')}</div>
      </section>`;
  }).filter(Boolean);

  root.innerHTML = blocks.length
    ? blocks.join('')
    : everLoaded
      ? (overview.quotas || []).length
        ? '<div class="empty"><b>Everything is hidden</b>Turn subscriptions or cards back on in Settings.<div class="empty-actions"><a class="btn" href="/settings.html">Open Settings</a></div></div>'
        : renderEmptyUsage()
      : skeletonUsage();
}

// renderSource is one subscription's detail view: source switcher sub-bar,
// hero card with collector health (matching Settings), and every card.
function renderSource(id) {
  const root = $('#view-source');
  if (!root || !overview) return;
  const g = (overview.layout || []).find((x) => x.id === id);
  if (!g) {
    root.innerHTML = `<div class="empty"><b>Unknown subscription</b>${esc(id)} is not collected by this panel.<div class="empty-actions"><a class="btn" href="#/overview">Back to Overview</a></div></div>`;
    return;
  }

  const tabs = sourceTabs();
  const switcher = tabs.map((t) =>
    `<a class="subpill" href="#/source/${encodeURIComponent(t.id)}"${t.id === g.id ? ' aria-current="page"' : ''}>${esc(t.title)}</a>`
  ).join('');

  const handle = groupHandle(g, g.items);
  const plan = g.detail
    ? `<span class="plan-pill ${planClass(g.detail)}">${esc(g.detail)}</span>`
    : '';

  const healthLines = g.providers.map((p) => {
    const run = runOf(p);
    const meta = (overview.providers || []).find((x) => x.name === p) || { title: p, interval_seconds: 0 };
    const status = run ? run.status : 'idle';
    const stateText = {
      ok: 'Connected', warning: 'Running low', running: 'Refreshing',
      unconfigured: 'Not set up', expired_auth: 'Sign-in expired', error: 'Error', idle: 'Waiting',
    }[status] || status;
    const when = run && run.last_run ? `<b data-ago="${run.last_run}"></b>` : 'never';
    const next = run && run.next_run ? ` · next <b data-in="${run.next_run}"></b>` : '';
    return `
      <div class="health-line">
        <span class="dot ${esc(status)}"></span>
        <span class="health-name">${esc(meta.title)}</span>
        <span class="health-state is-${esc(status)}">${esc(stateText)}</span>
        <span class="health-cadence">every ${fmtDuration(meta.interval_seconds)}</span>
        <span class="health-time">${when}${next}</span>
      </div>`;
  }).join('');

  const callouts = g.providers.map((p) => {
    const run = runOf(p);
    if (!run || !run.error || !['expired_auth', 'error', 'unconfigured'].includes(run.status)) return '';
    return `
      <div class="callout is-${esc(run.status)}">
        <span class="callout-msg" title="${esc(run.error)}">${esc(trim(run.error, 180))}</span>
        <a class="btn sm" href="/settings.html#${encodeURIComponent(g.id)}/connection">Configure</a>
      </div>`;
  }).filter(Boolean).join('');

  const items = g.items.filter((it) => snapshotOf(it));
  root.innerHTML = `
    <div class="subbar">
      <a class="back-link" href="#/overview">← Overview</a>
      <div class="subpills">${switcher}</div>
    </div>

    <div class="source-hero">
      <div class="source-hero-top">
        ${renderGroupEmblem(g, 'lg')}
        <div class="source-hero-text">
          <div class="source-hero-name">
            <h2>${esc(g.account_name || g.title)}</h2>
            ${plan}
          </div>
          <div class="source-hero-sub">${esc(handle || g.title)}</div>
        </div>
        <div class="source-actions">
          <a class="btn sm" href="/settings.html#${encodeURIComponent(g.id)}">Settings</a>
          <button class="btn sm" type="button" data-providers="${esc(g.providers.join(','))}">Refresh</button>
        </div>
      </div>
      <div class="source-health">
        ${healthLines}
        ${callouts}
      </div>
    </div>

    ${groupNotes(g, new Set(g.providers))}
    ${items.length
      ? `<div class="grid">${items.map((it) => cardFor(it, { showHiddenTag: true })).join('')}</div>`
      : '<div class="empty"><b>No readings yet</b>They appear after the first successful poll.</div>'}`;
  refreshTickers();
}

function renderGroupNote(sn) {
  return `<p class="group-note"><span class="pill is-ok">${esc(sn.unit || sn.label)}</span>${esc(sn.note || '')}</p>`;
}

function renderCard(sn, opts = {}) {
  if (sn.kind === 'note') return renderNoteCard(sn);
  if (sn.series && sn.series.length > 0) return renderSeriesCard(sn, opts);

  const style = cardStyleFor(sn);
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
    ? `<span class="pill is-${esc(sn.status)}">${esc(labelForStatus(sn.status))}</span>`
    : opts.showHiddenTag && opts.hidden
      ? '<span class="pill is-muted">Hidden</span>'
      : '';

  const reset = sn.reset_at
    ? `<span class="foot-reset">${CLOCK_ICON}resets in <b data-in="${sn.reset_at}" data-warn-under="3600000" data-urgent-under="900000"></b></span>`
    : total > 0 ? `<span>${pct.toFixed(1)}% left</span>` : '<span>metered</span>';

  const glyph = style.glyph
    ? `<span class="card-glyph">${style.glyph}</span>`
    : `<span class="card-dot"></span>`;

  return `
    <article class="card${opts.showHiddenTag && opts.hidden ? ' is-hidden' : ''}" data-state="${esc(sn.status || 'ok')}" style="--tint:${style.tint}">
      <div class="card-head">
        <div class="card-title" title="${esc(sn.label)}">${glyph}<span>${esc(sn.label)}</span></div>
        <div class="card-right">
          ${chip}
          <span class="card-ago" data-ago="${sn.updated_at}"></span>
        </div>
      </div>
      <div class="metric-row">
        <span class="metric-num ${tone === 'ok' ? '' : tone}">${fmtNumber(sn.remaining, sn.unit)}</span>
        <span class="metric-unit">${esc(unitLabel)}</span>
      </div>
      ${total > 0 ? pixels(pct, tone, 32) : ''}
      <div class="card-foot">${reset}</div>
    </article>`;
}

// renderSeriesCard shows one compact bar per quota window (5-hour, weekly).
function renderSeriesCard(sn, opts = {}) {
  const style = cardStyleFor(sn);
  const rows = sn.series.map((s) => {
    const unit = s.unit || '%';
    const pct = s.total > 0 ? Math.max(0, Math.min(100, (s.remaining / s.total) * 100)) : 0;
    const tone = unit === '%' ? toneFor(pct) : 'ok';
    const reset = s.reset_at
      ? `<span class="srow-reset">${CLOCK_ICON}<b data-in="${s.reset_at}" data-warn-under="3600000" data-urgent-under="900000"></b></span>`
      : '';
    return `
      <div class="srow">
        <div class="srow-head">
          <span class="srow-label">${esc(s.label || 'current')}</span>
          ${reset}
          <span class="srow-pct ${tone === 'ok' ? '' : tone}">${fmtNumber(s.remaining, unit)}<i>${unit === '%' ? '% left' : esc(unit)}</i></span>
        </div>
        ${pixels(pct, tone, 32)}
      </div>`;
  }).join('');

  const chip = sn.status && sn.status !== 'ok'
    ? `<span class="pill is-${esc(sn.status)}">${esc(labelForStatus(sn.status))}</span>`
    : opts.showHiddenTag && opts.hidden
      ? '<span class="pill is-muted">Hidden</span>'
      : '';

  const glyph = style.glyph
    ? `<span class="card-glyph">${style.glyph}</span>`
    : `<span class="card-dot"></span>`;

  return `
    <article class="card${opts.showHiddenTag && opts.hidden ? ' is-hidden' : ''}" data-state="${esc(sn.status || 'ok')}" style="--tint:${style.tint}">
      <div class="card-head">
        <div class="card-title" title="${esc(sn.label)}">${glyph}<span>${esc(sn.label)}</span></div>
        <div class="card-right">
          ${chip}
          <span class="card-ago" data-ago="${sn.updated_at}"></span>
        </div>
      </div>
      <div class="series">${rows}</div>
    </article>`;
}

function pixels(pct, tone, segments = 32) {
  const lit = Math.round((pct / 100) * segments);
  let html = '';
  for (let i = 0; i < segments; i++) {
    html += `<i class="${i < lit ? 'on' : ''}"></i>`;
  }
  return `<div class="pixels" data-tone="${esc(tone)}" style="--segs:${segments}" role="img" aria-label="${pct.toFixed(1)}% remaining">${html}</div>`;
}

function renderNoteCard(sn) {
  const style = cardStyleFor(sn);
  return `
    <article class="card" style="--tint:${style.tint}">
      <div class="card-head">
        <div class="card-title"><span class="card-dot"></span><span>${esc(sn.label)}</span></div>
        <span class="card-ago" data-ago="${sn.updated_at}"></span>
      </div>
      <p class="note-value">${esc(sn.unit || '—')}</p>
      <p class="notice-text">${esc(sn.note || '')}</p>
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
    const next = r.next_run ? ` · next <b data-in="${r.next_run}"></b>` : '';
    return `
      <div class="run">
        <span class="dot ${esc(r.status)}"></span>
        <span class="run-name">${esc(title)}</span>
        <span class="pill is-${esc(r.status)}">${esc(labelForStatus(r.status))}</span>
        <span class="run-meta">${esc(cadence)} · ${last}${next}</span>
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
  const syncEl = $('#sync');
  if (syncEl) {
    syncEl.innerHTML = latest ? `synced <b data-ago="${new Date(latest).toISOString()}"></b>` : 'no data yet';
  }

  const footer = $('#footer-meta');
  if (!footer) return;
  const uptime = overview.started_at
    ? fmtDuration((Date.now() - new Date(overview.started_at).getTime()) / 1000)
    : '—';
  footer.textContent = `uptime ${uptime} · ${(overview.providers || []).length} collectors`;
}

function labelForStatus(status) {
  return {
    ok: 'OK',
    warning: 'Low',
    error: 'Error',
    expired_auth: 'Expired',
    unconfigured: 'Setup',
    running: 'Running',
    idle: 'Idle',
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

async function refreshGroup(names, button) {
  button.disabled = true;
  button.classList.add('is-spinning');
  try {
    const results = await Promise.all(names.map((n) =>
      api(`/api/refresh/${encodeURIComponent(n)}`, { method: 'POST' }).catch((err) => ({ status: err.status === 429 ? 'cooldown' : 'error', error: err.message, provider: n }))));
    const bad = results.find((r) => r && r.status && !['ok', 'running'].includes(r.status));
    toast(bad ? (bad.status === 'cooldown' ? 'Cooling down, try again in a minute' : `${bad.provider}: ${trim(bad.error || bad.status, 80)}`) : 'Refreshed');
  } finally {
    button.disabled = false;
    button.classList.remove('is-spinning');
    await load();
  }
}

async function refreshAll(button) {
  button.disabled = true;
  button.classList.add('is-spinning');
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
    button.classList.remove('is-spinning');
    await load();
  }
}

$('#refresh-all').addEventListener('click', (e) => refreshAll(e.currentTarget));
for (const id of ['#view-usage', '#view-source']) {
  const el = $(id);
  if (el) el.addEventListener('click', (e) => {
    const btn = e.target.closest('button[data-provider], button[data-providers]');
    if (!btn) return;
    if (btn.dataset.provider) refreshProvider(btn.dataset.provider, btn);
    else refreshGroup(btn.dataset.providers.split(','), btn);
  });
}

/* ---------- boot ---------- */

showView();
load();
setInterval(load, 20000);
setInterval(refreshTickers, 1000);
document.addEventListener('visibilitychange', () => { if (!document.hidden) load(); });
