package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func writeD2(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("a -> b\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func fsEventRemove(path string) fsnotify.Event {
	return fsnotify.Event{Name: path, Op: fsnotify.Remove}
}

func newTestServer(t *testing.T) *server {
	t.Helper()
	s := newServer("elk", 0)
	t.Cleanup(func() { s.watcher.Close() })
	return s
}

func registered(t *testing.T, s *server) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/files", nil)
	rec := httptest.NewRecorder()
	s.handleFiles(rec, req)
	var out []fileMeta
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode /files: %v", err)
	}
	paths := make([]string, 0, len(out))
	for _, f := range out {
		paths = append(paths, f.Abs)
	}
	return paths
}

func post(t *testing.T, h http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// A file registered through /open must be removable through /close, so tabs and
// editors can drop diagrams they are done with instead of accumulating them in
// the file dropdown forever.
func TestCloseUnregistersFile(t *testing.T) {
	dir := t.TempDir()
	file := writeD2(t, dir, "diagram.d2")
	s := newTestServer(t)

	if rec := post(t, s.handleOpen, "/open", `{"path":"`+file+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("/open: got %d, want 200", rec.Code)
	}
	if got := registered(t, s); len(got) != 1 || got[0] != file {
		t.Fatalf("after /open: %v, want [%s]", got, file)
	}

	if rec := post(t, s.handleClose, "/close", `{"path":"`+file+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("/close: got %d, want 200", rec.Code)
	}
	if got := registered(t, s); len(got) != 0 {
		t.Fatalf("after /close: %v, want []", got)
	}
}

// Closing a file must also release everything hung off it: cached renders, the
// debounce timer, and the watches on the file and (once no file needs it) its
// directory.
func TestCloseReleasesFileState(t *testing.T) {
	dir := t.TempDir()
	one := writeD2(t, dir, "one.d2")
	two := writeD2(t, dir, "two.d2")
	s := newTestServer(t)

	s.register(one)
	s.register(two)
	s.mu.Lock()
	s.cache[cacheKey(one, false, "elk")] = "<svg/>"
	s.cache[cacheKey(two, false, "elk")] = "<svg/>"
	s.mu.Unlock()

	s.unregister(one)

	s.mu.Lock()
	_, cachedOne := s.cache[cacheKey(one, false, "elk")]
	_, cachedTwo := s.cache[cacheKey(two, false, "elk")]
	dirRefs := s.watchedDirs[dir]
	_, timerLeft := s.debounce[one]
	s.mu.Unlock()

	if cachedOne {
		t.Error("cached render for the closed file was kept")
	}
	if !cachedTwo {
		t.Error("cached render for the still-open file was dropped")
	}
	if timerLeft {
		t.Error("debounce timer for the closed file was kept")
	}
	if dirRefs != 1 {
		t.Errorf("watchedDirs[%s] = %d, want 1 (two.d2 still needs it)", dir, dirRefs)
	}

	s.unregister(two)
	s.mu.Lock()
	dirRefs = s.watchedDirs[dir]
	s.mu.Unlock()
	if dirRefs != 0 {
		t.Errorf("watchedDirs[%s] = %d after closing every file, want 0", dir, dirRefs)
	}
}

// Deleting a diagram on disk must drop it from the server too, so the dropdown
// does not fill up with entries that can no longer render.
func TestDeletedFileIsPruned(t *testing.T) {
	dir := t.TempDir()
	file := writeD2(t, dir, "gone.d2")
	s := newTestServer(t)
	s.register(file)

	if err := os.Remove(file); err != nil {
		t.Fatalf("remove: %v", err)
	}
	s.handleFsEvent(fsEventRemove(file))

	deadline := time.Now().Add(2 * time.Second)
	for {
		if len(registered(t, s)) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("deleted file still registered: %v", registered(t, s))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A rename-over-the-target save (write temp, rename into place) must NOT be
// mistaken for a deletion: the path still exists afterwards.
func TestRenameSaveKeepsFileRegistered(t *testing.T) {
	dir := t.TempDir()
	file := writeD2(t, dir, "saved.d2")
	s := newTestServer(t)
	s.register(file)

	tmp := writeD2(t, dir, "saved.d2.tmp")
	if err := os.Rename(tmp, file); err != nil {
		t.Fatalf("rename: %v", err)
	}
	s.handleFsEvent(fsEventRemove(file))

	time.Sleep(debounceDelay + 200*time.Millisecond)
	if got := registered(t, s); len(got) != 1 {
		t.Fatalf("after rename-save: %v, want the file to still be registered", got)
	}
}

// A tab pointed at a file that no longer exists must fall back to a file the
// server still has, instead of rendering a d2 failure card.
func TestIndexFallsBackWhenFileIsGone(t *testing.T) {
	dir := t.TempDir()
	alive := writeD2(t, dir, "alive.d2")
	s := newTestServer(t)
	s.register(alive)

	req := httptest.NewRequest(http.MethodGet, "/?file="+filepath.Join(dir, "vanished.d2"), nil)
	rec := httptest.NewRecorder()
	s.handleIndex(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("index: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, alive) {
		t.Errorf("index did not fall back to %s", alive)
	}
	if strings.Contains(body, "vanished.d2") {
		t.Errorf("index still points at the vanished file")
	}
}

// The single-shared-server guarantee rests on the flock outliving garbage
// collection. os.File has a finalizer that closes the fd, which releases the
// flock — so a lock the caller does not keep reachable is silently dropped and
// the next invocation becomes a second server. acquireOrConnect must own the
// lock itself rather than hand it back for the caller to hold (or forget).
func TestServerLockSurvivesGarbageCollection(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	ln, _, isServer, err := acquireOrConnect(0)
	if err != nil {
		t.Fatalf("acquireOrConnect: %v", err)
	}
	if !isServer {
		t.Fatal("acquireOrConnect: found an existing server in an isolated state dir")
	}
	t.Cleanup(func() { ln.Close() })

	for i := 0; i < 3; i++ {
		runtime.GC()
	}

	other, ok, err := acquireLock(lockPath())
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	if ok {
		other.Close()
		t.Fatal("the server's lock was released by GC: a second server could start")
	}
}
