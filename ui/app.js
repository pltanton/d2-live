const state = Object.assign({}, window.D2LIVE_BOOT);
const hud = document.getElementById('hud');
const viewer = document.getElementById('viewer');
const scene = document.getElementById('scene');
const fnameEl = document.getElementById('fname');
const toast = document.getElementById('toast');
const pngLoader = document.getElementById('png-loader');
const titleEl = document.getElementById('title');
const fileSelect = document.getElementById('file-select');
const layoutSelect = document.getElementById('layout-select');
const sketchToggle = document.getElementById('sketch-toggle');
const copySvgButton = document.getElementById('copy-svg');
const copyPngButton = document.getElementById('copy-png');
const downloadSvgButton = document.getElementById('download-svg');
const downloadPngButton = document.getElementById('download-png');

function stateKey() {
  return 'd2-live:' + state.file + ':' + state.sketch + ':' + state.layout;
}

function query(extra) {
  let s = 'file=' + encodeURIComponent(state.file) +
    '&sketch=' + state.sketch +
    '&layout=' + encodeURIComponent(state.layout);
  return extra ? s + '&' + extra : s;
}

function baseName() {
  const parts = state.file.split('/');
  return parts[parts.length - 1] || state.file;
}

const instance = panzoom(scene, {
  bounds: false,
  smoothScroll: false,
  transformOrigin: {x: 0.5, y: 0.5},
});

function loadState() {
  try {
    const raw = localStorage.getItem(stateKey());
    if (!raw) return null;
    return JSON.parse(raw);
  } catch (err) {
    return null;
  }
}

function saveState() {
  try {
    const transform = instance.getTransform();
    localStorage.setItem(stateKey(), JSON.stringify({
      x: transform.x,
      y: transform.y,
      scale: transform.scale,
    }));
  } catch (err) {
    // ignore storage failures
  }
}

function applyTransform(t) {
  const target = t || {x: 0, y: 0, scale: 1};
  instance.zoomAbs(0, 0, target.scale);
  instance.moveTo(target.x, target.y);
}

function setConnected(status) {
  hud.classList.remove('connecting', 'connected', 'disconnected');
  hud.classList.add(status);
}

let toastTimer = null;
// Client-side PNGs take milliseconds; the loader only shows for the slow server
// fallback, and then long enough not to flicker.
let pngLoaderTimer = null;
let pngLoaderShownAt = 0;
function setPngLoading(loading) {
  clearTimeout(pngLoaderTimer);
  if (loading) {
    pngLoaderTimer = setTimeout(() => {
      pngLoaderShownAt = Date.now();
      pngLoader.classList.add('visible');
    }, 250);
    return;
  }
  const left = pngLoaderShownAt ? 400 - (Date.now() - pngLoaderShownAt) : 0;
  pngLoaderTimer = setTimeout(() => {
    pngLoaderShownAt = 0;
    pngLoader.classList.remove('visible');
  }, Math.max(0, left));
}

function showToast(message) {
  toast.textContent = message;
  toast.classList.add('visible');
  if (toastTimer) {
    clearTimeout(toastTimer);
  }
  toastTimer = setTimeout(() => {
    toast.classList.remove('visible');
  }, 1200);
}

instance.on('panstart', () => {
  viewer.classList.add('dragging');
});
instance.on('panend', () => {
  viewer.classList.remove('dragging');
  saveState();
});
instance.on('zoomend', saveState);

const GRID = 24;
const grid = document.createElement('div');
grid.id = 'grid';
viewer.prepend(grid);
let gridScale = 0;
let frame = 0;
let settle = null;

function syncGrid() {
  frame = 0;
  const t = instance.getTransform();
  const size = GRID * t.scale;
  if (t.scale !== gridScale) {
    gridScale = t.scale;
    grid.style.backgroundSize = size + 'px ' + size + 'px';
  }
  const dx = ((t.x % size) + size) % size - size;
  const dy = ((t.y % size) + size) % size - size;
  grid.style.transform = 'translate3d(' + dx + 'px,' + dy + 'px,0)';
}

instance.on('transform', () => {
  if (!frame) frame = requestAnimationFrame(syncGrid);
  viewer.classList.add('moving');
  clearTimeout(settle);
  settle = setTimeout(() => viewer.classList.remove('moving'), 180);
});
syncGrid();

let hudTimer = null;
function openHud() {
  if (hudTimer) {
    clearTimeout(hudTimer);
    hudTimer = null;
  }
  hud.classList.add('open');
}

function closeHudSoon() {
  if (!hud.classList.contains('open')) {
    return;
  }
  if (hudTimer) {
    clearTimeout(hudTimer);
  }
  hudTimer = setTimeout(() => {
    hud.classList.remove('open');
  }, 120);
}

hud.addEventListener('mouseenter', openHud);
hud.addEventListener('mouseleave', closeHudSoon);
hud.addEventListener('focusin', openHud);
hud.addEventListener('focusout', closeHudSoon);

function currentSvgText() {
  const svg = scene.querySelector('svg');
  if (!svg) {
    return '';
  }
  const copy = svg.cloneNode(true);
  copy.querySelectorAll('[data-d2l]').forEach((el) => el.remove());
  return copy.outerHTML;
}

function currentSvgAsset() {
  const svgText = currentSvgText();
  if (!svgText) {
    return null;
  }
  const name = baseName();
  const safeName = name.endsWith('.svg') ? name : name + '.svg';
  return {
    file: new File([svgText], safeName, {type: 'image/svg+xml'}),
    text: svgText,
  };
}

async function copySvg() {
  const svgAsset = currentSvgAsset();
  if (!svgAsset) {
    showToast('nothing to copy');
    return;
  }

  try {
    if (window.ClipboardItem && ClipboardItem.supports && ClipboardItem.supports('image/svg+xml')) {
      await navigator.clipboard.write([new ClipboardItem({
        'image/svg+xml': svgAsset.file,
        'text/plain': new Blob([svgAsset.text], {type: 'text/plain'}),
      })]);
    } else {
      await navigator.clipboard.writeText(svgAsset.text);
    }
    showToast('copied SVG file');
  } catch (err) {
    showToast('copy SVG failed');
  }
}

function downloadBlob(blob, name) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = name;
  anchor.rel = 'noopener';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function currentPngName() {
  return baseName().replace(/\.[^.]+$/, '') + '.png';
}

// Rasterises the SVG already on screen; browsers that taint a canvas drawn from
// an SVG with foreignObject (markdown labels) fall back to the server's d2 export.
async function svgToPng(svgText, scale) {
  const box = new DOMParser().parseFromString(svgText, 'image/svg+xml').documentElement.viewBox.baseVal;
  const url = URL.createObjectURL(new Blob([svgText], {type: 'image/svg+xml'}));
  try {
    const img = new Image();
    img.src = url;
    await img.decode();
    const canvas = document.createElement('canvas');
    canvas.width = Math.round(box.width * scale);
    canvas.height = Math.round(box.height * scale);
    canvas.getContext('2d').drawImage(img, 0, 0, canvas.width, canvas.height);
    return await new Promise((resolve, reject) => canvas.toBlob((b) => (b ? resolve(b) : reject(new Error('empty'))), 'image/png'));
  } finally {
    URL.revokeObjectURL(url);
  }
}

async function fetchPngBlob() {
  const svgText = currentSvgText();
  if (svgText) {
    try {
      return await svgToPng(svgText, 2);
    } catch (err) {
      // fall through to the server
    }
  }
  const response = await fetch('/png?' + query('ts=' + Date.now()));
  if (!response.ok) {
    throw new Error('png fetch failed');
  }
  return await response.blob();
}

async function downloadSvg() {
  const svgAsset = currentSvgAsset();
  if (!svgAsset) {
    showToast('nothing to download');
    return;
  }
  downloadBlob(svgAsset.file, svgAsset.file.name);
  showToast('downloaded SVG');
}

async function copyPng() {
  setPngLoading(true);
  try {
    const png = await fetchPngBlob();
    await navigator.clipboard.write([new ClipboardItem({'image/png': png})]);
    showToast('copied PNG');
  } catch (err) {
    showToast('copy PNG failed');
  } finally {
    setPngLoading(false);
  }
}

async function downloadPng() {
  setPngLoading(true);
  try {
    const png = await fetchPngBlob();
    downloadBlob(png, currentPngName());
    showToast('downloaded PNG');
  } catch (err) {
    showToast('download PNG failed');
  } finally {
    setPngLoading(false);
  }
}

copySvgButton.addEventListener('click', copySvg);
copyPngButton.addEventListener('click', copyPng);
downloadSvgButton.addEventListener('click', downloadSvg);
downloadPngButton.addEventListener('click', downloadPng);

let snapshotURL = null;

// While the view moves, a decoded <img> of the same SVG stands in for the live
// DOM: browsers move a raster cheaply, while the inline SVG (masks on every edge
// label, embedded fonts) is repainted per frame — the drag lag seen in Firefox.
function prepareSnapshot(svg) {
  const backdropOff = '<style>.d2-svg > rect:first-child{display:none}</style>';
  const markup = svg.replace(/(<svg[^>]*>)/, '$1' + backdropOff);
  const url = URL.createObjectURL(new Blob([markup], {type: 'image/svg+xml'}));
  const img = new Image();
  img.id = 'snapshot';
  img.alt = '';
  img.src = url;
  img.decode().then(() => {
    if (!img.isConnected) return;
    img.classList.add('ready');
  }).catch(() => {});
  scene.prepend(img);
  if (snapshotURL) URL.revokeObjectURL(snapshotURL);
  snapshotURL = url;
}

let svgRequested = 0;
let svgShown = 0;

async function reloadSvg() {
  const seq = ++svgRequested;
  const preview = state.edit && window.d2live && window.d2live.previewText != null;
  try {
    const response = preview
      ? await fetch('/render?' + query(), {method: 'POST', body: window.d2live.previewText})
      : await fetch('/svg?' + query('ts=' + Date.now() + (state.edit ? '&edit=1' : '')));
    const detail = {
      error: response.headers.get('X-D2-Error'),
      hash: response.headers.get('X-D2-Hash'),
      preview,
      latest: seq === svgRequested,
    };
    if (!response.ok) {
      document.dispatchEvent(new CustomEvent('d2l:svg', {detail}));
      return;
    }
    const svg = await response.text();
    if (seq < svgShown) {
      return;
    }
    svgShown = seq;
    detail.latest = seq === svgRequested;
    scene.innerHTML = svg;
    prepareSnapshot(svg);
    document.dispatchEvent(new CustomEvent('d2l:svg', {detail}));
  } catch (err) {
    showToast('update failed');
  }
}

function refreshTitle() {
  const base = baseName();
  document.title = 'd2-live — ' + base.replace(/\.[^.]+$/, '');
  titleEl.textContent = base;
  fnameEl.textContent = base;
}

function updateURL() {
  const u = new URL(location.href);
  u.searchParams.set('file', state.file);
  u.searchParams.set('sketch', state.sketch);
  u.searchParams.set('layout', state.layout);
  if (state.edit) {
    u.searchParams.set('edit', '1');
  } else {
    u.searchParams.delete('edit');
  }
  history.replaceState(null, '', u);
}

let events = null;
function connect() {
  if (events) {
    events.close();
  }
  events = new EventSource('/events?file=' + encodeURIComponent(state.file));
  events.onmessage = (event) => {
    if (event.data === 'reload') {
      reloadSvg();
    }
  };
  events.addEventListener('source', (event) => {
    document.dispatchEvent(new CustomEvent('d2l:source', {detail: JSON.parse(event.data)}));
  });
  events.onopen = () => {
    setConnected('connected');
  };
  events.onerror = () => {
    setConnected('disconnected');
    events.close();
    setTimeout(connect, 1000);
  };
}

function fitView() {
  const svg = scene.querySelector('svg');
  if (!svg) {
    return;
  }
  const t = instance.getTransform();
  const v = viewer.getBoundingClientRect();
  const r = svg.getBoundingClientRect();
  const w = r.width / t.scale;
  const h = r.height / t.scale;
  if (!w || !h) {
    return;
  }
  const ox = (r.left - v.left - t.x) / t.scale;
  const oy = (r.top - v.top - t.y) / t.scale;
  const top = state.edit ? 64 : 16;
  const margin = 16;
  const s = Math.min((v.width - 2 * margin) / w, (v.height - top - margin) / h);
  instance.zoomAbs(0, 0, s);
  instance.moveTo((v.width - w * s) / 2 - ox * s, top + (v.height - top - margin - h * s) / 2 - oy * s);
  saveState();
}

function applyState(next, fileChanged) {
  saveState();
  Object.assign(state, next);
  updateURL();
  refreshTitle();
  const target = fileChanged ? null : loadState();
  reloadSvg().then(() => (target ? applyTransform(target) : fitView()));
  if (fileChanged) {
    connect();
    document.dispatchEvent(new CustomEvent('d2l:file'));
  }
}

async function refreshFiles() {
  try {
    const response = await fetch('/files');
    const files = await response.json();
    const current = state.file;
    fileSelect.innerHTML = '';
    files.forEach((f) => {
      const option = document.createElement('option');
      option.value = f.abs;
      option.textContent = f.basename;
      if (f.abs === current) {
        option.selected = true;
      }
      fileSelect.appendChild(option);
    });
  } catch (err) {
    // ignore; keep existing options
  }
}

fileSelect.addEventListener('mousedown', refreshFiles);
fileSelect.addEventListener('change', async (event) => {
  const next = event.target.value;
  if (state.edit && editModule) {
    event.target.value = state.file;
    await editModule.confirmLeave();
    event.target.value = next;
  }
  applyState({file: next}, true);
});
layoutSelect.addEventListener('change', (event) => {
  applyState({layout: event.target.value}, false);
});
sketchToggle.addEventListener('change', (event) => {
  applyState({sketch: event.target.checked}, false);
});

const editToggle = document.getElementById('edit-toggle');
let editModule = null;

async function setEditing(on) {
  state.edit = on;
  updateURL();
  document.body.classList.toggle('editing', on);
  if (on) {
    try {
      editModule = editModule || await import('/ui/edit.js');
      await editModule.enable(window.d2live);
    } catch (err) {
      console.error(err);
      showToast('edit mode failed to load');
      document.body.classList.remove('editing');
      state.edit = false;
      updateURL();
      return;
    }
  } else if (editModule) {
    await editModule.confirmLeave();
    editModule.disable();
  }
  await reloadSvg();
  fitView();
}

function typingTarget(el) {
  return el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.closest('.cm-editor'));
}

editToggle.addEventListener('click', () => setEditing(!state.edit));
document.addEventListener('keydown', (event) => {
  if (event.metaKey || event.ctrlKey || event.altKey || typingTarget(event.target)) {
    return;
  }
  if (event.key === 'e') {
    event.preventDefault();
    setEditing(!state.edit);
  } else if (event.key === 'f') {
    event.preventDefault();
    fitView();
  }
});

window.d2live = {
  state, viewer, scene, panzoom: instance, showToast, reloadSvg, setEditing, typingTarget, fitView,
};

prepareSnapshot(scene.innerHTML);
setConnected('connecting');
saveState();
connect();
if (new URLSearchParams(location.search).get('edit') === '1') {
  setEditing(true);
} else {
  requestAnimationFrame(fitView);
}
