import { StreamLanguage, HighlightStyle, syntaxHighlighting, tags as t } from './vendor/codemirror.js';

const reserved = new Set([
  'shape', 'style', 'label', 'class', 'classes', 'near', 'direction', 'vars', 'width', 'height',
  'icon', 'tooltip', 'link', 'constraint', 'grid-rows', 'grid-columns', 'grid-gap', 'layers',
  'scenarios', 'steps', 'source-arrowhead', 'target-arrowhead', 'fill', 'stroke', 'stroke-width',
  'stroke-dash', 'border-radius', 'font-color', 'font-size', 'opacity', 'shadow', 'bold', 'italic',
  'underline', 'multiple', 'double-border', 'animated', '3d', 'filled', 'fill-pattern', 'text-transform',
]);

export const d2Language = StreamLanguage.define({
  name: 'd2',
  startState: () => ({ block: null, afterColon: false }),
  token(stream, state) {
    if (state.block) {
      if (stream.sol()) {
        stream.eatSpace();
        if (stream.match(state.block)) {
          state.block = null;
          return 'string';
        }
      }
      stream.skipToEnd();
      return 'string';
    }
    if (stream.sol()) {
      state.afterColon = false;
    }
    if (stream.eatSpace()) {
      return null;
    }
    if (stream.match(/^#.*/)) {
      return 'comment';
    }
    const block = stream.match(/^(\|+)([a-z]*)/);
    if (block) {
      state.block = block[1];
      if (stream.match(new RegExp('^.*?' + block[1].replace(/\|/g, '\\|') + '(?!\\|)'))) {
        state.block = null;
      } else {
        stream.skipToEnd();
      }
      return 'string';
    }
    if (stream.match(/^"(?:[^"\\]|\\.)*"?/) || stream.match(/^'(?:[^'\\]|\\.)*'?/)) {
      return state.afterColon ? 'string' : 'variableName';
    }
    if (stream.match(/^(<->|<-|->|--)/)) {
      state.afterColon = false;
      return 'operator';
    }
    if (stream.match(/^[{}]/)) {
      state.afterColon = false;
      return 'brace';
    }
    if (stream.match(/^[;]/)) {
      state.afterColon = false;
      return 'punctuation';
    }
    if (stream.match(/^:/)) {
      state.afterColon = true;
      return 'punctuation';
    }
    if (stream.match(/^\[(\d+|\*)\]/) || stream.match(/^[()]/)) {
      return 'punctuation';
    }
    if (state.afterColon && stream.match(/^-?\d+(\.\d+)?(?=[\s;}]|$)/)) {
      return 'number';
    }
    const word = stream.match(/^[^\s:;{}#"'|]+?(?=\s*(->|<-|--|[:;{}]|$)|\s)/) || stream.match(/^[^\s:;{}#"'|]+/);
    if (word) {
      if (state.afterColon) {
        return /^(true|false|null)$/.test(word[0]) ? 'atom' : 'string';
      }
      const last = word[0].split('.').pop();
      return reserved.has(last) ? 'keyword' : 'variableName';
    }
    stream.next();
    return null;
  },
  languageData: { commentTokens: { line: '#' } },
});

export const d2Highlight = syntaxHighlighting(HighlightStyle.define([
  { tag: t.comment, color: '#6e6a86', fontStyle: 'italic' },
  { tag: t.keyword, color: '#c4a7e7' },
  { tag: t.variableName, color: '#e0def4', fontWeight: '600' },
  { tag: t.string, color: '#f6c177' },
  { tag: t.number, color: '#ea9a97' },
  { tag: t.atom, color: '#ea9a97' },
  { tag: t.operator, color: '#9ccfd8', fontWeight: '700' },
  { tag: t.brace, color: '#908caa' },
  { tag: t.punctuation, color: '#908caa' },
]));
