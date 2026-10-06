package main

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"oss.terrastruct.com/d2/d2ast"
	"oss.terrastruct.com/d2/d2graph"
)

type span struct {
	From int `json:"from"`
	To   int `json:"to"`
}

type commentModel struct {
	Text  string `json:"text"`
	Range span   `json:"range"`
}

type objectModel struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Parent   string            `json:"parent"`
	Kind     string            `json:"kind"`
	Label    string            `json:"label"`
	Markdown bool              `json:"markdown"`
	Props    map[string]string `json:"props"`
	Decl     *span             `json:"decl"`
	Refs     []span            `json:"refs"`
	Comment  *commentModel     `json:"comment"`
}

type edgeModel struct {
	ID       string            `json:"id"`
	Sequence bool              `json:"sequence"`
	Src      string            `json:"src"`
	Dst      string            `json:"dst"`
	Label    string            `json:"label"`
	Props    map[string]string `json:"props"`
	Decl     *span             `json:"decl"`
	Comment  *commentModel     `json:"comment"`
}

type classModel struct {
	Name  string            `json:"name"`
	Props map[string]string `json:"props"`
	Decl  span              `json:"decl"`
}

type compileError struct {
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Message string `json:"message"`
}

type diagramModel struct {
	Hash    string        `json:"hash"`
	Objects []objectModel `json:"objects"`
	Edges   []edgeModel   `json:"edges"`
	Classes []classModel  `json:"classes"`
	Error   *compileError `json:"error"`
}

func buildModel(src string) diagramModel {
	m := diagramModel{Hash: contentHash([]byte(src)), Objects: []objectModel{}, Edges: []edgeModel{}, Classes: []classModel{}}
	d, err := parseDoc(src)
	if err != nil {
		m.Error = parseCompileError(err)
		return m
	}
	x := newUTF16Index(src)

	for _, o := range d.g.Objects {
		om := objectModel{ID: o.AbsID(), Name: o.IDVal, Parent: o.Parent.AbsID(), Kind: objectKind(o), Props: map[string]string{}, Refs: []span{}}
		decl := d.objectDecl(o)
		for _, r := range o.References {
			if r.MapKey == decl && !r.InEdge() {
				continue
			}
			from, to := rng(r.Key.Path[r.KeyPathIndex].Unbox())
			om.Refs = append(om.Refs, x.span(from, to))
		}
		if decl != nil {
			from, to := rng(decl)
			s := x.span(from, to)
			om.Decl = &s
			om.Label, om.Markdown = keyLabel(decl)
			collectProps(decl.Value.Map, "", om.Props)
			om.Comment = d.commentModel(x, from)
		}
		m.Objects = append(m.Objects, om)
	}

	for _, e := range d.g.Edges {
		em := edgeModel{ID: e.AbsID(), Sequence: sequenceOf(e.Src) != nil, Src: e.Src.AbsID(), Dst: e.Dst.AbsID(), Props: map[string]string{}}
		if decl := edgeDecl(e); decl != nil {
			from, to := rng(decl)
			s := x.span(from, to)
			em.Decl = &s
			em.Label, _ = keyLabel(decl)
			collectProps(decl.Value.Map, "", em.Props)
			em.Comment = d.commentModel(x, from)
		}
		m.Edges = append(m.Edges, em)
	}

	if classes := d.classesMap(); classes != nil {
		for _, n := range classes.Nodes {
			mk := n.MapKey
			if mk == nil || mk.Key == nil || len(mk.Edges) > 0 {
				continue
			}
			cm := classModel{Name: strings.Join(mk.Key.StringIDA(), "."), Props: map[string]string{}}
			from, to := rng(mk)
			cm.Decl = x.span(from, to)
			collectProps(mk.Value.Map, "", cm.Props)
			m.Classes = append(m.Classes, cm)
		}
	}
	return m
}

// objectKind tells the editor which controls fit: sequence diagrams have
// actors and groups, everything else shapes and containers.
func objectKind(o *d2graph.Object) string {
	switch {
	case isSequence(o):
		return "sequence"
	case isSequence(o.Parent) && len(o.ChildrenArray) == 0:
		return "actor"
	case sequenceOf(o.Parent) != nil:
		return "group"
	case len(o.ChildrenArray) > 0:
		return "container"
	}
	return "shape"
}

func (d *doc) commentModel(x utf16Index, pos int) *commentModel {
	from, to, text, ok := d.commentRun(pos)
	if !ok {
		return nil
	}
	return &commentModel{Text: text, Range: x.span(from, to)}
}

// keyLabel is the label written on the key itself: the primary of `X: "l" {…}`
// or the scalar of `X: l`.
func keyLabel(mk *d2ast.Key) (string, bool) {
	if mk.Primary.Unbox() != nil {
		return scalarText(mk.Primary.Unbox())
	}
	if mk.Value.Map == nil && mk.Value.Unbox() != nil {
		if s, ok := mk.Value.Unbox().(d2ast.Scalar); ok {
			return scalarText(s)
		}
	}
	return "", false
}

func scalarText(s d2ast.Scalar) (string, bool) {
	if bs, ok := s.(*d2ast.BlockString); ok {
		return bs.Value, bs.Tag == "md"
	}
	return s.ScalarString(), false
}

// collectProps flattens scalar leaves of m into dotted keys: `style: {fill: x}`
// and `style.fill: x` both become props["style.fill"].
func collectProps(m *d2ast.Map, prefix string, out map[string]string) {
	if m == nil {
		return
	}
	for _, n := range m.Nodes {
		mk := n.MapKey
		if mk == nil || mk.Key == nil || len(mk.Edges) > 0 {
			continue
		}
		key := prefix + strings.Join(mk.Key.StringIDA(), ".")
		if mk.Value.Map != nil {
			collectProps(mk.Value.Map, key+".", out)
			continue
		}
		if s, ok := mk.Value.Unbox().(d2ast.Scalar); ok {
			out[key] = s.ScalarString()
		}
	}
}

var compileErrorRe = regexp.MustCompile(`^[^:\n]*:(\d+):(\d+):\s*(.*)$`)

func parseCompileError(err error) *compileError {
	first := strings.SplitN(err.Error(), "\n", 2)[0]
	if m := compileErrorRe.FindStringSubmatch(first); m != nil {
		line, _ := strconv.Atoi(m[1])
		col, _ := strconv.Atoi(m[2])
		return &compileError{Line: line, Col: col, Message: m[3]}
	}
	return &compileError{Message: first}
}

// handleModel compiles the posted buffer, so the model always matches what the
// editor shows even before it is saved.
func handleModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	text, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, buildModel(string(text)))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
