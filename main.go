package main

import (
	"bufio"
	"bytes"
	_ "embed"
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
	subs map[chan struct{}]struct{}
}

type server struct {
	layout string
	idle   time.Duration

	watcher *fsnotify.Watcher

	mu          sync.Mutex
	files       map[string]*fileEntry
	cache       map[string]string
	watchedDirs map[string]int
	debounce    map[string]*time.Timer

	subMu     sync.Mutex
	total     int
	idleSince time.Time
}

type serverInfo struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

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
      --ground: #232136;
      --surface: #2a273f;
      --surface-2: #393552;
      --surface-3: #44415a;
      --text: #e0def4;
      --muted: #908caa;
      --primary: #c4a7e7;
      --on-primary: #232136;
      --foam: #9ccfd8;
      --gold: #f6c177;
      --love: #eb6f92;
      --r-pill: 999px;
      --r-panel: 28px;
      --r-control: 16px;
      --spring: cubic-bezier(0.34, 1.4, 0.5, 1);
      --emph: cubic-bezier(0.2, 0, 0, 1);
      --shadow: 0 10px 30px rgba(20, 18, 33, 0.32), 0 2px 8px rgba(20, 18, 33, 0.24);
    }
    html, body {
      width: 100%;
      height: 100%;
      margin: 0;
      overflow: hidden;
      background: var(--ground);
      color: var(--text);
      font-family: ui-rounded, "SF Pro Rounded", system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    #hud {
      position: fixed;
      left: 20px;
      top: 20px;
      z-index: 2;
      width: max-content;
      font-size: 13px;
      line-height: 1.45;
      color: var(--text);
    }
    /* collapsed: ghost pill — status dot + filename, quiet at rest */
    #pill {
      display: inline-flex;
      align-items: center;
      gap: 10px;
      padding: 8px 13px 8px 11px;
      border-radius: var(--r-pill);
      background: transparent;
      color: var(--muted);
      opacity: 0.8;
      cursor: default;
      user-select: none;
      transition: transform 240ms var(--spring), background 220ms var(--emph),
                  opacity 220ms var(--emph), box-shadow 220ms var(--emph), color 220ms var(--emph);
    }
    #hud:hover #pill, #hud.open #pill {
      background: var(--surface-2);
      color: var(--text);
      opacity: 1;
      box-shadow: var(--shadow);
    }
    /* disconnected: drop the shyness and warn */
    #hud.disconnected #pill {
      opacity: 1;
      color: var(--text);
      background: color-mix(in srgb, var(--love) 18%, var(--surface));
    }
    .dot {
      width: 11px;
      height: 11px;
      border-radius: 50%;
      flex: 0 0 auto;
      background: var(--muted);
      transition: background 200ms var(--emph), box-shadow 200ms var(--emph);
    }
    #hud.connecting .dot {
      background: var(--gold);
      box-shadow: 0 0 0 4px color-mix(in srgb, var(--gold) 20%, transparent);
    }
    #hud.connected .dot {
      background: var(--foam);
      box-shadow: 0 0 0 4px color-mix(in srgb, var(--foam) 20%, transparent);
    }
    #hud.disconnected .dot {
      background: var(--love);
      box-shadow: 0 0 0 4px color-mix(in srgb, var(--love) 24%, transparent);
    }
    .fname {
      font-weight: 500;
      letter-spacing: 0.01em;
      white-space: nowrap;
    }
    #panel {
      position: absolute;
      left: 0;
      top: calc(100% + 6px);
      width: 300px;
      padding: 18px;
      background: var(--surface);
      border-radius: var(--r-panel);
      box-shadow: var(--shadow);
      display: flex;
      flex-direction: column;
      gap: 16px;
      transform-origin: top left;
      opacity: 0;
      transform: scale(0.92) translateY(-6px);
      pointer-events: none;
      will-change: transform, opacity;
      transition: opacity 200ms var(--emph), transform 320ms var(--spring);
    }
    #hud.open #panel {
      opacity: 1;
      transform: none;
      pointer-events: auto;
    }
    #panel .head {
      display: flex;
      flex-direction: column;
      gap: 3px;
    }
    #panel .head .row1 {
      display: inline-flex;
      align-items: center;
      gap: 10px;
    }
    #title {
      font-size: 17px;
      font-weight: 700;
      letter-spacing: -0.01em;
      color: var(--text);
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      max-width: 240px;
    }
    #panel .sub {
      font-size: 11px;
      color: var(--muted);
      letter-spacing: 0.05em;
      text-transform: uppercase;
      font-weight: 600;
      margin-left: 21px;
    }
    #controls {
      display: flex;
      flex-direction: column;
      gap: 9px;
    }
    #controls .ctl {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 14px;
    }
    #controls .lbl {
      font-size: 12.5px;
      color: var(--muted);
      font-weight: 600;
    }
    #controls select {
      appearance: none;
      -webkit-appearance: none;
      border: none;
      color: var(--text);
      background-color: var(--surface-2);
      border-radius: var(--r-control);
      padding: 9px 34px 9px 14px;
      font: inherit;
      font-size: 13px;
      font-weight: 600;
      cursor: pointer;
      max-width: 178px;
      background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='16' height='16' viewBox='0 0 16 16'%3E%3Cpath fill='%23c4a7e7' d='M4.2 6.1a.9.9 0 0 1 1.27 0L8 8.63l2.53-2.53a.9.9 0 1 1 1.27 1.27L8.63 10.5a.9.9 0 0 1-1.27 0L4.2 7.37a.9.9 0 0 1 0-1.27z'/%3E%3C/svg%3E");
      background-repeat: no-repeat;
      background-position: right 12px center;
      transition: background-color 160ms var(--emph);
    }
    #controls select:hover {
      background-color: var(--surface-3);
    }
    #controls select:focus-visible {
      outline: 2px solid var(--primary);
      outline-offset: 2px;
    }
    .switch {
      position: relative;
      width: 54px;
      height: 32px;
      flex: 0 0 auto;
      cursor: pointer;
    }
    .switch input {
      position: absolute;
      opacity: 0;
      inset: 0;
      margin: 0;
      cursor: pointer;
    }
    .switch .track {
      position: absolute;
      inset: 0;
      background: var(--surface-3);
      border-radius: var(--r-pill);
      transition: background 240ms var(--emph);
    }
    .switch .thumb {
      position: absolute;
      top: 50%;
      left: 6px;
      width: 16px;
      height: 16px;
      border-radius: 50%;
      background: var(--muted);
      transform: translate(0, -50%);
      transition: transform 320ms var(--spring), width 320ms var(--spring),
                  height 320ms var(--spring), background 240ms var(--emph), left 320ms var(--spring);
    }
    .switch input:checked ~ .track {
      background: var(--primary);
    }
    .switch input:checked ~ .thumb {
      width: 24px;
      height: 24px;
      left: 4px;
      background: var(--on-primary);
      transform: translate(22px, -50%);
    }
    .switch input:focus-visible ~ .track {
      outline: 2px solid var(--primary);
      outline-offset: 2px;
    }
    #actions {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 8px;
    }
    #actions button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      gap: 8px;
      border: none;
      border-radius: var(--r-pill);
      padding: 11px 14px;
      font: inherit;
      font-size: 12.5px;
      font-weight: 700;
      letter-spacing: 0.01em;
      cursor: pointer;
      color: var(--text);
      background: var(--surface-2);
      white-space: nowrap;
      transition: transform 160ms var(--spring), filter 160ms var(--emph), background 160ms var(--emph);
    }
    #actions button:hover {
      background: var(--surface-3);
    }
    #actions button:active {
      transform: scale(0.96);
    }
    #actions .primary {
      background: var(--primary);
      color: var(--on-primary);
    }
    #actions .primary:hover {
      background: var(--primary);
      filter: brightness(1.06);
    }
    #actions button:focus-visible {
      outline: 2px solid var(--primary);
      outline-offset: 2px;
    }
    .btn-icon {
      width: 14px;
      height: 14px;
      flex: 0 0 auto;
      fill: currentColor;
      opacity: 0.9;
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
      gap: 12px;
      padding: 12px 18px 12px 14px;
      border-radius: var(--r-pill);
      background: var(--surface);
      box-shadow: var(--shadow);
      color: var(--text);
      font-size: 13px;
      font-weight: 600;
    }
    #png-loader .spinner {
      width: 18px;
      height: 18px;
      border-radius: 50%;
      border: 2.5px solid color-mix(in srgb, var(--primary) 26%, transparent);
      border-top-color: var(--primary);
      animation: spin 0.8s linear infinite;
    }
    @keyframes spin {
      to { transform: rotate(360deg); }
    }
    #toast {
      position: fixed;
      left: 50%;
      top: 50%;
      z-index: 5;
      display: inline-flex;
      align-items: center;
      gap: 10px;
      padding: 12px 18px;
      border-radius: 16px;
      background: var(--text);
      color: var(--ground);
      box-shadow: var(--shadow);
      font-size: 13px;
      font-weight: 700;
      opacity: 0;
      transform: translate(-50%, -50%) translateY(8px) scale(0.96);
      pointer-events: none;
      transition: opacity 200ms var(--emph), transform 320ms var(--spring);
    }
    #toast.visible {
      opacity: 1;
      transform: translate(-50%, -50%) scale(1);
    }
    @media (prefers-reduced-motion: reduce) {
      * { animation-duration: 0.01ms !important; transition-duration: 0.01ms !important; }
    }
  </style>
</head>
<body>
  <div id="hud" class="connecting">
    <div id="pill">
      <span class="dot"></span>
      <span class="fname" id="fname">{{.Base}}</span>
    </div>
    <div id="panel">
      <div class="head">
        <div class="row1"><span class="dot"></span><span id="title">{{.Base}}</span></div>
        <div class="sub">Live preview</div>
      </div>
      <div id="controls">
        <label class="ctl"><span class="lbl">File</span><select id="file-select">{{range .Files}}<option value="{{.Abs}}"{{if eq .Abs $.File}} selected{{end}}>{{.Base}}</option>{{end}}</select></label>
        <label class="ctl"><span class="lbl">Layout</span><select id="layout-select">{{range .Layouts}}<option value="{{.}}"{{if eq . $.Layout}} selected{{end}}>{{.}}</option>{{end}}</select></label>
        <label class="ctl"><span class="lbl">Sketch</span><span class="switch"><input type="checkbox" id="sketch-toggle"{{if .Sketch}} checked{{end}}><span class="track"></span><span class="thumb"></span></span></label>
      </div>
      <div id="actions">
        <button id="copy-svg" class="primary" type="button">
          <svg class="btn-icon" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M3 2.75A1.75 1.75 0 0 1 4.75 1h4.5A1.75 1.75 0 0 1 11 2.75V4h-1.5V2.75a.25.25 0 0 0-.25-.25h-4.5a.25.25 0 0 0-.25.25V4H3z"/>
            <path d="M5.75 5A1.75 1.75 0 0 0 4 6.75v6.5A1.75 1.75 0 0 0 5.75 15h6.5A1.75 1.75 0 0 0 14 13.25v-6.5A1.75 1.75 0 0 0 12.25 5zm0 1.5h6.5a.25.25 0 0 1 .25.25v6.5a.25.25 0 0 1-.25.25h-6.5a.25.25 0 0 1-.25-.25v-6.5a.25.25 0 0 1 .25-.25"/>
          </svg>
          <span>Copy SVG</span>
        </button>
        <button id="copy-png" type="button">
          <svg class="btn-icon" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M3 2.75A1.75 1.75 0 0 1 4.75 1h4.5A1.75 1.75 0 0 1 11 2.75V4h-1.5V2.75a.25.25 0 0 0-.25-.25h-4.5a.25.25 0 0 0-.25.25V4H3z"/>
            <path d="M5.75 5A1.75 1.75 0 0 0 4 6.75v6.5A1.75 1.75 0 0 0 5.75 15h6.5A1.75 1.75 0 0 0 14 13.25v-6.5A1.75 1.75 0 0 0 12.25 5zm0 1.5h6.5a.25.25 0 0 1 .25.25v6.5a.25.25 0 0 1-.25.25h-6.5a.25.25 0 0 1-.25-.25v-6.5a.25.25 0 0 1 .25-.25"/>
          </svg>
          <span>Copy PNG</span>
        </button>
        <button id="download-svg" type="button">
          <svg class="btn-icon" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M8 1.75a.75.75 0 0 1 .75.75v6.69l1.22-1.22a.75.75 0 1 1 1.06 1.06l-2.5 2.5a.75.75 0 0 1-1.06 0l-2.5-2.5a.75.75 0 1 1 1.06-1.06L7.25 9.19V2.5A.75.75 0 0 1 8 1.75"/>
            <path d="M3.75 13a.75.75 0 0 1 .75-.75h7a.75.75 0 0 1 0 1.5h-7a.75.75 0 0 1-.75-.75"/>
          </svg>
          <span>Download SVG</span>
        </button>
        <button id="download-png" type="button">
          <svg class="btn-icon" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M8 1.75a.75.75 0 0 1 .75.75v6.69l1.22-1.22a.75.75 0 1 1 1.06 1.06l-2.5 2.5a.75.75 0 0 1-1.06 0l-2.5-2.5a.75.75 0 1 1 1.06-1.06L7.25 9.19V2.5A.75.75 0 0 1 8 1.75"/>
            <path d="M3.75 13a.75.75 0 0 1 .75-.75h7a.75.75 0 0 1 0 1.5h-7a.75.75 0 0 1-.75-.75"/>
          </svg>
          <span>Download PNG</span>
        </button>
      </div>
    </div>
  </div>
  <div id="viewer">
    <div id="scene">{{.SVG}}</div>
    <div id="png-loader" aria-hidden="true">
      <div class="card">
        <div class="spinner"></div>
        <div>Rendering PNG…</div>
      </div>
    </div>
  </div>
  <div id="toast" aria-live="polite"></div>
  <script src="https://unpkg.com/panzoom@9.4.0/dist/panzoom.min.js"></script>
  <script>
    const state = {
      file: {{.File}},
      sketch: {{.Sketch}},
      layout: {{.Layout}},
    };
    const hud = document.getElementById('hud');
    const viewer = document.getElementById('viewer');
    const scene = document.getElementById('scene');
    const fnameEl = document.getElementById('fname');
    const toast = document.getElementById('toast');
    const pngLoader = document.getElementById('png-loader');
    const titleEl = document.getElementById('title');
    const fileSelect = document.getElementById('file-select');
    const layoutSelect = document.getElementById('layout-select');
    const sketchToggle = document.getElementById('sketch-toggle');
    const copySvgButton = document.getElementById('copy-svg');
    const copyPngButton = document.getElementById('copy-png');
    const downloadSvgButton = document.getElementById('download-svg');
    const downloadPngButton = document.getElementById('download-png');

    function stateKey() {
      return 'd2-live:' + state.file + ':' + state.sketch + ':' + state.layout;
    }

    function query(extra) {
      let s = 'file=' + encodeURIComponent(state.file) +
        '&sketch=' + state.sketch +
        '&layout=' + encodeURIComponent(state.layout);
      return extra ? s + '&' + extra : s;
    }

    function baseName() {
      const parts = state.file.split('/');
      return parts[parts.length - 1] || state.file;
    }

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
        const raw = localStorage.getItem(stateKey());
        if (!raw) return null;
        return JSON.parse(raw);
      } catch (err) {
        return null;
      }
    }

    function saveState() {
      try {
        const transform = instance.getTransform();
        localStorage.setItem(stateKey(), JSON.stringify({
          x: transform.x,
          y: transform.y,
          scale: transform.scale,
        }));
      } catch (err) {
        // ignore storage failures
      }
    }

    function applyTransform(t) {
      const target = t || {x: 0, y: 0, scale: 1};
      instance.zoomAbs(0, 0, target.scale);
      instance.moveTo(target.x, target.y);
    }

    function setConnected(status) {
      hud.classList.remove('connecting', 'connected', 'disconnected');
      hud.classList.add(status);
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
      const name = baseName();
      const safeName = name.endsWith('.svg') ? name : name + '.svg';
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
      return baseName().replace(/\.[^.]+$/, '') + '.png';
    }

    async function fetchPngBlob() {
      const response = await fetch('/png?' + query('ts=' + Date.now()));
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
        const response = await fetch('/svg?' + query('ts=' + Date.now()));
        const svg = await response.text();
        scene.innerHTML = svg;
      } catch (err) {
        showToast('update failed');
      }
    }

    function refreshTitle() {
      const base = baseName();
      document.title = 'd2-live — ' + base.replace(/\.[^.]+$/, '');
      titleEl.textContent = base;
      fnameEl.textContent = base;
    }

    function updateURL() {
      const u = new URL(location.href);
      u.searchParams.set('file', state.file);
      u.searchParams.set('sketch', state.sketch);
      u.searchParams.set('layout', state.layout);
      history.replaceState(null, '', u);
    }

    let events = null;
    function connect() {
      if (events) {
        events.close();
      }
      events = new EventSource('/events?file=' + encodeURIComponent(state.file));
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

    function applyState(next, fileChanged) {
      saveState();
      Object.assign(state, next);
      updateURL();
      refreshTitle();
      const target = loadState();
      reloadSvg().then(() => applyTransform(target));
      if (fileChanged) {
        connect();
      }
    }

    async function refreshFiles() {
      try {
        const response = await fetch('/files');
        const files = await response.json();
        const current = state.file;
        fileSelect.innerHTML = '';
        files.forEach((f) => {
          const option = document.createElement('option');
          option.value = f.abs;
          option.textContent = f.basename;
          if (f.abs === current) {
            option.selected = true;
          }
          fileSelect.appendChild(option);
        });
      } catch (err) {
        // ignore; keep existing options
      }
    }

    fileSelect.addEventListener('mousedown', refreshFiles);
    fileSelect.addEventListener('change', (event) => {
      applyState({file: event.target.value}, true);
    });
    layoutSelect.addEventListener('change', (event) => {
      applyState({layout: event.target.value}, false);
    });
    sketchToggle.addEventListener('change', (event) => {
      applyState({sketch: event.target.checked}, false);
    });

    setConnected('connecting');
    saveState();
    connect();
  </script>
</body>
</html>`))

const version = "0.2.0"

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
	mux.HandleFunc("/svg", s.handleSVG)
	mux.HandleFunc("/png", s.handlePNG)
	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/files", s.handleFiles)

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
		cache:       map[string]string{},
		watchedDirs: map[string]int{},
		debounce:    map[string]*time.Timer{},
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
	s.files[abs] = &fileEntry{abs: abs, base: filepath.Base(abs), subs: map[chan struct{}]struct{}{}}
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
		case ch <- struct{}{}:
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

func (s *server) getSVG(file string, sketch bool, layout string) (string, error) {
	key := cacheKey(file, sketch, layout)
	s.mu.Lock()
	cached, ok := s.cache[key]
	s.mu.Unlock()
	if ok {
		return cached, nil
	}

	svg, err := renderSVG(file, layout, sketch)
	if err != nil {
		// svg holds an errorSVG describing the failure; surface it without caching.
		return svg, err
	}
	s.mu.Lock()
	s.cache[key] = svg
	s.mu.Unlock()
	return svg, nil
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
		s.notify(abs)
	})
	s.mu.Unlock()
}

func (s *server) notify(abs string) {
	s.mu.Lock()
	entry := s.files[abs]
	s.mu.Unlock()
	if entry == nil {
		return
	}
	s.subMu.Lock()
	for ch := range entry.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.subMu.Unlock()
}

func (s *server) subscribe(abs string, ch chan struct{}) bool {
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

func (s *server) unsubscribe(abs string, ch chan struct{}) {
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

func renderSVG(file, layout string, sketch bool) (string, error) {
	args := []string{file, "--stdout-format", "svg", "--layout", layout}
	if sketch {
		args = append(args, "--sketch")
	}
	args = append(args, "-")

	cmd := exec.Command("d2", args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return errorSVG(fmt.Sprintf("d2 render failed: %s", strings.TrimSpace(stderr.String()))), err
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
	go func() { _, _ = s.getSVG(abs, req.Sketch, layout) }()

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
		svg, _ = s.getSVG(file, sketch, layout)
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
	svg, _ := s.getSVG(file, q.Get("sketch") == "true", layout)
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	_, _ = w.Write([]byte(svg))
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

	ch := make(chan struct{}, 1)
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
		case <-ch:
			_, _ = io.WriteString(w, "data: reload\n\n")
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
