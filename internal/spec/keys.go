package spec

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// KeyError is an unknown key, with a suggestion when one is close.
type KeyError struct {
	Line, Col int
	Path      string // where the key sits, e.g. consul.datacenters.dc1
	Key       string
	Hint      string
}

func (e KeyError) Error() string {
	at := e.Key
	if e.Path != "" {
		at = e.Path + "." + e.Key
	}
	msg := fmt.Sprintf("line %d: %s: unknown key", e.Line, at)
	if e.Hint != "" {
		msg += " (" + e.Hint + ")"
	}
	return msg
}

// CheckKeys reports every key that the schema does not know, with "did you
// mean" suggestions and a hint when the key belongs at another level.
// Passthrough maps (config:, register:, vars:, ...) are not checked. A YAML
// syntax error returns nil; the decoder reports it better.
func CheckKeys(text []byte) []KeyError {
	f, err := parser.ParseBytes(text, 0)
	if err != nil {
		return nil
	}
	var out []KeyError
	for _, d := range f.Docs {
		if d.Body != nil {
			walk(d.Body, reflect.TypeFor[File](), "", &out)
		}
	}
	return out
}

func walk(n ast.Node, t reflect.Type, path string, out *[]KeyError) {
	n = unwrap(n)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		pairs := mappingPairs(n)
		if pairs == nil {
			return
		}
		fields := structFields(t)
		for _, kv := range pairs {
			if kv.Key.IsMergeKey() {
				continue
			}
			k := keyString(kv.Key)
			ft, ok := fields[k]
			if !ok {
				tok := kv.Key.GetToken()
				*out = append(*out, KeyError{
					Line: tok.Position.Line, Col: tok.Position.Column,
					Path: path, Key: k, Hint: hint(k, t),
				})
				continue
			}
			walk(kv.Value, ft, join(path, k), out)
		}
	case reflect.Map:
		if t.Key().Kind() != reflect.String || !typed(t.Elem()) {
			return // passthrough
		}
		for _, kv := range mappingPairs(n) {
			if kv.Key.IsMergeKey() {
				continue
			}
			walk(kv.Value, t.Elem(), join(path, keyString(kv.Key)), out)
		}
	}
}

// typed reports whether values of t have a schema of their own to check.
func typed(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return true
	case reflect.Map:
		return t.Key().Kind() == reflect.String && typed(t.Elem())
	}
	return false
}

func unwrap(n ast.Node) ast.Node {
	for {
		switch v := n.(type) {
		case *ast.AnchorNode:
			n = v.Value
		case *ast.TagNode:
			n = v.Value
		default:
			return n
		}
	}
}

func mappingPairs(n ast.Node) []*ast.MappingValueNode {
	switch v := n.(type) {
	case *ast.MappingNode:
		return v.Values
	case *ast.MappingValueNode:
		return []*ast.MappingValueNode{v}
	}
	return nil
}

func keyString(k ast.MapKeyNode) string {
	if s, ok := k.(*ast.StringNode); ok {
		return s.Value
	}
	if mk, ok := k.(*ast.MappingKeyNode); ok && mk.Value != nil {
		return mk.Value.GetToken().Value
	}
	return k.GetToken().Value
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

// structFields maps YAML key to field type, flattening inline structs.
func structFields(t reflect.Type) map[string]reflect.Type {
	m := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && (name == "" || strings.Contains(opts, "inline")) {
			for k, v := range structFields(f.Type) {
				m[k] = v
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		m[name] = f.Type
	}
	return m
}

// hint suggests a near-miss key at this level, else where the key belongs.
func hint(k string, t reflect.Type) string {
	if where := keyIndex()[k]; len(where) > 0 {
		if len(where) > 3 {
			where = where[:3]
		}
		return fmt.Sprintf("%s belongs under %s", k, strings.Join(where, " or "))
	}
	type cand struct {
		name string
		d    int
	}
	var cs []cand
	for name := range structFields(t) {
		if d := editDistance(k, name); d <= maxTypos(k) {
			cs = append(cs, cand{name, d})
		}
	}
	slices.SortFunc(cs, func(a, b cand) int {
		if a.d != b.d {
			return a.d - b.d
		}
		return strings.Compare(a.name, b.name)
	})
	if len(cs) > 2 {
		cs = cs[:2]
	}
	var qs []string
	for _, c := range cs {
		qs = append(qs, strconv.Quote(c.name))
	}
	if len(qs) > 0 {
		return "did you mean " + strings.Join(qs, " or ") + "?"
	}
	return ""
}

func maxTypos(k string) int {
	switch {
	case len(k) <= 3:
		return 1
	case len(k) <= 8:
		return 2
	}
	return 3
}

// placeholders name the free-form map keys in "belongs under" hints.
var placeholders = map[string]string{
	"datacenters": "<dc>",
	"nodes":       "<node>",
	"services":    "<service>",
	"processes":   "<process>",
	"networks":    "<network>",
	"depends_on":  "<process>",
	"overrides":   "<server>",
}

// keyIndex maps every known key to the places it is valid, shortest first.
var keyIndex = sync.OnceValue(func() map[string][]string {
	idx := map[string][]string{}
	var visit func(t reflect.Type, path string, depth int)
	visit = func(t reflect.Type, path string, depth int) {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if depth > 8 {
			return
		}
		switch t.Kind() {
		case reflect.Struct:
			where := path
			if where == "" {
				where = "the top level"
			}
			for k, ft := range structFields(t) {
				if !slices.Contains(idx[k], where) {
					idx[k] = append(idx[k], where)
				}
				visit(ft, join(path, k), depth+1)
			}
		case reflect.Map:
			if !typed(t.Elem()) {
				return
			}
			last := path[strings.LastIndex(path, ".")+1:]
			ph := placeholders[last]
			if ph == "" {
				ph = "<name>"
			}
			visit(t.Elem(), join(path, ph), depth+1)
		}
	}
	visit(reflect.TypeFor[File](), "", 0)
	for k := range idx {
		slices.SortFunc(idx[k], func(a, b string) int {
			if c := strings.Count(a, ".") - strings.Count(b, "."); c != 0 {
				return c
			}
			return strings.Compare(a, b)
		})
	}
	return idx
})

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
