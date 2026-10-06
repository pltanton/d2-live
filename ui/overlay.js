const SIDES = ['top', 'right', 'bottom', 'left'];

function el(tag, cls, attrs = {}) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  return e;
}

export function createOverlay(viewer, h) {
  const root = el('div', '', {id: 'd2l-overlay'});
  const hoverBox = el('div', 'd2l-box d2l-hover');
  const selBox = el('div', 'd2l-box d2l-sel');
  const mini = el('div', 'd2l-mini');
  const handles = SIDES.map((side) => {
    const b = el('button', 'd2l-handle', {type: 'button', 'data-side': side, title: 'Click: new state · drag: connect'});
    b.textContent = '+';
    b.addEventListener('pointerdown', (e) => h.onHandleDown(e));
    return b;
  });
  const band = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  band.setAttribute('class', 'd2l-band');
  const line = document.createElementNS('http://www.w3.org/2000/svg', 'line');
  band.appendChild(line);
  root.append(hoverBox, selBox, band, mini, ...handles);
  viewer.appendChild(root);

  let current = null;

  function local(rect) {
    const v = viewer.getBoundingClientRect();
    return {x: rect.left - v.left, y: rect.top - v.top, w: rect.width, h: rect.height};
  }

  function place(box, r, pad) {
    box.style.display = 'block';
    box.style.transform = `translate(${r.x - pad}px, ${r.y - pad}px)`;
    box.style.width = r.w + pad * 2 + 'px';
    box.style.height = r.h + pad * 2 + 'px';
  }

  function hover(g, kind) {
    if (!g || kind !== 'object' || (current && current.g === g)) {
      hoverBox.style.display = 'none';
      return;
    }
    place(hoverBox, local(g.getBoundingClientRect()), 5);
  }

  function anchorRect(g, kind) {
    if (kind === 'object') return local(g.getBoundingClientRect());
    const label = g.querySelector('text');
    if (label && label.textContent.trim()) return local(label.getBoundingClientRect());
    const path = g.querySelector('path.connection');
    const p = path.getPointAtLength(path.getTotalLength() / 2);
    const pt = new DOMPoint(p.x, p.y).matrixTransform(path.getScreenCTM());
    return local({left: pt.x, top: pt.y, width: 0, height: 0});
  }

  function select(g, kind, buttons) {
    current = g ? {g, kind, buttons} : null;
    mini.replaceChildren(...(buttons || []));
    update();
  }

  function update() {
    hoverBox.style.display = 'none';
    if (!current || !current.g.isConnected) {
      selBox.style.display = 'none';
      mini.style.display = 'none';
      handles.forEach((b) => (b.style.display = 'none'));
      return;
    }
    const r = anchorRect(current.g, current.kind);
    if (current.kind === 'object') {
      place(selBox, r, 6);
    } else {
      selBox.style.display = 'none';
    }
    mini.style.display = 'flex';
    const mw = mini.offsetWidth;
    const top = r.y - (current.kind === 'object' ? 40 : 0) - mini.offsetHeight - 10;
    mini.style.transform = `translate(${Math.round(r.x + r.w / 2 - mw / 2)}px, ${Math.round(Math.max(8, top))}px)`;
    const showHandles = current.kind === 'object';
    const pos = {
      top: [r.x + r.w / 2, r.y - 22],
      right: [r.x + r.w + 22, r.y + r.h / 2],
      bottom: [r.x + r.w / 2, r.y + r.h + 22],
      left: [r.x - 22, r.y + r.h / 2],
    };
    handles.forEach((b) => {
      b.style.display = showHandles ? 'grid' : 'none';
      const [x, y] = pos[b.dataset.side];
      b.style.translate = `${Math.round(x - 11)}px ${Math.round(y - 11)}px`;
    });
  }

  function bandFrom(g, x, y) {
    if (!g) {
      band.style.display = 'none';
      return;
    }
    const r = local(g.getBoundingClientRect());
    const v = viewer.getBoundingClientRect();
    band.style.display = 'block';
    line.setAttribute('x1', r.x + r.w / 2);
    line.setAttribute('y1', r.y + r.h / 2);
    line.setAttribute('x2', x - v.left);
    line.setAttribute('y2', y - v.top);
  }

  return {root, hover, select, update, bandFrom, current: () => current};
}
