package main

import (
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"html"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

//go:embed assets/favicon.png
var faviconPNG []byte

type previewApp struct {
	input       string
	layout      string
	sketch      bool
	browser     string
	noBrowser   bool
	mu          sync.RWMutex
	svg         string
	subscribers map[chan struct{}]struct{}
}

const debounceDelay = 150 * time.Millisecond

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.PageTitle}}</title>
  <link rel="icon" type="image/png" href="/favicon.png">
  <style>
    :root {
      color-scheme: dark;
      --bg: #111318;
      --panel: #181b22;
      --line: #2a2f3a;
      --text: #d7dae0;
      --muted: #8d95a5;
    }
    html, body {
      width: 100%;
      height: 100%;
      margin: 0;
      overflow: hidden;
      background: var(--bg);
      color: var(--text);
      font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    #hud {
      position: fixed;
      left: 20px;
      top: 20px;
      z-index: 2;
      width: max-content;
      height: max-content;
      background: transparent;
      overflow: visible;
      border-radius: 999px;
      font-size: 12px;
      line-height: 1.4;
    }
    #hud-body {
      position: absolute;
      left: -4px;
      top: -4px;
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      gap: 9px;
      padding: 12px 12px 12px 12px;
      border: 1px solid var(--line);
      border-radius: 20px;
      background: rgba(17, 19, 24, 0.58);
      box-shadow:
        0 12px 28px rgba(0, 0, 0, 0.28),
        inset 0 1px 0 rgba(255, 255, 255, 0.03);
      backdrop-filter: blur(14px) saturate(120%);
      width: max-content;
      white-space: nowrap;
      transform-origin: top left;
      transform: scale(0.92) translateY(0);
      opacity: 0;
      pointer-events: none;
      will-change: transform, opacity;
      transition:
        opacity 160ms ease,
        transform 160ms cubic-bezier(0.2, 0.8, 0.2, 1);
    }
    #hud.open #hud-body {
      opacity: 1;
      transform: scale(1);
      pointer-events: auto;
    }
    #hud-trigger {
      position: relative;
      left: 0;
      top: 0;
      width: 14px;
      height: 14px;
      border-radius: 999px;
      display: flex;
      align-items: center;
      justify-content: center;
      cursor: default;
      user-select: none;
      flex: 0 0 auto;
      overflow: visible;
      box-shadow: 0 0 0 1px rgba(255, 255, 255, 0.02);
      transition: opacity 120ms ease, transform 160ms ease;
    }
    #hud-trigger::after {
      content: "";
      width: 5px;
      height: 5px;
      border-radius: 50%;
      background: #8d95a5;
      box-shadow: 0 0 0 1px rgba(0, 0, 0, 0.18) inset;
    }
    #hud-trigger.connected::after {
      background: #64d18a;
    }
    #hud-trigger.disconnected::after {
      background: #e25555;
    }
    #hud-trigger.connecting::after {
      background: #e0b75f;
    }
    #hud.open #hud-trigger {
      opacity: 0;
      transform: scale(0.25);
    }
    #title {
      flex: 0 0 auto;
      font-weight: 600;
      white-space: nowrap;
      color: var(--text);
    }
    #actions {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 8px;
      width: 100%;
      max-width: 300px;
    }
    #actions button {
      border: 1px solid var(--line);
      background: #232833;
      color: var(--text);
      border-radius: 999px;
      padding: 6px 12px;
      font: inherit;
      font-size: 12px;
      cursor: pointer;
      transition: transform 120ms ease, background 120ms ease, border-color 120ms ease, color 120ms ease;
      white-space: nowrap;
      display: inline-flex;
      align-items: center;
      gap: 7px;
    }
    #actions button:hover {
      background: #2d3442;
      border-color: #3b4352;
    }
    #actions button:active {
      transform: translateY(1px);
    }
    #actions .primary {
      background: #31405a;
      border-color: #46506a;
    }
    #actions .primary:hover {
      background: #3a4b69;
    }
    .btn-icon {
      width: 13px;
      height: 13px;
      flex: 0 0 auto;
      fill: currentColor;
      opacity: 0.95;
    }
    #meta {
      display: flex;
      align-items: center;
      gap: 8px;
      color: var(--muted);
      font-size: 11px;
      letter-spacing: 0.02em;
    }
    #meta .dot {
      width: 6px;
      height: 6px;
      border-radius: 999px;
      background: #64d18a;
      box-shadow: 0 0 0 1px rgba(0, 0, 0, 0.18) inset;
    }
    #viewer {
      width: 100vw;
      height: 100vh;
      overflow: hidden;
      cursor: grab;
      user-select: none;
      touch-action: none;
      position: relative;
    }
    #viewer.dragging {
      cursor: grabbing;
    }
    #scene {
      width: 100%;
      height: 100%;
      display: block;
    }
    #scene svg {
      background: white;
    }
    #png-loader {
      position: absolute;
      inset: 0;
      display: grid;
      place-items: center;
      pointer-events: none;
      opacity: 0;
      transition: opacity 150ms ease;
      z-index: 4;
    }
    #png-loader.visible {
      opacity: 1;
    }
    #png-loader .card {
      display: inline-flex;
      align-items: center;
      gap: 10px;
      padding: 10px 14px;
      border: 1px solid rgba(255, 255, 255, 0.12);
      border-radius: 999px;
      background: rgba(17, 19, 24, 0.58);
      backdrop-filter: blur(14px) saturate(120%);
      box-shadow: 0 12px 28px rgba(0, 0, 0, 0.28);
      color: var(--text);
      font-size: 12px;
    }
    #png-loader .spinner {
      width: 14px;
      height: 14px;
      border-radius: 999px;
      border: 2px solid rgba(255, 255, 255, 0.18);
      border-top-color: rgba(255, 255, 255, 0.75);
      animation: spin 0.85s linear infinite;
    }
    @keyframes spin {
      to { transform: rotate(360deg); }
    }
    #toast {
      position: fixed;
      left: 50%;
      top: 50%;
      z-index: 5;
      padding: 10px 14px;
      border-radius: 999px;
      background: rgba(17, 19, 24, 0.58);
      color: var(--text);
      border: 1px solid rgba(255, 255, 255, 0.12);
      box-shadow: 0 12px 28px rgba(0, 0, 0, 0.28);
      backdrop-filter: blur(14px) saturate(120%);
      opacity: 0;
      transform: translateX(-50%) translateY(-50%) scale(0.96);
      pointer-events: none;
      transition: opacity 180ms ease, transform 180ms ease;
    }
    #toast.visible {
      opacity: 1;
      transform: translateX(-50%) translateY(-50%) scale(1);
    }
  </style>
</head>
<body>
  <div id="hud">
    <div id="hud-trigger" class="connecting" aria-label="connection status"></div>
    <div id="hud-body">
      <div id="title">{{.Title}}</div>
      <div id="meta"><span class="dot"></span><span>live preview</span></div>
      <div id="actions">
        <button id="copy-svg" class="primary" type="button">
          <svg class="btn-icon" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M3 2.75A1.75 1.75 0 0 1 4.75 1h4.5A1.75 1.75 0 0 1 11 2.75V4h-1.5V2.75a.25.25 0 0 0-.25-.25h-4.5a.25.25 0 0 0-.25.25V4H3z"/>
            <path d="M5.75 5A1.75 1.75 0 0 0 4 6.75v6.5A1.75 1.75 0 0 0 5.75 15h6.5A1.75 1.75 0 0 0 14 13.25v-6.5A1.75 1.75 0 0 0 12.25 5zm0 1.5h6.5a.25.25 0 0 1 .25.25v6.5a.25.25 0 0 1-.25.25h-6.5a.25.25 0 0 1-.25-.25v-6.5a.25.25 0 0 1 .25-.25"/>
          </svg>
          <span>copy SVG</span>
        </button>
        <button id="copy-png" type="button">
          <svg class="btn-icon" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M3 2.75A1.75 1.75 0 0 1 4.75 1h4.5A1.75 1.75 0 0 1 11 2.75V4h-1.5V2.75a.25.25 0 0 0-.25-.25h-4.5a.25.25 0 0 0-.25.25V4H3z"/>
            <path d="M5.75 5A1.75 1.75 0 0 0 4 6.75v6.5A1.75 1.75 0 0 0 5.75 15h6.5A1.75 1.75 0 0 0 14 13.25v-6.5A1.75 1.75 0 0 0 12.25 5zm0 1.5h6.5a.25.25 0 0 1 .25.25v6.5a.25.25 0 0 1-.25.25h-6.5a.25.25 0 0 1-.25-.25v-6.5a.25.25 0 0 1 .25-.25"/>
          </svg>
          <span>copy PNG</span>
        </button>
        <button id="download-svg" type="button">
          <svg class="btn-icon" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M8 1.75a.75.75 0 0 1 .75.75v6.69l1.22-1.22a.75.75 0 1 1 1.06 1.06l-2.5 2.5a.75.75 0 0 1-1.06 0l-2.5-2.5a.75.75 0 1 1 1.06-1.06L7.25 9.19V2.5A.75.75 0 0 1 8 1.75"/>
            <path d="M3.75 13a.75.75 0 0 1 .75-.75h7a.75.75 0 0 1 0 1.5h-7a.75.75 0 0 1-.75-.75"/>
          </svg>
          <span>download SVG</span>
        </button>
        <button id="download-png" type="button">
          <svg class="btn-icon" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M8 1.75a.75.75 0 0 1 .75.75v6.69l1.22-1.22a.75.75 0 1 1 1.06 1.06l-2.5 2.5a.75.75 0 0 1-1.06 0l-2.5-2.5a.75.75 0 1 1 1.06-1.06L7.25 9.19V2.5A.75.75 0 0 1 8 1.75"/>
            <path d="M3.75 13a.75.75 0 0 1 .75-.75h7a.75.75 0 0 1 0 1.5h-7a.75.75 0 0 1-.75-.75"/>
          </svg>
          <span>download PNG</span>
        </button>
      </div>
    </div>
  </div>
  <div id="viewer">
    <div id="scene">{{.SVG}}</div>
    <div id="png-loader" aria-hidden="true">
      <div class="card">
        <div class="spinner"></div>
        <div>Rendering PNG</div>
      </div>
    </div>
  </div>
  <div id="toast" aria-live="polite"></div>
  <script src="https://unpkg.com/panzoom@9.4.0/dist/panzoom.min.js"></script>
  <script>
    const stateKey = {{printf "%q" .StateKey}};
    const fileName = {{printf "%q" .FileName}};
    const hud = document.getElementById('hud');
    const viewer = document.getElementById('viewer');
    const scene = document.getElementById('scene');
    const connection = document.getElementById('hud-trigger');
    const toast = document.getElementById('toast');
    const pngLoader = document.getElementById('png-loader');
    const copySvgButton = document.getElementById('copy-svg');
    const copyPngButton = document.getElementById('copy-png');
    const downloadSvgButton = document.getElementById('download-svg');
    const downloadPngButton = document.getElementById('download-png');
    const saved = loadState();
    const instance = panzoom(scene, {
      initialX: saved ? saved.x : 0,
      initialY: saved ? saved.y : 0,
      initialZoom: saved ? saved.scale : 1,
      bounds: false,
      smoothScroll: false,
      transformOrigin: {x: 0.5, y: 0.5},
    });

    function loadState() {
      try {
        const raw = localStorage.getItem(stateKey);
        if (!raw) return null;
        return JSON.parse(raw);
      } catch (err) {
        return null;
      }
    }

    function saveState() {
      try {
        const transform = instance.getTransform();
        localStorage.setItem(stateKey, JSON.stringify(transform));
      } catch (err) {
        // ignore storage failures
      }
    }

    function setConnected(state) {
      connection.classList.remove('connecting', 'connected', 'disconnected');
      connection.classList.add(state);
    }

    let toastTimer = null;
    function setPngLoading(loading) {
      pngLoader.classList.toggle('visible', loading);
    }

    function showToast(message) {
      toast.textContent = message;
      toast.classList.add('visible');
      if (toastTimer) {
        clearTimeout(toastTimer);
      }
      toastTimer = setTimeout(() => {
        toast.classList.remove('visible');
      }, 1200);
    }

    instance.on('panstart', () => {
      viewer.classList.add('dragging');
    });
    instance.on('panend', () => {
      viewer.classList.remove('dragging');
      saveState();
    });
    instance.on('zoomend', saveState);

    let hudTimer = null;
    function openHud() {
      if (hudTimer) {
        clearTimeout(hudTimer);
        hudTimer = null;
      }
      hud.classList.add('open');
    }

    function closeHudSoon() {
      if (!hud.classList.contains('open')) {
        return;
      }
      if (hudTimer) {
        clearTimeout(hudTimer);
      }
      hudTimer = setTimeout(() => {
        hud.classList.remove('open');
      }, 120);
    }

    hud.addEventListener('mouseenter', openHud);
    hud.addEventListener('mouseleave', closeHudSoon);
    hud.addEventListener('focusin', openHud);
    hud.addEventListener('focusout', closeHudSoon);

    function currentSvgText() {
      return scene.querySelector('svg') ? scene.querySelector('svg').outerHTML : '';
    }

    function currentSvgAsset() {
      const svgText = currentSvgText();
      if (!svgText) {
        return null;
      }
      const safeName = fileName.endsWith('.svg') ? fileName : fileName + '.svg';
      return {
        file: new File([svgText], safeName, {type: 'image/svg+xml'}),
        text: svgText,
      };
    }

    async function copySvg() {
      const svgAsset = currentSvgAsset();
      if (!svgAsset) {
        showToast('nothing to copy');
        return;
      }

      try {
        if (window.ClipboardItem && ClipboardItem.supports && ClipboardItem.supports('image/svg+xml')) {
          await navigator.clipboard.write([new ClipboardItem({
            'image/svg+xml': svgAsset.file,
            'text/plain': new Blob([svgAsset.text], {type: 'text/plain'}),
          })]);
        } else {
          await navigator.clipboard.writeText(svgAsset.text);
        }
        showToast('copied SVG file');
      } catch (err) {
        showToast('copy SVG failed');
      }
    }

    function downloadBlob(blob, name) {
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = name;
      anchor.rel = 'noopener';
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    }

    function currentPngName() {
      return fileName.replace(/\.[^.]+$/, '.png');
    }

    async function fetchPngBlob() {
      const response = await fetch('/png?ts=' + Date.now());
      if (!response.ok) {
        throw new Error('png fetch failed');
      }
      return await response.blob();
    }

    async function downloadSvg() {
      const svgAsset = currentSvgAsset();
      if (!svgAsset) {
        showToast('nothing to download');
        return;
      }
      downloadBlob(svgAsset.file, svgAsset.file.name);
      showToast('downloaded SVG');
    }

    async function copyPng() {
      setPngLoading(true);
      try {
        const png = await fetchPngBlob();
        await navigator.clipboard.write([new ClipboardItem({'image/png': png})]);
        showToast('copied PNG');
      } catch (err) {
        showToast('copy PNG failed');
      } finally {
        setPngLoading(false);
      }
    }

    async function downloadPng() {
      setPngLoading(true);
      try {
        const png = await fetchPngBlob();
        downloadBlob(png, currentPngName());
        showToast('downloaded PNG');
      } catch (err) {
        showToast('download PNG failed');
      } finally {
        setPngLoading(false);
      }
    }

    copySvgButton.addEventListener('click', copySvg);
    copyPngButton.addEventListener('click', copyPng);
    downloadSvgButton.addEventListener('click', downloadSvg);
    downloadPngButton.addEventListener('click', downloadPng);

    async function reloadSvg() {
      try {
        const response = await fetch('/svg?ts=' + Date.now());
        const svg = await response.text();
        scene.innerHTML = svg;
      } catch (err) {
        showToast('update failed');
      }
    }

    function connect() {
      const events = new EventSource('/events');
      events.onmessage = (event) => {
        if (event.data === 'reload') {
          reloadSvg();
        }
      };
      events.onopen = () => {
        setConnected('connected');
      };
      events.onerror = () => {
        setConnected('disconnected');
        events.close();
        setTimeout(connect, 1000);
      };
    }

    setConnected('connecting');
    saveState();
    connect();
  </script>
</body>
</html>`))

func main() {
	var layout string
	var port int
	var browser string
	var noBrowser bool
	var sketch bool

	flag.StringVar(&layout, "layout", "elk", "D2 layout engine")
	flag.IntVar(&port, "port", 0, "Server port")
	flag.StringVar(&browser, "browser", "", "Browser command")
	flag.BoolVar(&noBrowser, "no-browser", false, "Do not open browser automatically")
	flag.BoolVar(&sketch, "sketch", false, "Enable sketch mode")
	flag.Parse()

	if flag.NArg() != 1 {
		log.Fatalf("usage: d2-live [--layout LAYOUT] [--port PORT] [--browser BROWSER] [--no-browser] [--sketch] <input.d2>")
	}

	input, err := filepath.Abs(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}

	app := &previewApp{
		input:       input,
		layout:      layout,
		sketch:      sketch,
		browser:     browser,
		noBrowser:   noBrowser,
		subscribers: map[chan struct{}]struct{}{},
	}

	if err := app.render(); err != nil {
		log.Printf("initial render failed: %v", err)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		log.Fatal(err)
	}

	go app.watch()

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/favicon.png", handleFavicon)
	mux.HandleFunc("/svg", app.handleSVG)
	mux.HandleFunc("/png", app.handlePNG)
	mux.HandleFunc("/events", app.handleEvents)

	server := &http.Server{Handler: mux}
	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	url := "http://" + ln.Addr().String()
	log.Printf("preview at %s", url)
	if !app.noBrowser {
		if err := openBrowser(app.browser, url); err != nil {
			log.Printf("open browser: %v", err)
		}
	}

	select {}
}

func (a *previewApp) render() error {
	args := []string{a.input, "--stdout-format", "svg", "--layout", a.layout}
	if a.sketch {
		args = append(args, "--sketch")
	}
	args = append(args, "-")

	cmd := exec.Command("d2", args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		a.setSVG(errorSVG(fmt.Sprintf("d2 render failed: %s", strings.TrimSpace(stderr.String()))))
		return err
	}

	a.setSVG(string(sanitizeSVG(stdout.Bytes())))
	a.broadcast()
	return nil
}

func (a *previewApp) renderPNG() ([]byte, error) {
	dir, err := os.MkdirTemp("", "d2-live-png-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	out := filepath.Join(dir, "diagram.png")
	args := []string{a.input, out, "--layout", a.layout}
	if a.sketch {
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

func (a *previewApp) watch() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("watcher: %v", err)
		return
	}
	defer watcher.Close()

	inputDir := filepath.Dir(a.input)
	if err := watcher.Add(inputDir); err != nil {
		log.Printf("watch dir: %v", err)
		return
	}
	if err := watcher.Add(a.input); err != nil {
		log.Printf("watch file: %v", err)
	}

	var timer *time.Timer
	schedule := func() {
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(debounceDelay, func() {
			if err := a.render(); err != nil {
				log.Printf("render: %v", err)
			}
		})
	}

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(event.Name) == a.input || filepath.Dir(event.Name) == inputDir {
				schedule()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("watch error: %v", err)
		}
	}
}

func (a *previewApp) handleIndex(w http.ResponseWriter, r *http.Request) {
	svg := a.currentSVG()
	baseName := filepath.Base(a.input)
	displayName := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, map[string]any{
		"Title":     "d2-live — " + displayName,
		"PageTitle": "d2-live — " + displayName,
		"FileName":  baseName,
		"SVG":       template.HTML(svg),
		"StateKey":  "d2-live:" + a.input + ":" + strconv.FormatBool(a.sketch),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *previewApp) handleSVG(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	_, _ = w.Write([]byte(a.currentSVG()))
}

func handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(faviconPNG)
}

func (a *previewApp) handlePNG(w http.ResponseWriter, r *http.Request) {
	png, err := a.renderPNG()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

func (a *previewApp) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan struct{}, 1)
	a.addSubscriber(ch)
	defer a.removeSubscriber(ch)

	_, _ = io.WriteString(w, "retry: 1000\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			_, _ = io.WriteString(w, "data: reload\n\n")
			flusher.Flush()
		}
	}
}

func (a *previewApp) setSVG(svg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.svg = svg
}

func (a *previewApp) currentSVG() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.svg == "" {
		return errorSVG("no SVG yet")
	}
	return a.svg
}

func (a *previewApp) addSubscriber(ch chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.subscribers[ch] = struct{}{}
}

func (a *previewApp) removeSubscriber(ch chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.subscribers, ch)
}

func (a *previewApp) broadcast() {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for ch := range a.subscribers {
		select {
		case ch <- struct{}{}:
		default:
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

func openBrowser(browser, url string) error {
	if browser != "" {
		return exec.Command(browser, url).Start()
	}

	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
