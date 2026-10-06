function el(tag, cls, attrs = {}) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  return e;
}

export function createOverlay(viewer, panzoom, h) {
  const root = el('div', '', {id: 'd2l-overlay'});
  const hoverBox = el('div', 'd2l-box d2l-hover');
  const selBox = el('div', 'd2l-box d2l-sel');
  const mini = el('div', 'd2l-mini');
  const handle = el('button', 'd2l-handle', {type: 'button', title: 'Click: new state after this one · drag onto a state: connect'});
  handle.textContent = '+';
  handle.addEventListener('pointerdown', (e) => h.onHandleDown(e));
  const band = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  band.setAttribute('class', 'd2l-band');
  const line = document.createElementNS('http://www.w3.org/2000/svg', 'line');
  band.appendChild(line);
  root.append(hoverBox, selBox, band, mini, handle);
  viewer.appendChild(root);
  // panzoom listens on the viewer; presses on our controls must not start a pan.
  for (const type of ['mousedown', 'touchstart', 'dblclick']) {
    root.addEventListener(type, (e) => e.stopPropagation());
  }

  let current = null;
  let frame = 0;

  function toScene(rect) {
    const v = viewer.getBoundingClientRect();
    const t = panzoom.getTransform();
    return {
      x: (rect.left - v.left - t.x) / t.scale,
      y: (rect.top - v.top - t.y) / t.scale,
      w: rect.width / t.scale,
      h: rect.height / t.scale,
    };
  }

  function toScreen(s) {
    const t = panzoom.getTransform();
    return {x: t.x + s.x * t.scale, y: t.y + s.y * t.scale, w: s.w * t.scale, h: s.h * t.scale};
  }

  function anchorRect(g, kind) {
    if (kind === 'object') return g.getBoundingClientRect();
    const label = g.querySelector('text');
    if (label && label.textContent.trim()) return label.getBoundingClientRect();
    const path = g.querySelector('path.connection');
    const p = path.getPointAtLength(path.getTotalLength() / 2);
    const pt = new DOMPoint(p.x, p.y).matrixTransform(path.getScreenCTM());
    return {left: pt.x, top: pt.y, width: 0, height: 0};
  }

  function place(box, r, pad) {
    if (box.style.display !== 'block') box.style.display = 'block';
    box.style.translate = `${r.x - pad}px ${r.y - pad}px`;
    const w = Math.round(r.w + pad * 2) + 'px';
    const hgt = Math.round(r.h + pad * 2) + 'px';
    if (box.style.width !== w) box.style.width = w;
    if (box.style.height !== hgt) box.style.height = hgt;
  }

  function hover(g, kind) {
    if (!g || kind !== 'object' || (current && current.g === g) || viewer.classList.contains('moving')) {
      hoverBox.style.display = 'none';
      return;
    }
    place(hoverBox, toScreen(toScene(g.getBoundingClientRect())), 5);
  }

  function select(g, kind, buttons) {
    current = g ? {g, kind} : null;
    mini.replaceChildren(...(buttons || []));
    measure();
  }

  function measure() {
    hoverBox.style.display = 'none';
    if (!current || !current.g.isConnected) {
      selBox.style.display = 'none';
      mini.style.display = 'none';
      handle.style.display = 'none';
      return;
    }
    mini.style.display = 'flex';
    current.geo = toScene(anchorRect(current.g, current.kind));
    current.miniW = mini.offsetWidth;
    current.miniH = mini.offsetHeight;
    draw();
  }

  // Per-frame path: style writes only. Reading layout here cost 89 forced layouts
  // per pan gesture (CDP Performance metrics, withdraw-fsm).
  function draw() {
    frame = 0;
    if (!current || !current.geo) return;
    const r = toScreen(current.geo);
    const isObject = current.kind === 'object';
    if (isObject) place(selBox, r, 6);
    else selBox.style.display = 'none';
    const top = r.y - (isObject ? 8 : 0) - current.miniH - 10;
    mini.style.translate = `${Math.round(r.x + r.w / 2 - current.miniW / 2)}px ${Math.round(Math.max(8, top))}px`;
    if (isObject) {
      if (handle.style.display !== 'grid') handle.style.display = 'grid';
      handle.style.translate = `${Math.round(r.x + r.w - 4)}px ${Math.round(r.y + r.h - 4)}px`;
    } else {
      handle.style.display = 'none';
    }
  }

  function schedule() {
    hoverBox.style.display = 'none';
    if (!frame) frame = requestAnimationFrame(draw);
  }

  function bandFrom(g, x, y) {
    if (!g) {
      band.style.display = 'none';
      return;
    }
    const v = viewer.getBoundingClientRect();
    const r = g.getBoundingClientRect();
    band.style.display = 'block';
    line.setAttribute('x1', r.left - v.left + r.width / 2);
    line.setAttribute('y1', r.top - v.top + r.height / 2);
    line.setAttribute('x2', x - v.left);
    line.setAttribute('y2', y - v.top);
  }

  return {root, hover, select, measure, schedule, bandFrom};
}
