'use strict';

// Settings UI shared by the menubar popover and the browser settings page.
//
// The server describes sections (one per source, plus General); this module
// renders them as grouped native-style rows. Every control saves on its own:
// switches and segmented controls on click, text and numbers on blur or
// Enter, secrets through a small inline editor. Clearing a text field resets
// it to its default.
//
//   const ui = GaugeSettings.create({ root, layout: 'stack' | 'split', toast, onNavigate })
//   ui.open()            // load and show the index (split: index + first section)
//   ui.open('colab')     // jump to a section
//   ui.back()            // stack layout: detail -> index; returns false at the index

(function () {
  const esc = (v) =>
    String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

  async function api(path, options = {}) {
    const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...options });
    const text = await res.text();
    let body = null;
    if (text) {
      try { body = JSON.parse(text); } catch { body = text; }
    }
    if (!res.ok) throw new Error((body && body.error) || res.statusText || `HTTP ${res.status}`);
    return body;
  }

  function fmtAgo(iso) {
    const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
    if (Number.isNaN(s)) return '';
    if (s < 60) return 'just now';
    const m = Math.floor(s / 60);
    if (m < 60) return `${m}m ago`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h ago`;
    return `${Math.floor(h / 24)}d ago`;
  }

  // Worst status first, so a section with one broken collector reads broken.
  const SEVERITY = ['expired_auth', 'error', 'unconfigured', 'warning', 'running', 'ok'];
  const STATE_TEXT = {
    ok: 'Connected',
    warning: 'Running low',
    running: 'Refreshing',
    unconfigured: 'Not set up',
    expired_auth: 'Sign-in expired',
    error: 'Error',
    idle: 'Waiting for first run',
  };

  const CHEVRON = '<svg class="sx-chev" viewBox="0 0 20 20" width="13" height="13" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" d="m8 5 5 5-5 5"/></svg>';

  const THEMES = [['auto', 'System'], ['light', 'Light'], ['dark', 'Dark']];

  // The icon setting is a server field like any other, but it is drawn as a
  // thumbnail picker in Appearance instead of a row under Options.
  const ICON_KEY = 'app_icon';
  const ICON_LABELS = { gauge: 'Gauge', classic: 'Classic' };

  // refreshMarks reloads every /mark.svg on the page (header, favicon) after
  // the icon changes; the server picks the file, the query defeats caching.
  function refreshMarks() {
    const bust = `/mark.svg?v=${Date.now()}`;
    document.querySelectorAll('img[src^="/mark.svg"]:not([data-icon-thumb])').forEach((img) => { img.src = bust; });
    document.querySelectorAll('link[rel~="icon"][href^="/mark.svg"]').forEach((link) => { link.href = bust; });
  }

  function create(opts) {
    const root = opts.root;
    const toast = opts.toast || (() => {});
    const layout = opts.layout || 'stack';
    let sections = [];
    let overview = null;
    let current = null; // section id shown in the detail pane
    let editing = null; // secret key whose editor is open
    const openSetup = new Set(); // sections whose "Connection & refresh" the user opened

    root.classList.add('sx', `sx-${layout}`);

    async function load() {
      const [settings, ov] = await Promise.all([
        api('/api/settings'),
        api('/api/overview').catch(() => null),
      ]);
      sections = settings.sections || [];
      overview = ov;
    }

    async function open(id) {
      try {
        await load();
      } catch (err) {
        root.innerHTML = `<p class="sx-empty">Cannot load settings: ${esc(err.message)}</p>`;
        return;
      }
      if (layout === 'split') current = id || current || (sections[0] && sections[0].id);
      else current = id || null;
      editing = null;
      shown = undefined;
      render();
    }

    function back() {
      if (layout === 'stack' && current) {
        current = null;
        editing = null;
        render();
        return true;
      }
      return false;
    }

    function section(id) {
      return sections.find((s) => s.id === id);
    }

    function runsFor(sec) {
      const runs = (overview && overview.runs) || [];
      return sec.providers.map((p) => runs.find((r) => r.provider === p)).filter(Boolean);
    }

    function stateOf(sec) {
      if (!sec.providers.length) return null;
      const runs = runsFor(sec);
      if (!runs.length) return { status: 'idle', run: null };
      const worst = runs.slice().sort((a, b) => SEVERITY.indexOf(a.status) - SEVERITY.indexOf(b.status))[0];
      return { status: worst.status, run: worst };
    }

    function layoutGroups() {
      return (overview && overview.layout) || [];
    }

    function groupOf(id) {
      return layoutGroups().find((g) => g.id === id);
    }

    // titleFor prefers the subscription name the server resolved ("Gemini
    // Ultra") over the static section title.
    function titleFor(sec) {
      const g = groupOf(sec.id);
      return (g && g.title) || sec.title;
    }

    function titleOf(name) {
      const p = ((overview && overview.providers) || []).find((x) => x.name === name);
      return p ? p.title : name;
    }

    /* ---------- render ---------- */

    // render redraws the current page. A redraw after a save keeps the scroll
    // position; only moving to another page tells the host (onNavigate).
    let shown;
    function render() {
      const sec = current && section(current);
      const moved = shown !== current;
      shown = current;
      const scroller = opts.scroller || root;
      const top = scroller.scrollTop;
      if (layout === 'split') {
        root.innerHTML = `
          <nav class="sx-index">${renderIndex()}</nav>
          <div class="sx-detail">${sec ? renderDetail(sec) : ''}</div>`;
      } else {
        root.innerHTML = sec ? `<div class="sx-detail">${renderDetail(sec)}</div>` : `<nav class="sx-index">${renderIndex()}</nav>`;
      }
      if (moved) {
        scroller.scrollTop = 0;
        if (opts.onNavigate) opts.onNavigate(sec ? titleFor(sec) : null);
      } else {
        scroller.scrollTop = top;
      }
    }

    // renderIndex is level one: every subscription in menu-bar order, each
    // with a drag handle and a visibility switch; General sits apart.
    function renderIndex() {
      const groups = layoutGroups().filter((g) => section(g.id));
      const others = sections.filter((s) => !groupOf(s.id));
      const groupRow = (g) => {
        const sec = section(g.id);
        const st = stateOf(sec);
        const state = st ? STATE_TEXT[st.status] || st.status : '';
        const sub = [g.detail, state].filter(Boolean).join(' · ');
        const active = g.id === current ? ' aria-current="true"' : '';
        return `
          <div class="sx-link sx-sortrow" data-sort-id="${esc(g.id)}" data-section="${esc(g.id)}" role="button" tabindex="0"${active}>
            ${handle(g.title)}
            <span class="sx-dot ${esc(st ? st.status : 'none')}"></span>
            <span class="sx-link-text"><b>${esc(g.title)}</b><span>${esc(sub)}</span></span>
            ${visSwitch('group', g.id, !g.hidden, `Show ${g.title}`)}
            ${CHEVRON}
          </div>`;
      };
      const plainRow = (s) => `
        <div class="sx-link" data-section="${esc(s.id)}" role="button" tabindex="0"${s.id === current ? ' aria-current="true"' : ''}>
          <span class="sx-link-text"><b>${esc(s.title)}</b><span>${esc(indexSummary(s))}</span></span>
          ${CHEVRON}
        </div>`;
      return `
        <div class="sx-group">
          <h3 class="sx-caption">Subscriptions</h3>
          <div class="sx-card sx-sortlist" data-sort="groups">${groups.map(groupRow).join('')}</div>
          <p class="sx-foot">Drag to reorder the menu bar. The switch shows or hides a subscription.</p>
        </div>
        <div class="sx-group">
          <div class="sx-card">${others.map(plainRow).join('')}</div>
        </div>`;
    }

    const GRIP = '<svg viewBox="0 0 12 16" width="9" height="13" aria-hidden="true"><g fill="currentColor"><circle cx="3.5" cy="3" r="1.3"/><circle cx="8.5" cy="3" r="1.3"/><circle cx="3.5" cy="8" r="1.3"/><circle cx="8.5" cy="8" r="1.3"/><circle cx="3.5" cy="13" r="1.3"/><circle cx="8.5" cy="13" r="1.3"/></g></svg>';

    function handle(label) {
      return `<button class="sx-handle" type="button" data-handle aria-label="Move ${esc(label)}; use the arrow keys">${GRIP}</button>`;
    }

    function visSwitch(kind, id, on, label) {
      return `<label class="sx-switch" title="${esc(label)}"><input type="checkbox" data-vis="${kind}" data-vis-id="${esc(id)}" aria-label="${esc(label)}"${on ? ' checked' : ''} /><span></span></label>`;
    }

    function indexSummary(s) {
      return s.fields.map((f) => f.label.replace(/^Read Google cookies from /, '')).slice(0, 2).join(' · ');
    }

    function renderDetail(sec) {
      const parts = [];
      if (layout === 'split') parts.push(`<h2 class="sx-title">${esc(titleFor(sec))}</h2>`);
      if (sec.summary) parts.push(`<p class="sx-summary">${esc(sec.summary)}</p>`);

      const st = stateOf(sec);
      if (st) parts.push(renderStatus(sec, st));
      if (sec.id === 'google') parts.push(renderAccounts());
      parts.push(renderCards(sec));

      // Sources split credentials from cadence; General keeps its own order.
      const fields = sec.fields.filter((f) => f.key !== ICON_KEY);
      const isSource = sec.providers.length > 0;
      const creds = isSource ? fields.filter((f) => !(f.kind === 'number' && f.unit)) : fields;
      const prefs = fields.filter((f) => !creds.includes(f));
      if (isSource) {
        // Credentials and cadence are rarely touched once a source works, so
        // they fold away; they open by themselves while something is broken
        // or a secret is being edited.
        const broken = st && ['expired_auth', 'error', 'unconfigured'].includes(st.status);
        const open = broken || (editing && fields.some((f) => f.key === editing)) || openSetup.has(sec.id);
        parts.push(`
          <details class="sx-more" data-more="${esc(sec.id)}"${open ? ' open' : ''}>
            <summary><span>Connection &amp; refresh</span>${CHEVRON}</summary>
            ${creds.length ? `<div class="sx-group"><div class="sx-card">${creds.map(renderField).join('')}</div></div>` : ''}
            ${prefs.length ? `<div class="sx-group"><h3 class="sx-caption">Refresh every</h3><div class="sx-card">${prefs.map(renderField).join('')}</div></div>` : ''}
          </details>`);
      } else if (creds.length) {
        parts.push(`
          <div class="sx-group">
            <h3 class="sx-caption">Options</h3>
            <div class="sx-card">${creds.map(renderField).join('')}</div>
          </div>`);
      }
      if (sec.id === 'general') parts.push(renderAppearance(sec.fields.find((f) => f.key === ICON_KEY)));
      return parts.join('');
    }

    function renderStatus(sec, st) {
      const runs = runsFor(sec);
      const lines = (runs.length ? runs : [{ provider: sec.providers[0], status: 'idle' }]).map((r) => {
        const label = sec.providers.length > 1 ? `<b>${esc(titleOf(r.provider))}</b> ` : '';
        const when = r.last_run ? ` · ${fmtAgo(r.last_run)}` : '';
        const err = r.error && r.status !== 'ok' ? `<span class="sx-error">${esc(r.error)}</span>` : '';
        return `
          <div class="sx-status-line">
            <span class="sx-dot ${esc(r.status)}"></span>
            <span class="sx-status-text">${label}${esc(STATE_TEXT[r.status] || r.status)}${esc(when)}${err}</span>
          </div>`;
      }).join('');
      return `
        <div class="sx-group">
          <div class="sx-card sx-status">
            ${lines}
            <div class="sx-status-actions">
              <button class="sx-btn" type="button" data-refresh="${esc(sec.providers.join(','))}">Refresh now</button>
            </div>
          </div>
        </div>`;
    }

    // renderCards is level two: the subscription's own switch, then its
    // cards in menu-bar order, each with a handle and a switch.
    function renderCards(sec) {
      const g = groupOf(sec.id);
      if (!g) return '';
      const rows = g.items.map((it) => `
        <div class="sx-row sx-sortrow" data-sort-id="${esc(it.id)}">
          ${handle(it.label)}
          <span class="sx-label"><b>${esc(it.label)}</b>${cardDetail(it)}</span>
          ${visSwitch('item', it.id, !it.hidden, `Show ${it.label}`)}
        </div>`).join('');
      const empty = g.items.length ? '' : '<div class="sx-row"><span class="sx-help">No cards yet. They appear after the first successful refresh.</span></div>';
      return `
        <div class="sx-group">
          <h3 class="sx-caption">Menu bar &amp; overview</h3>
          <div class="sx-card">
            <div class="sx-row">
              <span class="sx-label"><b>Show ${esc(g.title)}</b><span class="sx-help">${g.hidden ? 'Hidden: its cards are kept but not shown.' : 'Drag the cards below to reorder them.'}</span></span>
              ${visSwitch('group', g.id, !g.hidden, `Show ${g.title}`)}
            </div>
            <div class="sx-sortlist${g.hidden ? ' muted' : ''}" data-sort="items:${esc(g.id)}">${rows}</div>
            ${empty}
          </div>
        </div>`;
    }

    // cardDetail names the windows a card carries, from the latest snapshot.
    function cardDetail(it) {
      const q = ((overview && overview.quotas) || []).find((x) => x.provider === it.provider && x.metric === it.metric);
      const windows = q && q.series && q.series.length ? q.series.map((x) => x.label).join(' · ') : '';
      return windows ? `<span class="sx-help">${esc(windows)}</span>` : '';
    }

    /* ---------- layout: visibility and order ---------- */

    async function saveLayout() {
      const body = {
        groups: layoutGroups().map((g) => ({
          id: g.id,
          hidden: !!g.hidden,
          items: g.items.map((it) => ({ id: it.id, hidden: !!it.hidden })),
        })),
      };
      try {
        const res = await api('/api/display', { method: 'PUT', body: JSON.stringify(body) });
        if (overview && res && res.layout) overview.layout = res.layout;
        if (opts.onSaved) opts.onSaved();
        return true;
      } catch (err) {
        toast(err.message);
        return false;
      }
    }

    function setVisible(kind, id, visible) {
      if (kind === 'group') {
        const g = groupOf(id);
        if (g) g.hidden = !visible;
      } else {
        for (const g of layoutGroups()) {
          const it = g.items.find((x) => x.id === id);
          if (it) it.hidden = !visible;
        }
      }
    }

    // applyOrder copies the DOM order of a sortable list into the layout.
    function applyOrder(list) {
      const ids = [...list.querySelectorAll(':scope > [data-sort-id]')].map((el) => el.dataset.sortId);
      const pick = (arr) => ids.map((id) => arr.find((x) => x.id === id)).filter(Boolean)
        .concat(arr.filter((x) => !ids.includes(x.id)));
      const target = list.dataset.sort;
      if (target === 'groups') {
        overview.layout = pick(layoutGroups());
      } else if (target.startsWith('items:')) {
        const g = groupOf(target.slice(6));
        if (g) g.items = pick(g.items);
      }
    }

    async function commitOrder(list, focusId) {
      applyOrder(list);
      if (await saveLayout()) {
        render();
        if (focusId) root.querySelector(`[data-sort-id="${CSS.escape(focusId)}"] [data-handle]`)?.focus();
      } else {
        await reloadSection();
      }
    }

    // Drag with the pointer: the row follows the handle and swaps places with
    // the neighbour it crosses; the order is saved on release.
    let drag = null;
    root.addEventListener('pointerdown', (e) => {
      const h = e.target.closest('[data-handle]');
      if (!h || e.button !== 0) return;
      const row = h.closest('[data-sort-id]');
      if (!row) return;
      e.preventDefault();
      h.setPointerCapture(e.pointerId);
      drag = { row, list: row.parentElement, moved: false };
      row.classList.add('dragging');
    });
    root.addEventListener('pointermove', (e) => {
      if (!drag) return;
      const others = [...drag.list.querySelectorAll(':scope > [data-sort-id]')].filter((r) => r !== drag.row);
      const before = others.find((r) => {
        const b = r.getBoundingClientRect();
        return e.clientY < b.top + b.height / 2;
      });
      if (before) {
        if (drag.row.nextElementSibling !== before) {
          drag.list.insertBefore(drag.row, before);
          drag.moved = true;
        }
      } else if (drag.list.lastElementChild !== drag.row) {
        drag.list.appendChild(drag.row);
        drag.moved = true;
      }
    });
    const endDrag = () => {
      if (!drag) return;
      const { row, list, moved } = drag;
      drag = null;
      row.classList.remove('dragging');
      if (moved) commitOrder(list);
    };
    root.addEventListener('pointerup', endDrag);
    root.addEventListener('pointercancel', endDrag);

    function renderAccounts() {
      const accounts = (overview && overview.accounts) || [];
      const rows = accounts.map((a) => `
        <div class="sx-row">
          <span class="sx-avatar"><img src="/api/avatar/${a.slot}" alt="" onerror="this.remove()" /></span>
          <span class="sx-label"><b>${esc(a.name || a.email)}</b><span class="sx-help">${esc(a.email || '')}${a.tier ? ' · ' + esc(a.tier) : ''}</span></span>
          ${a.slot > 1 && opts.oauth ? `<button class="sx-btn danger" type="button" data-remove-account="${a.slot}">Remove</button>` : ''}
        </div>`).join('');
      const add = opts.oauth
        ? `<div class="sx-row"><span class="sx-label"><span class="sx-help">Signs in through Google; the token is stored encrypted on this host.</span></span>
             <button class="sx-btn" type="button" data-oauth="${accounts.length ? 'add' : 'primary'}">${accounts.length ? 'Add account' : 'Sign in with Google'}</button></div>`
        : '';
      if (!rows && !add) return '';
      return `
        <div class="sx-group">
          <h3 class="sx-caption">Google accounts</h3>
          <div class="sx-card">${rows || '<div class="sx-row"><span class="sx-help">No account yet.</span></div>'}${add}</div>
        </div>`;
    }

    function renderAppearance(icon) {
      const theme = localStorage.getItem('gauge-theme') || 'auto';
      const iconRow = icon ? `
            <div class="sx-row" data-key="${esc(icon.key)}">
              ${labelBlock(icon)}
              <span class="sx-icons" role="radiogroup" aria-label="${esc(icon.label)}">${(icon.options || []).map((o) => `
                <button class="sx-icon" type="button" role="radio" data-icon-choice="${esc(o)}" aria-checked="${o === icon.value}">
                  <img data-icon-thumb src="/mark.svg?icon=${encodeURIComponent(o)}" width="28" height="28" alt="" />
                  <span>${esc(ICON_LABELS[o] || o)}</span>
                </button>`).join('')}</span>
            </div>` : '';
      return `
        <div class="sx-group">
          <h3 class="sx-caption">Appearance</h3>
          <div class="sx-card">
            <div class="sx-row">
              <span class="sx-label"><b>Theme</b></span>
              <span class="sx-seg" role="radiogroup">${THEMES.map(([v, l]) =>
                `<button type="button" role="radio" data-theme-choice="${v}" aria-checked="${v === theme}">${l}</button>`).join('')}</span>
            </div>${iconRow}
          </div>
        </div>`;
    }

    function labelBlock(f) {
      const help = f.help ? `<span class="sx-help">${esc(f.help)}</span>` : '';
      return `<span class="sx-label"><b>${esc(f.label)}</b>${help}</span>`;
    }

    function renderField(f) {
      const key = esc(f.key);
      if (f.kind === 'switch') {
        const on = f.value === f.options[0];
        return `
          <div class="sx-row" data-key="${key}">
            ${labelBlock(f)}
            <label class="sx-switch"><input type="checkbox" data-switch="${key}"${on ? ' checked' : ''} /><span></span></label>
          </div>`;
      }
      if (f.kind === 'select') {
        return `
          <div class="sx-row" data-key="${key}">
            ${labelBlock(f)}
            <span class="sx-seg" role="radiogroup">${(f.options || []).map((o) =>
              `<button type="button" role="radio" data-select="${key}" data-value="${esc(o)}" aria-checked="${o === f.value}">${esc(o)}</button>`).join('')}</span>
          </div>`;
      }
      if (f.kind === 'secret') return renderSecret(f);

      const isNumber = f.kind === 'number';
      const value = f.configured ? f.value : '';
      const placeholder = f.placeholder || f.default || '';
      const input = `<input class="sx-input${isNumber ? ' num' : ''}" data-text="${key}" type="${isNumber ? 'number' : 'text'}"${isNumber ? ' step="any" inputmode="decimal"' : ''}
          value="${esc(value)}" placeholder="${esc(placeholder)}" autocomplete="off" spellcheck="false" />`;
      if (isNumber) {
        return `
          <div class="sx-row" data-key="${key}">
            ${labelBlock(f)}
            <span class="sx-flag" aria-live="polite"></span>
            <span class="sx-num">${input}${f.unit ? `<i>${esc(f.unit)}</i>` : ''}</span>
          </div>`;
      }
      // Short values sit on the right like any other control; long ones
      // (table paths) get the full width under the label.
      if ((f.placeholder || '').length <= 24 && (value || '').length <= 24) {
        return `
          <div class="sx-row" data-key="${key}">
            ${labelBlock(f)}
            <span class="sx-flag" aria-live="polite"></span>
            <span class="sx-text">${input}</span>
          </div>`;
      }
      return `
        <div class="sx-row stack" data-key="${key}">
          <span class="sx-row-head">${labelBlock(f)}<span class="sx-flag" aria-live="polite"></span></span>
          ${input}
        </div>`;
    }

    function renderSecret(f) {
      const key = esc(f.key);
      const state = f.configured
        ? `<span class="sx-secret-state set">${esc(f.value || 'Saved')}</span>`
        : `<span class="sx-secret-state">${f.optional ? 'Not set' : 'Required'}</span>`;
      const buttons = f.configured
        ? `<button class="sx-btn" type="button" data-edit="${key}">Replace</button>
           <button class="sx-btn danger" type="button" data-clear="${key}">Remove</button>`
        : `<button class="sx-btn" type="button" data-edit="${key}">Add</button>`;
      let editor = '';
      if (editing === f.key) {
        const ctl = f.multiline
          ? `<textarea class="sx-input" data-secret="${key}" rows="4" placeholder="${esc(f.placeholder || '')}" spellcheck="false"></textarea>`
          : `<input class="sx-input" data-secret="${key}" type="password" placeholder="${esc(f.placeholder || '')}" autocomplete="off" spellcheck="false" />`;
        editor = `
          <div class="sx-editor">
            ${ctl}
            <div class="sx-editor-actions">
              <button class="sx-btn" type="button" data-cancel>Cancel</button>
              <button class="sx-btn primary" type="button" data-save-secret="${key}">Save</button>
            </div>
          </div>`;
      }
      return `
        <div class="sx-row stack" data-key="${key}">
          ${labelBlock(f)}
          ${editing === f.key ? editor : `<span class="sx-secret">${state}<span class="sx-secret-actions">${buttons}</span></span>`}
        </div>`;
    }

    /* ---------- saving ---------- */

    function fieldOf(key) {
      for (const s of sections) {
        const f = s.fields.find((x) => x.key === key);
        if (f) return f;
      }
      return null;
    }

    function flag(key, state, text) {
      const el = root.querySelector(`.sx-row[data-key="${CSS.escape(key)}"] .sx-flag`);
      if (!el) return;
      el.dataset.state = state;
      el.textContent = text || '';
      if (state === 'saved') setTimeout(() => { if (el.dataset.state === 'saved') { el.textContent = ''; el.dataset.state = ''; } }, 1600);
    }

    async function save(key, value) {
      const f = fieldOf(key);
      try {
        if (value === '') await api(`/api/settings/${encodeURIComponent(key)}`, { method: 'DELETE' });
        else await api('/api/settings', { method: 'POST', body: JSON.stringify({ [key]: value }) });
        if (f) {
          f.configured = value !== '';
          f.value = value === '' ? f.default || '' : f.sensitive ? '••••' + value.slice(-4) : value;
        }
        if (opts.onSaved) opts.onSaved(key);
        return true;
      } catch (err) {
        toast(err.message);
        return false;
      }
    }

    async function saveText(input) {
      const key = input.dataset.text;
      const f = fieldOf(key);
      if (!f) return;
      const value = input.value.trim();
      const before = f.configured ? f.value : '';
      if (value === before) return;
      flag(key, 'saving', 'Saving…');
      const ok = await save(key, value);
      if (ok) {
        flag(key, 'saved', value === '' && f.default ? `Reset to ${f.default}` : 'Saved');
        if (value === '' && f.default) input.placeholder = f.default;
      } else {
        flag(key, 'error', 'Not saved');
      }
    }

    async function reloadSection() {
      try {
        await load();
      } catch { /* keep what is on screen */ }
      render();
    }

    root.addEventListener('click', async (e) => {
      if (e.target.closest('[data-handle], .sx-switch')) return;
      const nav = e.target.closest('[data-section]');
      if (nav) {
        current = nav.dataset.section;
        editing = null;
        render();
        return;
      }
      const t = e.target.closest('button, input');
      if (!t) return;
      const d = t.dataset;

      if (d.themeChoice) {
        const theme = d.themeChoice;
        const el = document.documentElement;
        if (theme === 'auto') delete el.dataset.theme;
        else el.dataset.theme = theme;
        localStorage.setItem('gauge-theme', theme);
        if (opts.onTheme) opts.onTheme(theme);
        root.querySelectorAll('[data-theme-choice]').forEach((b) => b.setAttribute('aria-checked', String(b.dataset.themeChoice === theme)));
        return;
      }
      if (d.iconChoice) {
        if (t.getAttribute('aria-checked') === 'true') return;
        if (await save(ICON_KEY, d.iconChoice)) {
          root.querySelectorAll('[data-icon-choice]').forEach((b) => b.setAttribute('aria-checked', String(b === t)));
          refreshMarks();
        }
        return;
      }
      if (d.select) {
        if (await save(d.select, d.value)) {
          root.querySelectorAll(`[data-select="${CSS.escape(d.select)}"]`).forEach((b) => b.setAttribute('aria-checked', String(b === t)));
        }
        return;
      }
      if (d.edit) {
        editing = d.edit;
        render();
        const input = root.querySelector(`[data-secret="${CSS.escape(d.edit)}"]`);
        if (input) input.focus();
        return;
      }
      if ('cancel' in d) {
        editing = null;
        render();
        return;
      }
      if (d.saveSecret) {
        const input = root.querySelector(`[data-secret="${CSS.escape(d.saveSecret)}"]`);
        const value = input ? input.value.trim() : '';
        if (!value) {
          toast('Paste a value first');
          return;
        }
        t.disabled = true;
        if (await save(d.saveSecret, value)) {
          editing = null;
          toast('Saved');
          await reloadSection();
        } else {
          t.disabled = false;
        }
        return;
      }
      if (d.clear) {
        t.disabled = true;
        if (await save(d.clear, '')) {
          toast('Removed');
          await reloadSection();
        } else {
          t.disabled = false;
        }
        return;
      }
      if (d.refresh) {
        t.disabled = true;
        t.textContent = 'Refreshing…';
        const names = d.refresh.split(',').filter(Boolean);
        const results = await Promise.all(names.map((n) =>
          api(`/api/refresh/${encodeURIComponent(n)}`, { method: 'POST' }).catch((err) => ({ error: err.message }))));
        const bad = results.find((r) => r && r.error);
        toast(bad ? 'Still failing; the status above has the details' : 'Refreshed');
        if (opts.onSaved) opts.onSaved();
        await reloadSection();
        return;
      }
      if (d.oauth) {
        startOAuth(d.oauth === 'add');
        return;
      }
      if (d.removeAccount) {
        t.disabled = true;
        try {
          await api(`/api/accounts/${d.removeAccount}`, { method: 'DELETE' });
          toast('Account removed');
        } catch (err) {
          toast(err.message);
        }
        await reloadSection();
      }
    });

    root.addEventListener('toggle', (e) => {
      const d = e.target.closest && e.target.closest('details[data-more]');
      if (!d) return;
      if (d.open) openSetup.add(d.dataset.more);
      else openSetup.delete(d.dataset.more);
    }, true);

    root.addEventListener('change', (e) => {
      const t = e.target;
      if (t.dataset.vis) {
        setVisible(t.dataset.vis, t.dataset.visId, t.checked);
        saveLayout().then((ok) => {
          if (!ok) t.checked = !t.checked;
          render();
        });
        return;
      }
      if (t.dataset.switch) {
        const f = fieldOf(t.dataset.switch);
        if (!f) return;
        const value = t.checked ? f.options[0] : f.options[1];
        save(t.dataset.switch, value).then((ok) => { if (!ok) t.checked = !t.checked; });
      } else if (t.dataset.text) {
        saveText(t);
      }
    });

    root.addEventListener('keydown', (e) => {
      const t = e.target;
      if ((e.key === 'ArrowUp' || e.key === 'ArrowDown') && t.matches('[data-handle]')) {
        e.preventDefault();
        const row = t.closest('[data-sort-id]');
        const sib = e.key === 'ArrowUp' ? row.previousElementSibling : row.nextElementSibling;
        if (!sib || !sib.dataset.sortId) return;
        if (e.key === 'ArrowUp') row.parentElement.insertBefore(row, sib);
        else row.parentElement.insertBefore(sib, row);
        commitOrder(row.parentElement, row.dataset.sortId);
        return;
      }
      if ((e.key === 'Enter' || e.key === ' ') && t.matches('[data-section]')) {
        e.preventDefault();
        t.click();
        return;
      }
      if (e.key === 'Enter' && t.dataset.text) {
        e.preventDefault();
        t.blur();
      } else if (e.key === 'Enter' && t.dataset.secret && t.tagName === 'INPUT') {
        e.preventDefault();
        root.querySelector(`[data-save-secret="${CSS.escape(t.dataset.secret)}"]`)?.click();
      } else if (e.key === 'Escape' && (t.dataset.secret || t.dataset.text)) {
        if (t.dataset.secret) {
          e.stopPropagation();
          editing = null;
          render();
        } else {
          const f = fieldOf(t.dataset.text);
          t.value = f && f.configured ? f.value : '';
          t.blur();
        }
      }
    });

    async function startOAuth(extra) {
      let path = '/api/oauth/start';
      if (extra) {
        const used = new Set(((overview && overview.accounts) || []).map((a) => a.slot));
        const slot = [2, 3].find((s) => !used.has(s));
        if (!slot) {
          toast('Three accounts is the limit');
          return;
        }
        path += `?slot=${slot}`;
      }
      try {
        const res = await api(path);
        const popup = window.open(res.url, 'gauge-oauth', 'width=520,height=760');
        if (!popup) {
          location.href = res.url;
          return;
        }
        toast('Finish sign-in in the popup');
        let ticks = 0;
        const timer = setInterval(async () => {
          await reloadSection();
          if (++ticks >= 18 || popup.closed) clearInterval(timer);
        }, 5000);
      } catch (err) {
        toast(`Sign-in failed: ${err.message}`);
      }
    }

    return { open, back, reload: reloadSection, current: () => current };
  }

  window.GaugeSettings = { create };
})();
