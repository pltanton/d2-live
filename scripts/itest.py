#!/usr/bin/env python3
"""End-to-end check of d2-live registration lifecycle against a real binary."""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request

BIN = sys.argv[1]
state = tempfile.mkdtemp(prefix="d2-live-itest-state-")
work = tempfile.mkdtemp(prefix="d2-live-itest-work-")
env = dict(os.environ, XDG_RUNTIME_DIR=state)

diagram = os.path.join(work, "one.d2")
other = os.path.join(work, "two.d2")
for p in (diagram, other):
    with open(p, "w") as fh:
        fh.write("a -> b\n")

failures = []


def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + ((" — " + detail) if detail else ""))
    if not ok:
        failures.append(name)


def rpc(proc, method, params, msg_id=None):
    msg = {"jsonrpc": "2.0", "method": method, "params": params}
    if msg_id is not None:
        msg["id"] = msg_id
    body = json.dumps(msg).encode()
    proc.stdin.write(b"Content-Length: %d\r\n\r\n" % len(body) + body)
    proc.stdin.flush()


def port():
    for _ in range(100):
        try:
            with open(os.path.join(state, "d2-live", "server.json")) as fh:
                p = json.load(fh)["port"]
            urllib.request.urlopen("http://127.0.0.1:%d/healthz" % p, timeout=1).read()
            return p
        except Exception:
            time.sleep(0.1)
    raise SystemExit("server never came up; state=%s" % state)


def files(p):
    with urllib.request.urlopen("http://127.0.0.1:%d/files" % p, timeout=2) as r:
        return sorted(f["abs"] for f in json.load(r))


def wait_files(p, want, timeout=3.0):
    deadline = time.time() + timeout
    while True:
        got = files(p)
        if got == sorted(want) or time.time() > deadline:
            return got
        time.sleep(0.05)


lsp = subprocess.Popen(
    [BIN, "--lsp", "--no-browser"],
    stdin=subprocess.PIPE,
    stdout=subprocess.DEVNULL,
    stderr=subprocess.DEVNULL,
    env=env,
)
try:
    rpc(lsp, "initialize", {"capabilities": {}}, msg_id=1)
    p = port()

    # 1. didOpen registers, didClose unregisters (the editor-driven lifecycle).
    rpc(lsp, "textDocument/didOpen", {"textDocument": {"uri": "file://" + diagram}})
    check("lsp didOpen registers", wait_files(p, [diagram]) == [diagram])
    rpc(lsp, "textDocument/didClose", {"textDocument": {"uri": "file://" + diagram}})
    check("lsp didClose unregisters", wait_files(p, []) == [])

    # 2. A second invocation joins the running server instead of starting one.
    add = subprocess.run(
        [BIN, "--no-browser", other], env=env, capture_output=True, text=True, timeout=15
    )
    joined = "connected to server at http://127.0.0.1:%d" % p in add.stderr + add.stdout
    check("second invocation reuses the server", joined, (add.stderr or add.stdout).strip())
    check("registered via CLI", wait_files(p, [other]) == [other])

    # 3. --close unregisters without touching the file.
    closed = subprocess.run(
        [BIN, "--close", other], env=env, capture_output=True, text=True, timeout=15
    )
    check("--close reports one file", "unregistered 1 file(s)" in closed.stderr + closed.stdout,
          (closed.stderr or closed.stdout).strip())
    check("--close unregisters", wait_files(p, []) == [])
    check("--close leaves the file on disk", os.path.exists(other))

    # 4. Deleting a diagram prunes it (real fsnotify watcher, not a synthetic event).
    subprocess.run([BIN, "--no-browser", diagram], env=env, capture_output=True, timeout=15)
    check("re-registered for prune test", wait_files(p, [diagram]) == [diagram])
    os.remove(diagram)
    check("deleted file is pruned", wait_files(p, []) == [])

    # 5. A stale link falls back to a file that still exists instead of erroring.
    subprocess.run([BIN, "--no-browser", other], env=env, capture_output=True, timeout=15)
    wait_files(p, [other])
    with urllib.request.urlopen(
        "http://127.0.0.1:%d/?file=%s" % (p, os.path.join(work, "vanished.d2")), timeout=5
    ) as r:
        page = r.read().decode()
    check("stale link falls back", other in page and "vanished.d2" not in page)
finally:
    lsp.stdin.close()
    lsp.wait(timeout=10)
    shutil.rmtree(state, ignore_errors=True)
    shutil.rmtree(work, ignore_errors=True)

print()
print("FAILED: %s" % ", ".join(failures) if failures else "all checks passed")
sys.exit(1 if failures else 0)
