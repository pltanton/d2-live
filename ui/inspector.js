import { icon } from './icons.js';

const SHAPES = ['', 'rectangle', 'square', 'oval', 'circle', 'diamond', 'hexagon', 'parallelogram', 'cylinder',
  'queue', 'page', 'document', 'package', 'step', 'callout', 'stored_data', 'person', 'cloud', 'text'];

const NODE_STYLE = [
  ['style.fill', 'Fill', 'color'],
  ['style.stroke', 'Stroke', 'color'],
  ['style.font-color', 'Text', 'color'],
  ['style.stroke-dash', 'Dash', 'number'],
  ['style.border-radius', 'Radius', 'number'],
];
const EDGE_STYLE = [
  ['style.stroke', 'Stroke', 'color'],
  ['style.stroke-width', 'Width', 'number'],
  ['style.stroke-dash', 'Dash', 'number'],
  ['style.font-color', 'Text', 'color'],
];
const CLASS_STYLE = [
  ['style.fill', 'Fill', 'color'],
  ['style.stroke', 'Stroke', 'color'],
  ['style.font-color', 'Text', 'color'],
  ['style.stroke-width', 'Width', 'number'],
  ['style.stroke-dash', 'Dash', 'number'],
  ['style.border-radius', 'Radius', 'number'],
];

function h(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (v === true) e.setAttribute(k, '');
    else if (v !== false && v != null) e.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c != null) e.append(c);
  }
  return e;
}

export function classFill(model, name) {
  const c = model && model.classes.find((x) => x.name === name);
  return c ? c.props['style.fill'] || c.props['style.stroke'] || '' : '';
}

function commitOn(input, initial, apply, multiline = false) {
  input.value = initial;
  let done = false;
  const commit = () => {
    if (done) return;
    if (input.value !== initial) {
      done = true;
      apply(input.value);
    }
  };
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (!multiline || e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      commit();
    } else if (e.key === 'Escape') {
      input.value = initial;
      input.blur();
      e.stopPropagation();
    }
  });
  input.addEventListener('blur', commit);
  if (input.tagName === 'SELECT') input.addEventListener('change', commit);
}

export function renderInspector(root, ctx) {
  const {sel, model, errors} = ctx;
  const item = ctx.find(sel);
  root.replaceChildren();
  if (!model) return;
  if (!sel || !item) {
    root.append(emptyState(ctx));
    return;
  }
  const kind = sel.kind;
  const err = (field) => (errors && errors[field] ? h('div', {class: 'd2l-field-error'}, errors[field]) : null);

  const field = (label, control, errorKey) => h('label', {class: 'd2l-field'},
    h('span', {class: 'd2l-label'}, label), control, err(errorKey));

  const textInput = (value, apply, attrs = {}) => {
    const i = h('input', {type: 'text', spellcheck: 'false', ...attrs});
    commitOn(i, value, apply);
    return i;
  };
  const textArea = (value, apply, attrs = {}) => {
    const t = h('textarea', {spellcheck: 'false', rows: Math.min(8, Math.max(2, value.split('\n').length + 1)), ...attrs});
    commitOn(t, value, apply, true);
    return t;
  };
  const set = (key) => (v) => ctx.op({kind: 'set', id: sel.id, key, value: v === '' ? null : v}, key);

  const classSelect = (value) => {
    const s = h('select', {class: 'd2l-select'},
      h('option', {value: ''}, '—'),
      model.classes.map((c) => h('option', {value: c.name}, c.name)));
    commitOn(s, value || '', set('class'));
    const sw = h('span', {class: 'd2l-swatch', style: `--sw: ${classFill(model, value) || 'transparent'}`});
    const row = h('div', {class: 'd2l-row'}, sw, s);
    if (value) {
      row.append(h('button', {type: 'button', class: 'd2l-link', title: 'Edit this class',
        onclick: () => ctx.select({kind: 'class', id: 'classes.' + value})}, 'edit'));
    }
    return row;
  };

  const styleSection = (spec, props, open) => {
    const rows = spec.map(([key, label, type]) => {
      const value = props[key] || '';
      if (type === 'color') {
        const text = textInput(value, set(key), {placeholder: 'inherit'});
        const picker = h('input', {type: 'color', value: /^#[0-9a-f]{6}$/i.test(value) ? value : '#ffffff'});
        picker.addEventListener('change', () => set(key)(picker.value.toUpperCase()));
        return field(label, h('div', {class: 'd2l-row'}, h('span', {class: 'd2l-color'}, picker), text), key);
      }
      return field(label, textInput(value, set(key), {inputmode: 'numeric', placeholder: 'inherit'}), key);
    });
    return h('details', {class: 'd2l-section', open}, h('summary', {}, 'Style overrides'), h('div', {class: 'd2l-grid'}, rows));
  };

  const hasStyle = (props) => Object.keys(props).some((k) => k.startsWith('style.'));

  const header = (chip, title, extra) => h('div', {class: 'd2l-ihead'},
    h('span', {class: 'd2l-chip d2l-chip-' + kind}, chip),
    h('span', {class: 'd2l-ititle', title}, title),
    extra);

  const deleteBtn = h('button', {type: 'button', class: 'd2l-danger', title: 'Delete (⌫)', onclick: () => ctx.remove()}, 'Delete');

  if (kind === 'object') {
    root.append(
      header('State', item.id, deleteBtn),
      field('ID', textInput(item.id, (v) => ctx.op({kind: 'rename', id: item.id, to: v}, 'to'), {'data-focus': 'id'}), 'to'),
      field(item.markdown ? 'Label · markdown' : 'Label',
        textArea(item.label, set('label'), {placeholder: 'shows the ID', class: item.markdown ? 'd2l-md' : ''}), 'label'),
      field('Class', classSelect(item.props.class), 'class'),
      field('Shape', (() => {
        const s = h('select', {class: 'd2l-select'}, SHAPES.map((x) => h('option', {value: x}, x || '—')));
        commitOn(s, item.props.shape || '', set('shape'));
        return s;
      })(), 'shape'),
      styleSection(NODE_STYLE, item.props, hasStyle(item.props)),
      field('Comment', textArea(item.comment ? item.comment.text : '',
        (v) => ctx.op({kind: 'setComment', id: item.id, text: v}, 'comment'), {placeholder: '# above the declaration'}), 'comment'),
    );
  } else if (kind === 'edge') {
    root.append(
      header('Transition', `${item.src} → ${item.dst}`, deleteBtn),
      h('div', {class: 'd2l-row d2l-ends'},
        h('button', {type: 'button', class: 'd2l-link', onclick: () => ctx.select({kind: 'object', id: item.src})}, item.src),
        h('span', {}, '→'),
        h('button', {type: 'button', class: 'd2l-link', onclick: () => ctx.select({kind: 'object', id: item.dst})}, item.dst),
        h('button', {type: 'button', class: 'd2l-ghost-btn', title: 'Reverse', onclick: () => ctx.op({kind: 'reverse', id: item.id})}, icon('swap'))),
      field('Label', textArea(item.label, set('label'), {placeholder: 'event or action', 'data-focus': 'id'}), 'label'),
      field('Class', classSelect(item.props.class), 'class'),
      styleSection(EDGE_STYLE, item.props, hasStyle(item.props)),
      field('Comment', textArea(item.comment ? item.comment.text : '',
        (v) => ctx.op({kind: 'setComment', id: item.id, text: v}, 'comment'), {placeholder: '# above the transition'}), 'comment'),
    );
  } else if (kind === 'class') {
    const users = model.objects.filter((o) => o.props.class === item.name).length +
      model.edges.filter((e) => e.props.class === item.name).length;
    root.append(
      header('Class', item.name, h('span', {class: 'd2l-muted'}, `${users} use${users === 1 ? '' : 's'}`)),
      field('Shape', (() => {
        const s = h('select', {class: 'd2l-select'}, SHAPES.map((x) => h('option', {value: x}, x || '—')));
        commitOn(s, item.props.shape || '', set('shape'));
        return s;
      })(), 'shape'),
      h('div', {class: 'd2l-grid'}, CLASS_STYLE.map(([key, label, type]) => {
        const value = item.props[key] || '';
        const input = textInput(value, set(key), {placeholder: '—'});
        if (type !== 'color') return field(label, input, key);
        const picker = h('input', {type: 'color', value: /^#[0-9a-f]{6}$/i.test(value) ? value : '#ffffff'});
        picker.addEventListener('change', () => set(key)(picker.value.toUpperCase()));
        return field(label, h('div', {class: 'd2l-row'}, h('span', {class: 'd2l-color'}, picker), input), key);
      })),
    );
  }
}

function emptyState(ctx) {
  const {model} = ctx;
  return h('div', {class: 'd2l-empty'},
    h('div', {class: 'd2l-keys'},
      h('div', {}, h('kbd', {}, 'click'), ' select'),
      h('div', {}, h('kbd', {}, 'N'), ' new state'),
      h('div', {}, h('kbd', {}, 'C'), ' connect from selected'),
      h('div', {}, h('kbd', {}, '⌫'), ' delete'),
      h('div', {}, h('kbd', {}, '⌘Z'), ' undo'),
      h('div', {}, h('kbd', {}, 'F'), ' fit to screen'),
      h('div', {}, h('kbd', {}, 'E'), ' leave edit mode')),
    model.classes.length ? h('div', {class: 'd2l-classes'},
      h('div', {class: 'd2l-label'}, 'Classes'),
      model.classes.map((c) => h('button', {type: 'button', class: 'd2l-class-pill',
        onclick: () => ctx.select({kind: 'class', id: 'classes.' + c.name})},
      h('span', {class: 'd2l-swatch', style: `--sw: ${classFill(model, c.name) || 'transparent'}`}), c.name))) : null,
  );
}
