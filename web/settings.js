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

function applyTheme(theme) {
  const root = document.documentElement;
  if (theme === 'auto') delete root.dataset.theme;
  else root.dataset.theme = theme;
  localStorage.setItem('gauge-theme', theme);
}

const themeParam = new URLSearchParams(location.search).get('theme');
applyTheme(THEMES.includes(themeParam) ? themeParam : localStorage.getItem('gauge-theme') || 'auto');
$('#theme').addEventListener('click', () => {
  const current = localStorage.getItem('gauge-theme') || 'auto';
  const next = THEMES[(THEMES.indexOf(current) + 1) % THEMES.length];
  applyTheme(next);
  toast(`Theme: ${next}`);
  ui.reload();
});

/* ---------- sections ---------- */

const initial = location.hash.replace(/^#\/?/, '') || undefined;

const ui = GaugeSettings.create({
  root: $('#settings-root'),
  layout: 'split',
  oauth: true,
  toast,
  onNavigate() {
    const id = ui && ui.current();
    if (id) history.replaceState(null, '', `#${id}`);
  },
});

ui.open(initial);
window.addEventListener('hashchange', () => ui.open(location.hash.replace(/^#\/?/, '') || undefined));
