package main

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

//go:embed assets/favicon.png
var faviconPNG []byte

const debounceDelay = 150 * time.Millisecond

// layouts offered in the UI layout selector.
var layouts = []string{"elk", "dagre", "tala"}

type fileMeta struct {
	Abs  string `json:"abs"`
	Base string `json:"basename"`
}

type fileEntry struct {
	abs  string
	base string
	subs map[chan string]struct{}
}

type server struct {
	layout string
	idle   time.Duration

	watcher *fsnotify.Watcher

	mu          sync.Mutex
	files       map[string]*fileEntry
	cache       map[string]rendered
	watchedDirs map[string]int
	debounce    map[string]*time.Timer
	seen        map[string]string
	lastGood    map[string]string
	inflight    map[string]*flight

	// writeMu serialises PUT /source so the stale check and the write are atomic.
	writeMu sync.Mutex

	subMu     sync.Mutex
	total     int
	idleSince time.Time
}

type serverInfo struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

//go:embed ui
var uiFS embed.FS

var pageTemplate = template.Must(template.ParseFS(uiFS, "ui/index.html"))

const version = "0.3.0"

func main() {
	var layout string
	var port int
	var browser string
	var noBrowser bool
	var sketch bool
	var lsp bool
	var closeFiles bool
	var idle time.Duration

	flag.StringVar(&layout, "layout", "elk", "D2 layout engine")
	flag.IntVar(&port, "port", 0, "Server port (0 = auto)")
	flag.StringVar(&browser, "browser", "", "Browser command")
	flag.BoolVar(&noBrowser, "no-browser", false, "Do not open browser automatically")
	flag.BoolVar(&sketch, "sketch", false, "Enable sketch mode")
	flag.BoolVar(&lsp, "lsp", false, "Run as a Language Server over stdio (for editor integration)")
	flag.BoolVar(&closeFiles, "close", false, "Unregister the file (or directory) from the running server and exit")
	flag.DurationVar(&idle, "idle-timeout", 10*time.Minute, "Exit after this long with no connected tabs (0 = never)")
	flag.Parse()

	log.SetFlags(0)

	if lsp {
		runLSP(layout, sketch, browser, noBrowser, port, idle)
		return
	}

	if flag.NArg() != 1 {
		log.Fatalf("usage: d2-live [--layout L] [--port P] [--browser B] [--no-browser] [--sketch] [--idle-timeout DUR] <input.d2 | dir>\n       d2-live --close <input.d2 | dir>   (unregister from the running server)\n       d2-live --lsp   (language-server mode for editors)")
	}

	if closeFiles {
		runClose(flag.Arg(0))
		return
	}

	files, err := resolveInputs(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}

	runCLI(files, layout, sketch, browser, noBrowser, port, idle)
}

// runClose unregisters files from the running server and returns. It never
// starts a server: with nothing running there is nothing to unregister. Paths
// that no longer exist are accepted, so a deleted diagram can be dropped too.
func runClose(arg string) {
	info, err := readServerInfo()
	if err != nil || !ping(info.Port) {
		log.Fatal("d2-live: no running server to unregister from")
	}

	abs, err := filepath.Abs(arg)
	if err != nil {
		log.Fatal(err)
	}

	files := []string{abs}
	if st, err := os.Stat(abs); err == nil && st.IsDir() {
		found, err := resolveInputs(abs)
		if err != nil {
			log.Fatal(err)
		}
		files = found
	}

	closed := 0
	for _, f := range files {
		if err := postClose(info.Port, f); err != nil {
			log.Printf("d2-live: %s: %v", filepath.Base(f), err)
			continue
		}
		closed++
	}
	log.Printf("d2-live: unregistered %d file(s) from http://127.0.0.1:%d", closed, info.Port)
}

// runCLI is the default, foreground behavior: become the shared preview server
// (and block, logging the URL) or, if one is already running, register the
// files with it, open a tab and return.
func runCLI(files []string, layout string, sketch bool, browser string, noBrowser bool, port int, idle time.Duration) {
	ln, srvPort, isServer, err := acquireOrConnect(port)
	if err != nil {
		log.Fatal(err)
	}

	if !isServer {
		for _, f := range files {
			if err := postOpen(srvPort, f, sketch, layout); err != nil {
				log.Fatal(err)
			}
		}
		log.Printf("d2-live: connected to server at http://127.0.0.1:%d (added %d file(s))", srvPort, len(files))
		maybeOpen(browser, noBrowser, srvPort, files[0], sketch, layout)
		return
	}

	defer releaseServerLock()
	defer os.Remove(infoPath())

	s := newServer(layout, idle)
	for _, f := range files {
		s.register(f)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		os.Remove(infoPath())
		log.Printf("\nd2-live: stopped")
		os.Exit(0)
	}()

	log.Printf("d2-live: serving at http://127.0.0.1:%d  (%d file(s), idle-timeout %s)", srvPort, len(files), idle)
	maybeOpen(browser, noBrowser, srvPort, files[0], sketch, layout)
	if err := s.start(ln); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// ---- client side ----

// resolveInputs turns the positional argument into the list of files to open.
// A regular file yields itself; a directory yields every *.d2 under it
// (recursively), sorted, so the first becomes the initial browser tab.
func resolveInputs(arg string) ([]string, error) {
	abs, err := filepath.Abs(arg)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{abs}, nil
	}

	var files []string
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip hidden directories (e.g. .git), but never the root itself.
			if path != abs && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".d2") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .d2 files found in %s", abs)
	}
	sort.Strings(files)
	return files, nil
}

func stateDir() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.Getenv("XDG_CACHE_HOME")
	}
	if base == "" {
		if home, err := os.UserCacheDir(); err == nil {
			base = home
		} else {
			base = os.TempDir()
		}
	}
	dir := filepath.Join(base, "d2-live")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func infoPath() string { return filepath.Join(stateDir(), "server.json") }
func lockPath() string { return filepath.Join(stateDir(), "server.lock") }

func readServerInfo() (serverInfo, error) {
	var info serverInfo
	b, err := os.ReadFile(infoPath())
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return info, err
	}
	if info.Port == 0 {
		return info, fmt.Errorf("no port recorded")
	}
	return info, nil
}

func writeServerInfo(info serverInfo) error {
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	tmp := infoPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, infoPath())
}

func ping(port int) bool {
	client := http.Client{Timeout: 300 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// serverLock holds the exclusive flock of the process that became the shared
// server. It is a package-level variable on purpose: os.File carries a
// finalizer that closes the fd, and closing the fd releases the flock. A lock
// kept only in a local variable the caller stops using can therefore be
// collected mid-run, silently unlocking the file and letting the next
// invocation start a *second* server. As a GC root, this variable cannot be
// collected for the lifetime of the process.
var serverLock *os.File

// releaseServerLock unlocks and forgets the server lock. Only the process that
// owns the lock should call it, on its way out.
func releaseServerLock() {
	if serverLock != nil {
		serverLock.Close()
		serverLock = nil
	}
}

// acquireOrConnect tries to become the single shared preview server. On success
// it stores the exclusive lock in serverLock and returns a bound listener and
// the chosen port with isServer=true. If another healthy server already holds
// the lock it returns that server's port with isServer=false (listener nil).
func acquireOrConnect(port int) (ln net.Listener, srvPort int, isServer bool, err error) {
	lock, locked, err := acquireLock(lockPath())
	if err != nil {
		return nil, 0, false, err
	}
	if !locked {
		// Another instance holds the lock; wait for it to publish a healthy
		// endpoint (it may still be starting up).
		deadline := time.Now().Add(3 * time.Second)
		for {
			if info, e := readServerInfo(); e == nil && ping(info.Port) {
				return nil, info.Port, false, nil
			}
			if time.Now().After(deadline) {
				return nil, 0, false, fmt.Errorf("a d2-live server holds the lock but never became ready")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		lock.Close()
		return nil, 0, false, fmt.Errorf("listen: %w", err)
	}
	srvPort = ln.Addr().(*net.TCPAddr).Port
	if err := writeServerInfo(serverInfo{Port: srvPort, PID: os.Getpid()}); err != nil {
		ln.Close()
		lock.Close()
		return nil, 0, false, err
	}
	serverLock = lock
	return ln, srvPort, true, nil
}

func previewURL(port int, file string, sketch bool, layout string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/?file=%s&sketch=%t&layout=%s",
		port, url.QueryEscape(file), sketch, url.QueryEscape(layout))
}

func maybeOpen(browser string, noBrowser bool, port int, file string, sketch bool, layout string) {
	if noBrowser {
		return
	}
	if err := openBrowser(browser, previewURL(port, file, sketch, layout)); err != nil {
		log.Printf("open browser: %v", err)
	}
}

func postOpen(port int, path string, sketch bool, layout string) error {
	body, err := json.Marshal(map[string]any{
		"path":   path,
		"sketch": sketch,
		"layout": layout,
	})
	if err != nil {
		return err
	}
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/open", port),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("open failed: %s", resp.Status)
	}
	return nil
}

func postClose(port int, path string) error {
	body, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		return err
	}
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/close", port),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("close failed: %s", resp.Status)
	}
	return nil
}

// ---- server side ----

// start wires the routes, kicks off the watch and idle goroutines, and serves
// on ln (blocking until the listener is closed).
func (s *server) start(ln net.Listener) error {
	go s.watchLoop()
	go s.idleMonitor()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/open", s.handleOpen)
	mux.HandleFunc("/close", s.handleClose)
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/favicon.png", handleFavicon)
	mux.Handle("/ui/", noCache(http.FileServer(http.FS(uiFS))))
	mux.HandleFunc("/svg", s.handleSVG)
	mux.HandleFunc("/png", s.handlePNG)
	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/files", s.handleFiles)
	mux.HandleFunc("/source", s.handleSource)
	mux.HandleFunc("/edit", handleEdit)
	mux.HandleFunc("/model", handleModel)

	return (&http.Server{Handler: mux}).Serve(ln)
}

// ---- LSP (language-server-over-stdio) mode ----

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type didOpenParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
}

// runLSP speaks just enough of the Language Server Protocol over stdio for an
// editor (e.g. Helix) to launch a live preview by attaching d2-live as a
// language server: on textDocument/didOpen it registers the file with the
// shared preview server and opens a browser tab. Reloads are handled by the
// server's file watcher, so saving the buffer is enough to refresh.
func runLSP(layout string, sketch bool, browser string, noBrowser bool, port int, idle time.Duration) {
	ln, srvPort, isServer, err := acquireOrConnect(port)
	if err != nil {
		log.Fatalf("d2-live lsp: %v", err)
	}
	if isServer {
		defer releaseServerLock()
		s := newServer(layout, idle)
		go func() {
			if e := s.start(ln); e != nil && e != http.ErrServerClosed {
				log.Printf("d2-live lsp: server: %v", e)
			}
		}()
		log.Printf("d2-live lsp: preview server at http://127.0.0.1:%d", srvPort)
	} else {
		log.Printf("d2-live lsp: using preview server at http://127.0.0.1:%d", srvPort)
	}

	opened := map[string]bool{}
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)

	for {
		msg, err := readRPC(reader)
		if err != nil {
			if err != io.EOF {
				log.Printf("d2-live lsp: read: %v", err)
			}
			return
		}

		switch msg.Method {
		case "initialize":
			writeRPCResult(writer, msg.ID, map[string]any{
				"capabilities": map[string]any{
					"textDocumentSync": map[string]any{"openClose": true, "change": 0},
				},
				"serverInfo": map[string]any{"name": "d2-live", "version": version},
			})
		case "shutdown":
			writeRPCResult(writer, msg.ID, nil)
		case "exit":
			if isServer {
				os.Remove(infoPath())
			}
			return
		case "textDocument/didOpen":
			var p didOpenParams
			if json.Unmarshal(msg.Params, &p) != nil {
				continue
			}
			path := uriToPath(p.TextDocument.URI)
			if path == "" || opened[path] {
				continue
			}
			opened[path] = true
			if err := postOpen(srvPort, path, sketch, layout); err != nil {
				log.Printf("d2-live lsp: open %s: %v", path, err)
				continue
			}
			maybeOpen(browser, noBrowser, srvPort, path, sketch, layout)
		case "textDocument/didClose":
			var p didOpenParams
			if json.Unmarshal(msg.Params, &p) != nil {
				continue
			}
			path := uriToPath(p.TextDocument.URI)
			if path == "" || !opened[path] {
				continue
			}
			delete(opened, path)
			// Closing the buffer takes the diagram out of the preview's file
			// list; reopening it registers it again.
			if err := postClose(srvPort, path); err != nil {
				log.Printf("d2-live lsp: close %s: %v", path, err)
			}
		}
	}
}

func readRPC(r *bufio.Reader) (*rpcMessage, error) {
	contentLength := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if rest, ok := cutPrefixFold(line, "content-length:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				return nil, fmt.Errorf("bad Content-Length: %w", err)
			}
			contentLength = n
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

func writeRPCResult(w *bufio.Writer, id json.RawMessage, result any) {
	if id == nil {
		return // notification: nothing to reply to
	}
	writeRPC(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeRPC(w *bufio.Writer, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		log.Printf("d2-live lsp: marshal: %v", err)
		return
	}
	fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body))
	w.Write(body)
	w.Flush()
}

func uriToPath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return ""
	}
	p := strings.TrimPrefix(uri, "file://")
	if decoded, err := url.PathUnescape(p); err == nil {
		p = decoded
	}
	return p
}

func acquireLock(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, false, nil
		}
		return nil, false, err
	}
	return f, true, nil
}

func newServer(layout string, idle time.Duration) *server {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("watcher: %v", err)
	}
	return &server{
		layout:      layout,
		idle:        idle,
		watcher:     watcher,
		files:       map[string]*fileEntry{},
		cache:       map[string]rendered{},
		watchedDirs: map[string]int{},
		debounce:    map[string]*time.Timer{},
		seen:        map[string]string{},
		lastGood:    map[string]string{},
		inflight:    map[string]*flight{},
		idleSince:   time.Now(),
	}
}

func cacheKey(file string, sketch bool, layout string) string {
	return file + "|" + strconv.FormatBool(sketch) + "|" + layout
}

func (s *server) register(abs string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.files[abs]; ok {
		return
	}
	s.files[abs] = &fileEntry{abs: abs, base: filepath.Base(abs), subs: map[chan string]struct{}{}}
	dir := filepath.Dir(abs)
	if s.watchedDirs[dir] == 0 {
		if err := s.watcher.Add(dir); err != nil {
			log.Printf("watch dir %s: %v", dir, err)
		}
	}
	s.watchedDirs[dir]++
	if err := s.watcher.Add(abs); err != nil {
		log.Printf("watch file %s: %v", abs, err)
	}
}

// unregister drops a file from the server: it stops watching it, releases its
// cached renders and any pending rerender, and nudges tabs that were showing it
// so they reload onto a file that is still open. Reports whether the file was
// registered in the first place.
func (s *server) unregister(abs string) bool {
	s.mu.Lock()
	entry, ok := s.files[abs]
	if !ok {
		s.mu.Unlock()
		return false
	}
	delete(s.files, abs)
	delete(s.seen, abs)

	if timer := s.debounce[abs]; timer != nil {
		timer.Stop()
		delete(s.debounce, abs)
	}

	prefix := abs + "|"
	for key := range s.cache {
		if strings.HasPrefix(key, prefix) {
			delete(s.cache, key)
		}
	}
	for key := range s.lastGood {
		if strings.HasPrefix(key, prefix) {
			delete(s.lastGood, key)
		}
	}

	dir := filepath.Dir(abs)
	if s.watchedDirs[dir] > 0 {
		s.watchedDirs[dir]--
		if s.watchedDirs[dir] == 0 {
			delete(s.watchedDirs, dir)
			// Best-effort: the watch may already be gone if the directory was
			// removed along with the file.
			_ = s.watcher.Remove(dir)
		}
	}
	_ = s.watcher.Remove(abs)
	s.mu.Unlock()

	s.subMu.Lock()
	for ch := range entry.subs {
		select {
		case ch <- "reload":
		default:
		}
	}
	s.subMu.Unlock()
	return true
}

func (s *server) fileList() []fileMeta {
	s.mu.Lock()
	out := make([]fileMeta, 0, len(s.files))
	for abs, e := range s.files {
		out = append(out, fileMeta{Abs: abs, Base: e.base})
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Base == out[j].Base {
			return out[i].Abs < out[j].Abs
		}
		return out[i].Base < out[j].Base
	})
	return out
}

type rendered struct {
	svg  string
	hash string
}

type flight struct {
	done chan struct{}
	svg  string
	err  error
}

// getSVG renders the file's current content, keyed by its hash so a render that
// raced a newer save is never served for it.
func (s *server) getSVG(file string, sketch bool, layout string) (svg, hash string, err error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return errorSVG("cannot read " + filepath.Base(file)), "", err
	}
	hash = contentHash(src)
	key := cacheKey(file, sketch, layout)
	s.mu.Lock()
	cached, ok := s.cache[key]
	s.mu.Unlock()
	if ok && cached.hash == hash {
		return cached.svg, hash, nil
	}

	fkey := key + "|" + hash
	s.mu.Lock()
	f, running := s.inflight[fkey]
	if !running {
		f = &flight{done: make(chan struct{})}
		s.inflight[fkey] = f
	}
	s.mu.Unlock()
	if running {
		<-f.done
		return f.svg, hash, f.err
	}

	f.svg, f.err = renderSVG(file, src, layout, sketch)
	s.mu.Lock()
	delete(s.inflight, fkey)
	if f.err == nil {
		s.cache[key] = rendered{svg: f.svg, hash: hash}
		s.lastGood[key] = f.svg
	}
	s.mu.Unlock()
	close(f.done)
	return f.svg, hash, f.err
}

func (s *server) invalidate(file string) {
	prefix := file + "|"
	s.mu.Lock()
	for key := range s.cache {
		if strings.HasPrefix(key, prefix) {
			delete(s.cache, key)
		}
	}
	s.mu.Unlock()
}

func (s *server) watchLoop() {
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			s.handleFsEvent(event)
		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("watch error: %v", err)
		}
	}
}

func (s *server) handleFsEvent(event fsnotify.Event) {
	clean := filepath.Clean(event.Name)
	dir := filepath.Dir(clean)

	s.mu.Lock()
	var affected []string
	for abs := range s.files {
		if abs == clean || filepath.Dir(abs) == dir {
			affected = append(affected, abs)
		}
	}
	s.mu.Unlock()

	for _, abs := range affected {
		s.scheduleRerender(abs)
	}
}

func (s *server) scheduleRerender(abs string) {
	s.mu.Lock()
	if timer := s.debounce[abs]; timer != nil {
		timer.Stop()
	}
	s.debounce[abs] = time.AfterFunc(debounceDelay, func() {
		// Editors often save by writing a temp file and renaming it over the
		// target, which looks like a removal. Once the dust has settled, a path
		// that is still missing really is gone: drop it instead of keeping a
		// dropdown entry that can no longer render.
		if _, err := os.Stat(abs); err != nil {
			s.unregister(abs)
			return
		}
		s.invalidate(abs)
		s.notify(abs, "reload")
		if b, err := os.ReadFile(abs); err == nil && s.sourceChanged(abs, contentHash(b)) {
			s.notify(abs, "source")
		}
	})
	s.mu.Unlock()
}

func (s *server) notify(abs, kind string) {
	s.mu.Lock()
	entry := s.files[abs]
	s.mu.Unlock()
	if entry == nil {
		return
	}
	s.subMu.Lock()
	for ch := range entry.subs {
		select {
		case ch <- kind:
		default:
		}
	}
	s.subMu.Unlock()
}

func (s *server) subscribe(abs string, ch chan string) bool {
	s.mu.Lock()
	entry := s.files[abs]
	s.mu.Unlock()
	if entry == nil {
		return false
	}
	s.subMu.Lock()
	entry.subs[ch] = struct{}{}
	s.total++
	s.subMu.Unlock()
	return true
}

func (s *server) unsubscribe(abs string, ch chan string) {
	s.mu.Lock()
	entry := s.files[abs]
	s.mu.Unlock()
	s.subMu.Lock()
	if entry != nil {
		delete(entry.subs, ch)
	}
	s.total--
	if s.total <= 0 {
		s.total = 0
		s.idleSince = time.Now()
	}
	s.subMu.Unlock()
}

func (s *server) idleMonitor() {
	if s.idle <= 0 {
		return
	}
	interval := min(max(s.idle/4, time.Second), 30*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		s.subMu.Lock()
		idle := s.total == 0 && time.Since(s.idleSince) >= s.idle
		s.subMu.Unlock()
		if idle {
			os.Remove(infoPath())
			log.Printf("idle for %s with no tabs; exiting", s.idle)
			os.Exit(0)
		}
	}
}

func renderSVG(file string, src []byte, layout string, sketch bool) (string, error) {
	if svg, ok, err := renderInProcess(file, src, layout, sketch); ok {
		if err != nil {
			return errorSVG("d2 render failed: " + err.Error()), err
		}
		return svg, nil
	}
	args := []string{"-", "--stdout-format", "svg", "--layout", layout}
	if sketch {
		args = append(args, "--sketch")
	}
	args = append(args, "-")

	cmd := exec.Command("d2", args...)
	cmd.Dir = filepath.Dir(file)
	cmd.Stdin = bytes.NewReader(src)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		return errorSVG("d2 render failed: " + msg), fmt.Errorf("%v: %s", err, msg)
	}
	return string(sanitizeSVG(stdout.Bytes())), nil
}

func renderPNG(file, layout string, sketch bool) ([]byte, error) {
	dir, err := os.MkdirTemp("", "d2-live-png-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	out := filepath.Join(dir, "diagram.png")
	args := []string{file, out, "--layout", layout}
	if sketch {
		args = append(args, "--sketch")
	}

	cmd := exec.Command("d2", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("d2 png render failed: %s", strings.TrimSpace(stderr.String()))
	}

	return os.ReadFile(out)
}

// ---- HTTP handlers ----

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path   string `json:"path"`
		Sketch bool   `json:"sketch"`
		Layout string `json:"layout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.register(abs)

	layout := req.Layout
	if layout == "" {
		layout = s.layout
	}
	// Warm the cache for the combo the new tab will request.
	go func() { _, _, _ = s.getSVG(abs, req.Sketch, layout) }()

	w.WriteHeader(http.StatusOK)
}

func (s *server) handleClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.unregister(abs) {
		http.Error(w, "not registered", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	file := q.Get("file")
	if file != "" {
		if _, err := os.Stat(file); err == nil {
			s.register(file)
		} else {
			// Stale link: the diagram was deleted or renamed away. Fall back to
			// a file the server still serves instead of rendering a d2 failure.
			file = ""
		}
	}

	files := s.fileList()
	if file == "" && len(files) > 0 {
		file = files[0].Abs
	}

	sketch := q.Get("sketch") == "true"
	layout := q.Get("layout")
	if layout == "" {
		layout = s.layout
	}

	var svg string
	if file == "" {
		svg = errorSVG("no file open")
	} else {
		svg, _, _ = s.getSVG(file, sketch, layout)
	}

	base := filepath.Base(file)
	display := strings.TrimSuffix(base, filepath.Ext(base))
	if file == "" {
		base = "no file"
		display = "no file"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, map[string]any{
		"PageTitle": "d2-live — " + display,
		"Base":      base,
		"File":      file,
		"Sketch":    sketch,
		"Layout":    layout,
		"Files":     files,
		"Layouts":   layouts,
		"SVG":       template.HTML(svg),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *server) handleSVG(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	file := q.Get("file")
	if file == "" {
		http.Error(w, "missing file", http.StatusBadRequest)
		return
	}
	layout := q.Get("layout")
	if layout == "" {
		layout = s.layout
	}
	sketch := q.Get("sketch") == "true"
	svg, hash, err := s.getSVG(file, sketch, layout)
	w.Header().Set("X-D2-Hash", hash)
	if err != nil && q.Get("edit") == "1" {
		// The editor shows the error next to the code; keep the diagram in place.
		s.mu.Lock()
		good, ok := s.lastGood[cacheKey(file, sketch, layout)]
		s.mu.Unlock()
		if ok {
			svg = good
		}
		w.Header().Set("X-D2-Error", strings.ReplaceAll(err.Error(), "\n", " "))
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	_, _ = w.Write([]byte(svg))
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

func handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(faviconPNG)
}

func (s *server) handlePNG(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	file := q.Get("file")
	if file == "" {
		http.Error(w, "missing file", http.StatusBadRequest)
		return
	}
	layout := q.Get("layout")
	if layout == "" {
		layout = s.layout
	}
	png, err := renderPNG(file, layout, q.Get("sketch") == "true")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

func (s *server) handleFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.fileList())
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	file := r.URL.Query().Get("file")
	if file == "" {
		http.Error(w, "missing file", http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(file); err == nil {
		s.register(file)
	}

	ch := make(chan string, 4)
	if !s.subscribe(file, ch) {
		http.Error(w, "unknown file", http.StatusNotFound)
		return
	}
	defer s.unsubscribe(file, ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	_, _ = io.WriteString(w, "retry: 1000\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case kind := <-ch:
			if kind == "source" {
				text, err := readText(file)
				if err != nil {
					continue
				}
				payload, _ := json.Marshal(map[string]string{"text": text, "hash": contentHash([]byte(text))})
				fmt.Fprintf(w, "event: source\ndata: %s\n\n", payload)
			} else {
				_, _ = io.WriteString(w, "data: reload\n\n")
			}
			flusher.Flush()
		}
	}
}

func sanitizeSVG(input []byte) []byte {
	svg := bytes.TrimSpace(input)
	if bytes.HasPrefix(svg, []byte("<?xml")) {
		if end := bytes.Index(svg, []byte("?>")); end >= 0 {
			svg = bytes.TrimSpace(svg[end+2:])
		}
	}
	return svg
}

func errorSVG(message string) string {
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 300">
<rect width="100%%" height="100%%" fill="#111318"/>
<text x="32" y="88" fill="#f7d794" font-family="monospace" font-size="28">d2-live</text>
<text x="32" y="142" fill="#d7dae0" font-family="monospace" font-size="22">%s</text>
</svg>`, html.EscapeString(message))
}

func openBrowser(browser, target string) error {
	if browser != "" {
		return exec.Command(browser, target).Start()
	}

	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target).Start()
	default:
		return exec.Command("xdg-open", target).Start()
	}
}
