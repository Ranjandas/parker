package labfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/goccy/go-yaml/token"
)

// Op is one edit to a file, at a path of keys (strings) and list indexes
// (numbers):
//
//	set     path = value (adds the key, and any missing parents)
//	delete  path
//	rename  path's key to key
//	append  value to the list at path (made if missing)
//	remove  the list item at path
type Op struct {
	Op    string          `json:"op"`
	Path  []any           `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
	Key   string          `json:"key,omitempty"`
}

// Patch applies ops to text by splicing it, so that everything the ops
// don't touch (comments, anchors, flow or block style, key order) stays
// as it was. Each result is checked: it must decode to what applying the
// op to the decoded file gives, or Patch refuses (say, a value shared
// through an anchor).
func Patch(text string, ops []Op) (string, error) {
	for i, op := range ops {
		var v any
		if len(op.Value) > 0 {
			var err error
			if v, err = decodeOrdered(op.Value); err != nil {
				return "", fmt.Errorf("op %d: value: %w", i, err)
			}
		}
		next, err := apply(text, op, v)
		if err != nil {
			return "", fmt.Errorf("%s %s: %w", op.Op, pathString(op.Path), err)
		}
		if err := verify(text, next, op, v); err != nil {
			return "", fmt.Errorf("%s %s: %w", op.Op, pathString(op.Path), err)
		}
		text = next
	}
	return text, nil
}

func pathString(p []any) string {
	var b strings.Builder
	for _, s := range p {
		switch s := s.(type) {
		case string:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(s)
		default:
			fmt.Fprintf(&b, "[%v]", s)
		}
	}
	return b.String()
}

// seg is a path segment: a key, or an index when key is "".
func seg(s any) (string, int, error) {
	switch s := s.(type) {
	case string:
		return s, -1, nil
	case float64:
		return "", int(s), nil
	case int:
		return "", s, nil
	case json.Number:
		n, err := s.Int64()
		return "", int(n), err
	}
	return "", 0, fmt.Errorf("bad path segment %v", s)
}

// ---------- locating ----------

type file struct {
	src   string
	lines []int // offset of each line's start
	root  ast.Node
}

func parse(text string) (*file, error) {
	af, err := parser.ParseBytes([]byte(text), parser.ParseComments)
	if err != nil {
		return nil, err
	}
	f := &file{src: text, lines: []int{0}}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			f.lines = append(f.lines, i+1)
		}
	}
	if len(af.Docs) > 0 {
		f.root = af.Docs[0].Body
	}
	return f, nil
}

// at is a token's offset in the text.
func (f *file) at(tk *token.Token) int {
	l := tk.Position.Line - 1
	if l < 0 || l >= len(f.lines) {
		return len(f.src)
	}
	o := f.lines[l] + tk.Position.Column - 1
	return min(o, len(f.src))
}

func (f *file) lineOf(off int) int {
	lo, hi := 0, len(f.lines)-1
	for lo < hi {
		m := (lo + hi + 1) / 2
		if f.lines[m] <= off {
			lo = m
		} else {
			hi = m - 1
		}
	}
	return lo
}

func (f *file) lineEnd(l int) int {
	if l+1 < len(f.lines) {
		return f.lines[l+1] - 1
	}
	return len(f.src)
}

func (f *file) indentOf(l int) int {
	s := f.src[f.lines[l]:f.lineEnd(l)]
	return len(s) - len(strings.TrimLeft(s, " "))
}

// blank tells whether line l holds nothing but spaces or a comment.
func (f *file) blank(l int) bool {
	s := strings.TrimSpace(f.src[f.lines[l]:f.lineEnd(l)])
	return s == "" || strings.HasPrefix(s, "#")
}

// entry is a key and its value inside a mapping.
type entry struct {
	mv  *ast.MappingValueNode
	key string
}

func entries(n ast.Node) ([]entry, bool) {
	var vals []*ast.MappingValueNode
	switch m := n.(type) {
	case *ast.MappingNode:
		vals = m.Values
	case *ast.MappingValueNode:
		vals = []*ast.MappingValueNode{m}
	default:
		return nil, false
	}
	out := make([]entry, 0, len(vals))
	for _, mv := range vals {
		out = append(out, entry{mv, keyString(mv.Key)})
	}
	return out, true
}

func keyString(k ast.MapKeyNode) string {
	if s, ok := k.(*ast.StringNode); ok {
		return s.Value
	}
	return strings.Trim(k.String(), `"' `)
}

// unwrap looks through anchors and tags; an alias can't be edited.
func unwrap(n ast.Node) (ast.Node, error) {
	for {
		switch x := n.(type) {
		case *ast.AnchorNode:
			n = x.Value
		case *ast.TagNode:
			n = x.Value
		case *ast.AliasNode:
			return nil, fmt.Errorf("the value is an alias (%s); edit what it points to", strings.TrimSpace(x.String()))
		default:
			return n, nil
		}
	}
}

// find walks path from the root. It returns the value node, or nil and
// how much of the path exists.
func (f *file) find(path []any) (ast.Node, int, error) {
	n := f.root
	for i, s := range path {
		if n == nil {
			return nil, i, nil
		}
		u, err := unwrap(n)
		if err != nil {
			return nil, i, err
		}
		key, idx, err := seg(s)
		if err != nil {
			return nil, i, err
		}
		if idx >= 0 {
			sq, ok := u.(*ast.SequenceNode)
			if !ok || idx >= len(sq.Values) {
				return nil, i, nil
			}
			n = sq.Values[idx]
			continue
		}
		es, ok := entries(u)
		if !ok {
			return nil, i, nil
		}
		var next ast.Node
		for _, e := range es {
			if e.key == key {
				next = e.mv.Value
				if next == nil {
					next = &ast.NullNode{}
				}
			}
		}
		if next == nil {
			return nil, i, nil
		}
		n = next
	}
	return n, len(path), nil
}

func (f *file) entryAt(path []any) (*entry, ast.Node, error) {
	parent, got, err := f.find(path[:len(path)-1])
	if err != nil || got < len(path)-1 {
		return nil, nil, err
	}
	u, err := unwrap(parent)
	if err != nil {
		return nil, nil, err
	}
	key, _, _ := seg(path[len(path)-1])
	es, _ := entries(u)
	for _, e := range es {
		if e.key == key {
			return &e, u, nil
		}
	}
	return nil, u, nil
}

// ---------- extents ----------

// span is where a value node sits in the text: [start, end).
func (f *file) span(n ast.Node) (int, int) {
	switch x := n.(type) {
	case *ast.AnchorNode:
		s := f.at(x.GetToken())
		_, e := f.span(x.Value)
		return s, e
	case *ast.TagNode:
		s := f.at(x.GetToken())
		_, e := f.span(x.Value)
		return s, e
	case *ast.MappingNode:
		if x.IsFlowStyle {
			return f.at(x.Start), f.at(x.End) + 1
		}
		return f.blockSpan(n)
	case *ast.MappingValueNode:
		return f.blockSpan(n)
	case *ast.SequenceNode:
		if x.IsFlowStyle {
			return f.at(x.Start), f.at(x.End) + 1
		}
		return f.blockSpan(n)
	case *ast.LiteralNode:
		return f.blockSpan(n)
	case *ast.NullNode:
		if x.Token == nil {
			return -1, -1
		}
	}
	s := f.at(n.GetToken())
	return s, f.scalarEnd(s)
}

// scalarEnd finds where a scalar starting at s ends.
func (f *file) scalarEnd(s int) int {
	src := f.src
	if s >= len(src) {
		return s
	}
	switch c := src[s]; c {
	case '"', '\'':
		for i := s + 1; i < len(src); i++ {
			if c == '"' && src[i] == '\\' {
				i++
				continue
			}
			if src[i] == c {
				if c == '\'' && i+1 < len(src) && src[i+1] == '\'' {
					i++
					continue
				}
				return i + 1
			}
		}
		return len(src)
	case '*':
		i := s + 1
		for i < len(src) && !strings.ContainsRune(" \t\n,]}", rune(src[i])) {
			i++
		}
		return i
	}
	// A plain scalar: to the end of the line, a comment, or (in a flow
	// collection) a separator.
	flow := f.inFlow(s)
	i := s
	for i < len(src) && src[i] != '\n' {
		if src[i] == '#' && i > s && (src[i-1] == ' ' || src[i-1] == '\t') {
			break
		}
		if flow && strings.ContainsRune(",]}", rune(src[i])) {
			break
		}
		if flow && src[i] == ':' && i+1 < len(src) && strings.ContainsRune(" \n", rune(src[i+1])) {
			break
		}
		i++
	}
	return s + len(strings.TrimRight(src[s:i], " \t"))
}

// inFlow tells whether offset o sits inside a flow collection on its line.
func (f *file) inFlow(o int) bool {
	l := f.lineOf(o)
	depth, q := 0, byte(0)
	for i := f.lines[l]; i < o; i++ {
		c := f.src[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
		case c == '#' && i > 0 && f.src[i-1] == ' ':
			return depth > 0
		}
	}
	return depth > 0
}

// blockSpan is a block node's span: from its first token through the
// last line indented under where it starts.
func (f *file) blockSpan(n ast.Node) (int, int) {
	s := f.at(n.GetToken())
	if sq, ok := n.(*ast.SequenceNode); ok && len(sq.Values) > 0 {
		s = f.at(sq.GetToken())
	}
	if mv, ok := n.(*ast.MappingValueNode); ok {
		s = f.at(mv.Key.GetToken())
	}
	if m, ok := n.(*ast.MappingNode); ok && len(m.Values) > 0 {
		s = f.at(m.Values[0].Key.GetToken())
	}
	l0 := f.lineOf(s)
	ind := s - f.lines[l0] // the node's own column
	if _, ok := n.(*ast.LiteralNode); ok {
		ind = f.indentOf(l0) // a | or > block: lines indented under its key's line
	}
	end := f.lineEnd(l0)
	for l := l0 + 1; l < len(f.lines); l++ {
		if f.blank(l) {
			continue
		}
		in := f.indentOf(l)
		line := f.src[f.lines[l]+in : f.lineEnd(l)]
		if _, isSeq := n.(*ast.SequenceNode); in > ind || (isSeq && in == ind && strings.HasPrefix(line, "-")) {
			end = f.lineEnd(l)
			continue
		}
		if _, lit := n.(*ast.LiteralNode); lit && in > ind {
			end = f.lineEnd(l)
			continue
		}
		break
	}
	return s, end
}

// ---------- rendering ----------

// render writes v as YAML: flow when it is short and shallow, else block
// at indent. It returns text to put after "key:" (with its leading space
// or newline).
func render(v any, indent int, path []any) string {
	if !isColl(v) || isEmpty(v) {
		return " " + flow(v)
	}
	if fl := flow(v); !blockAt(path) && depth(v) <= 2 && indent+len(pathKey(path))+2+len(fl) <= 80 {
		return " " + fl
	}
	return "\n" + block(v, indent+2, path)
}

func pathKey(path []any) string {
	if len(path) == 0 {
		return ""
	}
	return fmt.Sprint(path[len(path)-1])
}

var (
	blockTop  = regexp.MustCompile(`^consul(\.defaults|\.(datacenters|peering)(\.[^.]+)?)?$`)
	blockNode = regexp.MustCompile(`\.(nodes|partitions)(\.[^.]+)?$`)
)

// blockAt tells whether the map at path is written in block style, as
// the examples (and the builder) do.
func blockAt(path []any) bool {
	p := pathString(path)
	return blockTop.MatchString(p) || (blockNode.MatchString(p) && !strings.Contains(p, ".service"))
}

func isColl(v any) bool {
	switch v.(type) {
	case yaml.MapSlice, []any:
		return true
	}
	return false
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case yaml.MapSlice:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return false
}

func depth(v any) int {
	d := 0
	switch x := v.(type) {
	case yaml.MapSlice:
		for _, it := range x {
			d = max(d, depth(it.Value))
		}
		return d + 1
	case []any:
		for _, it := range x {
			d = max(d, depth(it))
		}
		return d
	}
	return 0
}

var plain = regexp.MustCompile(`^[A-Za-z0-9_./+][A-Za-z0-9_./+-]*$`)
var numberLike = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)
var keywordLike = regexp.MustCompile(`(?i)^(true|false|null|yes|no|on|off|~)$`)

func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	case string:
		if plain.MatchString(x) && !numberLike.MatchString(x) && !keywordLike.MatchString(x) {
			return x
		}
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

func flow(v any) string {
	switch x := v.(type) {
	case yaml.MapSlice:
		if len(x) == 0 {
			return "{}"
		}
		parts := make([]string, len(x))
		for i, it := range x {
			parts[i] = scalar(it.Key) + ": " + flow(it.Value)
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case []any:
		parts := make([]string, len(x))
		for i, it := range x {
			parts[i] = flow(it)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return scalar(v)
}

// block writes a collection as block lines at indent (no trailing newline).
func block(v any, indent int, path []any) string {
	pad := strings.Repeat(" ", indent)
	var lines []string
	switch x := v.(type) {
	case yaml.MapSlice:
		for _, it := range x {
			lines = append(lines, pad+scalar(it.Key)+":"+render(it.Value, indent, append(append([]any(nil), path...), fmt.Sprint(it.Key))))
		}
	case []any:
		for _, it := range x {
			lines = append(lines, item(it, indent))
		}
	}
	return strings.Join(lines, "\n")
}

// item writes one block list item at indent.
func item(v any, indent int) string {
	pad := strings.Repeat(" ", indent)
	m, ok := v.(yaml.MapSlice)
	if !ok || len(m) == 0 {
		return pad + "- " + flow(v)
	}
	if fl := flow(v); depth(v) <= 2 && indent+2+len(fl) <= 80 {
		return pad + "- " + fl
	}
	var lines []string
	for i, it := range m {
		lead := pad + "  "
		if i == 0 {
			lead = pad + "- "
		}
		lines = append(lines, lead+scalar(it.Key)+":"+render(it.Value, indent+2, []any{fmt.Sprint(it.Key)}))
	}
	return strings.Join(lines, "\n")
}

// ---------- ops ----------

func apply(text string, op Op, v any) (string, error) {
	if len(op.Path) == 0 {
		return "", errors.New("empty path")
	}
	f, err := parse(text)
	if err != nil {
		return "", err
	}
	switch op.Op {
	case "set":
		return f.set(op.Path, v)
	case "delete":
		return f.del(op.Path)
	case "rename":
		return f.rename(op.Path, op.Key)
	case "append":
		return f.appendItem(op.Path, v)
	case "remove":
		return f.removeItem(op.Path)
	}
	return "", fmt.Errorf("unknown op %q", op.Op)
}

func (f *file) splice(s, e int, with string) string {
	return f.src[:s] + with + f.src[e:]
}

func (f *file) set(path []any, v any) (string, error) {
	if _, idx, _ := seg(path[len(path)-1]); idx >= 0 {
		n, got, err := f.find(path)
		if err != nil {
			return "", err
		}
		if got < len(path) {
			return "", errors.New("no such list item")
		}
		return f.replaceItem(n, v)
	}
	e, parent, err := f.entryAt(path)
	if err != nil {
		return "", err
	}
	if e != nil {
		return f.replaceEntry(e, v, path)
	}
	if parent == nil {
		// Make the parent: set it to {key: v}.
		key, _, _ := seg(path[len(path)-1])
		return f.set(path[:len(path)-1], yaml.MapSlice{{Key: key, Value: v}})
	}
	key, _, _ := seg(path[len(path)-1])
	return f.insertKey(parent, path[:len(path)-1], key, v)
}

func isNullValue(n ast.Node) bool {
	nn, ok := n.(*ast.NullNode)
	return ok && (nn.Token == nil || nn.Token.Value == "" || nn.Token.Value == "null" || nn.Token.Value == "~")
}

// replaceEntry puts v as the value of a key, from just after its colon.
func (f *file) replaceEntry(e *entry, v any, path []any) (string, error) {
	kt := f.at(e.mv.Key.GetToken())
	col := kt - f.lines[f.lineOf(kt)]
	colon := f.at(e.mv.GetToken())
	n := e.mv.Value
	switch x := n.(type) {
	case *ast.AliasNode:
		return "", errors.New("the value is an alias; edit what it points to")
	case *ast.AnchorNode:
		// Keep the anchor; the value goes in flow style after it.
		s, end := f.span(x.Value)
		if s < 0 {
			return "", errors.New("can't place the value")
		}
		return f.splice(s, end, flow(v)), nil
	}
	end := colon + 1
	if n != nil && !isNullValue(n) {
		_, end = f.span(n)
	} else {
		end = f.scalarEndAfter(colon + 1)
	}
	if f.inFlow(colon) {
		return f.splice(colon+1, end, " "+flow(v)), nil
	}
	return f.splice(colon+1, end, render(v, col, path)), nil
}

// replaceItem puts v in place of a list item.
func (f *file) replaceItem(n ast.Node, v any) (string, error) {
	if _, ok := n.(*ast.AliasNode); ok {
		return "", errors.New("the item is an alias; edit what it points to")
	}
	s, end := f.span(n)
	if s < 0 {
		return "", errors.New("can't place the value")
	}
	if a, ok := n.(*ast.AnchorNode); ok {
		s, end = f.span(a.Value)
		return f.splice(s, end, flow(v)), nil
	}
	if f.inFlow(s) {
		return f.splice(s, end, flow(v)), nil
	}
	l0 := f.lineOf(s)
	dash := strings.Index(f.src[f.lines[l0]:s], "-")
	if dash < 0 {
		return "", errors.New("can't find the item's dash")
	}
	return f.splice(f.lines[l0], end, item(v, dash)), nil
}

// insertKey adds key: v to the mapping parent (at path).
func (f *file) insertKey(parent ast.Node, ppath []any, key string, v any) (string, error) {
	switch m := parent.(type) {
	case *ast.MappingNode:
		if m.IsFlowStyle {
			end := f.at(m.End)
			if len(m.Values) == 0 {
				return f.splice(f.at(m.Start), end+1, "{ "+scalar(key)+": "+flow(v)+" }"), nil
			}
			_, last := f.span(m.Values[len(m.Values)-1].Value)
			return f.splice(last, last, ", "+scalar(key)+": "+flow(v)), nil
		}
	case *ast.MappingValueNode:
	case *ast.NullNode:
		return f.set(ppath, yaml.MapSlice{{Key: key, Value: v}})
	default:
		return "", fmt.Errorf("%s is not a map", pathString(ppath))
	}
	// A block mapping: after its last entry, at its keys' column.
	es, _ := entries(parent)
	first := f.at(es[0].mv.Key.GetToken())
	col := first - f.lines[f.lineOf(first)]
	_, end := f.blockSpan(es[len(es)-1].mv)
	pad := strings.Repeat(" ", col)
	return f.splice(end, end, "\n"+pad+scalar(key)+":"+render(v, col, append(append([]any(nil), ppath...), key))), nil
}

func (f *file) del(path []any) (string, error) {
	if _, idx, _ := seg(path[len(path)-1]); idx >= 0 {
		return f.removeItem(path)
	}
	e, parent, err := f.entryAt(path)
	if err != nil {
		return "", err
	}
	if e == nil {
		return f.src, nil // already gone
	}
	es, _ := entries(parent)
	if m, ok := parent.(*ast.MappingNode); ok && m.IsFlowStyle {
		s := f.at(e.mv.Key.GetToken())
		_, end := f.span(e.mv.Value)
		if e.mv.Value == nil {
			end = f.at(e.mv.GetToken()) + 1
		}
		// Take the comma after, or else the one before.
		rest := f.src[end:]
		if t := strings.TrimLeft(rest, " "); strings.HasPrefix(t, ",") {
			end += len(rest) - len(t) + 1
			for end < len(f.src) && f.src[end] == ' ' {
				end++
			}
		} else {
			before := strings.TrimRight(f.src[:s], " ")
			if strings.HasSuffix(before, ",") {
				s = len(before) - 1
			}
		}
		return f.splice(s, end, ""), nil
	}
	if len(es) == 1 && len(path) > 1 {
		return f.set(path[:len(path)-1], yaml.MapSlice{})
	}
	s, end := f.blockSpan(e.mv)
	l0 := f.lineOf(s)
	start := f.lines[l0]
	if strings.TrimSpace(f.src[start:s]) != "" {
		return "", errors.New("can't remove a key that shares its line")
	}
	if end < len(f.src) {
		end++ // its newline
	}
	return f.splice(start, end, ""), nil
}

func (f *file) rename(path []any, to string) (string, error) {
	e, _, err := f.entryAt(path)
	if err != nil {
		return "", err
	}
	if e == nil {
		return "", errors.New("no such key")
	}
	s := f.at(e.mv.Key.GetToken())
	end := s + len(strings.TrimRight(f.src[s:f.at(e.mv.GetToken())], " "))
	return f.splice(s, end, scalar(to)), nil
}

func (f *file) appendItem(path []any, v any) (string, error) {
	n, got, err := f.find(path)
	if err != nil {
		return "", err
	}
	if got < len(path) || isNullValue(n) {
		return f.set(path, []any{v})
	}
	if a, ok := n.(*ast.AnchorNode); ok {
		n = a.Value
	}
	sq, ok := n.(*ast.SequenceNode)
	if !ok {
		return "", errors.New("not a list")
	}
	if sq.IsFlowStyle {
		end := f.at(sq.End)
		if len(sq.Values) == 0 {
			return f.splice(f.at(sq.Start), end+1, "["+flow(v)+"]"), nil
		}
		_, last := f.span(sq.Values[len(sq.Values)-1])
		return f.splice(last, last, ", "+flow(v)), nil
	}
	if len(sq.Values) == 0 {
		return f.set(path, []any{v})
	}
	dash := f.at(sq.GetToken())
	col := dash - f.lines[f.lineOf(dash)]
	_, end := f.blockSpan(sq)
	return f.splice(end, end, "\n"+item(v, col)), nil
}

func (f *file) removeItem(path []any) (string, error) {
	n, got, err := f.find(path[:len(path)-1])
	if err != nil {
		return "", err
	}
	_, idx, _ := seg(path[len(path)-1])
	if got < len(path)-1 {
		return f.src, nil
	}
	if a, ok := n.(*ast.AnchorNode); ok {
		n = a.Value
	}
	sq, ok := n.(*ast.SequenceNode)
	if !ok || idx < 0 || idx >= len(sq.Values) {
		return "", errors.New("no such list item")
	}
	if len(sq.Values) == 1 {
		return f.del(path[:len(path)-1])
	}
	if sq.IsFlowStyle {
		s, e := f.span(sq.Values[idx])
		rest := f.src[e:]
		if t := strings.TrimLeft(rest, " "); strings.HasPrefix(t, ",") {
			e += len(rest) - len(t) + 1
			for e < len(f.src) && f.src[e] == ' ' {
				e++
			}
		} else if before := strings.TrimRight(f.src[:s], " "); strings.HasSuffix(before, ",") {
			s = len(before) - 1
		}
		return f.splice(s, e, ""), nil
	}
	// Block: the item's lines, from its dash to before the next item.
	is, _ := f.span(sq.Values[idx])
	l0 := f.lineOf(is)
	dashCol := strings.Index(f.src[f.lines[l0]:f.lineEnd(l0)], "-")
	if dashCol < 0 || strings.TrimSpace(f.src[f.lines[l0]:f.lines[l0]+dashCol]) != "" {
		return "", errors.New("can't find the item's dash")
	}
	end := f.lineEnd(l0)
	for l := l0 + 1; l < len(f.lines); l++ {
		if f.blank(l) {
			continue
		}
		if f.indentOf(l) > dashCol {
			end = f.lineEnd(l)
			continue
		}
		break
	}
	if end < len(f.src) {
		end++
	}
	return f.splice(f.lines[l0], end, ""), nil
}

// ---------- checking ----------

// verify checks that next decodes to the decoded text with the op applied.
func verify(text, next string, op Op, v any) error {
	before, err := decodePlain(text)
	if err != nil {
		return err
	}
	after, err := decodePlain(next)
	if err != nil {
		return fmt.Errorf("the edit broke the YAML: %w", err)
	}
	want, err := applyPlain(before, op, plainOf(v))
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(after, want) {
		return errors.New("can't edit this in place (is it shared through an anchor, or merged with <<?); edit the YAML instead")
	}
	return nil
}

// decodePlain decodes YAML to plain JSON values (aliases resolved).
func decodePlain(text string) (any, error) {
	var v any
	if err := yaml.Unmarshal([]byte(text), &v); err != nil {
		return nil, err
	}
	return plainOf(v), nil
}

// plainOf normalizes a value through JSON: maps, lists, float64s.
func plainOf(v any) any {
	b, err := json.Marshal(jsonable(v))
	if err != nil {
		return v
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func jsonable(v any) any {
	switch x := v.(type) {
	case yaml.MapSlice:
		m := map[string]any{}
		for _, it := range x {
			m[fmt.Sprint(it.Key)] = jsonable(it.Value)
		}
		return m
	case map[string]any:
		m := map[string]any{}
		for k, it := range x {
			m[k] = jsonable(it)
		}
		return m
	case map[any]any:
		m := map[string]any{}
		for k, it := range x {
			m[fmt.Sprint(k)] = jsonable(it)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = jsonable(it)
		}
		return out
	}
	return v
}

func applyPlain(doc any, op Op, v any) (any, error) {
	path := op.Path
	var walk func(cur any, i int) (any, error)
	walk = func(cur any, i int) (any, error) {
		key, idx, err := seg(path[i])
		if err != nil {
			return nil, err
		}
		last := i == len(path)-1
		if idx >= 0 {
			l, _ := cur.([]any)
			if idx >= len(l) {
				return nil, errors.New("no such list item")
			}
			l = append([]any(nil), l...)
			if last {
				switch op.Op {
				case "remove", "delete":
					l = append(l[:idx], l[idx+1:]...)
					return l, nil
				case "set":
					l[idx] = v
					return l, nil
				}
			}
			nv, err := walk(l[idx], i+1)
			if err != nil {
				return nil, err
			}
			l[idx] = nv
			return l, nil
		}
		m, _ := cur.(map[string]any)
		cp := map[string]any{}
		for k, x := range m {
			cp[k] = x
		}
		if last {
			switch op.Op {
			case "set":
				cp[key] = v
			case "delete":
				delete(cp, key)
			case "rename":
				cp[op.Key] = cp[key]
				delete(cp, key)
			case "append":
				l, _ := cp[key].([]any)
				cp[key] = append(append([]any(nil), l...), v)
			}
			return cp, nil
		}
		nv, err := walk(cp[key], i+1)
		if err != nil {
			return nil, err
		}
		if l, ok := nv.([]any); ok && len(l) == 0 && op.Op == "remove" && i+1 == len(path)-1 {
			delete(cp, key) // the last item went, and its list with it
			return cp, nil
		}
		cp[key] = nv
		return cp, nil
	}
	out, err := walk(doc, 0)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// decodeOrdered decodes JSON keeping object key order (as yaml.MapSlice).
func decodeOrdered(raw json.RawMessage) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := orderedValue(d)
	if err != nil {
		return nil, err
	}
	return numbers(v), nil
}

func numbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case yaml.MapSlice:
		for i := range x {
			x[i].Value = numbers(x[i].Value)
		}
	case []any:
		for i := range x {
			x[i] = numbers(x[i])
		}
	}
	return v
}

func orderedValue(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := t.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := yaml.MapSlice{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return nil, err
				}
				v, err := orderedValue(d)
				if err != nil {
					return nil, err
				}
				m = append(m, yaml.MapItem{Key: k, Value: v})
			}
			_, err := d.Token()
			return m, err
		case '[':
			l := []any{}
			for d.More() {
				v, err := orderedValue(d)
				if err != nil {
					return nil, err
				}
				l = append(l, v)
			}
			_, err := d.Token()
			return l, err
		}
	}
	return t, nil
}

// scalarEndAfter skips spaces after o and returns the end of the scalar
// there, or o when the line ends first.
func (f *file) scalarEndAfter(o int) int {
	i := o
	for i < len(f.src) && f.src[i] == ' ' {
		i++
	}
	if i >= len(f.src) || f.src[i] == '\n' || f.src[i] == '#' {
		return o
	}
	return f.scalarEnd(i)
}
