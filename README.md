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
- `--lsp` run as a language server over stdio (see below)

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

## Notes

- PNG export uses native `d2` rendering, so tooltips and other D2-specific export details come through there.
- SVG copy is exported as an SVG file object in the browser clipboard.
- Server discovery uses a lock file under `$XDG_RUNTIME_DIR/d2-live/` (with a `$XDG_CACHE_HOME`/temp fallback); concurrent first launches resolve to a single server via an exclusive `flock`.
