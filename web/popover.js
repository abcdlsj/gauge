'use strict';

const $ = (sel, root = document) => root.querySelector(sel);
const esc = (v) =>
  String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

function fmtNumber(v, unit) {
  if (unit === '%') return v.toLocaleString('en-US', { maximumFractionDigits: 0 });
  if (/^[A-Z]{3}$/.test(unit || '')) return v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  if (Math.abs(v) >= 1000) return v.toLocaleString('en-US', { maximumFractionDigits: 0 });
  return v.toLocaleString('en-US', { maximumFractionDigits: 1 });
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

function toneFor(pct) {
  if (pct <= 5) return 'danger';
  if (pct < 25) return 'warning';
  return 'ok';
}

function refreshTickers() {
  const now = Date.now();
  document.querySelectorAll('[data-in]').forEach((el) => {
    const target = new Date(el.dataset.in).getTime();
    const ms = target - now;
    el.textContent = Number.isNaN(ms) ? '—' : ms <= 0 ? 'due' : fmtDuration(ms / 1000);
    el.classList.toggle('soon', ms > 0 && ms <= 3600000);
    el.classList.toggle('urgent', ms > 0 && ms <= 900000);
  });
  document.querySelectorAll('[data-ago]').forEach((el) => {
    const ms = now - new Date(el.dataset.ago).getTime();
    el.textContent = Number.isNaN(ms) ? '—' : ms < 5000 ? 'now' : `${fmtDuration(ms / 1000)} ago`;
  });
}

let timer = null;
function toast(text) {
  const el = $('#toast');
  text = String(text).replace(/\s+/g, ' ').trim();
  el.textContent = text.length > 90 ? text.slice(0, 89) + '…' : text;
  el.hidden = false;
  clearTimeout(timer);
  timer = setTimeout(() => { el.hidden = true; }, 2400);
}

async function api(path, options = {}) {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...options });
  const text = await res.text();
  let body = null;
  if (text) {
    try { body = JSON.parse(text); } catch { body = text; }
  }
  if (!res.ok) throw new Error((body && body.error) || res.statusText);
  return body;
}

/* ---------- theme ---------- */

const THEMES = ['auto', 'light', 'dark'];
const THEME_LABELS = { auto: 'Follow system', light: 'Light', dark: 'Dark' };
const THEME_ICONS = {
  // Half-filled disc: no choice made, the system decides.
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

// storedTheme prefers an explicit ?theme= (used for screenshots and links) and
// falls back to the same localStorage key the dashboard uses.
function storedTheme() {
  const forced = new URLSearchParams(location.search).get('theme');
  if (THEMES.includes(forced)) return forced;
  const stored = localStorage.getItem('gauge-theme');
  return THEMES.includes(stored) ? stored : 'auto';
}

let currentTheme = 'auto';

function paintThemeButton(theme) {
  for (const id of ['#theme', '#settings-theme']) {
    const btn = $(id);
    if (!btn) continue;
    btn.innerHTML = THEME_ICONS[theme];
    btn.title = `Theme: ${THEME_LABELS[theme]}`;
    btn.setAttribute('aria-label', btn.title);
  }
}

// applyTheme drops data-theme for auto so app.css falls back to
// prefers-color-scheme, exactly like the browser build.
function applyTheme(theme) {
  const root = document.documentElement;
  if (theme === 'auto') delete root.dataset.theme;
  else root.dataset.theme = theme;
  localStorage.setItem('gauge-theme', theme);
  currentTheme = theme;
  paintThemeButton(theme);
}

function cycleTheme() {
  const next = THEMES[(THEMES.indexOf(currentTheme) + 1) % THEMES.length];
  applyTheme(next);
  toast(`Theme: ${THEME_LABELS[next]}`);
}

applyTheme(storedTheme());
$('#theme').addEventListener('click', cycleTheme);

/* ---------- render ---------- */

function meter(pct, tone) {
  const segments = 40;
  const lit = Math.round((Math.max(0, Math.min(100, pct)) / 100) * segments);
  let html = '';
  for (let i = 0; i < segments; i++) html += `<i class="${i < lit ? 'on' : ''}"></i>`;
  return `<div class="meter" data-tone="${esc(tone)}" role="img" aria-label="${pct.toFixed(0)}% remaining">${html}</div>`;
}

// metricFamily strips the "@N" slot suffix the collector appends to extra
// accounts, so "claude_models@2" still reads as the claude family.
function metricFamily(metric) {
  return String(metric || '').split('@')[0];
}

// tint is the meter colour of a card: the model family for Antigravity, the
// source for everything else.
function tintFor(sn) {
  if (metricFamily(sn.metric) === 'claude_models') return 'claude';
  if (sn.provider === 'commandcode' || sn.provider === 'opendesign') return sn.provider;
  return 'gemini';
}

const familyIcons = {
  // four-point sparkle
  gemini: '<svg viewBox="0 0 16 16" aria-hidden="true"><path fill="currentColor" d="M8 0l1.9 5.6L15.5 7.5 9.9 9.4 8 15l-1.9-5.6L.5 7.5l5.6-1.9Z"/></svg>',
  // six-spoke asterisk
  claude: '<svg viewBox="0 0 16 16" aria-hidden="true"><g stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M8 1.5v13M2.4 4.75l11.2 6.5M13.6 4.75l-11.2 6.5"/></g></svg>',
};

const shortNames = {
  compute_units: 'Colab',
  flow_credits: 'Flow credits',
  gemini_cli_requests: 'CLI calls',
  paid_api_requests_10m: 'API calls · 10m',
  credits_remaining: 'Credits',
  month_to_date_cost: 'Cost',
};

// renderAccountLine is the Google identity under the subscription title:
// avatar, name, email and the exact plan.
function renderAccountLine(a, detail) {
  if (!a) return '';
  const tier = detail || a.tier || '';
  const kind = /ultra/i.test(tier) ? 'ultra' : /pro/i.test(tier) ? 'pro' : 'free';
  return `
    <div class="src-account">
      <span class="avatar" data-tier="${kind}"><img src="/api/avatar/${a.slot}" alt="" onerror="this.remove()" /></span>
      <span class="who"><b>${esc(a.account_name || a.name || a.email || '')}</b>${(a.account_name || a.name) && a.email ? `<span>${esc(a.email)}</span>` : ''}</span>
      ${detail ? `<span class="plan ${kind === 'ultra' ? '' : kind}">${esc(detail)}</span>` : ''}
    </div>`;
}

function renderWindow(series) {
  const unit = series.unit || '%';
  const pct = series.total > 0 ? Math.max(0, Math.min(100, (series.remaining / series.total) * 100)) : 0;
  const tone = unit === '%' ? toneFor(pct) : 'ok';
  const reset = series.reset_at ? `<b data-in="${series.reset_at}"></b>` : 'not started';
  return `
    <div class="win">
      <div class="win-top">
        <span class="win-name">${esc(series.label || 'current')}</span>
        <span class="win-reset"><span class="clock">◷</span>${reset}</span>
        <span class="win-pct ${tone === 'ok' ? '' : tone}">${fmtNumber(series.remaining, unit)}<i>${unit === '%' ? '% left' : esc(unit)}</i></span>
      </div>
      ${meter(pct, tone)}
    </div>`;
}

// renderPool is one card with quota windows (an Antigravity model pool, a
// Command Code plan), titled with the card label.
function renderPool(sn, label) {
  const series = sn.series && sn.series.length
    ? sn.series
    : [{ label: 'current', remaining: sn.remaining, total: sn.total, unit: sn.unit, reset_at: sn.reset_at }];
  const tint = tintFor(sn);
  const head = label
    ? `<div class="fam-head">${familyIcons[tint] ? `<span class="fam-logo">${familyIcons[tint]}</span>` : ''}<span class="fam-name">${esc(label)}</span></div>`
    : '';
  return `<div class="fam" data-family="${esc(tint)}">${head}${series.map(renderWindow).join('')}</div>`;
}

function renderMini(sn, label) {
  return `
    <div class="mini">
      <span class="label">${esc(label)}</span>
      <span class="spacer"></span>
      <span class="note">${sn.reset_at ? `resets in <b data-in="${sn.reset_at}"></b>` : `<b data-ago="${sn.updated_at}"></b>`}</span>
      <span class="value">${fmtNumber(sn.remaining, sn.unit)}<span>${esc(sn.unit || '')}</span></span>
    </div>`;
}

// renderGroups stacks the visible subscriptions in the user's order; inside
// each, the visible cards in the user's order. A subscription with several
// identities renders one section per identity instead of one shared block.
function renderGroups(overview) {
  const quotas = overview.quotas || [];
  const find = (it) => quotas.find((q) => q.provider === it.provider && q.metric === it.metric);
  return (overview.layout || []).filter((g) => !g.hidden).map((g) => {
    const cards = g.items.filter((it) => !it.hidden).map((it) => ({ it, sn: find(it) })).filter((c) => c.sn);
    if (!cards.length) return '';
    const isGoogle = g.id === 'google' || g.id.startsWith('google-');
    const user = !isGoogle && cards[0].sn.account
      ? `<span class="src-user">${esc(cards[0].sn.account.split('@')[0])}</span>` : '';
    return `
      <section class="src" data-group="${esc(g.id)}"${g.slot ? ` data-slot="${g.slot}"` : ''}>
        <div class="src-head">
          <span class="src-name">${esc(g.title)}</span>
          ${staleTag(cards)}
          ${user}
        </div>
        ${isGoogle ? renderAccountLine(g, g.detail) : ''}
        ${bodyFor(cards)}
      </section>`;
  }).join('');
}

function staleTag(cards) {
  return cards.some((c) => c.sn.status === 'error' || c.sn.status === 'expired_auth')
    ? '<span class="src-stale" title="Last refresh failed; numbers may be old">stale</span>' : '';
}

function bodyFor(cards) {
  const pools = cards.filter((c) => c.sn.series && c.sn.series.length).length;
  return cards.map(({ it, sn }) => (sn.series && sn.series.length
    // A lone plan card needs no label under the subscription title, but an
    // Antigravity pool keeps its name even when it is the only card left: a
    // second Google identity hides Flow and Colab, and dropping the label
    // made its pool look unlike the same pool on the primary identity.
    ? renderPool(sn, it.provider === 'antigravity' || pools > 1 || cards.length > 1 ? it.label : '')
    : renderMini(sn, shortNames[sn.metric] || it.label))).join('');
}

function render(overview) {
  const quotas = overview.quotas || [];
  const root = $('#pop-body');
  const sources = renderGroups(overview);
  root.innerHTML = sources
    || (quotas.length
      ? '<div class="mini notice">Every card is hidden. Turn some on in Settings.</div>'
      : '<div class="mini notice">No snapshots yet — collectors are warming up.</div>');

  const times = quotas.map((q) => new Date(q.updated_at).getTime()).filter((n) => !Number.isNaN(n));
  const latest = times.length ? Math.max(...times) : null;
  $('#sync').innerHTML = latest ? `updated <b data-ago="${new Date(latest).toISOString()}"></b>` : 'loading…';
  refreshTickers();
  fitWindow();
  syncScrollDepth();
}

async function load() {
  try {
    render(await api('/api/overview'));
  } catch {
    /* keep the last paint; the next tick retries */
  }
}

/* ---------- settings view ---------- */

// The settings pages are drawn by web/settings-ui.js; this file only swaps
// the list for them and keeps the window fitted.
const settingsUI = GaugeSettings.create({
  root: $('#settings-root'),
  scroller: $('#settings-root'),
  layout: 'stack',
  // The menubar popover can add and remove Google accounts too; the consent
  // step hands off to the system browser (see settings-ui.js).
  oauth: true,
  toast,
  onNavigate(meta) {
    const m = typeof meta === 'string' ? { title: meta || 'Settings', depth: meta ? 1 : 0 } : (meta || { depth: 0, title: 'Settings' });
    const eyebrow = $('#settings-eyebrow');
    const title = $('#settings-title');
    const themeBtn = $('#settings-theme');
    const refreshBtn = $('#settings-refresh');
    const homeBtn = $('#settings-home');
    const backBtn = $('#close-settings');

    title.textContent = m.title || 'Settings';
    if (m.depth > 0 && m.parent) {
      eyebrow.textContent = m.parent;
      eyebrow.hidden = false;
    } else {
      eyebrow.textContent = '';
      eyebrow.hidden = true;
    }
    const atRoot = !m.depth;
    themeBtn.hidden = !atRoot;
    homeBtn.hidden = atRoot;
    refreshBtn.hidden = !(m.providers && m.providers.length);
    backBtn.title = atRoot ? 'Back to Usage' : `Back to ${m.parent || 'Settings'}`;
    backBtn.setAttribute('aria-label', backBtn.title);
    requestAnimationFrame(syncScrollDepth);
  },
  onTheme(theme) {
    currentTheme = theme;
    paintThemeButton(theme);
  },
  onSaved() {
    load();
    requestAnimationFrame(syncScrollDepth);
  },
});

// While settings are open the window keeps one height: at least what the
// list had, and never less than SETTINGS_MIN_HEIGHT. Pages scroll inside it,
// so moving between them or saving does not resize (and jump) the window.
const SETTINGS_MIN_HEIGHT = 600;
let settingsHeight = 0;

async function openSettings(initialRoute) {
  settingsHeight = Math.max(SETTINGS_MIN_HEIGHT, Math.ceil(window.innerHeight));
  $('#list-scroll').hidden = true;
  $('#view-settings').hidden = false;
  document.body.classList.add('settings-open');
  await settingsUI.open(initialRoute);
  fitWindow();
  syncScrollDepth();
}

function closeSettings() {
  settingsHeight = 0;
  $('#view-settings').hidden = true;
  $('#list-scroll').hidden = false;
  document.body.classList.remove('settings-open');
  fitWindow();
}

$('#open-settings').addEventListener('click', () => openSettings());
$('#close-settings').addEventListener('click', () => {
  if (!settingsUI.back()) closeSettings();
});
$('#settings-eyebrow').addEventListener('click', () => settingsUI.back());
$('#settings-theme').addEventListener('click', () => {
  cycleTheme();
  settingsUI.reload();
});
$('#settings-home').addEventListener('click', closeSettings);
$('#settings-refresh').addEventListener('click', async (e) => {
  const btn = e.currentTarget;
  if (btn.disabled) return;
  btn.disabled = true;
  btn.classList.add('spinning');
  try {
    await settingsUI.refreshCurrent();
  } finally {
    btn.classList.remove('spinning');
    btn.disabled = false;
  }
});

$('#refresh-all').addEventListener('click', async (e) => {
  const btn = e.currentTarget;
  btn.disabled = true;
  try {
    await api('/api/refresh-all', { method: 'POST' });
    toast('Refreshed');
    await load();
  } catch (err) {
    toast(`Refresh failed: ${err.message}`);
  } finally {
    btn.disabled = false;
  }
});

// syncScrollDepth flags the scroll areas that still have content below the
// fold; popover.css fades that edge so a half-cut row never looks clipped.
function syncScrollDepth() {
  document.querySelectorAll('.scroll-wrap').forEach((wrap) => {
    const area = wrap.querySelector('.scroll-area');
    if (!area) return;
    wrap.classList.toggle('has-more', area.scrollHeight - area.clientHeight - area.scrollTop > 1);
  });
}

document.addEventListener('scroll', syncScrollDepth, { capture: true, passive: true });
window.addEventListener('resize', syncScrollDepth);
// Scroll events are enough in the webview; the ticker is a cheap backstop for
// the cases where layout moves without a scroll (late fonts, resized cards).
setInterval(syncScrollDepth, 1000);

// fitWindow asks the shell to size the popover to the rendered content. The
// shell caps the window (see cmd/gauge); past that cap the list or the
// settings form scrolls inside the popover.
let fitTimer = null;

// naturalHeight is the height the panel wants, including the body inset. The
// panel is capped at the window height once the content is too tall, so it is
// lifted for the measurement; otherwise a capped page would report the window
// height back and the window could never settle.
function naturalHeight(panel) {
  const body = document.body;
  const scrollers = [document.querySelector('#pop-body'), document.querySelector('#settings-root')].filter(Boolean);
  const offsets = scrollers.map((el) => el.scrollTop);
  const prevBodyHeight = body.style.getPropertyValue('height');
  const prevMax = panel.style.maxHeight;
  body.style.setProperty('height', 'auto', 'important');
  panel.style.maxHeight = 'none';
  const content = panel.getBoundingClientRect().height;
  body.style.removeProperty('height');
  panel.style.maxHeight = prevMax;
  if (prevBodyHeight) body.style.setProperty('height', prevBodyHeight, 'important');
  // Lifting the cap scrolls the list back to the top; put it back where it was.
  scrollers.forEach((el, i) => { el.scrollTop = offsets[i]; });
  const style = getComputedStyle(body);
  const inset = (parseFloat(style.paddingTop) || 0) + (parseFloat(style.paddingBottom) || 0);
  return Math.ceil(content + inset);
}

function fitWindow() {
  clearTimeout(fitTimer);
  fitTimer = setTimeout(() => {
    const panel = document.querySelector('.panel');
    if (!panel) return;
    const height = settingsHeight || naturalHeight(panel);
    syncScrollDepth();
    fetch('/api/ui/height', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ height }),
    }).catch(() => {});
  }, 60);
}

load();
paintThemeButton(currentTheme);
// ?settings=1 opens the settings index, ?settings=<section> one section
// (screenshots and links use it).
const settingsParam = new URLSearchParams(location.search).get('settings');
if (settingsParam) openSettings(settingsParam === '1' ? undefined : settingsParam);
setInterval(load, 20000);
setInterval(refreshTickers, 1000);
document.addEventListener('visibilitychange', () => { if (!document.hidden) load(); });
syncScrollDepth();
