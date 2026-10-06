# Edit mode for d2-live — design

Date: 2026-10-06
Status: approved in brainstorming, pending spec review

## Goal

Edit a diagram from the preview tab: select a node or edge on the diagram, see and
change its code in a side editor, rename, restyle, add and delete states and
transitions, edit comments — and have every change land in the `.d2` file as
clean, idiomatic D2 that a human would have written. The reference workload is
the FSM diagrams in `~/dev/wallet/daedalus/docs/diagrams/*-fsm.d2`: flat
top-level states with `class:` from a `classes:` block, edges with `class:`,
`#` comments above declarations, markdown `title` / `notes` nodes.

Non-negotiables:

- **Lightweight.** View mode stays as light as today. Editor code is loaded only
  on first entry into edit mode. No frontend build step.
- **The file is the source of truth.** External changes (editor, agent, git)
  keep showing up live, as they do now.
- **Minimal diffs.** An operation changes only the lines it is about; comments,
  ordering and formatting of the rest survive.

## Out of scope (MVP)

Nested containers (top-level objects only), drag-to-reposition, multi-select,
sequence diagrams, multi-board files (layers/scenarios/steps), collaborative
editing, dark theme of the canvas.

## Architecture

```
┌ browser ──────────────────────────────────────────────┐
│ SVG + overlay  ⇄  CodeMirror 6  ⇄  inspector           │
│     │ click → id        │ typing      │ field → op      │
└─────┼───────────────────┼─────────────┼─────────────────┘
      │ GET /model        │ PUT /source │ POST /edit
┌ d2-live (Go) ──────────────────────────────────────────┐
│ /model  compile → objects, edges, classes, ranges      │
│ /edit   op on AST → new text                           │
│ /source atomic write, returns new hash                 │
│ watcher + SSE: as today, plus "source" event           │
└────────────────────────────────────────────────────────┘
```

Structural edits run on the server against the real D2 AST
(`oss.terrastruct.com/d2`: `d2compiler`, `d2graph`, `d2ast`), applied as byte
splices of the original text (see below).
Alternatives rejected: D2 compiled to WASM in the browser (megabytes per tab, the
server already exists); a hand-written JS parser for an FSM subset (breaks on the
first nested map or block string). Cost of the chosen route: the binary grows
from ~6 MB to ~20 MB (measured with a probe build); nothing heavy reaches the
browser.

### Code layout

`main.go` is already ~1900 lines. As part of this work:

- `ui/index.html`, `ui/app.js` (view mode, moved out of the Go template),
  `ui/edit.js` (edit mode, lazy-loaded), `ui/edit.css` — embedded with
  `//go:embed`. Still one Go package, no bundler.
- `model.go` — compile source, build the model for the client.
- `edit.go` — operations.
- `source.go` — reading/writing the file, content hashes, echo suppression.

## Server

### `GET /model?file=…`

Compiles the current file text and returns:

```json
{
  "hash": "sha256 of the text",
  "objects": [{"id": "DECISION_APPROVED", "label": "…", "labelIsMarkdown": false,
               "class": "active", "shape": "rectangle", "style": {"fill": "…"},
               "decl": {"from": 1290, "to": 1325},
               "refs": [{"from": 2210, "to": 2227}],
               "comment": {"text": "…", "range": {…}} }],
  "edges":   [{"id": "(A -> B)[0]", "src": "A", "dst": "B", "label": "…",
               "class": "evt", "decl": {…}, "comment": {…}}],
  "classes": [{"name": "pause", "style": {"fill": "#FFF8E8", …}, "decl": {…}}],
  "error":   null
}
```

Ranges are UTF-16 offsets into the text (what CodeMirror uses), converted from
the AST's byte ranges. `decl` is the reference
that carries the declaration's map/label (the first one if several); `refs` are
the other mentions. `comment` is the run of consecutive `#` lines directly above
`decl` (no blank line in between), or null. On a compile error the response
carries `error: {line, col, message}` and the last good model is kept on the
client.

### `POST /edit`

Request: `{"file": "…", "baseHash": "…", "op": {"kind": "rename", …}}`. The server reads the file;
if its hash differs from `baseHash` it answers `409 {"reason": "stale", "text":
current}`. Otherwise it applies the op and answers `200 {"text": new, "select":
id}`. The server does **not** write the file — the client applies the new text
to the editor (so it becomes an undo step and gets diff decorations) and the
autosave writes it. Op failures (rename onto an existing id, unknown id) answer
`422 {"field": "…", "message": "…"}` and change nothing.

Ops:

| op | effect |
|---|---|
| `rename {id, to}` | every reference of the object (declaration, edge ends) |
| `createNode {class?}` | new `NEW_STATE` / `NEW_STATE_2` / …, see Placement |
| `createEdge {src, dst, label?, class?}` | new edge line, see Placement |
| `createNodeWithEdge {src, class?, edgeClass?}` | both, one response = one undo step |
| `delete {id}` | object: its declaration lines, every edge touching it, their comment runs; edge: its line and comment run |
| `reverse {id}` | swap the src/dst text of the edge declaration |
| `set {id, key, value}` | in-place property setter; `value: null` removes the key |
| `setComment {id, text}` | replace/insert/remove the `#` run above `decl` |

`id` addresses objects (`STATE`), edges (`(A -> B)[0]`) and classes
(`classes.pause`). `key` is one of `label`, `class`, `shape`, `style.<prop>`
with prop in `fill, stroke, stroke-width, stroke-dash, border-radius,
font-color`.

#### Text splicing instead of `d2oracle` + `d2format`

Every op is a list of byte-range replacements on the original text, computed
from the compiled graph's AST ranges (`d2graph.Object.References`,
`d2graph.Edge.References`, `d2ast` node ranges). The result is compiled; if it
does not compile, or the op's postcondition fails (e.g. the renamed id is not in
the new graph), the op answers 422 and the file is untouched.

Why not `d2oracle` (probed on `withdraw-fsm.d2`, d2 v0.7.1 and master
`716f6185da68`):

- `Set("(A -> B)[0].class", "evt")` on `A -> B: "…" {class: sync}` appends a
  second `class: evt` instead of replacing.
- `Set("classes.pause.style.fill", …)` appends a top-level
  `classes.pause.style.fill: …` line at the end of the file instead of editing
  the `classes:` block.
- `Set("title.label", …)` turns a `|md … |` block string into a quoted string.
- Any oracle op goes through `d2format`, which rewrites unrelated lines
  (`A -> B {class: x}` → `A -> B: {class: x}`), and d2format derives blank lines
  and inline-vs-block maps from node ranges, which new nodes do not have.

Splicing changes only the bytes the op is about. Constructs outside the FSM
subset — edge chains (`a -> b -> c`), globs, imports, nested containers,
`(A -> B)[0].x` references to an edge outside its declaration — make the op
answer 422 "edit this by hand"; they stay editable in the code editor.

#### Setter

1. Target map: the map value of `decl` (object/edge), or for a class the
   `classes: {name: {…}}` map.
2. Walk the dotted key (`style.fill`) inside that map, accepting both nested
   (`style: {fill: …}`) and dotted (`style.fill: …`) forms. Found → replace the
   value's byte range. Not found → insert into the deepest existing map on the
   path: into an inline map as `; key: value` before its `}`, into a block map
   as a new line at the map's indentation + 2 before its closing `}` line. No
   map on `decl` at all → append ` {key: value}` after the decl key's
   label/primary.
3. Label: for objects/edges the label is the key's primary value
   (`X: "label" {…}`) or the value itself (`X: label`). A block string keeps its
   quote and tag and only swaps the body, re-indented to the key's indentation
   + 2. An object with no label gets `X: "label"` inserted after the key.
4. Values are written unquoted when they match `^[A-Za-z0-9_#.\-]+$` and are
   not a D2 keyword, otherwise double-quoted with `\"`, `\\` and `\n`
   escapes. A markdown label (old value is `|md`) stays a block string.
5. `value: null` removes the key (and the `; ` / line around it); an emptied
   `style: {}` and an emptied `{}` on the declaration go too.

#### Placement

- New node: a new line right after the last top-level object declaration (a key
  without edges that is not `classes`, `title`, `notes`, `direction`, `vars`,
  `style`); written as `NAME: {class: X}` or `NAME`.
- New edge: a new line right after the last edge declaration whose source is
  the same object; if none, after the last top-level edge declaration; if the
  file has no edges, after the last object declaration. Written as
  `SRC -> DST: "label" {class: X}`, parts omitted when empty.

### `PUT /source`

Body `{"file", "baseHash", "text"}`. Writes atomically (temp file in the same
dir + rename, preserving file mode). `409` with the current text if the on-disk
hash differs from `baseHash`. On success returns `{"hash": new}` and records the
hash as "written by us", so the watcher event it causes is not sent back to this
client as an external change. The file must be one registered with the server;
any other path is `403`.

### SSE

`/events` keeps sending `reload` for the SVG. Additionally, when the file content
changes and its hash is not the one just written by a `PUT /source`, it sends
`source` with `{hash, text}`.

### Render while typing

Unchanged path: autosave writes the file → watcher → re-render → `reload`. When
`d2` fails to render, the server keeps serving the last successful SVG for that
key and sends the error separately (`/model` error) instead of the error SVG —
only for requests with `edit=1` (`/svg?…&edit=1`). View mode behaves as today.

## Client

### Canvas (both modes)

- Hide d2's own background: CSS `#scene .d2-svg > rect:first-child { display:
none }` — d2 draws it as the first `rect.fill-N7` of the nested `<svg>`, and
  masks (not white plates) behind edge labels, so labels stay clean on any
  background. Exported SVG/PNG are untouched (they keep d2's white).
- `#viewer` gets a soft blue-grey background with a dot grid; on panzoom
  `transform` its `background-position`/`background-size` follow the scene, so
  the grid pans and zooms with the diagram. The grid is fainter in view mode.

### Entering edit mode

`✎ Edit` button in the HUD or key `E`; state in the URL (`&edit=1`). On first
entry `ui/edit.js` and CodeMirror 6 are loaded as ES modules from jsdelivr
(`@codemirror/state`, `view`, `commands`, `language`, `search`); syntax
highlighting is a small `StreamLanguage` mode for D2 (keys, strings, block
strings, comments, `->`, braces).

Layout: diagram on the left, a resizable right panel (width in localStorage)
with the editor on top and the inspector below (collapsible). A toolbar over the
diagram: `+ State (N)`, `→ Connect (C)`, `⌫ Delete`.

### Selection and code ↔ diagram link

- d2 marks each shape/edge `<g>` with `class` = base64 of its id; the client
  decodes it and looks the id up in the model.
- Hover outlines the element; click selects it; `Esc` clears. Edges get a
  transparent 10px-wide stroke clone for hit-testing.
- Selecting highlights `decl` strongly and `refs` faintly in the editor and
  scrolls to `decl`. Moving the cursor in the editor selects the element whose
  range contains it and outlines it in the SVG.
- Double-click a node = select + focus the ID field.

### On-node overlay

An HTML layer above the viewer, positioned from the selected `<g>`'s
`getBoundingClientRect()` and recomputed on panzoom `transform` and after every
re-render. Fixed screen size regardless of zoom; pointer events never reach
panzoom.

- Node: a mini toolbar above it — `✎` (focus ID), `● class ▾` (swatch with the
  class's fill, quick class switch), `→` (connect), `⌫` — and four `(+)`
  handles on its sides.
  - Click `(+)` → `createNodeWithEdge` from this node; the new node is selected,
    ID focused. (The side is just a grab point; ELK decides the layout.)
  - Drag from `(+)` or from `→` → a dashed rubber band; drop on a node →
    `createEdge` (drop on the source itself = self-loop); drop on empty canvas →
    `createNodeWithEdge`. Focus goes to the new edge's label / new node's ID.
- Edge: a mini toolbar at its label midpoint — `✎ label`, `● class ▾`, `⇄`
  (reverse), `⌫`.
- Defaults: a new node takes the last class used for a node in this tab, else
  the most common node class in the file; a new edge likewise for edges.

### Inspector

- Node: `ID` (Enter → `rename`), `Label` (multi-line when markdown), `Class`
  (from `classes` + "—"), `Shape`, collapsed `Style overrides` (fill, stroke,
  stroke-dash, border-radius, font-color), `Comment`.
- Edge: `from → to` with `⇄`, `Label`, `Class`, `Comment`.
- Class: clicking the class name next to the dropdown opens the class itself;
  its `style.*` are edited with the same fields (`set` on `classes.<name>`).
- `title` / `notes` are ordinary nodes; their markdown label gets the multi-line
  field.
- A field applies on Enter or blur; one field = one op = one undo step. Errors
  from 422 show under the field; invalid input never reaches the file.

### Applying text from the server

For every new text (from `/edit` or an SSE `source` event) the client computes a
line diff against the editor buffer and dispatches it as one CodeMirror
transaction — so undo/redo (`⌘Z`/`⇧⌘Z`) treat structural ops and typing alike,
and the cursor survives unrelated changes.

Diff visualisation: added/changed lines get a green background that fades out
over ~2 s; removed lines are shown for the same time as red "ghost" block
widgets at the place they were removed. If the first change is off-screen, the
editor scrolls to it. External changes get the same treatment.

### Autosave and external changes

- Typing marks the buffer dirty; 300 ms after the last keystroke the buffer is
  `PUT /source` with the last known hash. Ops from `/edit` are saved
  immediately.
- SSE `source` with a clean buffer → apply as above.
- SSE `source` while dirty, or a 409 on save → a banner "Changed on disk —
  Take theirs / Keep mine". "Keep mine" re-saves with the new base hash; "Take
  theirs" applies their text.
- `/edit` 409 stale → apply the returned text, then re-send the op once.
- Network/write failure → toast; the buffer stays dirty and retries on the next
  change.

### Errors while typing

While the text does not compile, the last good SVG and model stay on screen; the
error is a red line decoration on its line plus the message under the editor.

## Testing

- Go table tests for every op on a copy of `withdraw-fsm.d2` (checked in as
  `testdata/`), comparing against golden output files: rename across refs,
  createNode/createEdge placement, createNodeWithEdge, set class on an edge with
  an inline map (no duplicate key), set `classes.<name>.style.fill` inside the
  `classes:` block, markdown label stays `|md`, remove a key and empty maps,
  setComment insert/replace/remove, delete (node with edges, no leftover blank
  line), reverse.
- `/source`: atomic write, 409 on stale hash, 403 on unregistered path, own
  write not echoed as `source`, an external write is.
- `/model`: ids, ranges and comments on the reference file; error payload on a
  broken file.
- `scripts/itest.py`: an HTTP scenario — open, edit via `/edit` + `PUT
/source`, observe `reload`, external write, observe `source`.
- Manual check in the browser: the run skill / Chrome against the daedalus
  diagrams.
