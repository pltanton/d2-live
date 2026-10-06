package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"oss.terrastruct.com/d2/d2graph"
	"oss.terrastruct.com/d2/d2layouts/d2dagrelayout"
	"oss.terrastruct.com/d2/d2layouts/d2elklayout"
	"oss.terrastruct.com/d2/d2lib"
	"oss.terrastruct.com/d2/d2renderers/d2svg"
	"oss.terrastruct.com/d2/lib/log"
	"oss.terrastruct.com/d2/lib/textmeasure"
	"oss.terrastruct.com/util-go/go2"
)

var inProcessLayouts = map[string]d2graph.LayoutGraph{
	"elk":   func(ctx context.Context, g *d2graph.Graph) error { return d2elklayout.DefaultLayout(ctx, g) },
	"dagre": func(ctx context.Context, g *d2graph.Graph) error { return d2dagrelayout.DefaultLayout(ctx, g) },
}

var (
	rulerMu sync.Mutex
	ruler   *textmeasure.Ruler
)

// renderInProcess lays out and renders with the d2 library, skipping a d2
// process per render (withdraw-fsm: dagre 0.44s via CLI vs 0.1s here, elk 2.0s vs 1.5s).
// ok is false for engines that only exist as external plugins (tala).
func renderInProcess(file string, src []byte, layout string, sketch bool) (svg string, ok bool, err error) {
	layoutFn, ok := inProcessLayouts[layout]
	if !ok {
		return "", false, nil
	}
	rulerMu.Lock()
	defer rulerMu.Unlock()
	if ruler == nil {
		if ruler, err = textmeasure.NewRuler(); err != nil {
			return "", true, err
		}
	}
	ctx := log.WithDefault(context.Background())
	renderOpts := &d2svg.RenderOpts{Pad: go2.Pointer(int64(d2svg.DEFAULT_PADDING)), Sketch: go2.Pointer(sketch)}
	diagram, _, err := d2lib.Compile(ctx, string(src), &d2lib.CompileOptions{
		Ruler:          ruler,
		FS:             os.DirFS(filepath.Dir(file)),
		InputPath:      filepath.Base(file),
		LayoutResolver: func(string) (d2graph.LayoutGraph, error) { return layoutFn, nil },
	}, renderOpts)
	if err != nil {
		return "", true, err
	}
	out, err := d2svg.Render(diagram, renderOpts)
	if err != nil {
		return "", true, err
	}
	return string(sanitizeSVG(out)), true, nil
}
