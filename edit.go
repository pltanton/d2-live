package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"oss.terrastruct.com/d2/d2ast"
	"oss.terrastruct.com/d2/d2graph"
)

type editOp struct {
	Kind      string  `json:"kind"`
	ID        string  `json:"id"`
	To        string  `json:"to"`
	Key       string  `json:"key"`
	Value     *string `json:"value"`
	Text      string  `json:"text"`
	Src       string  `json:"src"`
	Dst       string  `json:"dst"`
	Label     string  `json:"label"`
	Class     string  `json:"class"`
	EdgeClass string  `json:"edgeClass"`
}

type opError struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (e *opError) Error() string { return e.Message }

func errf(field, format string, args ...any) error {
	return &opError{Field: field, Message: fmt.Sprintf(format, args...)}
}

var errByHand = func(what string) error {
	return errf("", "%s is outside what edit mode can change safely — edit it in the code", what)
}

type splice struct {
	from, to int
	text     string
}

// applySplices applies non-overlapping splices of src; insertions at the same
// offset keep their order.
func applySplices(src string, ss []splice) (string, error) {
	sorted := append([]splice(nil), ss...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].from < sorted[j].from })
	var b strings.Builder
	pos := 0
	for _, s := range sorted {
		if s.from < pos || s.to < s.from || s.to > len(src) {
			return "", errf("", "internal error: overlapping edit")
		}
		b.WriteString(src[pos:s.from])
		b.WriteString(s.text)
		pos = s.to
	}
	b.WriteString(src[pos:])
	return b.String(), nil
}

// applyOp runs o against src and returns the new text and the id the client
// should select afterwards.
func applyOp(src string, o editOp) (string, string, error) {
	d, err := parseDoc(src)
	if err != nil {
		return "", "", errf("", "the diagram does not compile; fix it in the code first")
	}
	var ss []splice
	sel := o.ID
	switch o.Kind {
	case "rename":
		ss, sel, err = d.opRename(o.ID, o.To)
	case "set":
		ss, err = d.opSet(o.ID, o.Key, o.Value)
	case "setComment":
		ss, err = d.opSetComment(o.ID, o.Text)
	case "createNode":
		ss, sel = d.opCreateNode(o.Class)
	case "createEdge":
		ss, err = d.opCreateEdge(o.Src, o.Dst, o.Label, o.Class)
		sel = ""
	case "createNodeWithEdge":
		ss, sel, err = d.opCreateNodeWithEdge(o.Src, o.Class, o.EdgeClass)
	case "delete":
		ss, err = d.opDelete(o.ID)
		sel = ""
	case "reverse":
		ss, err = d.opReverse(o.ID)
		sel = ""
	default:
		err = errf("", "unknown op %q", o.Kind)
	}
	if err != nil {
		return "", "", err
	}
	out, err := applySplices(src, ss)
	if err != nil {
		return "", "", err
	}
	nd, err := parseDoc(out)
	if err != nil {
		return "", "", errf("", "the change would break the diagram: %v", parseCompileError(err).Message)
	}
	switch o.Kind {
	case "rename":
		if nd.objectFold(o.To) == nil {
			return "", "", errf("to", "%q is not a valid state name", o.To)
		}
		sel = nd.objectFold(o.To).ID
	case "createEdge":
		sel = lastEdgeID(nd, o.Src, o.Dst)
	case "reverse":
		if e := d.edge(o.ID); e != nil {
			sel = lastEdgeID(nd, e.Dst.ID, e.Src.ID)
		}
	}
	return out, sel, nil
}

func lastEdgeID(d *doc, src, dst string) string {
	id := ""
	for _, e := range d.g.Edges {
		if strings.EqualFold(e.Src.ID, src) && strings.EqualFold(e.Dst.ID, dst) {
			id = e.AbsID()
		}
	}
	return id
}

func (d *doc) mustObject(id string) (*d2graph.Object, error) {
	o := d.object(id)
	if o == nil {
		return nil, errf("", "no state %q in the diagram", id)
	}
	return o, nil
}

func (d *doc) mustEdge(id string) (*d2graph.Edge, error) {
	e := d.edge(id)
	if e == nil {
		return nil, errf("", "no transition %q in the diagram", id)
	}
	return e, nil
}

func (d *doc) opRename(id, to string) ([]splice, string, error) {
	o, err := d.mustObject(id)
	if err != nil {
		return nil, "", err
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return nil, "", errf("to", "name is empty")
	}
	if other := d.objectFold(to); other != nil && other != o {
		return nil, "", errf("to", "%q already exists", to)
	}
	seen := map[int]bool{}
	var ss []splice
	for _, r := range o.References {
		if r.IsVar {
			return nil, "", errByHand("a state used through vars")
		}
		from, end := rng(r.Key.Path[r.KeyPathIndex].Unbox())
		if seen[from] {
			continue
		}
		seen[from] = true
		ss = append(ss, splice{from, end, formatKey(to)})
	}
	return ss, to, nil
}

// target resolves the key whose map holds the properties of id: an object's
// declaration, an edge's declaration or a class in `classes:`.
func (d *doc) target(id string) (*d2ast.Key, *d2graph.Object, error) {
	if name, ok := strings.CutPrefix(id, "classes."); ok {
		mk := d.classDecl(name)
		if mk == nil {
			return nil, nil, errf("", "no class %q", name)
		}
		return mk, nil, nil
	}
	if strings.HasPrefix(id, "(") {
		e, err := d.mustEdge(id)
		if err != nil {
			return nil, nil, err
		}
		if len(e.References) != 1 || len(e.References[0].MapKey.Edges) != 1 || e.References[0].MapKey.EdgeKey != nil {
			return nil, nil, errByHand("a transition declared in several places or in a chain")
		}
		return edgeDecl(e), nil, nil
	}
	o, err := d.mustObject(id)
	if err != nil {
		return nil, nil, err
	}
	return d.objectDecl(o), o, nil
}

func (d *doc) opSet(id, key string, value *string) ([]splice, error) {
	mk, obj, err := d.target(id)
	if err != nil {
		return nil, err
	}
	if !validPropKey(key) {
		return nil, errf("key", "unsupported property %q", key)
	}
	if mk == nil {
		// An object that only appears as an edge end gets its own declaration.
		if value == nil {
			return nil, nil
		}
		line := formatKey(obj.IDVal)
		if key == "label" {
			line += ": " + formatValue(*value)
		} else {
			line += ": {" + key + ": " + formatValue(*value) + "}"
		}
		return []splice{d.insertLine(d.nodeInsertPos(), line)}, nil
	}
	path := strings.Split(key, ".")
	if key == "label" && !strings.HasPrefix(id, "classes.") {
		if chain, _ := findProp(mk.Value.Map, path); chain == nil {
			return d.setKeyLabel(mk, value)
		}
	}
	return d.setProp(mk, path, value)
}

func validPropKey(k string) bool {
	switch k {
	case "label", "class", "shape", "near", "width", "height":
		return true
	}
	if p, ok := strings.CutPrefix(k, "style."); ok {
		switch p {
		case "fill", "stroke", "stroke-width", "stroke-dash", "border-radius", "font-color",
			"font-size", "bold", "italic", "opacity", "shadow", "3d", "multiple", "double-border", "animated":
			return true
		}
	}
	return false
}

// findProp walks path through m in nested (`style: {fill: x}`) or dotted
// (`style.fill: x`) form; chain holds the keys from m down to the leaf, maps the
// map each of them sits in. The last match wins, as it does in D2.
func findProp(m *d2ast.Map, path []string) (chain []*d2ast.Key, maps []*d2ast.Map) {
	if m == nil {
		return nil, nil
	}
	for _, n := range m.Nodes {
		mk := n.MapKey
		if mk == nil || mk.Key == nil || len(mk.Edges) > 0 {
			continue
		}
		kp := mk.Key.StringIDA()
		if len(kp) > len(path) || !equalFoldPath(kp, path[:len(kp)]) {
			continue
		}
		if len(kp) == len(path) {
			chain, maps = []*d2ast.Key{mk}, []*d2ast.Map{m}
			continue
		}
		if c, ms := findProp(mk.Value.Map, path[len(kp):]); c != nil {
			chain, maps = append([]*d2ast.Key{mk}, c...), append([]*d2ast.Map{m}, ms...)
		}
	}
	return chain, maps
}

// deepestMap is the innermost existing map along path and the part of the path
// below it.
func deepestMap(m *d2ast.Map, path []string) (*d2ast.Map, []string) {
	for _, n := range m.Nodes {
		mk := n.MapKey
		if mk == nil || mk.Key == nil || len(mk.Edges) > 0 || mk.Value.Map == nil {
			continue
		}
		kp := mk.Key.StringIDA()
		if len(kp) < len(path) && equalFoldPath(kp, path[:len(kp)]) {
			return deepestMap(mk.Value.Map, path[len(kp):])
		}
	}
	return m, path
}

func equalFoldPath(a, b []string) bool {
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

func (d *doc) setProp(mk *d2ast.Key, path []string, value *string) ([]splice, error) {
	chain, maps := findProp(mk.Value.Map, path)
	if chain != nil {
		leaf := chain[len(chain)-1]
		if value == nil {
			return d.removeChain(mk, chain, maps), nil
		}
		if leaf.Value.Map != nil {
			return nil, errf("", "%s is a map, not a value", strings.Join(path, "."))
		}
		from, to := rng(leaf.Value.Unbox())
		return []splice{{from, to, formatValue(*value)}}, nil
	}
	if value == nil {
		return nil, nil
	}
	if mk.Value.Map == nil {
		kv := strings.Join(path, ".") + ": " + formatValue(*value)
		end := mk.Range.End.Byte
		if mk.Value.Unbox() == nil {
			return []splice{{end, end, ": {" + kv + "}"}}, nil
		}
		return []splice{{end, end, " {" + kv + "}"}}, nil
	}
	m, rest := deepestMap(mk.Value.Map, path)
	return []splice{d.insertIntoMap(m, strings.Join(rest, ".")+": "+formatValue(*value))}, nil
}

func (d *doc) insertIntoMap(m *d2ast.Map, kv string) splice {
	start, end := rng(m)
	closing := end - 1
	keys := mapNodes(m)
	if m.Range.OneLine() {
		if len(keys) == 0 {
			return splice{closing, closing, kv}
		}
		return splice{closing, closing, "; " + kv}
	}
	ls := lineStart(d.src, closing)
	if !isBlank(d.src[ls:closing]) {
		return splice{closing, closing, "; " + kv}
	}
	indent := indentOf(d.src, start) + "  "
	if len(keys) > 0 {
		first, _ := rng(keys[0])
		indent = indentOf(d.src, first)
	}
	return splice{ls, ls, indent + kv + "\n"}
}

func mapNodes(m *d2ast.Map) []d2ast.Node {
	var out []d2ast.Node
	for _, n := range m.Nodes {
		if u := n.Unbox(); u != nil {
			out = append(out, u)
		}
	}
	return out
}

// removeChain deletes the leaf of chain and every map on the way up that the
// deletion leaves empty; the declaration's own map goes too, leaving `ID`.
func (d *doc) removeChain(decl *d2ast.Key, chain []*d2ast.Key, maps []*d2ast.Map) []splice {
	for i := len(chain) - 1; i >= 0; i-- {
		if len(mapNodes(maps[i])) > 1 {
			return []splice{d.removeFromMap(maps[i], chain[i])}
		}
	}
	from := keyEnd(decl)
	if p := decl.Primary.Unbox(); p != nil {
		_, from = rng(p)
	}
	_, end := rng(decl)
	return []splice{{from, end, ""}}
}

func (d *doc) removeFromMap(m *d2ast.Map, k *d2ast.Key) splice {
	nodes := mapNodes(m)
	idx := 0
	for i, n := range nodes {
		if n == d2ast.Node(k) {
			idx = i
		}
	}
	from, to := rng(k)
	ls, le := lineStart(d.src, from), lineEnd(d.src, to)
	if !m.Range.OneLine() && isBlank(d.src[ls:from]) && isBlank(d.src[to:le]) {
		return splice{ls, le, ""}
	}
	if idx > 0 {
		_, prevEnd := rng(nodes[idx-1])
		return splice{prevEnd, to, ""}
	}
	nextStart, _ := rng(nodes[1])
	return splice{from, nextStart, ""}
}

func (d *doc) setKeyLabel(mk *d2ast.Key, value *string) ([]splice, error) {
	var cur d2ast.Scalar
	if p := mk.Primary.Unbox(); p != nil {
		cur = p
	} else if s, ok := mk.Value.Unbox().(d2ast.Scalar); ok && mk.Value.Map == nil {
		cur = s
	}
	if value == nil {
		if cur == nil {
			return nil, nil
		}
		from, to := rng(cur)
		if mk.Value.Map != nil {
			mapStart, _ := rng(mk.Value.Map)
			return []splice{{from, mapStart, ""}}, nil
		}
		return []splice{{keyEnd(mk), to, ""}}, nil
	}
	if cur != nil {
		from, to := rng(cur)
		if bs, ok := cur.(*d2ast.BlockString); ok {
			return []splice{{from, to, d.rebuildBlock(mk, bs, *value)}}, nil
		}
		return []splice{{from, to, formatValue(*value)}}, nil
	}
	if mk.Value.Map != nil {
		mapStart, _ := rng(mk.Value.Map)
		if end := keyEnd(mk); !strings.Contains(d.src[end:mapStart], ":") {
			return []splice{{end, mapStart, ": " + formatValue(*value) + " "}}, nil
		}
		return []splice{{mapStart, mapStart, formatValue(*value) + " "}}, nil
	}
	end := keyEnd(mk)
	return []splice{{end, end, ": " + formatValue(*value)}}, nil
}

// rebuildBlock keeps a block string's delimiters and tag and swaps its body.
func (d *doc) rebuildBlock(mk *d2ast.Key, bs *d2ast.BlockString, body string) string {
	from, to := rng(bs)
	text := d.src[from:to]
	open := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		open = text[:i]
	}
	closing := strings.TrimSpace(text[strings.LastIndexByte(text, '\n')+1:])
	keyStart, _ := rng(mk)
	indent := indentOf(d.src, keyStart)
	var b strings.Builder
	b.WriteString(open)
	b.WriteByte('\n')
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if line != "" {
			b.WriteString(indent + "  " + line)
		}
		b.WriteByte('\n')
	}
	b.WriteString(indent + closing)
	return b.String()
}

func (d *doc) opSetComment(id, text string) ([]splice, error) {
	mk, _, err := d.target(id)
	if err != nil {
		return nil, err
	}
	if mk == nil {
		return nil, errf("", "%s has no declaration to comment", id)
	}
	start, _ := rng(mk)
	ls := lineStart(d.src, start)
	if !isBlank(d.src[ls:start]) {
		return nil, errByHand("a declaration that shares its line")
	}
	indent := d.src[ls:start]
	var lines strings.Builder
	text = strings.TrimRight(text, "\n ")
	if text != "" {
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimRight(line, " "); line == "" {
				lines.WriteString(indent + "#\n")
			} else {
				lines.WriteString(indent + "# " + line + "\n")
			}
		}
	}
	if from, to, _, ok := d.commentRun(start); ok {
		return []splice{{from, to, lines.String()}}, nil
	}
	return []splice{{ls, ls, lines.String()}}, nil
}

var notStates = map[string]bool{"title": true, "notes": true, "legend": true}

// nodeInsertPos is the start of the line after the last top-level state
// declaration.
func (d *doc) nodeInsertPos() int {
	pos := -1
	for _, n := range d.g.AST.Nodes {
		mk := n.MapKey
		if mk == nil || mk.Key == nil || len(mk.Edges) > 0 || len(mk.Key.Path) != 1 {
			continue
		}
		name := mk.Key.Path[0].Unbox().ScalarString()
		if notStates[strings.ToLower(name)] || d.objectFold(name) == nil {
			continue
		}
		_, end := rng(mk)
		pos = max(pos, end)
	}
	if pos < 0 {
		return len(d.src)
	}
	return lineEnd(d.src, pos)
}

func (d *doc) edgeInsertPos(src *d2graph.Object) int {
	sameSrc, any := -1, -1
	for _, e := range d.g.Edges {
		mk := edgeDecl(e)
		if mk == nil || e.References[0].Scope != d.g.AST {
			continue
		}
		_, end := rng(mk)
		any = max(any, end)
		if e.Src == src {
			sameSrc = max(sameSrc, end)
		}
	}
	switch {
	case sameSrc >= 0:
		return lineEnd(d.src, sameSrc)
	case any >= 0:
		return lineEnd(d.src, any)
	}
	return d.nodeInsertPos()
}

func (d *doc) insertLine(pos int, line string) splice {
	if pos == len(d.src) && pos > 0 && d.src[pos-1] != '\n' {
		return splice{pos, pos, "\n" + line + "\n"}
	}
	return splice{pos, pos, line + "\n"}
}

func (d *doc) uniqueName(base string) string {
	name := base
	for i := 2; d.objectFold(name) != nil; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	return name
}

func nodeLine(name, class string) string {
	if class == "" {
		return formatKey(name)
	}
	return formatKey(name) + ": {class: " + formatValue(class) + "}"
}

func edgeLine(src, dst, label, class string) string {
	line := src + " -> " + dst
	sep := ": "
	if label != "" {
		line += sep + formatValue(label)
		sep = " "
	}
	if class != "" {
		line += sep + "{class: " + formatValue(class) + "}"
	}
	return line
}

func (d *doc) opCreateNode(class string) ([]splice, string) {
	name := d.uniqueName("NEW_STATE")
	return []splice{d.insertLine(d.nodeInsertPos(), nodeLine(name, class))}, name
}

func (d *doc) opCreateEdge(src, dst, label, class string) ([]splice, error) {
	so, err := d.mustObject(src)
	if err != nil {
		return nil, err
	}
	if _, err := d.mustObject(dst); err != nil {
		return nil, err
	}
	return []splice{d.insertLine(d.edgeInsertPos(so), edgeLine(src, dst, label, class))}, nil
}

func (d *doc) opCreateNodeWithEdge(src, class, edgeClass string) ([]splice, string, error) {
	so, err := d.mustObject(src)
	if err != nil {
		return nil, "", err
	}
	name := d.uniqueName("NEW_STATE")
	return []splice{
		d.insertLine(d.nodeInsertPos(), nodeLine(name, class)),
		d.insertLine(d.edgeInsertPos(so), edgeLine(src, formatKey(name), "", edgeClass)),
	}, name, nil
}

func (d *doc) opDelete(id string) ([]splice, error) {
	var keys []*d2ast.Key
	if strings.HasPrefix(id, "(") {
		e, err := d.mustEdge(id)
		if err != nil {
			return nil, err
		}
		for _, r := range e.References {
			keys = append(keys, r.MapKey)
		}
	} else {
		o, err := d.mustObject(id)
		if err != nil {
			return nil, err
		}
		for _, r := range o.References {
			if r.Scope != d.g.AST {
				return nil, errByHand("a state referenced inside a container")
			}
			keys = append(keys, r.MapKey)
		}
	}
	type lineSpan struct{ from, to int }
	var spans []lineSpan
	seen := map[*d2ast.Key]bool{}
	for _, mk := range keys {
		if seen[mk] {
			continue
		}
		seen[mk] = true
		if len(mk.Edges) > 1 {
			return nil, errByHand("a transition chain (a -> b -> c)")
		}
		from, to := rng(mk)
		ls, le := lineStart(d.src, from), lineEnd(d.src, to)
		if !isBlank(d.src[ls:from]) || !restIsComment(d.src[to:le]) {
			return nil, errByHand("a declaration that shares its line")
		}
		if cf, _, _, ok := d.commentRun(from); ok {
			ls = cf
		}
		spans = append(spans, lineSpan{ls, le})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].from < spans[j].from })
	var merged []lineSpan
	for _, s := range spans {
		if n := len(merged); n > 0 && s.from <= merged[n-1].to {
			merged[n-1].to = max(merged[n-1].to, s.to)
			continue
		}
		merged = append(merged, s)
	}
	ss := make([]splice, 0, len(merged))
	for _, s := range merged {
		prevBlank := s.from == 0 || isBlank(d.src[lineStart(d.src, s.from-1):s.from])
		nextBlank := s.to == len(d.src) || isBlank(d.src[s.to:lineEnd(d.src, s.to)])
		switch {
		case prevBlank && nextBlank && s.to < len(d.src):
			s.to = lineEnd(d.src, s.to)
		case prevBlank && s.to == len(d.src) && s.from > 0:
			s.from = lineStart(d.src, s.from-1)
		}
		ss = append(ss, splice{s.from, s.to, ""})
	}
	return ss, nil
}

func restIsComment(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || strings.HasPrefix(s, "#")
}

func (d *doc) opReverse(id string) ([]splice, error) {
	e, err := d.mustEdge(id)
	if err != nil {
		return nil, err
	}
	if len(e.References) != 1 {
		return nil, errByHand("a transition declared in several places")
	}
	mk := edgeDecl(e)
	if len(mk.Edges) != 1 || mk.EdgeIndex != nil || mk.EdgeKey != nil {
		return nil, errByHand("a transition chain")
	}
	ae := mk.Edges[0]
	sf, st := rng(ae.Src)
	df, dt := rng(ae.Dst)
	return []splice{{sf, st, d.src[df:dt]}, {df, dt, d.src[sf:st]}}, nil
}

// handleEdit is a pure function of the client's buffer: it never touches the
// file, the client applies the result and autosaves it.
func handleEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Text string `json:"text"`
		Op   editOp `json:"op"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out, sel, err := applyOp(req.Text, req.Op)
	var oe *opError
	if errors.As(err, &oe) {
		writeJSON(w, http.StatusUnprocessableEntity, oe)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": out, "select": sel})
}
