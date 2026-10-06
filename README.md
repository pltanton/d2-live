# d2-live

Small local D2 preview server for `*.d2` files.

What it does:

- live SVG preview with pan and zoom
- PNG render through native `d2` export
- sketch mode and layout engine switchable from the UI
- copy / download SVG and PNG
- zoomable UI with preserving position, for seamless re-rendering
- one shared server for many files, with a file dropdown in the UI
- editor integration over LSP (no keybindings or custom glue needed)
- edit mode: change states and transitions on the diagram, with the code alongside

## Run

```bash
d2-live diagram.d2      # a single file
d2-live ./diagrams      # a directory: every *.d2 under it (recursive)
```

The first invocation runs the preview server in the foreground and logs its
URL:

```
d2-live: serving at http://127.0.0.1:43117  (3 file(s), idle-timeout 10m0s)
```

It keeps running until you `Ctrl-C` it (or until no tab has been connected for
`--idle-timeout`). Any later `d2-live <file>` connects to that same server,
registers its file, opens a tab, and returns immediately:

```
d2-live: connected to server at http://127.0.0.1:43117 (added 1 file(s))
```

So there is always a single server process serving every file you have open.

In a tab you can switch the file (dropdown), the layout engine, and sketch mode
in place — no new tab, no restart. Each `(file, sketch, layout)` combination
keeps its own pan/zoom position.

## Flags

- `--layout elk|dagre|tala` initial D2 layout engine (also switchable in the UI)
- `--port PORT` bind the server to a specific local port (`0` = auto)
- `--browser BROWSER` open a specific browser command
- `--no-browser` register the file without opening a browser
- `--sketch` start the tab in sketch mode
- `--idle-timeout DUR` exit after this long with no connected tabs (default `10m`, `0` = never)
- `--close` unregister the file (or every `*.d2` under the directory) from the running server and exit
- `--lsp` run as a language server over stdio (see below)

## Unregistering files

Files leave the server three ways:

```bash
d2-live --close diagram.d2      # explicitly, without touching the file
```

Deleting a diagram on disk drops it automatically (the watcher waits out the
debounce first, so a save that writes a temp file and renames it over the target
is not mistaken for a deletion). In LSP mode, closing the buffer unregisters the
file too — reopening it registers it again.

Tabs viewing a file that goes away reload onto a file the server still has,
rather than showing a render failure.

## Editor integration (LSP)

`d2-live --lsp` speaks just enough of the Language Server Protocol over stdio to
launch a live preview from your editor — no keybindings or custom commands.
Attach it as a language server for `.d2` files: when you open one, d2-live
registers it with the preview server and opens a browser tab. Saving the buffer
refreshes the preview (via the file watcher). When the editor exits, it shuts
the language server down, which takes the preview server with it — so nothing is
left orphaned.

### Helix

In `~/.config/helix/languages.toml`:

```toml
[language-server.d2-live]
command = "d2-live"
args = ["--lsp"]

[[language]]
name = "d2"
file-types = ["d2"]
language-servers = ["d2-live"]
```

`d2-live` must be on Helix's `PATH` (use an absolute `command` otherwise). Open
any `.d2` file and the preview appears automatically.

## Claude Code integration

`skills/d2-live/` is a Claude Code skill that teaches Claude to use this tool
while it edits diagrams: it registers every `*.d2` file it touches with the
shared server, hands you a preview URL, and then leaves the preview alone —
the file watcher does the refreshing. It also stops Claude from the two obvious
mistakes: calling `d2-live` in the foreground (which blocks, because the first
invocation _is_ the server) and re-running it after every edit (which opens a
browser tab each time).

Install as a plugin (updates with `git pull` on the marketplace):

```
/plugin marketplace add pltanton/d2-live
/plugin install d2-live
```

Or link it as a personal skill:

```bash
ln -s "$PWD/skills/d2-live" ~/.claude/skills/d2-live
```

The skill ships one helper, `skills/d2-live/scripts/d2-live-preview`, which is
useful on its own — it reuses a healthy server, starts a detached one if there
is none, registers files by absolute path, and prints the preview URL without
ever blocking:

```bash
skills/d2-live/scripts/d2-live-preview ./docs/diagrams
```

## Edit mode

Press `E` (or the **Edit** button) in a tab. A code panel opens next to the
diagram; the diagram, the code and the file stay in sync both ways.

- **Select** a state or transition by clicking it, or by putting the cursor in
  its declaration. Its declaration is highlighted in the code, its other
  mentions faintly.
- **Inspector** under the code: ID (renaming follows every reference), label
  (markdown labels stay `|md` blocks), class, shape, style overrides and the
  `#` comment above the declaration. Click **edit** next to a class to change
  the class itself.
- **On the diagram:** the toolbar over a selected state changes its class,
  connects or deletes it. Connect (`→`), then click another state for a
  transition, or empty canvas for a new state wired to it. Keys: `N` new state, `C` connect, `⌫` delete, `Enter` rename,
  `Esc` deselect, `F` fit to screen, `⌘Z` / `⇧⌘Z` undo/redo, `⌘S` save.
- Typing in the code panel previews the buffer live without touching the file;
  `⌘S` (or **Save**) writes it. Structural changes from the inspector or the
  diagram, and undo/redo from the toolbar, are written at once. Every change
  flashes in the code as a diff, and touches only the lines it is about:
  comments, ordering and formatting elsewhere stay as they were. Leaving edit
  mode or switching files with unsaved typing asks first.
- External edits (your editor, an agent, `git checkout`) appear live. If one
  races unsaved typing, a banner offers **Take theirs** / **Keep mine**.
- While the code does not compile, the last good diagram stays on screen and the
  error is shown under the editor.

Structural edits cover flat diagrams (top-level states, single `a -> b`
transitions) — the shape of a state machine. Containers, chains like
`a -> b -> c` and globs stay editable in the code panel; the inspector says so
instead of guessing.

## Development

```bash
go test ./...                                    # unit tests
go build -o d2-live . && ./scripts/itest.py ./d2-live   # end-to-end: LSP stdio, CLI, watcher, prune, edit endpoints
go test -run TestOps -update                     # regenerate testdata/golden after an intended change to an edit op
```

The code panel uses a prebuilt CodeMirror bundle, `ui/vendor/codemirror.js`.
To rebuild it: `cd scripts/codemirror && npm ci && npm run build`.

The end-to-end script drives a real binary in an isolated `XDG_RUNTIME_DIR`, so
it never touches a server you have running.

## Notes

- PNG export uses native `d2` rendering, so tooltips and other D2-specific export details come through there.
- SVG copy is exported as an SVG file object in the browser clipboard.
- Server discovery uses a lock file under `$XDG_RUNTIME_DIR/d2-live/` (with a `$XDG_CACHE_HOME`/temp fallback); concurrent first launches resolve to a single server via an exclusive `flock`. The lock is held in a package-level variable on purpose: `os.File` has a finalizer that closes the fd, and a closed fd releases the `flock` — a lock kept only in a local variable can be collected mid-run, which used to let a second server start alongside a healthy one.
