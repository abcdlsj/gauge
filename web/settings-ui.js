'use strict';

// Settings UI shared by the menubar popover (stack) and the browser settings
// page (split). Renders a 3-level hierarchy matching the Home popover's
// borderless instrument aesthetic:
//   Level 1: Subscriptions + Preferences index
//   Level 2: Subscription detail (cards, health, drill-downs), Add Subscription, General
//   Level 3: Connection & Auth, Refresh Schedule, New Account, Alert Webhook

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
    if (!iso) return '';
    const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
    if (Number.isNaN(s)) return '';
    if (s < 60) return 'just now';
    const m = Math.floor(s / 60);
    if (m < 60) return `${m}m ago`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h ago`;
    return `${Math.floor(h / 24)}d ago`;
  }

  function fmtVal(v, unit) {
    if (typeof v !== 'number' || Number.isNaN(v)) return '—';
    if (unit === '%') return v.toLocaleString('en-US', { maximumFractionDigits: 0 });
    if (/^[A-Z]{3}$/.test(unit || '')) return v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
    if (Math.abs(v) >= 1000) return v.toLocaleString('en-US', { maximumFractionDigits: 0 });
    return v.toLocaleString('en-US', { maximumFractionDigits: 1 });
  }

  function fmtMinutes(m) {
    const n = Number(m);
    if (!Number.isFinite(n) || n <= 0) return String(m);
    if (n >= 60 && n % 60 === 0) return `${n / 60}h`;
    return `${n}m`;
  }

  // Worst status first, so a section with one broken collector reads broken.
  const SEVERITY = ['expired_auth', 'error', 'unconfigured', 'warning', 'running', 'idle', 'ok'];
  const STATE_TEXT = {
    ok: 'Connected',
    warning: 'Running low',
    running: 'Refreshing',
    unconfigured: 'Not set up',
    expired_auth: 'Sign-in expired',
    error: 'Error',
    idle: 'Waiting',
  };
  const ISSUE_PILL = {
    expired_auth: 'Expired',
    error: 'Error',
    unconfigured: 'Setup',
  };

  const CHEVRON = '<svg class="sx-chev" viewBox="0 0 20 20" width="13" height="13" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" d="m8 5 5 5-5 5"/></svg>';
  const GRIP = '<svg viewBox="0 0 12 16" width="9" height="13" aria-hidden="true"><g fill="currentColor"><circle cx="3.5" cy="3" r="1.3"/><circle cx="8.5" cy="3" r="1.3"/><circle cx="3.5" cy="8" r="1.3"/><circle cx="8.5" cy="8" r="1.3"/><circle cx="3.5" cy="13" r="1.3"/><circle cx="8.5" cy="13" r="1.3"/></g></svg>';

  const FAMILY_ICONS = {
    gemini: '<svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path fill="currentColor" d="M8 0l1.9 5.6L15.5 7.5 9.9 9.4 8 15l-1.9-5.6L.5 7.5l5.6-1.9Z"/></svg>',
    claude: '<svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><g stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M8 1.5v13M2.4 4.75l11.2 6.5M13.6 4.75l-11.2 6.5"/></g></svg>',
  };

  const SOURCE_META = {
    google: {
      tint: 'var(--gemini)',
      icon: '<svg viewBox="0 0 18 18" width="14" height="14" aria-hidden="true"><path fill="currentColor" d="M9 1.5l1.95 5.55L16.5 9l-5.55 1.95L9 16.5l-1.95-5.55L1.5 9l5.55-1.95Z"/></svg>',
      desc: 'Antigravity, Flow & Colab',
    },
    commandcode: {
      tint: 'var(--commandcode)',
      icon: '<svg viewBox="0 0 18 18" width="14" height="14" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" d="M4.2 5.8 7.8 9l-3.6 3.2M9.2 12.2h4.4"/></svg>',
      desc: '5-hour & weekly plan windows',
    },
    opendesign: {
      tint: 'var(--opendesign)',
      icon: '<svg viewBox="0 0 18 18" width="14" height="14" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.65" stroke-linejoin="round" d="M9 2.6 15.2 9 9 15.4 2.8 9Z"/><circle cx="9" cy="9" r="1.45" fill="currentColor"/></svg>',
      desc: 'Design Plan & USD balance',
    },
    gcp: {
      tint: 'var(--gcp)',
      icon: '<svg viewBox="0 0 18 18" width="14" height="14" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" d="M5.4 13.5h7.3a2.7 2.7 0 0 0 .4-5.37 4.1 4.1 0 0 0-7.9-1.15 3.1 3.1 0 0 0 .2 6.52Z"/></svg>',
      desc: 'BigQuery billing & Gemini API calls',
    },
    general: {
      tint: '#636b78',
      icon: '<svg viewBox="0 0 18 18" width="14" height="14" aria-hidden="true"><circle cx="9" cy="9" r="5.5" fill="none" stroke="currentColor" stroke-width="1.65"/><path d="M9 3.5a5.5 5.5 0 0 1 0 11Z" fill="currentColor"/></svg>',
      desc: 'Theme & menu bar mark',
    },
    network: {
      tint: 'var(--accent)',
      icon: '<svg viewBox="0 0 18 18" width="14" height="14" aria-hidden="true"><circle cx="9" cy="9" r="6" fill="none" stroke="currentColor" stroke-width="1.6"/><path fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" d="M3 9h12M9 3c1.8 2 2.6 3.9 2.6 6S10.8 13 9 15c-1.8-2-2.6-3.9-2.6-6S7.2 5 9 3Z"/></svg>',
      desc: 'Proxy for upstream requests',
    },
    webhook: {
      tint: 'var(--accent-2)',
      icon: '<svg viewBox="0 0 18 18" width="14" height="14" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" d="M5.5 7.6a3.5 3.5 0 0 1 7 0c0 2.4 1 3.5 1.5 4H4c.5-.5 1.5-1.6 1.5-4ZM7.6 13.7a1.5 1.5 0 0 0 2.8 0"/></svg>',
      desc: 'State-change alerts',
    },
    connection: {
      tint: 'var(--accent)',
      icon: '<svg viewBox="0 0 18 18" width="14" height="14" aria-hidden="true"><circle cx="6.3" cy="9" r="2.7" fill="none" stroke="currentColor" stroke-width="1.65"/><path fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" d="M9 9h5.8M12.7 9v2.2"/></svg>',
    },
    schedule: {
      tint: 'var(--ok)',
      icon: '<svg viewBox="0 0 18 18" width="14" height="14" aria-hidden="true"><circle cx="9" cy="9" r="5.8" fill="none" stroke="currentColor" stroke-width="1.6"/><path fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" d="M9 5.8V9l2.3 1.4"/></svg>',
    },
  };

  const THEMES = [
    ['auto', 'System', 'Follow macOS'],
    ['light', 'Light', 'Warm paper'],
    ['dark', 'Dark', 'Carbon'],
  ];
  const ICON_KEY = 'app_icon';
  const ICON_LABELS = { gauge: 'Gauge', classic: 'Classic' };
  const ICON_DESC = { gauge: 'Dial arc', classic: 'Classic G' };
  const TRAY_GLYPH_SVG = {
    gauge: '<svg viewBox="0 0 22 22" width="15" height="15" aria-hidden="true"><path d="M14.4 6.2A5.2 5.2 0 1 0 15.8 11.4" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round"/><circle cx="15.1" cy="5.8" r="1.4" fill="currentColor"/></svg>',
    classic: '<svg viewBox="0 0 22 22" width="15" height="15" aria-hidden="true"><path d="M15.4 7.6A5.6 5.6 0 1 0 16.5 11.5H11" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round"/><circle cx="11" cy="11" r="1.4" fill="currentColor"/></svg>',
  };

  function refreshMarks() {
    const bust = `/mark.svg?v=${Date.now()}`;
    document.querySelectorAll('img[src^="/mark.svg"]:not([data-icon-thumb])').forEach((img) => { img.src = bust; });
    document.querySelectorAll('link[rel~="icon"][href^="/mark.svg"]').forEach((link) => { link.href = bust; });
  }

  function presetsFor(f) {
    const min = Math.max(1, Number(f.min) || 1);
    const def = Number(f.default) || min;
    let candidates;
    if (def >= 180) candidates = [60, 180, 360, 720];
    else if (def >= 30) candidates = [15, 30, 45, 60, 120];
    else if (min <= 2) candidates = [2, 5, 15, 30];
    else if (min <= 5) candidates = [5, 10, 15, 30];
    else candidates = [10, 20, 30, 60];
    const out = candidates.filter((v) => v >= min);
    if (!out.includes(def)) out.push(def);
    return out.sort((a, b) => a - b).slice(0, 4);
  }

  function create(opts) {
    const root = opts.root;
    const toast = opts.toast || (() => {});
    const layout = opts.layout || 'stack';

    let sections = [];
    let overview = null;
    let route = null; // parsed route object, or null for Level 1 index
    let navStack = []; // serialized routes for stack back()
    let editing = null; // secret key currently being edited
    let confirmRemove = false;
    let pendingDir = null; // 'fwd' | 'back' | null

    root.classList.add('sx', `sx-${layout}`);

    // Handle avatar image 404s cleanly under strict CSP (no inline onerror).
    root.addEventListener('error', (e) => {
      if (e.target && e.target.matches && e.target.matches('img[data-avatar]')) {
        e.target.remove();
      }
    }, true);
    root.addEventListener('load', (e) => {
      if (e.target && e.target.matches && e.target.matches('img[data-avatar]')) {
        e.target.parentElement?.querySelector('.sx-avatar-init')?.remove();
      }
    }, true);

    async function load() {
      const [settings, ov] = await Promise.all([
        api('/api/settings'),
        api('/api/overview').catch(() => null),
      ]);
      sections = settings.sections || [];
      overview = ov;
    }

    function section(id) {
      const direct = sections.find((s) => s.id === id);
      if (direct) return direct;
      const m = /^([a-z][a-z-]*)-(\d+)$/.exec(id || '');
      return m ? sections.find((s) => s.id === m[1]) : undefined;
    }

    function baseId(id) {
      const m = /^([a-z][a-z-]*)-(\d+)$/.exec(id || '');
      return m ? m[1] : id;
    }

    function instanceOf(id) {
      const m = /^([a-z][a-z-]*)-(\d+)$/.exec(id || '');
      if (!m) return null;
      return sections.some((s) => s.id === m[1]) ? { base: m[1], slot: Number(m[2]) } : null;
    }

    function fieldSlot(key) {
      const m = /_(\d)$/.exec(key || '');
      return m ? Number(m[1]) : 0;
    }

    function parseRoute(str) {
      const clean = String(str || '').replace(/^\/+|\/+$/g, '');
      if (!clean) return null;
      if (clean === 'add') return { kind: 'add', id: 'add', sub: null, depth: 1 };
      if (clean.startsWith('add/')) {
        const prov = clean.slice(4);
        if (prov === 'commandcode' || prov === 'opendesign') {
          return { kind: 'add-instance', id: prov, sub: null, depth: 2 };
        }
        return { kind: 'add', id: 'add', sub: null, depth: 1 };
      }
      if (clean === 'general') return { kind: 'general', id: 'general', sub: null, depth: 1 };
      if (clean === 'general/webhook') return { kind: 'webhook', id: 'general', sub: 'webhook', depth: 2 };
      if (clean === 'general/network') return { kind: 'network', id: 'general', sub: 'network', depth: 2 };
      const [id, sub] = clean.split('/');
      const sec = section(id);
      if (!sec) return null;
      if (sec.id === 'general') {
        if (sub === 'webhook') return { kind: 'webhook', id: 'general', sub: 'webhook', depth: 2 };
        if (sub === 'network') return { kind: 'network', id: 'general', sub: 'network', depth: 2 };
        return { kind: 'general', id: 'general', sub: null, depth: 1 };
      }
      if (sub === 'connection' || sub === 'schedule') {
        return { kind: sub, id, sub, depth: 2 };
      }
      return { kind: 'section', id, sub: null, depth: 1 };
    }

    function serializeRoute(r) {
      if (!r) return '';
      if (r.kind === 'add') return 'add';
      if (r.kind === 'add-instance') return `add/${r.id}`;
      if (r.kind === 'general') return 'general';
      if (r.kind === 'webhook') return 'general/webhook';
      if (r.kind === 'network') return 'general/network';
      if (r.kind === 'connection' || r.kind === 'schedule') return `${r.id}/${r.sub}`;
      return r.id || '';
    }

    function layoutGroups() {
      return (overview && overview.layout) || [];
    }

    function groupOf(id) {
      return layoutGroups().find((g) => g.id === id);
    }

    function accountsList() {
      return (overview && overview.accounts) || [];
    }

    function resolveGoogleAccount(id) {
      const g = groupOf(id);
      const inst = instanceOf(id);
      const slot = (g && g.slot) || (inst ? inst.slot : id === 'google' ? 1 : 0);
      const acc = accountsList().find((a) => a.slot === slot) || null;
      if (!slot) return null;
      if (g && (g.email || g.account_name || g.detail)) {
        return {
          slot,
          name: g.account_name || (acc && acc.name) || '',
          email: g.email || (acc && acc.email) || '',
          tier: g.detail || (acc && acc.tier) || '',
          signedIn: Boolean(g.email || g.account_name || acc),
        };
      }
      if (acc) {
        return {
          slot,
          name: acc.name || '',
          email: acc.email || '',
          tier: acc.tier || '',
          signedIn: true,
        };
      }
      return { slot, name: '', email: '', tier: '', signedIn: false };
    }

    function tierKind(tier) {
      const t = String(tier || '');
      if (/ultra/i.test(t)) return 'ultra';
      if (/pro/i.test(t)) return 'pro';
      return 'free';
    }

    function accountInitial(acc) {
      const s = (acc && (acc.name || acc.account_name || acc.email)) || 'G';
      return String(s).trim().charAt(0).toUpperCase() || 'G';
    }

    function titleFor(sec, id) {
      const g = groupOf(id) || (sec && groupOf(sec.id));
      if (g && g.title) return g.title;
      return sec ? sec.title : '';
    }

    function titleOfProvider(name) {
      const p = ((overview && overview.providers) || []).find((x) => x.name === name);
      return p ? p.title : name;
    }

    function providersForPage(sec, id) {
      if (!sec) return [];
      const g = groupOf(id);
      if (g && g.providers && g.providers.length) return g.providers;
      if (id && id.startsWith('google-')) return ['antigravity'];
      return sec.providers || [];
    }

    function runsForProviders(provs) {
      const runs = (overview && overview.runs) || [];
      return provs.map((p) => runs.find((r) => r.provider === p)).filter(Boolean);
    }

    function stateForProviders(provs) {
      if (!provs || !provs.length) return null;
      const runs = runsForProviders(provs);
      if (!runs.length) return { status: 'idle', run: null };
      const worst = runs.slice().sort((a, b) => SEVERITY.indexOf(a.status) - SEVERITY.indexOf(b.status))[0];
      return { status: worst.status, run: worst };
    }

    function navMeta(r) {
      if (!r) return { depth: 0, title: 'Settings', parent: null, providers: [] };
      if (r.kind === 'add') return { depth: 1, title: 'Add Subscription', parent: 'Settings', providers: [] };
      if (r.kind === 'add-instance') {
        const sec = section(r.id);
        return { depth: 2, title: `New ${sec ? sec.title : r.id}`, parent: 'Add Subscription', providers: [] };
      }
      if (r.kind === 'general') return { depth: 1, title: 'General', parent: 'Settings', providers: [] };
      if (r.kind === 'webhook') return { depth: 2, title: 'Alert Webhook', parent: 'General', providers: [] };
      if (r.kind === 'network') return { depth: 2, title: 'Network', parent: 'General', providers: [] };
      const sec = section(r.id);
      const secTitle = titleFor(sec, r.id) || r.id;
      const provs = providersForPage(sec, r.id);
      if (r.kind === 'connection') return { depth: 2, title: 'Connection & Auth', parent: secTitle, providers: provs };
      if (r.kind === 'schedule') return { depth: 2, title: 'Refresh Schedule', parent: secTitle, providers: provs };
      return { depth: 1, title: secTitle, parent: 'Settings', providers: provs };
    }

    const instanceBaseKeys = ['commandcode_api_key', 'opendesign_control_key'];

    function fieldsForPage(sec, id) {
      const inst = instanceOf(id);
      return sec.fields.filter((f) => {
        if (f.key === ICON_KEY) return false;
        const slot = fieldSlot(f.key);
        if (inst) {
          if (slot) return slot === inst.slot;
          return !instanceBaseKeys.includes(f.key);
        }
        return !slot;
      });
    }

    function isScheduleField(f) {
      return f.kind === 'number' && f.key.startsWith('refresh_') && f.unit;
    }

    function fieldsForConnection(sec, id) {
      if (id && id.startsWith('google-')) return [];
      return fieldsForPage(sec, id).filter((f) => !isScheduleField(f));
    }

    function fieldsForSchedule(sec, id) {
      const list = fieldsForPage(sec, id).filter(isScheduleField);
      if (id && id.startsWith('google-')) {
        return list.filter((f) => f.key === 'refresh_antigravity_minutes');
      }
      return list;
    }

    async function open(target) {
      try {
        await load();
      } catch (err) {
        root.innerHTML = `<p class="sx-empty">Cannot load settings: ${esc(err.message)}</p>`;
        return;
      }
      const parsed = parseRoute(target);
      if (layout === 'split') {
        route = parsed || parseRoute((sections[0] && sections[0].id) || 'general');
      } else {
        route = parsed;
      }
      navStack = [];
      editing = null;
      confirmRemove = false;
      pendingDir = null;
      shownKey = undefined;
      render();
    }

    function navigate(targetStr, dir = 'fwd') {
      const next = parseRoute(targetStr);
      if (layout === 'stack' && dir === 'fwd') {
        const curStr = serializeRoute(route);
        if (curStr !== serializeRoute(next)) {
          if (navStack[navStack.length - 1] !== curStr) navStack.push(curStr);
        }
      }
      route = next;
      editing = null;
      confirmRemove = false;
      pendingDir = dir;
      render();
    }

    function back() {
      if (layout !== 'stack' || !route) return false;
      let prevStr = navStack.length ? navStack.pop() : null;
      if (prevStr === null) {
        if (route.kind === 'add-instance') prevStr = 'add';
        else if (route.kind === 'webhook' || route.kind === 'network') prevStr = 'general';
        else if (route.kind === 'connection' || route.kind === 'schedule') prevStr = route.id;
        else prevStr = '';
      }
      route = parseRoute(prevStr);
      editing = null;
      confirmRemove = false;
      pendingDir = 'back';
      render();
      return true;
    }

    /* ---------- render helpers ---------- */

    function handle(label) {
      return `<button class="sx-handle" type="button" data-handle aria-label="Move ${esc(label)}; use the arrow keys">${GRIP}</button>`;
    }

    function visSwitch(kind, id, on, label) {
      return `<label class="sx-switch" title="${esc(label)}"><input type="checkbox" data-vis="${kind}" data-vis-id="${esc(id)}" aria-label="${esc(label)}"${on ? ' checked' : ''} /><span></span></label>`;
    }

    function badgeDot(st) {
      if (!st || st.status === 'ok' || st.status === 'idle') return '';
      return `<span class="sx-badge-dot ${esc(st.status)}"></span>`;
    }

    function sourceBadge(id, st, size = 'md') {
      const base = baseId(id);
      const meta = SOURCE_META[base] || SOURCE_META.general;
      return `<span class="sx-badge sx-badge-${size}" style="--tint: ${meta.tint}">${meta.icon}${badgeDot(st)}</span>`;
    }

    function googleAvatar(acc, st, size = 'md') {
      const kind = tierKind(acc.tier);
      const initial = accountInitial(acc);
      return `
        <span class="sx-avatar sx-avatar-${size}" data-tier="${kind}">
          <span class="sx-avatar-inner">
            <span class="sx-avatar-init">${esc(initial)}</span>
            <img data-avatar src="/api/avatar/${acc.slot}" alt="" />
          </span>
          ${badgeDot(st)}
        </span>`;
    }

    function microMeter(pct, tone, tint) {
      const segments = 12;
      const lit = Math.round((Math.max(0, Math.min(100, pct)) / 100) * segments);
      let bars = '';
      for (let i = 0; i < segments; i++) bars += `<i class="${i < lit ? 'on' : ''}"></i>`;
      return `<span class="sx-mmeter" data-tone="${esc(tone)}" style="--tint: ${tint}" aria-hidden="true">${bars}</span>`;
    }

    function quotaTone(pct) {
      if (pct <= 5) return 'danger';
      if (pct < 25) return 'warning';
      return 'ok';
    }

    function groupTelemetry(g, st, provs) {
      const quotas = (overview && overview.quotas) || [];
      const items = g.items || [];
      const activeItems = items.filter((it) => !it.hidden);
      const pool = activeItems.length ? activeItems : items;
      const snaps = pool.map((it) => quotas.find((q) => q.provider === it.provider && q.metric === it.metric)).filter(Boolean);

      // Find the lowest percentage window for the micro-meter summary.
      let minPct = null;
      let minTint = (SOURCE_META[baseId(g.id)] || SOURCE_META.general).tint;
      for (const sn of snaps) {
        const series = sn.series && sn.series.length ? sn.series : [sn];
        for (const s of series) {
          if ((s.unit || '%') === '%' && s.total > 0) {
            const pct = Math.max(0, Math.min(100, (s.remaining / s.total) * 100));
            if (minPct === null || pct < minPct) {
              minPct = pct;
              minTint = sn.metric.startsWith('claude_models') ? 'var(--claude)' : (SOURCE_META[baseId(g.id)] || SOURCE_META.general).tint;
            }
          }
        }
      }

      const runs = runsForProviders(provs);
      const brokenRuns = runs.filter((r) => ['expired_auth', 'error', 'unconfigured'].includes(r.status));
      let issueHtml = '';
      if (brokenRuns.length) {
        const r = brokenRuns[0];
        const shortWho = titleOfProvider(r.provider).replace(/^(Google|GCP)\s+/i, '');
        const who = provs.length > 1 ? `${shortWho} ` : '';
        const text = r.status === 'expired_auth'
          ? `${who}expired`
          : r.status === 'unconfigured'
            ? (provs.length > 1 ? `${who}setup` : 'Setup required')
            : `${who}error`;
        issueHtml = `<span class="sx-sub-issue is-${esc(r.status)}">${esc(text)}</span>`;
      }

      if (minPct !== null) {
        const tone = quotaTone(minPct);
        const pctText = `<span class="sx-sub-pct${tone !== 'ok' ? ` is-${tone}` : ''}">${Math.round(minPct)}% left</span>`;
        const extra = issueHtml || (snaps.length > 1 ? `<span class="sx-sub-dim">${pool.length} cards</span>` : '');
        return `${microMeter(minPct, tone, minTint)}${pctText}${extra ? `<span class="sx-sub-sep">·</span>${extra}` : ''}`;
      }

      if (snaps.length) {
        const sn = snaps[0];
        const text = `${fmtVal(sn.remaining, sn.unit)} ${esc(sn.unit || '')}`.trim();
        return `<span class="sx-sub-pct">${text}</span>${issueHtml ? `<span class="sx-sub-sep">·</span>${issueHtml}` : ''}`;
      }

      if (issueHtml) return issueHtml;
      return `<span class="sx-sub-dim">${esc(st ? (STATE_TEXT[st.status] || st.status) : 'Ready')}</span>`;
    }

    function cardLiveValue(it) {
      const q = ((overview && overview.quotas) || []).find((x) => x.provider === it.provider && x.metric === it.metric);
      if (!q) return { sub: '', right: '' };
      if (q.series && q.series.length) {
        const sub = q.series.map((s) => s.label).join(' · ');
        let lowPct = null;
        for (const s of q.series) {
          if ((s.unit || '%') === '%' && s.total > 0) {
            const p = Math.max(0, Math.min(100, (s.remaining / s.total) * 100));
            if (lowPct === null || p < lowPct) lowPct = p;
          }
        }
        if (lowPct !== null) {
          const tone = quotaTone(lowPct);
          return { sub, right: `<span class="sx-card-val${tone !== 'ok' ? ` is-${tone}` : ''}">${Math.round(lowPct)}<i>%</i></span>` };
        }
      }
      const unit = q.unit || '';
      return {
        sub: q.reset_at ? 'Resets periodically' : (fmtAgo(q.updated_at) ? `Updated ${fmtAgo(q.updated_at)}` : ''),
        right: `<span class="sx-card-val">${fmtVal(q.remaining, unit)}${unit ? `<i>${esc(unit)}</i>` : ''}</span>`,
      };
    }

    function cardDotClass(it) {
      if (it.provider === 'antigravity') {
        const base = it.metric.split('@')[0];
        if (base === 'gemini_models') return { tint: 'var(--gemini)', icon: FAMILY_ICONS.gemini };
        if (base === 'claude_models') return { tint: 'var(--claude)', icon: FAMILY_ICONS.claude };
      }
      if (it.provider === 'google-flow' || it.provider === 'colab') return { tint: 'var(--gemini)', icon: null };
      if (it.provider === 'gcp-billing' || it.provider === 'gcp-monitoring') return { tint: 'var(--gcp)', icon: null };
      const meta = SOURCE_META[it.provider] || SOURCE_META[baseId(it.provider)] || SOURCE_META.general;
      return { tint: meta.tint, icon: null };
    }

    function conSummary(sec, id, st) {
      if (id.startsWith('google-')) {
        const acc = resolveGoogleAccount(id);
        return acc && acc.signedIn ? 'OAuth connected' : 'Sign in required';
      }
      const fields = fieldsForConnection(sec, id);
      if (sec.id === 'google') {
        const acc = resolveGoogleAccount('google');
        const chrome = fields.find((f) => f.key === 'browser_cookies');
        const tok = fields.find((f) => f.key === 'antigravity_refresh_token');
        const src = overview && overview.sources && overview.sources.antigravity;
        const p1 = src === 'local_ide'
          ? 'Local IDE'
          : ((acc && acc.signedIn) || (tok && tok.configured) ? 'OAuth ready' : 'OAuth unset');
        const p2 = chrome && chrome.value === 'chrome' ? 'Chrome cookies' : 'Manual cookies';
        return `${p1} · ${p2}`;
      }
      if (sec.id === 'commandcode' || sec.id === 'opendesign') {
        const keyField = fields.find((f) => f.kind === 'secret');
        if (keyField && keyField.configured) return `Key ${keyField.value}`;
        return instanceOf(id) ? 'API key not set' : 'Auto CLI sign-in';
      }
      if (sec.id === 'gcp') {
        const proj = fields.find((f) => f.key === 'gcp_project_id');
        const tbl = fields.find((f) => f.key === 'gcp_billing_table');
        if (proj && proj.configured && tbl && tbl.configured) return proj.value;
        if (proj && proj.configured) return `${proj.value} · billing table unset`;
        return 'Project & billing export';
      }
      return st ? (STATE_TEXT[st.status] || st.status) : 'Credentials';
    }

    function schedSummary(sec, id) {
      const fields = fieldsForSchedule(sec, id);
      if (!fields.length) return 'Default';
      return fields.map((f) => {
        const val = f.configured ? f.value : f.default;
        return fields.length > 1 ? `${f.label} ${fmtMinutes(val)}` : `Every ${fmtMinutes(val)}`;
      }).join(' · ');
    }

    /* ---------- Level 1: Index ---------- */

    function renderIndex() {
      const groups = layoutGroups().filter((g) => section(g.id));
      const visibleCount = groups.filter((g) => !g.hidden).length;
      const curStr = serializeRoute(route);

      const groupRows = groups.map((g) => {
        const sec = section(g.id) || {};
        const provs = providersForPage(sec, g.id);
        const st = stateForProviders(provs);
        const isGoogle = g.id === 'google' || g.id.startsWith('google-');
        const inst = instanceOf(g.id);
        const active = layout === 'split' && (route && route.id === g.id) ? ' aria-current="true"' : '';

        let badge;
        let titleHtml;
        if (isGoogle) {
          const acc = resolveGoogleAccount(g.id);
          badge = acc && acc.signedIn ? googleAvatar(acc, st, 'sm') : sourceBadge(g.id, st, 'sm');
          const kind = tierKind(g.detail || (acc && acc.tier));
          const plan = g.detail || (acc && acc.tier);
          titleHtml = `<b>${esc(g.title)}</b>${plan ? `<span class="sx-plan ${kind === 'ultra' ? '' : kind}">${esc(plan)}</span>` : ''}`;
        } else {
          badge = sourceBadge(g.id, st, 'sm');
          if (inst && g.account_name) {
            titleHtml = `<b>${esc(sec.title)}</b><span class="sx-tag">${esc(g.account_name)}</span>`;
          } else if (g.account_name) {
            titleHtml = `<b>${esc(g.title)}</b><span class="sx-tag">${esc(g.account_name)}</span>`;
          } else {
            titleHtml = `<b>${esc(g.title)}</b>`;
          }
        }

        const issueBadge = st && ISSUE_PILL[st.status] && !isGoogle
          ? `<span class="sx-pill is-${esc(st.status)}">${ISSUE_PILL[st.status]}</span>` : '';

        return `
          <div class="sx-item sx-sortrow${g.hidden ? ' is-hidden' : ''}" data-sort-id="${esc(g.id)}" data-nav="${esc(g.id)}" role="button" tabindex="0"${active}>
            ${handle(g.account_name || g.email || g.title)}
            ${badge}
            <div class="sx-item-main">
              <div class="sx-item-top">${titleHtml}${issueBadge}</div>
              <div class="sx-item-sub">${groupTelemetry(g, st, provs)}</div>
            </div>
            ${visSwitch('group', g.id, !g.hidden, `Show ${g.title}`)}
            ${CHEVRON}
          </div>`;
      }).join('');

      const isAddActive = curStr === 'add' || Boolean(route && route.kind === 'add-instance');

      const genSec = section('general');
      const theme = localStorage.getItem('gauge-theme') || 'auto';
      const themeLabel = (THEMES.find((t) => t[0] === theme) || THEMES[0])[1];
      const iconField = genSec && genSec.fields.find((f) => f.key === ICON_KEY);
      const iconLabel = iconField ? (ICON_LABELS[iconField.value] || iconField.value) : 'Gauge';
      const webhookField = genSec && genSec.fields.find((f) => f.key === 'webhook_url');
      const webhookOn = Boolean(webhookField && webhookField.configured);

      return `
        <section class="sx-sec">
          <div class="sx-eyebrow-bar">
            <span class="sx-eyebrow">Subscriptions</span>
            <span class="sx-eyebrow-meta">${visibleCount} of ${groups.length} shown</span>
          </div>
          <div class="sx-list sx-sortlist" data-sort="groups">
            ${groupRows}
          </div>
          <p class="sx-hint">Drag grip to reorder · Switch toggles home visibility</p>
        </section>
        <section class="sx-sec">
          <div class="sx-eyebrow-bar">
            <span class="sx-eyebrow">Preferences</span>
          </div>
          <div class="sx-list">
            <div class="sx-item" data-nav="general" role="button" tabindex="0"${curStr === 'general' ? ' aria-current="true"' : ''}>
              ${sourceBadge('general', null, 'sm')}
              <div class="sx-item-main">
                <div class="sx-item-top"><b>Appearance</b></div>
                <div class="sx-item-sub"><span class="sx-sub-dim">${esc(themeLabel)} theme · ${esc(iconLabel)} mark</span></div>
              </div>
              <img class="sx-mini-mark" src="/mark.svg" width="18" height="18" alt="" />
              ${CHEVRON}
            </div>
            <div class="sx-item" data-nav="general/webhook" role="button" tabindex="0"${curStr === 'general/webhook' ? ' aria-current="true"' : ''}>
              ${sourceBadge('webhook', null, 'sm')}
              <div class="sx-item-main">
                <div class="sx-item-top"><b>Notifications</b></div>
                <div class="sx-item-sub"><span class="sx-sub-dim">State-change alert webhook</span></div>
              </div>
              <span class="sx-pill ${webhookOn ? 'is-ok' : 'is-muted'}">${webhookOn ? 'Active' : 'Off'}</span>
              ${CHEVRON}
            </div>
            <div class="sx-item" data-nav="general/network" role="button" tabindex="0"${curStr === 'general/network' ? ' aria-current="true"' : ''}>
              ${sourceBadge('network', null, 'sm')}
              <div class="sx-item-main">
                <div class="sx-item-top"><b>Network</b></div>
                <div class="sx-item-sub"><span class="sx-sub-dim">${esc(proxySummary(genSec))}</span></div>
              </div>
              ${CHEVRON}
            </div>
          </div>
        </section>
        <div class="sx-add-dock">
          <button class="sx-add-cta" type="button" data-nav="add"${isAddActive ? ' aria-current="true"' : ''}>
            <span class="sx-add-cta-icon" aria-hidden="true">
              <svg viewBox="0 0 16 16" width="13" height="13">
                <path fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" d="M8 3.2v9.6M3.2 8h9.6"/>
              </svg>
            </span>
            <span class="sx-add-cta-text">
              <b>Add subscription</b>
              <span>Google AI · Command Code · Open Design · GCP</span>
            </span>
            ${CHEVRON}
          </button>
        </div>`;
    }

    /* ---------- Level 2: Subscription Detail ---------- */

    function renderSplitSubnav(id, activeKind) {
      if (layout !== 'split') return '';
      return `
        <div class="sx-subnav" role="tablist">
          <button type="button" role="tab" aria-selected="${activeKind === 'section'}" data-nav="${esc(id)}">Overview</button>
          <button type="button" role="tab" aria-selected="${activeKind === 'connection'}" data-nav="${esc(id)}/connection">Connection &amp; Auth</button>
          <button type="button" role="tab" aria-selected="${activeKind === 'schedule'}" data-nav="${esc(id)}/schedule">Refresh Schedule</button>
        </div>`;
    }

    function renderHero(sec, id, st, provs) {
      const isGoogle = id === 'google' || id.startsWith('google-');
      const refreshBtn = layout === 'split'
        ? `<button class="sx-btn" type="button" data-refresh="${esc(provs.join(','))}">Refresh now</button>` : '';

      if (isGoogle) {
        const acc = resolveGoogleAccount(id);
        if (acc && acc.signedIn) {
          const kind = tierKind(acc.tier);
          return `
            <div class="sx-hero">
              ${googleAvatar(acc, st, 'lg')}
              <div class="sx-hero-text">
                <div class="sx-hero-top">
                  <b>${esc(acc.name || acc.email || 'Google Account')}</b>
                  ${acc.tier ? `<span class="sx-plan ${kind === 'ultra' ? '' : kind}">${esc(acc.tier)}</span>` : ''}
                </div>
                ${acc.email ? `<span class="sx-hero-sub">${esc(acc.email)}</span>` : ''}
              </div>
              ${refreshBtn}
            </div>`;
        }
        return `
          <div class="sx-hero">
            ${sourceBadge('google', st, 'lg')}
            <div class="sx-hero-text">
              <b>Google AI</b>
              <span class="sx-hero-sub">Sign in to read Antigravity quota and plan tier</span>
            </div>
            ${opts.oauth ? `<button class="sx-btn primary" type="button" data-oauth-slot="${acc ? acc.slot : 1}">Sign in</button>` : refreshBtn}
          </div>`;
      }

      const g = groupOf(id);
      const inst = instanceOf(id);
      const tag = (g && g.account_name) || (inst ? `Slot ${inst.slot}` : '');
      return `
        <div class="sx-hero">
          ${sourceBadge(id, st, 'lg')}
          <div class="sx-hero-text">
            <div class="sx-hero-top">
              <b>${esc(sec.title)}</b>
              ${tag ? `<span class="sx-tag">${esc(tag)}</span>` : ''}
            </div>
            ${sec.summary ? `<span class="sx-hero-sub">${esc(sec.summary)}</span>` : ''}
          </div>
          ${refreshBtn}
        </div>`;
    }

    function renderHealth(id, provs) {
      if (!provs.length) return '';
      const runs = runsForProviders(provs);
      const list = runs.length ? runs : provs.map((p) => ({ provider: p, status: 'idle' }));
      const lines = list.map((r) => {
        const name = provs.length > 1 ? titleOfProvider(r.provider) : 'Collector';
        const state = STATE_TEXT[r.status] || r.status;
        const when = r.last_run ? fmtAgo(r.last_run) : '';
        return `
          <div class="sx-health-line">
            <span class="sx-dot ${esc(r.status)}"></span>
            <span class="sx-health-name">${esc(name)}</span>
            <span class="sx-health-state is-${esc(r.status)}">${esc(state)}</span>
            ${when ? `<span class="sx-health-time">${esc(when)}</span>` : ''}
          </div>`;
      }).join('');

      const broken = list.find((r) => r.error && r.status !== 'ok');
      const callout = broken ? `
        <div class="sx-callout is-${esc(broken.status)}">
          <div class="sx-callout-msg">${esc(broken.error)}</div>
          <button class="sx-btn" type="button" data-nav="${esc(id)}/connection">Configure</button>
        </div>` : '';

      return `<div class="sx-health">${lines}${callout}</div>`;
    }

    function renderSection(sec, id) {
      const provs = providersForPage(sec, id);
      const st = stateForProviders(provs);
      const g = groupOf(id);
      const inst = instanceOf(id);
      const isGoogleExtra = id.startsWith('google-') && g && g.slot > 1 && opts.oauth;
      const removable = isGoogleExtra || Boolean(inst);

      let cardsHtml = '';
      if (g) {
        const visCount = g.items.filter((it) => !it.hidden).length;
        const rows = g.items.map((it) => {
          const info = cardLiveValue(it);
          const glyph = cardDotClass(it);
          const lead = glyph.icon
            ? `<span class="sx-fam-glyph" style="--tint: ${glyph.tint}">${glyph.icon}</span>`
            : `<span class="sx-card-dot" style="--tint: ${glyph.tint}"></span>`;
          return `
            <div class="sx-row sx-sortrow${it.hidden ? ' is-hidden' : ''}" data-sort-id="${esc(it.id)}">
              ${handle(it.label)}
              ${lead}
              <span class="sx-label">
                <b>${esc(it.label)}</b>
                ${info.sub ? `<span class="sx-help">${esc(info.sub)}</span>` : ''}
              </span>
              ${info.right}
              ${visSwitch('item', it.id, !it.hidden, `Show ${it.label}`)}
            </div>`;
        }).join('');

        cardsHtml = `
          <section class="sx-sec">
            <div class="sx-eyebrow-bar">
              <span class="sx-eyebrow">Menu bar &amp; overview</span>
              <span class="sx-eyebrow-meta">${g.items.length ? `${visCount} of ${g.items.length} shown` : 'No snapshots yet'}</span>
            </div>
            <div class="sx-list">
              <div class="sx-row">
                <span class="sx-label">
                  <b>Show in menu bar</b>
                  <span class="sx-help">${g.hidden ? 'Hidden from Home; snapshots still refresh.' : 'Drag cards below to set Home order.'}</span>
                </span>
                ${visSwitch('group', g.id, !g.hidden, `Show ${g.title}`)}
              </div>
              <div class="sx-sortlist${g.hidden ? ' muted' : ''}" data-sort="items:${esc(g.id)}">
                ${rows || '<div class="sx-row"><span class="sx-help">Cards appear after the first successful refresh.</span></div>'}
              </div>
            </div>
          </section>`;
      }

      const broken = st && ['expired_auth', 'error', 'unconfigured'].includes(st.status);
      const configHtml = `
        <section class="sx-sec">
          <div class="sx-eyebrow-bar"><span class="sx-eyebrow">Configuration</span></div>
          <div class="sx-list">
            <div class="sx-item" data-nav="${esc(id)}/connection" role="button" tabindex="0">
              ${sourceBadge('connection', broken ? st : null, 'sm')}
              <div class="sx-item-main">
                <div class="sx-item-top"><b>Connection &amp; Auth</b>${broken ? `<span class="sx-pill is-${esc(st.status)}">${ISSUE_PILL[st.status] || 'Check'}</span>` : ''}</div>
                <div class="sx-item-sub"><span class="sx-sub-dim">${esc(conSummary(sec, id, st))}</span></div>
              </div>
              ${CHEVRON}
            </div>
            <div class="sx-item" data-nav="${esc(id)}/schedule" role="button" tabindex="0">
              ${sourceBadge('schedule', null, 'sm')}
              <div class="sx-item-main">
                <div class="sx-item-top"><b>Refresh Schedule</b></div>
                <div class="sx-item-sub"><span class="sx-sub-dim">${esc(schedSummary(sec, id))}</span></div>
              </div>
              ${CHEVRON}
            </div>
          </div>
        </section>`;

      let removeHtml = '';
      if (removable) {
        const who = (g && (g.account_name || g.email)) || titleFor(sec, id);
        if (confirmRemove) {
          removeHtml = `
            <section class="sx-sec sx-danger-zone">
              <div class="sx-confirm-bar">
                <span>Remove <b>${esc(who)}</b> from this device?</span>
                <div class="sx-confirm-btns">
                  <button class="sx-btn" type="button" data-cancel-remove>Cancel</button>
                  <button class="sx-btn danger-fill" type="button" ${isGoogleExtra ? `data-do-remove-account="${g.slot}"` : 'data-do-remove-instance'}>Remove</button>
                </div>
              </div>
            </section>`;
        } else {
          removeHtml = `
            <section class="sx-sec sx-danger-zone">
              <button class="sx-danger-link" type="button" data-confirm-remove>Remove account from Gauge…</button>
            </section>`;
        }
      }

      return `
        ${renderSplitSubnav(id, 'section')}
        <section class="sx-sec sx-sec-hero">
          ${renderHero(sec, id, st, provs)}
          ${renderHealth(id, provs)}
        </section>
        ${cardsHtml}
        ${configHtml}
        ${removeHtml}`;
    }

    /* ---------- Level 2: Add Subscription ---------- */

    function primaryConfigured(provider, secretKey) {
      const sec = section(provider);
      const keyField = sec && sec.fields.find((f) => f.key === secretKey);
      if (keyField && keyField.configured) return true;
      const g = groupOf(provider);
      if (g && g.items && g.items.length > 0) return true;
      const st = stateForProviders([provider]);
      return Boolean(st && st.status === 'ok');
    }

    function renderAdd() {
      const accounts = accountsList();
      const groups = layoutGroups();
      const ccPrimary = primaryConfigured('commandcode', 'commandcode_api_key');
      const ccExtras = groups.filter((g) => g.id.startsWith('commandcode-')).length;
      const ccCount = (ccPrimary ? 1 : 0) + ccExtras;
      const ccTotalSlots = groups.filter((g) => g.id === 'commandcode' || g.id.startsWith('commandcode-')).length;

      const odPrimary = primaryConfigured('opendesign', 'opendesign_control_key');
      const odExtras = groups.filter((g) => g.id.startsWith('opendesign-')).length;
      const odCount = (odPrimary ? 1 : 0) + odExtras;
      const odTotalSlots = groups.filter((g) => g.id === 'opendesign' || g.id.startsWith('opendesign-')).length;

      const gcpSec = section('gcp');
      const gcpProj = gcpSec && gcpSec.fields.find((f) => f.key === 'gcp_project_id');
      const gcpTbl = gcpSec && gcpSec.fields.find((f) => f.key === 'gcp_billing_table');
      const gcpReady = Boolean(gcpProj && gcpProj.configured && gcpTbl && gcpTbl.configured);

      const googleAtMax = accounts.length >= 3;
      const googleAction = opts.oauth && !googleAtMax
        ? 'data-oauth-slot="next" role="button" tabindex="0"'
        : !opts.oauth ? 'data-nav="google" role="button" tabindex="0"' : 'aria-disabled="true"';

      const ccFull = ccTotalSlots >= 3 && ccPrimary;
      const odFull = odTotalSlots >= 3 && odPrimary;
      const ccTarget = ccPrimary ? 'add/commandcode' : 'commandcode/connection';
      const odTarget = odPrimary ? 'add/opendesign' : 'opendesign/connection';

      return `
        <section class="sx-sec">
          <p class="sx-lead">Connect a new quota source or sign in another account. Up to 3 accounts are supported per provider.</p>
          <div class="sx-list">
            <div class="sx-item${googleAtMax && opts.oauth ? ' is-disabled' : ''}" ${googleAction}>
              ${sourceBadge('google', null, 'md')}
              <div class="sx-item-main">
                <div class="sx-item-top"><b>Google AI Account</b></div>
                <div class="sx-item-sub"><span class="sx-sub-dim">${esc(SOURCE_META.google.desc)} · ${accounts.length}/3 connected</span></div>
              </div>
              <span class="sx-pill ${googleAtMax ? 'is-muted' : 'is-accent'}">${googleAtMax ? 'Max 3' : (opts.oauth ? 'OAuth' : 'Open')}</span>
              ${googleAtMax && opts.oauth ? '' : CHEVRON}
            </div>

            <div class="sx-item${ccFull ? ' is-disabled' : ''}" ${ccFull ? 'aria-disabled="true"' : `data-nav="${ccTarget}" role="button" tabindex="0"`}>
              ${sourceBadge('commandcode', null, 'md')}
              <div class="sx-item-main">
                <div class="sx-item-top"><b>Command Code Account</b></div>
                <div class="sx-item-sub"><span class="sx-sub-dim">${esc(SOURCE_META.commandcode.desc)} · ${ccCount}/3 connected</span></div>
              </div>
              <span class="sx-pill ${ccFull ? 'is-muted' : (ccPrimary ? 'is-muted' : 'is-accent')}">${ccFull ? 'Max 3' : 'API key'}</span>
              ${ccFull ? '' : CHEVRON}
            </div>

            <div class="sx-item${odFull ? ' is-disabled' : ''}" ${odFull ? 'aria-disabled="true"' : `data-nav="${odTarget}" role="button" tabindex="0"`}>
              ${sourceBadge('opendesign', null, 'md')}
              <div class="sx-item-main">
                <div class="sx-item-top"><b>Open Design Account</b></div>
                <div class="sx-item-sub"><span class="sx-sub-dim">${esc(SOURCE_META.opendesign.desc)} · ${odCount}/3 connected</span></div>
              </div>
              <span class="sx-pill ${odFull ? 'is-muted' : (odPrimary ? 'is-muted' : 'is-accent')}">${odFull ? 'Max 3' : 'Control key'}</span>
              ${odFull ? '' : CHEVRON}
            </div>

            <div class="sx-item" data-nav="${gcpReady ? 'gcp' : 'gcp/connection'}" role="button" tabindex="0">
              ${sourceBadge('gcp', null, 'md')}
              <div class="sx-item-main">
                <div class="sx-item-top"><b>Google Cloud</b></div>
                <div class="sx-item-sub"><span class="sx-sub-dim">${esc(SOURCE_META.gcp.desc)}</span></div>
              </div>
              <span class="sx-pill ${gcpReady ? 'is-ok' : 'is-warning'}">${gcpReady ? 'Configured' : 'Setup'}</span>
              ${CHEVRON}
            </div>
          </div>
        </section>`;
    }

    /* ---------- Level 2: General & Appearance ---------- */

    function themePreviewThumb(mode) {
      if (mode === 'auto') {
        return `
          <span class="sx-tthumb is-auto" aria-hidden="true">
            <span class="sx-tthumb-half is-light"><i></i><i></i></span>
            <span class="sx-tthumb-half is-dark"><i></i><i></i></span>
          </span>`;
      }
      return `
        <span class="sx-tthumb is-${mode}" aria-hidden="true">
          <span class="sx-tthumb-bar"></span>
          <span class="sx-tthumb-ticks"><i></i><i></i><i></i><u></u></span>
        </span>`;
    }

    function renderGeneral(sec) {
      const theme = localStorage.getItem('gauge-theme') || 'auto';
      const iconField = sec.fields.find((f) => f.key === ICON_KEY);
      const webhookField = sec.fields.find((f) => f.key === 'webhook_url');
      const webhookOn = Boolean(webhookField && webhookField.configured);

      const themeCards = THEMES.map(([val, label, sub]) => `
        <button class="sx-vis-card" type="button" role="radio" data-theme-choice="${val}" aria-checked="${val === theme}">
          ${themePreviewThumb(val)}
          <b>${esc(label)}</b>
          <span>${esc(sub)}</span>
        </button>`).join('');

      const iconCards = iconField ? (iconField.options || []).map((o) => `
        <button class="sx-vis-card sx-icon-card" type="button" role="radio" data-icon-choice="${esc(o)}" aria-checked="${o === iconField.value}">
          <span class="sx-icon-stage">
            <img data-icon-thumb src="/mark.svg?icon=${encodeURIComponent(o)}" width="30" height="30" alt="" />
            <span class="sx-tray-pill" title="Menu bar template">${TRAY_GLYPH_SVG[o] || ''}<span>21:09</span></span>
          </span>
          <b>${esc(ICON_LABELS[o] || o)}</b>
          <span>${esc(ICON_DESC[o] || '')}</span>
        </button>`).join('') : '';

      return `
        <section class="sx-sec">
          <div class="sx-eyebrow-bar"><span class="sx-eyebrow">Theme</span></div>
          <div class="sx-theme-grid" role="radiogroup" aria-label="Theme">${themeCards}</div>
        </section>
        ${iconCards ? `
        <section class="sx-sec">
          <div class="sx-eyebrow-bar"><span class="sx-eyebrow">App &amp; menu bar icon</span></div>
          <div class="sx-icon-grid" role="radiogroup" aria-label="Icon">${iconCards}</div>
        </section>` : ''}
        <section class="sx-sec">
          <div class="sx-eyebrow-bar"><span class="sx-eyebrow">Notifications</span></div>
          <div class="sx-list">
            <div class="sx-item" data-nav="general/webhook" role="button" tabindex="0">
              ${sourceBadge('webhook', null, 'sm')}
              <div class="sx-item-main">
                <div class="sx-item-top"><b>Alert Webhook</b></div>
                <div class="sx-item-sub"><span class="sx-sub-dim">${webhookOn ? `Endpoint ${esc(webhookField.value)}` : 'Receive JSON on state changes'}</span></div>
              </div>
              <span class="sx-pill ${webhookOn ? 'is-ok' : 'is-muted'}">${webhookOn ? 'Active' : 'Off'}</span>
              ${CHEVRON}
            </div>
          </div>
        </section>
        <section class="sx-sec">
          <div class="sx-eyebrow-bar"><span class="sx-eyebrow">Network</span></div>
          <div class="sx-list">
            <div class="sx-item" data-nav="general/network" role="button" tabindex="0">
              ${sourceBadge('network', null, 'sm')}
              <div class="sx-item-main">
                <div class="sx-item-top"><b>Proxy</b></div>
                <div class="sx-item-sub"><span class="sx-sub-dim">${esc(proxySummary(sec))}</span></div>
              </div>
              ${CHEVRON}
            </div>
          </div>
        </section>`;
    }

    // proxySummary names the route in one line for the General and index rows.
    function proxySummary(sec) {
      const f = sec && sec.fields.find((x) => x.key === 'proxy_url');
      if (!f || !f.configured) return 'Automatic (system proxy)';
      return f.value;
    }

    /* ---------- Level 3: Connection & Auth ---------- */

    function labelBlock(f) {
      return `<span class="sx-label"><b>${esc(f.label)}</b>${f.help ? `<span class="sx-help">${esc(f.help)}</span>` : ''}</span>`;
    }

    function renderSecret(f, autoOpen = false) {
      const key = esc(f.key);
      const isEditing = editing === f.key || (autoOpen && !f.configured);
      const state = f.configured
        ? `<span class="sx-secret-pill is-set">${esc(f.value || 'Saved')}</span>`
        : `<span class="sx-secret-pill">${f.optional ? 'Not set' : 'Required'}</span>`;
      const buttons = f.configured
        ? `<button class="sx-btn" type="button" data-edit="${key}">Replace</button>
           <button class="sx-btn danger" type="button" data-clear="${key}">Remove</button>`
        : `<button class="sx-btn" type="button" data-edit="${key}">Set…</button>`;

      if (isEditing) {
        const ctl = f.multiline
          ? `<textarea class="sx-input" data-secret="${key}" rows="4" placeholder="${esc(f.placeholder || '')}" spellcheck="false"></textarea>`
          : `<input class="sx-input" data-secret="${key}" type="${f.key === 'webhook_url' ? 'url' : 'password'}" placeholder="${esc(f.placeholder || '')}" autocomplete="off" spellcheck="false" />`;
        return `
          <div class="sx-row stack" data-key="${key}">
            ${labelBlock(f)}
            <div class="sx-editor">
              ${ctl}
              <div class="sx-editor-actions">
                ${autoOpen && !f.configured ? '' : '<button class="sx-btn" type="button" data-cancel>Cancel</button>'}
                <button class="sx-btn primary" type="button" data-save-secret="${key}">Save</button>
              </div>
            </div>
          </div>`;
      }

      return `
        <div class="sx-row stack" data-key="${key}">
          ${labelBlock(f)}
          <div class="sx-secret">
            ${state}
            <div class="sx-secret-actions">${buttons}</div>
          </div>
        </div>`;
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
      if ((f.placeholder || '').length <= 20 && (value || '').length <= 20) {
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

    function renderOAuthCard(id) {
      const acc = resolveGoogleAccount(id);
      if (!acc) return '';
      const btnText = acc.signedIn ? 'Re-authenticate' : 'Sign in with Google';
      const btnClass = acc.signedIn ? 'sx-btn' : 'sx-btn primary';
      const action = opts.oauth
        ? `<button class="${btnClass}" type="button" data-oauth-slot="${acc.slot}">${btnText}</button>`
        : '';
      return `
        <div class="sx-oauth-box">
          ${acc.signedIn ? googleAvatar(acc, null, 'md') : sourceBadge('google', null, 'md')}
          <div class="sx-oauth-info">
            <b>${esc(acc.signedIn ? (acc.name || acc.email || `Slot ${acc.slot}`) : 'Not signed in')}</b>
            <span>${esc(acc.signedIn ? (acc.email || 'Google OAuth connected') : 'Connect via Google sign-in')}</span>
          </div>
          ${action}
        </div>`;
    }

    function renderConnection(sec, id) {
      const subnav = renderSplitSubnav(id, 'connection');
      const inst = instanceOf(id);

      // Extra Google identity (slot 2 or 3): isolated OAuth slot, no slot-1 cookies.
      if (id.startsWith('google-')) {
        return `
          ${subnav}
          <section class="sx-sec">
            <div class="sx-eyebrow-bar"><span class="sx-eyebrow">Google OAuth</span></div>
            ${renderOAuthCard(id)}
            <p class="sx-hint">This account monitors Antigravity quota using its own encrypted OAuth refresh token (slot ${inst ? inst.slot : ''}). Flow credits and Colab units belong to the primary Google AI account.</p>
          </section>`;
      }

      const fields = fieldsForConnection(sec, id);
      if (id === 'google') {
        const tokenField = fields.find((f) => f.key === 'antigravity_refresh_token');
        const chromeSwitch = fields.find((f) => f.key === 'browser_cookies');
        const cookieFields = fields.filter((f) => f.key === 'flow_cookie' || f.key === 'colab_cookie');
        const chromeOn = chromeSwitch && chromeSwitch.value === 'chrome';

        return `
          ${subnav}
          <section class="sx-sec">
            <div class="sx-eyebrow-bar"><span class="sx-eyebrow">Antigravity OAuth</span></div>
            ${renderOAuthCard('google')}
            ${tokenField ? `<div class="sx-list sx-list-inset">${renderField(tokenField)}</div>` : ''}
          </section>
          <section class="sx-sec">
            <div class="sx-eyebrow-bar">
              <span class="sx-eyebrow">Flow &amp; Colab cookies</span>
              ${chromeOn ? '<span class="sx-eyebrow-meta">Via Chrome</span>' : ''}
            </div>
            <div class="sx-list">
              ${chromeSwitch ? renderField(chromeSwitch) : ''}
              <div class="${chromeOn ? 'sx-dimmed' : ''}">
                ${cookieFields.map(renderField).join('')}
              </div>
            </div>
          </section>`;
      }

      if (sec.id === 'gcp') {
        const exportKeys = ['gcp_project_id', 'gcp_billing_table', 'gcp_initial_credits', 'gcp_currency'];
        const exportFields = fields.filter((f) => exportKeys.includes(f.key));
        const authFields = fields.filter((f) => !exportKeys.includes(f.key));
        return `
          ${subnav}
          <section class="sx-sec">
            <div class="sx-eyebrow-bar"><span class="sx-eyebrow">BigQuery export</span></div>
            <div class="sx-list">${exportFields.map(renderField).join('')}</div>
          </section>
          <section class="sx-sec">
            <div class="sx-eyebrow-bar"><span class="sx-eyebrow">Authentication</span></div>
            <div class="sx-list">${authFields.map(renderField).join('')}</div>
            <p class="sx-hint">Falls back to Application Default Credentials (gcloud auth) when both fields are empty.</p>
          </section>`;
      }

      // Command Code, Open Design, or generic source: open the credential input
      // straight away when no key is stored yet so onboarding takes one step.
      const autoOpenSecret = sec.id === 'commandcode' || sec.id === 'opendesign';
      return `
        ${subnav}
        <section class="sx-sec">
          <div class="sx-eyebrow-bar"><span class="sx-eyebrow">${inst ? 'Account credentials' : 'Credentials'}</span></div>
          <div class="sx-list">${fields.map((f) => (f.kind === 'secret' ? renderSecret(f, autoOpenSecret) : renderField(f))).join('')}</div>
        </section>`;
    }

    /* ---------- Level 3: Refresh Schedule ---------- */

    function renderSchedule(sec, id) {
      const fields = fieldsForSchedule(sec, id);
      const provs = providersForPage(sec, id);
      const runs = runsForProviders(provs);

      const blocks = fields.map((f) => {
        const key = esc(f.key);
        const prov = f.key.replace(/^refresh_/, '').replace(/_minutes$/, '');
        const run = runs.find((r) => r.provider === prov);
        const currentNum = Number(f.configured ? f.value : f.default);
        const presets = presetsFor(f);
        const defNum = Number(f.default);

        const chips = presets.map((v) => {
          const active = currentNum === v;
          return `<button type="button" class="sx-chip" data-preset="${v}" data-preset-key="${key}" data-default="${defNum}" aria-pressed="${active}">${fmtMinutes(v)}</button>`;
        }).join('');

        return `
          <div class="sx-sched-row" data-key="${key}">
            <div class="sx-sched-head">
              <span class="sx-label">
                <b>${esc(f.label)}</b>
                <span class="sx-help">Minimum ${esc(f.min || 1)} min · Default ${esc(f.default)} min${run && run.last_run ? ` · Last ${esc(fmtAgo(run.last_run))}` : ''}</span>
              </span>
              <span class="sx-flag" aria-live="polite"></span>
              <span class="sx-num">
                <input class="sx-input num" data-text="${key}" type="number" step="1" min="${esc(f.min || 1)}" inputmode="numeric"
                  value="${esc(f.configured ? f.value : '')}" placeholder="${esc(f.default || '')}" autocomplete="off" />
                <i>min</i>
              </span>
            </div>
            <div class="sx-presets" role="group" aria-label="${esc(f.label)} presets">${chips}</div>
          </div>`;
      }).join('');

      return `
        ${renderSplitSubnav(id, 'schedule')}
        <section class="sx-sec">
          <p class="sx-lead">How often Gauge polls each collector in the background. Selecting the default clears any custom override.</p>
          <div class="sx-list">${blocks || '<div class="sx-row"><span class="sx-help">No configurable intervals for this source.</span></div>'}</div>
        </section>`;
    }

    /* ---------- Level 3: Add Instance Form ---------- */

    function renderAddInstance(provider) {
      const sec = section(provider);
      const title = sec ? sec.title : provider;
      const secretField = sec && sec.fields.find((f) => f.kind === 'secret');
      const backBtn = layout === 'split'
        ? `<button class="sx-back-crumb" type="button" data-nav="add">‹ Add Subscription</button>` : '';
      return `
        ${backBtn}
        <section class="sx-sec">
          <div class="sx-hero">
            ${sourceBadge(provider, null, 'lg')}
            <div class="sx-hero-text">
              <b>New ${esc(title)}</b>
              <span class="sx-hero-sub">Tracked as its own subscription in the menu bar.</span>
            </div>
          </div>
          <div class="sx-form">
            <label class="sx-field-block">
              <span class="sx-field-lbl">Account name</span>
              <input class="sx-input" data-new-instance-name type="text" placeholder="e.g. Work or Studio" autocomplete="off" spellcheck="false" />
            </label>
            <label class="sx-field-block">
              <span class="sx-field-lbl">${esc(secretField ? secretField.label : 'API key')} <i>(optional)</i></span>
              <input class="sx-input" data-new-instance-secret type="password" placeholder="${esc(secretField ? secretField.placeholder : '')}" autocomplete="off" spellcheck="false" />
              <span class="sx-help">Stored encrypted on this device. You can also add or replace it later.</span>
            </label>
            <div class="sx-form-actions">
              <button class="sx-btn" type="button" data-back>Cancel</button>
              <button class="sx-btn primary" type="button" data-create-instance="${esc(provider)}">Add account</button>
            </div>
          </div>
        </section>`;
    }

    /* ---------- Level 3: Alert Webhook ---------- */

    function renderWebhook(sec) {
      const f = sec && sec.fields.find((x) => x.key === 'webhook_url');
      const backBtn = layout === 'split'
        ? `<button class="sx-back-crumb" type="button" data-nav="general">‹ General</button>` : '';
      if (!f) return '<p class="sx-empty">Webhook setting unavailable.</p>';
      return `
        ${backBtn}
        <section class="sx-sec">
          <p class="sx-lead">Sends an HTTP POST JSON payload whenever a source transitions state (expired auth, errors, or low quota).</p>
          <div class="sx-list">${renderSecret(f, true)}</div>
        </section>
        <section class="sx-sec">
          <div class="sx-eyebrow-bar"><span class="sx-eyebrow">Payload format</span></div>
          <pre class="sx-code">{
  "text": "Gauge · antigravity: Claude &amp; GPT models at 12%",
  "provider": "antigravity",
  "message": "Claude &amp; GPT models at 12%",
  "source": "gauge"
}</pre>
        </section>`;
    }

    /* ---------- Level 3: Network (proxy) ---------- */

    function renderNetwork(sec) {
      const f = sec && sec.fields.find((x) => x.key === 'proxy_url');
      const backBtn = layout === 'split'
        ? `<button class="sx-back-crumb" type="button" data-nav="general">‹ General</button>` : '';
      if (!f) return `${backBtn}<p class="sx-empty">Proxy setting unavailable.</p>`;
      return `
        ${backBtn}
        <section class="sx-sec">
          <p class="sx-lead">Every upstream request — Google, Command Code, Open Design — takes this route. Empty follows the environment, then the macOS system proxy, then a direct connection.</p>
          <div class="sx-list">${renderField(f)}</div>
          <div class="sx-form-actions">
            <button class="sx-btn" type="button" data-proxy-test>Test request</button>
            <button class="sx-btn primary" type="button" data-proxy-detect>Detect local proxy</button>
          </div>
        </section>
        <section class="sx-sec">
          <div class="sx-eyebrow-bar"><span class="sx-eyebrow">Current route</span></div>
          <div class="sx-net">
            <span class="sx-pill is-muted" data-net-pill>Checking…</span>
            <span class="sx-net-text" data-net-text></span>
          </div>
          <div class="sx-net-found" data-net-found hidden></div>
          <p class="sx-hint">Calls to 127.0.0.1 (the Antigravity IDE) never use the proxy.</p>
        </section>`;
    }

    // netStatus paints the saved route without touching the network.
    async function netStatus() {
      try {
        const res = await api('/api/proxy');
        paintNet(res.config);
      } catch (err) {
        paintNet(null, err.message);
      }
    }

    function paintNet(cfg, error) {
      const pill = root.querySelector('[data-net-pill]');
      const text = root.querySelector('[data-net-text]');
      if (!pill || !text) return;
      if (!cfg) {
        pill.className = 'sx-pill is-error';
        pill.textContent = 'Unknown';
        text.textContent = error || '';
        return;
      }
      const origin = { setting: 'custom', environment: 'environment', system: 'system proxy' }[cfg.source];
      if (cfg.url) {
        pill.className = 'sx-pill is-ok';
        pill.textContent = cfg.source === 'setting' ? 'Custom' : 'Automatic';
        text.textContent = `${cfg.url}${origin && cfg.source !== 'setting' ? ` · ${origin}` : ''}`;
      } else {
        pill.className = 'sx-pill is-muted';
        pill.textContent = 'Direct';
        text.textContent = cfg.source === 'setting' ? 'proxy off' : 'no proxy configured';
      }
    }

    // runProxyCheck probes the saved route (or what the field currently holds)
    // and lists the local proxies that answered, so a new machine needs one
    // click instead of a research session.
    async function runProxyCheck() {
      const input = root.querySelector('[data-text="proxy_url"]');
      const proxy = input ? input.value.trim() : '';
      const found = root.querySelector('[data-net-found]');
      for (const b of root.querySelectorAll('[data-proxy-test], [data-proxy-detect]')) b.disabled = true;
      try {
        const res = await api('/api/proxy/test', { method: 'POST', body: JSON.stringify({ proxy }) });
        paintNet(res.config);
        const text = root.querySelector('[data-net-text]');
        if (text) {
          text.textContent = res.ok
            ? `reached Google in ${Math.max(1, res.latency_ms)} ms`
            : `failed — ${res.error || 'no answer'}`;
          text.dataset.state = res.ok ? 'ok' : 'error';
        }
        if (found) {
          const cands = res.candidates || [];
          found.hidden = false;
          found.innerHTML = cands.length
            ? `<span class="sx-net-found-head">Local proxies</span>${cands.map((c) => `
                <button class="sx-chip" type="button" data-proxy-use="${esc(c.url)}" aria-label="Use ${esc(c.url)}">
                  ${esc(c.url.replace(/^http:\/\//, ''))}<i>${Math.max(1, c.latency_ms)} ms</i>
                </button>`).join('')}`
            : `<span class="sx-sub-dim">No local proxy answered on the usual ports (Clash, Surge, v2rayN).</span>`;
          if (opts.onRender) opts.onRender();
        }
      } catch (err) {
        toast(err.message);
        if (found) {
          found.hidden = false;
          found.innerHTML = `<span class="sx-sub-dim">${esc(err.message)}</span>`;
        }
      } finally {
        for (const b of root.querySelectorAll('[data-proxy-test], [data-proxy-detect]')) b.disabled = false;
      }
    }

    /* ---------- main render ---------- */

    function renderDetailView(r) {
      if (!r) return '';
      if (r.kind === 'add') return renderAdd();
      if (r.kind === 'add-instance') return renderAddInstance(r.id);
      const sec = section(r.id);
      if (!sec) return '';
      if (r.kind === 'general') return renderGeneral(sec);
      if (r.kind === 'webhook') return renderWebhook(sec);
      if (r.kind === 'network') return renderNetwork(sec);
      if (r.kind === 'connection') return renderConnection(sec, r.id);
      if (r.kind === 'schedule') return renderSchedule(sec, r.id);
      return renderSection(sec, r.id);
    }

    let shownKey;
    function render() {
      const curKey = serializeRoute(route);
      const moved = shownKey !== curKey;
      shownKey = curKey;
      const scroller = opts.scroller || root;
      const top = scroller.scrollTop;
      const anim = moved && pendingDir ? ` sx-anim-${pendingDir}` : '';
      pendingDir = null;

      if (layout === 'split') {
        root.innerHTML = `
          <nav class="sx-index">${renderIndex()}</nav>
          <div class="sx-detail${anim}">${renderDetailView(route)}</div>`;
      } else if (route) {
        root.innerHTML = `<div class="sx-detail${anim}">${renderDetailView(route)}</div>`;
      } else {
        root.innerHTML = `<nav class="sx-index${anim}">${renderIndex()}</nav>`;
      }

      if (moved) {
        scroller.scrollTop = 0;
        if (opts.onNavigate) opts.onNavigate(navMeta(route));
      } else {
        scroller.scrollTop = top;
      }
      if (opts.onRender) opts.onRender();
      if (route && route.kind === 'network') netStatus();
    }

    /* ---------- persistence & layout ---------- */

    function fieldOf(key) {
      for (const s of sections) {
        const f = s.fields.find((x) => x.key === key);
        if (f) return f;
      }
      return null;
    }

    function flag(key, state, text) {
      const el = root.querySelector(`.sx-row[data-key="${CSS.escape(key)}"] .sx-flag, .sx-sched-row[data-key="${CSS.escape(key)}"] .sx-flag`);
      if (!el) return;
      el.dataset.state = state;
      el.textContent = text || '';
      if (state === 'saved') {
        setTimeout(() => {
          if (el.dataset.state === 'saved') {
            el.textContent = '';
            el.dataset.state = '';
          }
        }, 1600);
      }
    }

    async function save(key, value) {
      const f = fieldOf(key);
      try {
        if (value === '') await api(`/api/settings/${encodeURIComponent(key)}`, { method: 'DELETE' });
        else await api('/api/settings', { method: 'POST', body: JSON.stringify({ [key]: value }) });
        if (f) {
          f.configured = value !== '';
          f.value = value === '' ? (f.default || '') : (f.sensitive ? '••••' + value.slice(-4) : value);
        }
        if (opts.onSaved) opts.onSaved(key);
        return true;
      } catch (err) {
        toast(err.message);
        return false;
      }
    }

    function schedulePostSaveRefresh() {
      for (const delay of [1500, 4000]) {
        setTimeout(() => {
          if (!root.isConnected || document.hidden) return;
          if (opts.onSaved) opts.onSaved();
          reloadSection();
        }, delay);
      }
    }

    function triggersRefresh(key) {
      return key === 'proxy_url'
        || key === 'browser_cookies'
        || key === 'flow_cookie'
        || key === 'colab_cookie'
        || key.startsWith('antigravity_refresh_token')
        || key.startsWith('commandcode_api_key')
        || key.startsWith('opendesign_control_key')
        || key.startsWith('gcp_')
        || key === 'google_refresh_token';
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
        if (isScheduleField(f)) render();
        flag(key, 'saved', value === '' && f.default ? `Reset to ${f.default}` : 'Saved');
        if (value === '' && f.default) {
          const cur = root.querySelector(`[data-text="${CSS.escape(key)}"]`);
          if (cur) cur.placeholder = f.default;
        }
        if (triggersRefresh(key)) schedulePostSaveRefresh();
      } else {
        flag(key, 'error', 'Not saved');
      }
    }

    async function saveLayout() {
      const body = {
        groups: layoutGroups().map((g) => ({
          id: g.id,
          hidden: !!g.hidden,
          items: (g.items || []).map((it) => ({ id: it.id, hidden: !!it.hidden })),
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
        return;
      }
      for (const g of layoutGroups()) {
        const it = g.items.find((x) => x.id === id);
        if (it) it.hidden = !visible;
      }
    }

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

    async function reloadSection() {
      try {
        await load();
      } catch { /* keep last paint */ }
      render();
    }

    async function refreshProviders(names) {
      const results = await Promise.all(names.map((n) =>
        api(`/api/refresh/${encodeURIComponent(n)}`, { method: 'POST' }).catch((err) => ({ error: err.message }))));
      const bad = results.find((r) => r && r.error);
      toast(bad ? (bad.error === 'cooldown' ? 'Recently refreshed; try in a moment' : 'Still failing; see status details') : 'Refreshed');
      if (opts.onSaved) opts.onSaved();
      await reloadSection();
    }

    async function refreshCurrent() {
      const m = navMeta(route);
      if (!m.providers || !m.providers.length) return;
      await refreshProviders(m.providers);
    }

    /* ---------- drag reordering ---------- */

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

    /* ---------- interactions ---------- */

    async function createInstance(provider, btn) {
      const nameEl = root.querySelector('[data-new-instance-name]');
      const secEl = root.querySelector('[data-new-instance-secret]');
      const name = (nameEl && nameEl.value.trim()) || '';
      const secret = (secEl && secEl.value.trim()) || '';
      if (!name) {
        toast('Give the account a name first');
        nameEl?.focus();
        return;
      }
      if (btn) btn.disabled = true;
      try {
        const res = await api(`/api/instances/${encodeURIComponent(provider)}`, {
          method: 'POST',
          body: JSON.stringify({ name }),
        });
        if (secret) {
          const baseKey = provider === 'commandcode' ? 'commandcode_api_key' : 'opendesign_control_key';
          await api('/api/settings', {
            method: 'POST',
            body: JSON.stringify({ [`${baseKey}_${res.slot}`]: secret }),
          });
          api(`/api/refresh/${encodeURIComponent(provider)}`, { method: 'POST' }).catch(() => {});
        }
        await load();
        if (opts.onSaved) opts.onSaved();
        navStack = [''];
        navigate(`${res.provider}-${res.slot}`, 'fwd');
        toast(`Added ${name}`);
      } catch (err) {
        toast(err.message);
        if (btn) btn.disabled = false;
      }
    }

    root.addEventListener('click', async (e) => {
      if (e.target.closest('[data-handle], .sx-switch')) return;

      const nav = e.target.closest('[data-nav]');
      if (nav && nav.getAttribute('aria-disabled') !== 'true') {
        navigate(nav.dataset.nav, 'fwd');
        return;
      }
      if (e.target.closest('[data-back]')) {
        back();
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
      if (d.preset !== undefined) {
        const key = d.presetKey;
        const f = fieldOf(key);
        const val = Number(d.preset) === Number(d.default) ? '' : String(d.preset);
        if (val === '' && f && !f.configured) return;
        if (await save(key, val)) {
          render();
          flag(key, 'saved', val === '' ? `Default (${d.default}m)` : 'Saved');
        }
        return;
      }
      if (d.edit) {
        editing = d.edit;
        render();
        root.querySelector(`[data-secret="${CSS.escape(d.edit)}"]`)?.focus();
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
          input?.focus();
          return;
        }
        t.disabled = true;
        if (await save(d.saveSecret, value)) {
          editing = null;
          const refreshes = triggersRefresh(d.saveSecret);
          toast(refreshes ? 'Saved · refreshing…' : 'Saved');
          await reloadSection();
          if (refreshes) schedulePostSaveRefresh();
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
      if ('proxyTest' in d || 'proxyDetect' in d) {
        if (t.disabled) return;
        const detecting = 'proxyDetect' in d;
        t.textContent = detecting ? 'Detecting…' : 'Testing…';
        await runProxyCheck();
        if (t.isConnected) t.textContent = detecting ? 'Detect local proxy' : 'Test request';
        return;
      }
      if (d.proxyUse) {
        if (await save('proxy_url', d.proxyUse)) {
          toast('Proxy set · refreshing…');
          render();
          schedulePostSaveRefresh();
        }
        return;
      }
      if (d.refresh) {
        t.disabled = true;
        t.textContent = 'Refreshing…';
        await refreshProviders(d.refresh.split(',').filter(Boolean));
        return;
      }
      if (d.oauthSlot) {
        startOAuth(d.oauthSlot);
        return;
      }
      if (d.createInstance) {
        createInstance(d.createInstance, t);
        return;
      }
      if ('confirmRemove' in d) {
        confirmRemove = true;
        render();
        return;
      }
      if ('cancelRemove' in d) {
        confirmRemove = false;
        render();
        return;
      }
      if ('doRemoveInstance' in d) {
        const inst = route && instanceOf(route.id);
        if (!inst) return;
        t.disabled = true;
        try {
          await api(`/api/instances/${encodeURIComponent(inst.base)}/${inst.slot}`, { method: 'DELETE' });
          toast('Account removed');
          if (opts.onSaved) opts.onSaved();
          await load();
          if (layout === 'stack') {
            navStack = [];
            route = null;
            pendingDir = 'back';
          } else {
            route = parseRoute(inst.base);
          }
          confirmRemove = false;
          render();
        } catch (err) {
          toast(err.message);
          t.disabled = false;
        }
        return;
      }
      if (d.doRemoveAccount) {
        t.disabled = true;
        try {
          await api(`/api/accounts/${d.doRemoveAccount}`, { method: 'DELETE' });
          toast('Account removed');
          if (opts.onSaved) opts.onSaved();
          await load();
          if (layout === 'stack') {
            navStack = [];
            route = null;
            pendingDir = 'back';
          } else {
            route = parseRoute('google');
          }
          confirmRemove = false;
          render();
        } catch (err) {
          toast(err.message);
          t.disabled = false;
        }
      }
    });

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
        save(t.dataset.switch, value).then((ok) => {
          if (!ok) t.checked = !t.checked;
          else {
            render();
            if (triggersRefresh(t.dataset.switch)) schedulePostSaveRefresh();
          }
        });
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
      if ((e.key === 'Enter' || e.key === ' ') && t.matches('[data-nav]') && t.tagName !== 'BUTTON') {
        e.preventDefault();
        t.click();
        return;
      }
      if (e.key === 'Enter' && (t.matches('[data-new-instance-name]') || t.matches('[data-new-instance-secret]'))) {
        e.preventDefault();
        root.querySelector('[data-create-instance]')?.click();
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

    async function startOAuth(slotMode) {
      let path = '/api/oauth/start';
      let slot = 1;
      if (slotMode === 'next') {
        const used = new Set(accountsList().map((a) => a.slot));
        slot = [1, 2, 3].find((s) => !used.has(s)) || 0;
        if (!slot) {
          toast('Three accounts is the limit');
          return;
        }
      } else {
        slot = Number(slotMode) || 1;
      }
      if (slot > 1) path += `?slot=${slot}`;

      try {
        const res = await api(path);
        const popup = await openConsent(res.url);
        if (popup === null) {
          toast('Finish sign-in in your browser');
        } else if (!popup) {
          location.href = res.url;
          return;
        } else {
          toast('Finish sign-in in the popup');
        }
        const hadSlot = accountsList().some((a) => a.slot === slot);
        let ticks = 0;
        const timer = setInterval(async () => {
          await reloadSection();
          if (opts.onSaved) opts.onSaved();
          const hasNow = accountsList().some((a) => a.slot === slot);
          if (!hadSlot && hasNow) {
            clearInterval(timer);
            toast('Google account connected');
            return;
          }
          if (++ticks >= 36 || (popup && popup.closed)) clearInterval(timer);
        }, 2500);
      } catch (err) {
        toast(`Sign-in failed: ${err.message}`);
      }
    }

    async function openConsent(url) {
      if (document.documentElement.dataset.shell === 'popover') {
        try {
          const runtime = await import('/wails/runtime.js');
          await runtime.Browser.OpenURL(url);
          return null;
        } catch {
          /* not the desktop shell; fall through to a popup */
        }
      }
      return window.open(url, 'gauge-oauth', 'width=520,height=760') || undefined;
    }

    return { open, back, reload: reloadSection, refreshCurrent, current: () => serializeRoute(route) };
  }

  window.GaugeSettings = { create };
})();
