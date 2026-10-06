import {
  EditorState, StateField, StateEffect, Annotation, Transaction,
  EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, Decoration,
  WidgetType, drawSelection, highlightSpecialChars, gutter, GutterMarker,
  defaultKeymap, history, historyKeymap, indentWithTab,
  bracketMatching, indentOnInput, searchKeymap, highlightSelectionMatches,
  closeBrackets, closeBracketsKeymap,
} from './vendor/codemirror.js';
import { d2Language, d2Highlight } from './d2lang.js';
import { diffLines } from './linediff.js';

const programmatic = Annotation.define();

const FLASH_MS = 2200;

const setFlash = StateEffect.define();
const clearFlash = StateEffect.define();

class GhostWidget extends WidgetType {
  constructor(text) {
    super();
    this.text = text;
  }
  eq(other) {
    return other.text === this.text;
  }
  toDOM() {
    const el = document.createElement('div');
    el.className = 'cm-d2l-ghost';
    el.textContent = this.text.replace(/\n$/, '');
    return el;
  }
}

const flashField = StateField.define({
  create: () => ({id: 0, deco: Decoration.none}),
  update(value, tr) {
    let {id, deco} = value;
    deco = deco.map(tr.changes);
    for (const e of tr.effects) {
      if (e.is(setFlash)) {
        const ranges = [];
        for (const pos of e.value.added) {
          ranges.push(Decoration.line({class: 'cm-d2l-added'}).range(pos));
        }
        for (const r of e.value.removed) {
          ranges.push(Decoration.widget({widget: new GhostWidget(r.text), block: true, side: -1}).range(r.pos));
        }
        id = e.value.id;
        deco = Decoration.set(ranges, true);
      } else if (e.is(clearFlash) && e.value === id) {
        deco = Decoration.none;
      }
    }
    return {id, deco};
  },
  provide: (f) => EditorView.decorations.from(f, (v) => v.deco),
});

class WrapMarker extends GutterMarker {
  constructor(rows, rowHeight) {
    super();
    this.rows = rows;
    this.rowHeight = rowHeight;
  }
  eq(other) {
    return other.rows === this.rows && other.rowHeight === this.rowHeight;
  }
  toDOM() {
    const el = document.createElement('div');
    el.className = 'cm-d2l-wrapmarks';
    for (let i = 0; i < this.rows; i++) {
      const row = document.createElement('div');
      row.style.height = this.rowHeight + 'px';
      row.textContent = i ? '↪' : '';
      el.appendChild(row);
    }
    return el;
  }
}

const wrapGutter = gutter({
  class: 'cm-d2l-wrap-gutter',
  lineMarker(view, line) {
    const rowHeight = view.defaultLineHeight;
    const rows = Math.round(line.height / rowHeight);
    return rows > 1 ? new WrapMarker(rows, rowHeight) : null;
  },
  lineMarkerChange: (update) => update.geometryChanged,
});

const setMarks = StateEffect.define();

const marksField = StateField.define({
  create: () => Decoration.none,
  update(deco, tr) {
    deco = deco.map(tr.changes);
    for (const e of tr.effects) {
      if (!e.is(setMarks)) continue;
      const {decl, refs, errorLine} = e.value;
      const len = tr.state.doc.length;
      const ok = (r) => r && r.from <= r.to && r.to <= len;
      const ranges = [];
      if (ok(decl) && decl.from < decl.to) {
        ranges.push(Decoration.mark({class: 'cm-d2l-decl'}).range(decl.from, decl.to));
      }
      for (const r of refs || []) {
        if (ok(r) && r.from < r.to) ranges.push(Decoration.mark({class: 'cm-d2l-ref'}).range(r.from, r.to));
      }
      if (errorLine && errorLine <= tr.state.doc.lines) {
        ranges.push(Decoration.line({class: 'cm-d2l-error-line'}).range(tr.state.doc.line(errorLine).from));
      }
      deco = Decoration.set(ranges, true);
    }
    return deco;
  },
  provide: (f) => EditorView.decorations.from(f),
});

const theme = EditorView.theme({
  '&': {height: '100%', fontSize: '11.5px', color: '#e0def4', backgroundColor: 'transparent'},
  '.cm-scroller': {fontFamily: 'ui-monospace, "SF Mono", "JetBrains Mono", Menlo, monospace', lineHeight: '1.45'},
  '.cm-content': {caretColor: '#c4a7e7', padding: '8px 0'},
  '.cm-cursor': {borderLeftColor: '#c4a7e7', borderLeftWidth: '2px'},
  '.cm-gutters': {backgroundColor: 'transparent', color: '#6e6a86', border: 'none'},
  '.cm-d2l-wrap-gutter .cm-gutterElement': {padding: '0 2px 0 0', minWidth: '12px', color: '#c4a7e7', textAlign: 'right'},
  '.cm-d2l-wrapmarks > div': {fontSize: '11px', opacity: '0.75', display: 'flex', alignItems: 'center', justifyContent: 'flex-end'},
  '.cm-lineWrapping > .cm-line': {paddingLeft: 'calc(6px + 2ch)', textIndent: '-2ch'},
  '.cm-activeLine': {backgroundColor: 'rgba(224, 222, 244, 0.04)'},
  '.cm-activeLineGutter': {backgroundColor: 'transparent', color: '#e0def4'},
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection': {backgroundColor: 'rgba(196, 167, 231, 0.28) !important'},
  '.cm-matchingBracket': {backgroundColor: 'rgba(156, 207, 216, 0.22)', outline: 'none'},
  '.cm-selectionMatch': {backgroundColor: 'rgba(246, 193, 119, 0.12)'},
  '.cm-panels': {backgroundColor: '#2a273f', color: '#e0def4'},
  '.cm-panels input, .cm-panels button': {color: '#e0def4', background: '#393552', border: 'none', borderRadius: '6px'},
}, {dark: true});

export function createEditor(parent, {onDocChange, onCursor}) {
  const extensions = [
    lineNumbers(), wrapGutter, highlightActiveLineGutter(), highlightSpecialChars(), history(), drawSelection(),
    indentOnInput(), bracketMatching(), closeBrackets(), highlightActiveLine(), highlightSelectionMatches(),
    keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, indentWithTab]),
    d2Language, d2Highlight, flashField, marksField, theme, EditorView.lineWrapping,
    EditorView.updateListener.of((u) => {
      const isProgrammatic = u.transactions.some((tr) => tr.annotation(programmatic));
      if (u.docChanged) onDocChange(view.state.doc.toString(), isProgrammatic);
      if (u.selectionSet && !isProgrammatic && u.transactions.some((tr) => tr.isUserEvent('select') || tr.docChanged)) {
        onCursor(u.state.selection.main.head);
      }
    }),
  ];
  const view = new EditorView({parent, state: EditorState.create({doc: '', extensions})});

  let flashSeq = 0;

  function text() {
    return view.state.doc.toString();
  }

  function replace(next, {flash = true, history: addToHistory = true} = {}) {
    const cur = text();
    if (cur === next) return;
    const {a, b, hunks} = diffLines(cur, next);
    const aStart = offsets(a);
    const bStart = offsets(b);
    const changes = hunks.map((h) => ({
      from: aStart[h.aFrom], to: aStart[h.aTo], insert: b.slice(h.bFrom, h.bTo).join(''),
    }));
    const effects = [];
    if (flash && hunks.length) {
      const added = [];
      const removed = [];
      for (const h of hunks) {
        for (let k = h.bFrom; k < h.bTo; k++) added.push(bStart[k]);
        if (h.aTo > h.aFrom) removed.push({pos: bStart[h.bFrom], text: a.slice(h.aFrom, h.aTo).join('')});
      }
      const id = ++flashSeq;
      effects.push(setFlash.of({id, added, removed}));
      effects.push(EditorView.scrollIntoView(bStart[hunks[0].bFrom], {y: 'nearest', yMargin: 60}));
      setTimeout(() => view.dispatch({effects: clearFlash.of(id)}), FLASH_MS);
    }
    view.dispatch({
      changes, effects,
      annotations: [programmatic.of(true), Transaction.addToHistory.of(addToHistory)],
    });
  }

  function reset(next) {
    view.setState(EditorState.create({doc: next, extensions}));
  }

  function mark({decl = null, refs = [], errorLine = null, scroll = false} = {}) {
    const effects = [setMarks.of({decl, refs, errorLine})];
    if (scroll && decl) effects.push(EditorView.scrollIntoView(decl.from, {y: 'center'}));
    view.dispatch({effects, annotations: programmatic.of(true)});
  }

  return {view, text, replace, reset, mark, focus: () => view.focus()};
}

function offsets(lines) {
  const out = new Array(lines.length + 1);
  let pos = 0;
  for (let i = 0; i < lines.length; i++) {
    out[i] = pos;
    pos += lines[i].length;
  }
  out[lines.length] = pos;
  return out;
}
