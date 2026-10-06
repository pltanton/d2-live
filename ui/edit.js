import { createEditor } from './editor.js';
import { createOverlay } from './overlay.js';
import { renderInspector, classFill } from './inspector.js';
import { undo, redo, undoDepth, redoDepth } from './vendor/codemirror.js';

const AUTOSAVE_MS = 300;
const MODEL_MS = 120;
const PANEL_KEY = 'd2-live:panel-width';
const INSPECTOR_KEY = 'd2-live:inspector-collapsed';
const RENDER_TIMEOUT_MS = 10000;

let api = null;
let ui = null;
let ed = null;
let overlay = null;

const st = {
  disk: {text: '', hash: ''},
  model: null,
  modelText: null,
  sel: null,
  errors: {},
  saveTimer: null,
  saving: null,
  theirs: null,
  modelTimer: null,
  lastClass: {object: null, edge: null},
  gById: new Map(),
  idByG: new WeakMap(),
  connect: null,
  down: null,
  active: false,
  renderTimer: null,
  reveal: null,
};

export async function enable(d2live) {
  api = d2live;
  if (!ui) build();
  st.active = true;
  ui.panel.hidden = false;
  ui.toolbar.hidden = false;
  overlay.root.hidden = false;
  syncHistoryButtons();
  await loadSource();
}

export function disable() {
  st.active = false;
  flushSave();
  cancelConnect();
  select(null);
  ui.panel.hidden = true;
  ui.toolbar.hidden = true;
  overlay.root.hidden = true;
  setRendering(false);
}

function h(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v);
  }
  e.append(...children.filter((c) => c != null));
  return e;
}

function build() {
  document.head.append(h('link', {rel: 'stylesheet', href: '/ui/edit.css'}));
  const status = h('span', {class: 'd2l-status', 'data-state': 'saved'}, 'Saved');
  const banner = h('div', {class: 'd2l-banner', hidden: ''},
    h('span', {}, 'Changed on disk'),
    h('button', {type: 'button', onclick: takeTheirs}, 'Take theirs'),
    h('button', {type: 'button', onclick: keepMine}, 'Keep mine'));
  const code = h('div', {class: 'd2l-code'});
  const error = h('div', {class: 'd2l-error', hidden: ''});
  const inspector = h('div', {class: 'd2l-inspector'});
  const toggle = h('button', {type: 'button', class: 'd2l-ins-toggle', title: 'Collapse / expand'});
  const inspectorBox = h('section', {class: 'd2l-ins'},
    h('header', {class: 'd2l-ins-head', onclick: () => setInspectorCollapsed(!inspectorBox.classList.contains('collapsed'))},
      h('span', {}, 'Inspector'), toggle),
    inspector);
  const resizer = h('div', {class: 'd2l-resizer', title: 'Drag to resize'});
  const panel = h('aside', {id: 'd2l-panel'},
    resizer,
    h('header', {class: 'd2l-head'},
      h('span', {class: 'd2l-head-title'}, 'Code'),
      status,
      h('button', {type: 'button', class: 'd2l-close', title: 'Leave edit mode (E)', onclick: () => api.setEditing(false)}, '✕')),
    banner, code, error, inspectorBox);
  const undoBtn = h('button', {type: 'button', title: 'Undo (⌘Z)', class: 'd2l-icon', onclick: () => history(undo)}, '↶');
  const redoBtn = h('button', {type: 'button', title: 'Redo (⇧⌘Z)', class: 'd2l-icon', onclick: () => history(redo)}, '↷');
  const toolbar = h('div', {id: 'd2l-toolbar'},
    undoBtn, redoBtn, h('span', {class: 'd2l-sep'}),
    h('button', {type: 'button', title: 'New state (N)', onclick: createNode}, h('b', {}, '+'), ' State'),
    h('span', {class: 'd2l-sep'}),
    h('button', {type: 'button', title: 'Fit to screen (F)', class: 'd2l-icon', onclick: () => api.fitView()}, '⤢'));
  const hint = h('div', {id: 'd2l-hint', hidden: ''});
  const rendering = h('div', {id: 'd2l-rendering', hidden: ''}, h('span', {class: 'd2l-spinner'}), 'Rendering…');
  document.body.append(panel, toolbar, hint, rendering);
  ui = {panel, status, banner, code, error, inspector, inspectorBox, toggle, toolbar, undoBtn, redoBtn, hint, rendering};

  try {
    const w = parseInt(localStorage.getItem(PANEL_KEY), 10);
    if (w) document.documentElement.style.setProperty('--d2l-panel-w', w + 'px');
  } catch (err) { /* storage unavailable */ }
  resizer.addEventListener('pointerdown', startResize);
  let collapsed = false;
  try {
    collapsed = localStorage.getItem(INSPECTOR_KEY) === '1';
  } catch (err) { /* storage unavailable */ }
  setInspectorCollapsed(collapsed);

  ed = createEditor(code, {onDocChange, onCursor});
  overlay = createOverlay(api.viewer, api.panzoom);

  api.panzoom.on('transform', () => {
    closeClassMenu();
    overlay.schedule();
  });
  window.addEventListener('resize', () => overlay.measure());
  document.addEventListener('d2l:svg', onSvg);
  document.addEventListener('d2l:source', (e) => onSource(e.detail));
  document.addEventListener('d2l:file', () => st.active && loadSource());
  document.addEventListener('keydown', onKey);
  document.addEventListener('pointermove', onPointerMove);
  api.scene.addEventListener('pointerdown', (e) => (st.down = {x: e.clientX, y: e.clientY}));
  api.scene.addEventListener('click', onSceneClick);
  api.scene.addEventListener('dblclick', onSceneDblClick);
  api.scene.addEventListener('pointerover', (e) => {
    if (!st.active || e.buttons) return;
    const hit = hitTest(e.target);
    overlay.hover(hit && hit.g, hit && hit.kind);
  });
}

function setInspectorCollapsed(on) {
  ui.inspectorBox.classList.toggle('collapsed', on);
  ui.toggle.textContent = on ? '▸' : '▾';
  try {
    localStorage.setItem(INSPECTOR_KEY, on ? '1' : '0');
  } catch (err) { /* storage unavailable */ }
}

function startResize(e) {
  e.preventDefault();
  const move = (ev) => {
    const w = Math.min(Math.max(window.innerWidth - ev.clientX, 320), window.innerWidth - 240);
    document.documentElement.style.setProperty('--d2l-panel-w', w + 'px');
    overlay.measure();
  };
  const up = (ev) => {
    window.removeEventListener('pointermove', move);
    window.removeEventListener('pointerup', up);
    try {
      localStorage.setItem(PANEL_KEY, String(Math.round(window.innerWidth - ev.clientX)));
    } catch (err) { /* storage unavailable */ }
  };
  window.addEventListener('pointermove', move);
  window.addEventListener('pointerup', up);
}

// ---- file sync ----

async function loadSource() {
  const res = await fetch('/source?file=' + encodeURIComponent(api.state.file));
  if (!res.ok) {
    api.showToast('cannot read the file');
    return;
  }
  const {text, hash} = await res.json();
  st.disk = {text, hash};
  st.sel = null;
  st.model = null;
  ed.reset(text);
  setStatus('saved');
  hideBanner();
  await refreshModel();
}

function onDocChange(text, programmatic) {
  scheduleModel();
  syncHistoryButtons();
  if (text === st.disk.text) {
    setStatus('saved');
    return;
  }
  setStatus('dirty');
  clearTimeout(st.saveTimer);
  st.saveTimer = setTimeout(save, programmatic ? 0 : AUTOSAVE_MS);
}

function flushSave() {
  if (st.saveTimer) {
    clearTimeout(st.saveTimer);
    save();
  }
}

async function save() {
  st.saveTimer = null;
  if (st.saving) {
    await st.saving;
  }
  const text = ed.text();
  if (text === st.disk.text || st.theirs) return;
  setStatus('saving');
  setRendering(true);
  st.saving = (async () => {
    try {
      const res = await fetch('/source', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({file: api.state.file, baseHash: st.disk.hash, text}),
      });
      const body = await res.json().catch(() => ({}));
      if (res.status === 409) {
        setRendering(false);
        showBanner({text: body.text, hash: body.hash});
        return;
      }
      if (!res.ok) throw new Error(res.status);
      st.disk = {text, hash: body.hash};
      setStatus(ed.text() === text ? 'saved' : 'dirty');
    } catch (err) {
      setRendering(false);
      setStatus('error');
      api.showToast('save failed');
    }
  })();
  await st.saving;
  st.saving = null;
  if (ed.text() !== st.disk.text && !st.saveTimer && !st.theirs) {
    st.saveTimer = setTimeout(save, AUTOSAVE_MS);
  }
}

function onSource({text, hash}) {
  if (!st.active || hash === st.disk.hash) return;
  if (text === ed.text()) {
    st.disk = {text, hash};
    setStatus('saved');
    return;
  }
  if (ed.text() === st.disk.text && !st.saving && !st.saveTimer) {
    st.disk = {text, hash};
    ed.replace(text);
    setStatus('saved');
    setRendering(true);
    return;
  }
  showBanner({text, hash});
}

function showBanner(theirs) {
  st.theirs = theirs;
  ui.banner.hidden = false;
  setStatus('conflict');
}

function hideBanner() {
  st.theirs = null;
  ui.banner.hidden = true;
}

function takeTheirs() {
  const theirs = st.theirs;
  hideBanner();
  st.disk = theirs;
  ed.replace(theirs.text);
  setStatus('saved');
}

function keepMine() {
  st.disk = st.theirs;
  hideBanner();
  save();
}

const STATUS_TEXT = {saved: 'Saved', dirty: 'Editing…', saving: 'Saving…', error: 'Not saved', conflict: 'Conflict'};

function setStatus(s) {
  ui.status.dataset.state = s;
  ui.status.textContent = STATUS_TEXT[s];
}

// ---- model ----

function scheduleModel() {
  clearTimeout(st.modelTimer);
  st.modelTimer = setTimeout(refreshModel, MODEL_MS);
}

async function refreshModel() {
  clearTimeout(st.modelTimer);
  const text = ed.text();
  const res = await fetch('/model', {method: 'POST', body: text});
  const m = await res.json();
  if (text !== ed.text()) return;
  if (m.error) {
    ui.error.hidden = false;
    ui.error.textContent = (m.error.line ? `line ${m.error.line}: ` : '') + m.error.message;
    ed.mark({errorLine: m.error.line});
    return;
  }
  ui.error.hidden = true;
  st.model = m;
  st.modelText = text;
  indexSvg();
  if (st.sel && !find(st.sel)) st.sel = null;
  applySelection({scroll: false, inspector: !ui.inspector.contains(document.activeElement)});
}

function find(sel) {
  if (!sel || !st.model) return null;
  if (sel.kind === 'object') return st.model.objects.find((o) => o.id === sel.id);
  if (sel.kind === 'edge') return st.model.edges.find((e) => e.id === sel.id);
  if (sel.kind === 'class') return st.model.classes.find((c) => 'classes.' + c.name === sel.id);
  return null;
}

function kindOf(id) {
  if (id.startsWith('(')) return 'edge';
  if (id.startsWith('classes.')) return 'class';
  return 'object';
}

// ---- svg ↔ ids ----

function decodeId(token) {
  try {
    const bytes = Uint8Array.from(atob(token), (c) => c.charCodeAt(0));
    const s = new TextDecoder('utf-8', {fatal: true}).decode(bytes);
    const t = document.createElement('textarea');
    t.innerHTML = s;
    return t.value;
  } catch (err) {
    return null;
  }
}

function onSvg(e) {
  if (!st.active) return;
  if (!e.detail.hash || e.detail.hash === st.disk.hash) setRendering(false);
  indexSvg();
  applySelection({scroll: false, inspector: false});
  if (st.reveal) {
    revealInView(st.gById.get(st.reveal));
    st.reveal = null;
  }
}

function setRendering(on) {
  clearTimeout(st.renderTimer);
  ui.rendering.hidden = !on;
  document.body.classList.toggle('d2l-pending', on);
  if (on) st.renderTimer = setTimeout(() => setRendering(false), RENDER_TIMEOUT_MS);
}

function revealInView(g) {
  if (!g) return;
  const v = api.viewer.getBoundingClientRect();
  const r = g.getBoundingClientRect();
  const margin = 60;
  if (r.left >= v.left + margin && r.right <= v.right - margin && r.top >= v.top + margin && r.bottom <= v.bottom - margin) return;
  const t = api.panzoom.getTransform();
  const dx = (v.left + v.width / 2) - (r.left + r.width / 2);
  const dy = (v.top + v.height / 2) - (r.top + r.height / 2);
  api.panzoom.smoothMoveTo(t.x + dx, t.y + dy);
}

function syncHistoryButtons() {
  if (!ui || !ed) return;
  ui.undoBtn.disabled = undoDepth(ed.view.state) === 0;
  ui.redoBtn.disabled = redoDepth(ed.view.state) === 0;
}

function history(cmd) {
  cmd(ed.view);
  syncHistoryButtons();
}

function indexSvg() {
  st.gById = new Map();
  st.idByG = new WeakMap();
  if (!st.model) return;
  const known = new Set([...st.model.objects.map((o) => o.id), ...st.model.edges.map((e) => e.id)]);
  for (const g of api.scene.querySelectorAll('g[class]')) {
    const token = g.getAttribute('class').split(' ')[0];
    if (!/^[A-Za-z0-9+/]+=*$/.test(token)) continue;
    const id = decodeId(token);
    if (!id || !known.has(id)) continue;
    st.gById.set(id, g);
    st.idByG.set(g, id);
    if (id.startsWith('(') && !g.querySelector('[data-d2l]')) {
      const path = g.querySelector('path.connection');
      if (path) {
        const hit = path.cloneNode(false);
        for (const a of ['class', 'marker-end', 'marker-start', 'mask', 'style']) hit.removeAttribute(a);
        hit.setAttribute('data-d2l', '');
        hit.setAttribute('class', 'd2l-hit');
        g.appendChild(hit);
      }
    }
  }
}

function hitTest(target) {
  for (let n = target; n && n !== api.scene; n = n.parentNode) {
    const id = st.idByG.get(n);
    if (id) return {id, g: n, kind: kindOf(id)};
  }
  return null;
}

// ---- selection ----

function select(sel, {scroll = true} = {}) {
  closeClassMenu();
  st.sel = sel;
  st.errors = {};
  applySelection({scroll, inspector: true});
}

function applySelection({scroll, inspector}) {
  if (!ui) return;
  const item = find(st.sel);
  const fresh = st.modelText === ed.text();
  ed.mark(item && fresh ? {decl: item.decl, refs: item.refs || [], scroll} : {});
  if (inspector) {
    renderInspector(ui.inspector, {
      sel: st.sel, model: st.model, errors: st.errors, find,
      op: runOp, select, remove: removeSelected,
    });
  }
  highlightEdge(st.sel && st.sel.kind === 'edge' ? st.gById.get(st.sel.id) : null);
  const g = st.sel && st.gById.get(st.sel.id);
  overlay.select(g || null, st.sel && st.sel.kind, g ? miniButtons(st.sel, item) : null);
}

let edgeStyle = null;
function highlightEdge(g) {
  if (!edgeStyle) {
    edgeStyle = h('style', {});
    document.head.append(edgeStyle);
  }
  if (!g) {
    edgeStyle.textContent = '';
    return;
  }
  const token = CSS.escape(g.getAttribute('class').split(' ')[0]);
  edgeStyle.textContent = `#scene g.${token} > path.connection { stroke: #7c5cc4 !important; stroke-width: 3.5px !important; }
#scene g.${token} > text { fill: #5b3fa3 !important; font-weight: 700; }`;
}

function miniButtons(sel, item) {
  if (!item || !st.model) return null;
  const btn = (label, title, onclick, cls = '') => h('button', {type: 'button', title, class: cls, onclick}, label);
  const classPick = () => {
    const current = item.props.class || '';
    const pill = h('button', {type: 'button', title: 'Class', class: 'd2l-mini-class', style: `--sw: ${classFill(st.model, current) || 'transparent'}`},
      h('span', {class: 'd2l-swatch'}), current || 'no class', h('span', {class: 'd2l-caret'}, '▾'));
    pill.addEventListener('click', (e) => {
      e.stopPropagation();
      openClassMenu(pill, current, (value) => runOp({kind: 'set', id: sel.id, key: 'class', value}, 'class'));
    });
    return pill;
  };
  const focusField = () => {
    const f = ui.inspector.querySelector('[data-focus="id"]');
    if (f) {
      f.focus();
      f.select && f.select();
    }
  };
  if (sel.kind === 'object') {
    return [
      btn('✎', 'Rename (Enter)', focusField),
      classPick(),
      btn('→', 'Connect to another state (C)', () => toggleConnect()),
      btn('⌫', 'Delete', removeSelected, 'd2l-danger-btn'),
    ];
  }
  if (sel.kind === 'edge') {
    return [
      btn('✎', 'Edit label (Enter)', focusField),
      classPick(),
      btn('⇄', 'Reverse', () => runOp({kind: 'reverse', id: sel.id})),
      btn('⌫', 'Delete', removeSelected, 'd2l-danger-btn'),
    ];
  }
  return null;
}

let classMenu = null;

function closeClassMenu() {
  if (classMenu) {
    classMenu.remove();
    classMenu = null;
  }
}

function openClassMenu(anchor, current, pick) {
  if (classMenu) {
    closeClassMenu();
    return;
  }
  const choose = (value) => () => {
    closeClassMenu();
    if (value !== current) pick(value || null);
  };
  const r = anchor.getBoundingClientRect();
  classMenu = h('div', {class: 'd2l-menu', style: `left: ${Math.round(r.left)}px; top: ${Math.round(r.bottom + 6)}px`},
    ...st.model.classes.map((c) => h('button', {type: 'button', class: c.name === current ? 'd2l-current' : '', onclick: choose(c.name)},
      h('span', {class: 'd2l-swatch', style: `--sw: ${classFill(st.model, c.name) || 'transparent'}`}), c.name)),
    h('button', {type: 'button', class: current ? '' : 'd2l-current', onclick: choose('')}, h('span', {class: 'd2l-swatch'}), 'no class'));
  document.body.append(classMenu);
  setTimeout(() => document.addEventListener('pointerdown', (e) => {
    if (classMenu && !classMenu.contains(e.target)) closeClassMenu();
  }, {once: true}), 0);
}

function onCursor(pos) {
  if (!st.model || st.modelText !== ed.text()) return;
  const inside = (r) => r && r.from <= pos && pos <= r.to;
  let hit = null;
  for (const e of st.model.edges) if (inside(e.decl)) hit = {kind: 'edge', id: e.id};
  for (const o of st.model.objects) if (inside(o.decl)) hit = {kind: 'object', id: o.id};
  for (const c of st.model.classes) if (inside(c.decl)) hit = {kind: 'class', id: 'classes.' + c.name};
  if (!hit && st.sel) {
    const item = find(st.sel);
    if (item && item.refs && item.refs.some(inside)) return;
  }
  if (hit && st.sel && hit.kind === st.sel.kind && hit.id === st.sel.id) return;
  if (hit || st.sel) select(hit, {scroll: false});
}

// ---- ops ----

async function runOp(op, field) {
  let res;
  try {
    res = await fetch('/edit', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({text: ed.text(), op}),
    });
  } catch (err) {
    api.showToast('edit failed');
    return false;
  }
  const body = await res.json().catch(() => ({message: 'edit failed'}));
  if (!res.ok) {
    st.errors = {[body.field || field || '_']: body.message};
    applySelection({scroll: false, inspector: true});
    if (!body.field && !field) api.showToast(body.message);
    return false;
  }
  if (op.kind === 'set' && op.key === 'class' && op.value && st.sel) st.lastClass[st.sel.kind] = op.value;
  ed.replace(body.text);
  await refreshModel();
  const target = body.select || (op.kind !== 'delete' && st.sel && st.sel.id);
  if (target) st.reveal = target;
  if (!op.kind.startsWith('create') && ui.inspector.contains(document.activeElement)) {
    document.activeElement.blur();
    api.viewer.focus({preventScroll: true});
  }
  if (body.select) {
    select({kind: kindOf(body.select), id: body.select}, {scroll: true});
  } else if (op.kind === 'delete') {
    select(null);
  } else {
    select(st.sel, {scroll: false});
  }
  return true;
}

function defaultClass(kind) {
  if (st.lastClass[kind]) return st.lastClass[kind];
  const items = kind === 'object'
    ? st.model.objects.filter((o) => !['title', 'notes'].includes(o.id))
    : st.model.edges;
  const counts = new Map();
  for (const it of items) {
    if (it.props.class) counts.set(it.props.class, (counts.get(it.props.class) || 0) + 1);
  }
  let best = '', n = 0;
  for (const [c, k] of counts) if (k > n) [best, n] = [c, k];
  return best;
}

async function createNode() {
  if (!st.model) return;
  if (await runOp({kind: 'createNode', class: defaultClass('object')})) focusId();
}

async function createFrom(src) {
  const ok = await runOp({kind: 'createNodeWithEdge', src, class: defaultClass('object'), edgeClass: defaultClass('edge')});
  if (ok) focusId();
}

async function connect(src, dst) {
  if (await runOp({kind: 'createEdge', src, dst, class: defaultClass('edge')})) focusId();
}

function focusId() {
  setInspectorCollapsed(false);
  requestAnimationFrame(() => {
    const f = ui.inspector.querySelector('[data-focus="id"]');
    if (f) {
      f.focus();
      if (f.select) f.select();
    }
  });
}

function removeSelected() {
  if (!st.sel || st.sel.kind === 'class') return;
  runOp({kind: 'delete', id: st.sel.id});
}

// ---- connecting ----

function toggleConnect() {
  if (st.connect) cancelConnect();
  else startConnect();
}

function startConnect() {
  if (!st.sel || st.sel.kind !== 'object') {
    api.showToast('select a state first');
    return;
  }
  st.connect = {src: st.sel.id};
  api.viewer.classList.add('d2l-connecting');
  ui.hint.hidden = false;
  ui.hint.textContent = `From ${st.sel.id}: click a state to connect · empty canvas for a new state · Esc to cancel`;
}

function cancelConnect() {
  st.connect = null;
  if (api) api.viewer.classList.remove('d2l-connecting');
  if (overlay) overlay.bandFrom(null);
  if (ui) ui.hint.hidden = true;
}

function onPointerMove(e) {
  if (!st.connect) return;
  overlay.bandFrom(st.gById.get(st.connect.src), e.clientX, e.clientY);
}

function onSceneClick(e) {
  if (!st.active) return;
  if (st.down && Math.hypot(e.clientX - st.down.x, e.clientY - st.down.y) > 4) return;
  const hit = hitTest(e.target);
  if (st.connect) {
    const src = st.connect.src;
    cancelConnect();
    if (hit && hit.kind === 'object') connect(src, hit.id);
    else createFrom(src);
    return;
  }
  select(hit ? {kind: hit.kind, id: hit.id} : null);
}

function onSceneDblClick(e) {
  if (!st.active) return;
  const hit = hitTest(e.target);
  if (!hit) return;
  select({kind: hit.kind, id: hit.id});
  focusId();
}

function onKey(e) {
  if (!st.active) return;
  if (e.key === 'Escape') {
    if (st.connect) cancelConnect();
    else if (!api.typingTarget(e.target)) select(null);
    return;
  }
  if (api.typingTarget(e.target)) return;
  const mod = e.metaKey || e.ctrlKey;
  if (mod && e.key.toLowerCase() === 'z') {
    e.preventDefault();
    history(e.shiftKey ? redo : undo);
    return;
  }
  if (mod || e.altKey) return;
  switch (e.key) {
    case 'n':
    case 'N':
      e.preventDefault();
      createNode();
      break;
    case 'c':
    case 'C':
      e.preventDefault();
      toggleConnect();
      break;
    case 'Delete':
    case 'Backspace':
      e.preventDefault();
      removeSelected();
      break;
    case 'Enter':
      e.preventDefault();
      focusId();
      break;
  }
}
