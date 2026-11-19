'use strict';

// The browser settings page: a section list on the left, the selected
// section on the right. Rendering and saving live in web/settings-ui.js,
// shared with the menubar popover.

if (new URLSearchParams(location.search).get('shell') === 'desktop') {
  document.documentElement.dataset.shell = 'desktop';
}

const $ = (sel, root = document) => root.querySelector(sel);

let toastTimer = null;
function toast(message) {
  const el = $('#toast');
  el.textContent = message;
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, 2800);
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

const themeParam = new URLSearchParams(location.search).get('theme');
applyTheme(THEMES.includes(themeParam) ? themeParam : localStorage.getItem('gauge-theme') || 'auto');
$('#theme').addEventListener('click', () => {
  const current = localStorage.getItem('gauge-theme') || 'auto';
  const next = THEMES[(THEMES.indexOf(current) + 1) % THEMES.length];
  applyTheme(next);
  toast(`Theme: ${THEME_LABELS[next]}`);
  ui.reload();
});

/* ---------- sections ---------- */

const initial = location.hash.replace(/^#\/?/, '') || undefined;

const ui = GaugeSettings.create({
  root: $('#settings-root'),
  layout: 'split',
  oauth: true,
  toast,
  onTheme: applyTheme,
  onNavigate() {
    const id = ui && ui.current();
    if (id) history.replaceState(null, '', `#${id}`);
  },
});

ui.open(initial);
window.addEventListener('hashchange', () => ui.open(location.hash.replace(/^#\/?/, '') || undefined));
