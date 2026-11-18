'use strict';

// The desktop shell appends ?shell=desktop and gets a sidebar layout instead of
// the top navigation used by the browser build.
if (new URLSearchParams(location.search).get('shell') === 'desktop') {
  document.documentElement.dataset.shell = 'desktop';
}

const $ = (sel, root = document) => root.querySelector(sel);
const esc = (v) =>
  String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

let toastTimer = null;
function toast(message) {
  const el = $('#toast');
  el.textContent = message;
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, 2800);
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

/* ---------- accounts ---------- */

let accounts = [];

function brandFor(tier) {
  if (/ultra/i.test(tier)) return { cls: 'ultra', label: 'Google AI Ultra', ring: 'ultra' };
  if (/pro/i.test(tier)) return { cls: 'pro', label: 'Google AI Pro', ring: 'pro' };
  return { cls: 'free', label: tier || 'Free', ring: 'free' };
}

async function loadAccounts() {
  try {
    const data = await api('/api/overview');
    accounts = data.accounts || [];
    renderAccounts();
  } catch {
    /* the dashboard poll will retry */
  }
}

function renderAccounts() {
  const panel = $('#accounts-panel');
  const list = $('#accounts-list');
  if (!accounts.length) {
    panel.hidden = true;
    return;
  }
  panel.hidden = false;

  list.innerHTML = accounts.map((a) => {
    const brand = brandFor(a.tier);
    const initial = esc((a.name || a.email || '?').trim().charAt(0).toUpperCase());
    const face = a.picture
      ? `<img src="${esc(a.picture)}" alt="" referrerpolicy="no-referrer" />`
      : `<span class="avatar-initial">${initial}</span>`;
    return `
      <div class="account-card">
        <span class="avatar" data-tier="${brand.ring}">${face}</span>
        <div class="who">
          <b>${esc(a.name || a.email)}</b>
          <span>${esc(a.email || '')}</span>
        </div>
        <div class="plan">
          <span class="brand-badge ${brand.cls}">${esc(brand.label)}</span>
          <span class="chip">${esc(a.tier || '—')}</span>
          ${a.slot > 1 ? `<button class="link" type="button" data-remove="${a.slot}">remove</button>` : ''}
        </div>
      </div>`;
  }).join('');
}

function nextSlot() {
  const used = new Set(accounts.map((a) => a.slot));
  for (let slot = 2; slot <= 3; slot++) if (!used.has(slot)) return slot;
  return 0;
}

async function addAccount() {
  const slot = nextSlot();
  if (!slot) {
    toast('Three accounts is the limit');
    return;
  }
  try {
    const res = await api(`/api/oauth/start?slot=${slot}`);
    const popup = window.open(res.url, 'gauge-oauth', 'width=520,height=760');
    if (!popup) location.href = res.url;
    else toast('Finish sign-in in the popup');
    let ticks = 0;
    const timer = setInterval(async () => {
      await loadAccounts();
      if (++ticks >= 18) clearInterval(timer);
    }, 5000);
  } catch (err) {
    toast(`Sign-in failed: ${err.message}`);
  }
}

$('#add-account').addEventListener('click', addAccount);

$('#accounts-list').addEventListener('click', async (e) => {
  const btn = e.target.closest('[data-remove]');
  if (!btn) return;
  btn.disabled = true;
  try {
    await api(`/api/accounts/${btn.dataset.remove}`, { method: 'DELETE' });
    toast('Account removed');
    await loadAccounts();
  } catch (err) {
    toast(`Remove failed: ${err.message}`);
  } finally {
    btn.disabled = false;
  }
});

/* ---------- settings ---------- */

async function loadSettings() {
  try {
    const data = await api('/api/settings');
    renderSettings(data.fields || []);
  } catch (err) {
    $('#settings-fields').innerHTML = `<p class="note">Cannot load settings: ${esc(err.message)}</p>`;
  }
}

function renderSettings(fields) {
  const groups = new Map();
  for (const f of fields) {
    if (!groups.has(f.group)) groups.set(f.group, []);
    groups.get(f.group).push(f);
  }

  $('#settings-fields').innerHTML = [...groups.entries()].map(([group, items]) => {
    // Only fields that have neither a stored value nor a default need action.
    const required = items.filter((f) => !f.optional && !f.default);
    const missing = required.filter((f) => !f.configured);
    const open = missing.length ? ' open' : '';
    const state = missing.length
      ? `<span class="sgroup-state">${required.length - missing.length}/${required.length} set</span>`
      : '<span class="sgroup-state done">✓ ready</span>';
    return `
      <details class="sgroup"${open}>
        <summary>
          <h3>${esc(group)}</h3>
          ${state}
        </summary>
        <div class="sgroup-body">
          ${group === 'Antigravity' ? oauthRow() : ''}
          ${items.map(renderField).join('')}
        </div>
      </details>`;
  }).join('');
}

function oauthRow() {
  return `
    <div class="oauth-row">
      <button class="btn primary" type="button" data-oauth="antigravity">Sign in with Google</button>
      <span class="hint">Only needed when the IDE is closed. With Antigravity running, the panel reads the language server directly.</span>
    </div>`;
}

function renderField(f) {
  let stored = '';
  if (f.configured) stored = '<span class="tick" title="configured">✓</span>';
  else if (f.default) stored = `<span class="hint">default: ${esc(f.default)}</span>`;
  else if (f.optional) stored = '<span class="hint">optional</span>';

  const placeholder = f.configured && f.sensitive ? f.value : f.placeholder || f.default || '';
  const help = f.help ? `<span class="hint">${esc(f.help)}</span>` : '';
  const valueAttr = !f.sensitive && f.configured ? ` value="${esc(f.value)}"` : '';

  let control;
  if (f.kind === 'select') {
    const options = f.options || [];
    const current = f.configured ? f.value : options[0] || '';
    control = `<select name="${esc(f.key)}">${options.map((o) =>
      `<option value="${esc(o)}"${o === current ? ' selected' : ''}>${esc(o)}</option>`).join('')}</select>`;
  } else if (f.kind === 'textarea') {
    const content = !f.sensitive && f.configured ? esc(f.value) : '';
    control = `<textarea name="${esc(f.key)}" rows="3" placeholder="${esc(placeholder)}">${content}</textarea>`;
  } else {
    const type = f.kind === 'password' || f.sensitive ? 'password' : f.kind === 'number' ? 'number' : 'text';
    const step = type === 'number' ? ' step="any"' : '';
    control = `<input type="${type}" name="${esc(f.key)}" placeholder="${esc(placeholder)}"${valueAttr}${step} autocomplete="off" spellcheck="false" />`;
  }

  return `
    <div class="field">
      <label for="${esc(f.key)}">${esc(f.label)} ${stored}</label>
      <div class="ctl">
        ${control}
        ${help}
      </div>
    </div>`;
}

async function saveSettings(form) {
  const payload = {};
  for (const el of form.querySelectorAll('input, textarea')) {
    const value = el.value.trim();
    if (value) payload[el.name] = value;
  }
  const status = $('#settings-status');
  if (!Object.keys(payload).length) {
    status.textContent = 'Nothing to save.';
    return;
  }
  status.textContent = 'Saving…';
  try {
    const res = await api('/api/settings', { method: 'POST', body: JSON.stringify(payload) });
    form.reset();
    status.textContent = `Saved ${res.saved} value(s).`;
    toast('Settings saved');
    await loadSettings();
loadAccounts();
  } catch (err) {
    status.textContent = `Save failed: ${err.message}`;
  }
}

$('#settings-form').addEventListener('submit', (e) => {
  e.preventDefault();
  saveSettings(e.currentTarget);
});

$('#settings-fields').addEventListener('click', async (e) => {
  const btn = e.target.closest('[data-oauth]');
  if (!btn) return;
  btn.disabled = true;
  try {
    const res = await api('/api/oauth/start');
    const popup = window.open(res.url, 'gauge-oauth', 'width=520,height=760');
    if (!popup) {
      location.href = res.url;
      return;
    }
    toast('Finish sign-in in the popup');
    let ticks = 0;
    const timer = setInterval(async () => {
      if (++ticks >= 18) {
        clearInterval(timer);
        await loadSettings();
loadAccounts();
      }
    }, 5000);
  } catch (err) {
    toast(`Sign-in failed: ${err.message}`);
    btn.disabled = false;
  }
});

loadSettings();
loadAccounts();
