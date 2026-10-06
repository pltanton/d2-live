package main

import (
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"oss.terrastruct.com/d2/d2ast"
	"oss.terrastruct.com/d2/d2compiler"
	"oss.terrastruct.com/d2/d2graph"
)

// doc is a compiled diagram together with the exact text it came from, so AST
// byte ranges can be turned into splices of that text.
type doc struct {
	src string
	g   *d2graph.Graph
}

func parseDoc(src string) (*doc, error) {
	g, _, err := d2compiler.Compile("diagram.d2", strings.NewReader(src), nil)
	if err != nil {
		return nil, err
	}
	return &doc{src: src, g: g}, nil
}

func (d *doc) topObjects() []*d2graph.Object {
	return d.g.Root.ChildrenArray
}

func (d *doc) object(id string) *d2graph.Object {
	for _, o := range d.topObjects() {
		if o.ID == id {
			return o
		}
	}
	return nil
}

// objectFold matches the way D2 resolves ids: case-insensitively.
func (d *doc) objectFold(id string) *d2graph.Object {
	for _, o := range d.topObjects() {
		if strings.EqualFold(o.ID, id) {
			return o
		}
	}
	return nil
}

func (d *doc) edge(id string) *d2graph.Edge {
	for _, e := range d.g.Edges {
		if e.AbsID() == id {
			return e
		}
	}
	return nil
}

// objectDecl is the top-level `ID` / `ID: …` key that declares o, or nil when o
// only appears as an edge end.
func (d *doc) objectDecl(o *d2graph.Object) *d2ast.Key {
	for _, r := range o.References {
		if r.InEdge() || r.Scope != d.g.AST || r.MapKey.EdgeKey != nil {
			continue
		}
		if len(r.MapKey.Key.Path) == 1 {
			return r.MapKey
		}
	}
	return nil
}

func edgeDecl(e *d2graph.Edge) *d2ast.Key {
	if len(e.References) == 0 {
		return nil
	}
	return e.References[0].MapKey
}

// classDecl finds `name: {…}` inside the top-level `classes: {…}` map.
func (d *doc) classDecl(name string) *d2ast.Key {
	classes := d.classesMap()
	if classes == nil {
		return nil
	}
	for _, n := range classes.Nodes {
		if n.MapKey != nil && n.MapKey.Key != nil && len(n.MapKey.Edges) == 0 &&
			strings.Join(n.MapKey.Key.StringIDA(), ".") == name {
			return n.MapKey
		}
	}
	return nil
}

func (d *doc) classesMap() *d2ast.Map {
	for _, n := range d.g.AST.Nodes {
		mk := n.MapKey
		if mk != nil && mk.Key != nil && len(mk.Edges) == 0 && len(mk.Key.Path) == 1 &&
			mk.Key.Path[0].Unbox().ScalarString() == "classes" && mk.Value.Map != nil {
			return mk.Value.Map
		}
	}
	return nil
}

func rng(n d2ast.Node) (int, int) {
	r := n.GetRange()
	return r.Start.Byte, r.End.Byte
}

// keyEnd is where the key part of mk ends: after the last edge or key path, not
// counting the label or map.
func keyEnd(mk *d2ast.Key) int {
	end := 0
	if mk.Key != nil {
		end = mk.Key.Range.End.Byte
	}
	if len(mk.Edges) > 0 {
		end = max(end, mk.Edges[len(mk.Edges)-1].Range.End.Byte)
	}
	if mk.EdgeIndex != nil {
		end = max(end, mk.EdgeIndex.Range.End.Byte)
	}
	if mk.EdgeKey != nil {
		end = max(end, mk.EdgeKey.Range.End.Byte)
	}
	return end
}

func lineStart(src string, pos int) int {
	return strings.LastIndexByte(src[:pos], '\n') + 1
}

// lineEnd is the offset just past the '\n' that ends pos's line (or len(src)).
func lineEnd(src string, pos int) int {
	if i := strings.IndexByte(src[pos:], '\n'); i >= 0 {
		return pos + i + 1
	}
	return len(src)
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

func indentOf(src string, pos int) string {
	ls := lineStart(src, pos)
	i := ls
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	return src[ls:i]
}

// commentLines holds the start offset of every line that is nothing but a `#`
// comment in the top-level map. The parser folds consecutive comment lines into
// one node, so each node can cover several lines.
func (d *doc) commentLines() map[int]bool {
	out := map[int]bool{}
	for _, n := range d.g.AST.Nodes {
		if n.Comment == nil {
			continue
		}
		start, end := rng(n.Comment)
		if !isBlank(d.src[lineStart(d.src, start):start]) {
			continue
		}
		for ls := lineStart(d.src, start); ls < end; ls = lineEnd(d.src, ls) {
			out[ls] = true
		}
	}
	return out
}

// commentRun is the block of consecutive comment-only lines directly above the
// line holding pos: its byte span (whole lines) and text without `# `.
func (d *doc) commentRun(pos int) (from, to int, text string, ok bool) {
	comments := d.commentLines()
	to = lineStart(d.src, pos)
	from = to
	for from > 0 && comments[lineStart(d.src, from-1)] {
		from = lineStart(d.src, from-1)
	}
	if from == to {
		return 0, 0, "", false
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSuffix(d.src[from:to], "\n"), "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "#")
		lines = append(lines, strings.TrimPrefix(line, " "))
	}
	return from, to, strings.Join(lines, "\n"), true
}

var (
	bareValue = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)
	bareKey   = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)
)

func formatValue(v string) string {
	if bareValue.MatchString(v) {
		return v
	}
	return quote(v)
}

func formatKey(k string) string {
	if bareKey.MatchString(k) {
		return k
	}
	return quote(k)
}

func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// utf16Index converts byte offsets of src into UTF-16 code unit offsets, the
// unit CodeMirror positions are counted in.
type utf16Index []int

func newUTF16Index(src string) utf16Index {
	idx := make(utf16Index, len(src)+1)
	u := 0
	for i := 0; i < len(src); {
		r, size := utf8.DecodeRuneInString(src[i:])
		for k := 0; k < size; k++ {
			idx[i+k] = u
		}
		u += utf16.RuneLen(r)
		i += size
	}
	idx[len(src)] = u
	return idx
}

func (x utf16Index) span(from, to int) span {
	return span{From: x[from], To: x[to]}
}
