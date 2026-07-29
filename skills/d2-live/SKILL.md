---
name: d2-live
description: Use when writing, editing, generating or debugging D2 diagrams (*.d2 files — architecture, sequence, flowchart, state diagrams in D2 syntax) in any project where the d2-live command is on PATH, including when asked to show, preview or render a diagram.
---

# Live D2 previews with d2-live

## Overview

`d2-live` is a single shared local preview server for `*.d2` files. One process serves every registered file; the browser tab live-reloads from a file watcher. So the user watches the diagram change while you edit it.

**Core principle: register the file once, then just edit it.** The preview updates itself. Re-running the command per edit is the main way to use this tool wrong.

## Protocol

1. **Write the file first.** `d2-live` only accepts paths that already exist.
2. **Register it and get the URL** — run the helper next to this skill (its base directory is printed when the skill loads):

   ```bash
   scripts/d2-live-preview path/to/diagram.d2
   ```

   It prints:

   ```
   server: http://127.0.0.1:55064
   preview: http://127.0.0.1:55064/?file=/abs/path/to/diagram.d2
   files: 1
   ```

   It reuses a running server when there is one (the user may already have a tab open from their editor), starts a detached one when there is not, and returns immediately either way.

3. **Give the user the `preview:` URL** in your reply. A freshly started server also opens the tab itself.
4. **Editing several diagrams?** Pass the directory instead: `scripts/d2-live-preview ./docs/diagrams`. Every `*.d2` under it lands in the tab's file dropdown.
5. **After every subsequent edit, do nothing.** The watcher reloads the open tab. Do not re-run the command to "refresh".
6. **Verify the diagram compiles** after each edit:

   ```bash
   d2 path/to/diagram.d2 - >/dev/null
   ```

   Exit 1 prints `file:line:col` errors. This matters: the preview endpoint renders a _card containing the error text_ instead of failing, so a broken diagram still looks like a rendered page. Never report a diagram as done without a clean exit here.

7. **New file later in the session?** Run the helper for that path too — it registers with the same server without opening another tab. Add `--open` if the user wants a tab for it.
8. **Done with a throwaway diagram?** `d2-live --close scratch.d2` takes it out of the file dropdown without deleting it (deleting it works too — the server prunes files that disappear). Returns immediately and never starts a server.

## Quick reference

| Need                                       | Command                                                                               |
| ------------------------------------------ | ------------------------------------------------------------------------------------- |
| Register a file, get its URL               | `scripts/d2-live-preview file.d2`                                                     |
| Register a whole tree                      | `scripts/d2-live-preview ./diagrams`                                                  |
| Open a tab for an already-registered file  | `scripts/d2-live-preview --open file.d2`                                              |
| Unregister a file (or a whole tree)        | `d2-live --close file.d2`                                                             |
| Check D2 syntax                            | `d2 file.d2 - >/dev/null`                                                             |
| See the render yourself (layout debugging) | `curl -fsS "$server/png?file=/abs/file.d2" -o out.png`, then Read `out.png`           |
| List registered files                      | `curl -s "$server/files"`                                                             |
| Server log (detached start)                | `$XDG_RUNTIME_DIR/d2-live/d2-live.log`, macOS: `~/Library/Caches/d2-live/d2-live.log` |

Sketch mode and layout engine (`elk`, `dagre`, `tala`) are switches in the UI — the user flips them, you do not need flags for them. Add `&sketch=true` / `&layout=dagre` to a URL only if the user explicitly asks for that view.

## Common mistakes

- **Running `d2-live file.d2` as a normal foreground command.** The first invocation _becomes_ the server and blocks until killed — your shell call hangs until it times out. Use the helper script; if you must call the binary directly, run it in the background.
- **Re-running the CLI after each edit.** Every direct invocation opens another browser tab. The watcher already handles reloads.
- **POSTing a relative path to `/open`.** The server resolves relative paths against _its own_ working directory, not yours, so the file silently ends up wrong or unregistered. Always absolute — the helper does this for you.
- **Starting a second server.** If a healthy one is running, its port is the one the user's tab is on; registering a file with a different server means the user never sees it. The helper checks `/healthz` before starting anything.
- **Killing the server or passing `--idle-timeout 0`.** Leave it alone; it exits on its own after 10 minutes with no connected tab.
- **Using `--lsp`.** That mode is for editors (Helix and friends) attaching d2-live as a language server. It is not for you.
- **Reporting success on a diagram that does not compile.** See step 6.

## Red flags

- "I'll just re-run `d2-live` so the preview picks up my change" → it already did; you are spawning tabs.
- "The path is fine, the server will figure it out" → it will resolve it against the wrong cwd.
- "The preview page loaded, so the diagram is fine" → the page may be showing a D2 error card. Run the syntax check.
