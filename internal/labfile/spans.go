package labfile

import (
	"strconv"

	"github.com/goccy/go-yaml/ast"
)

// Spans says where each path in the file sits, as [first, last] lines
// (1-based), paths named as validate names them: consul.datacenters.dc1,
// consul.datacenters.dc1.config_entries[0]. The builder uses it to show a
// selected thing's lines and to find what a line belongs to. A file that
// doesn't parse has none.
func Spans(text string) map[string][2]int {
	out := map[string][2]int{}
	f, err := parse(text)
	if err != nil || f.root == nil {
		return out
	}
	var walk func(n ast.Node, path string, depth int)
	walk = func(n ast.Node, path string, depth int) {
		if depth > 64 {
			return
		}
		u, err := unwrap(n)
		if err != nil || u == nil {
			return
		}
		if es, ok := entries(u); ok {
			for _, e := range es {
				p := e.key
				if path != "" {
					p = path + "." + e.key
				}
				s := f.at(e.mv.Key.GetToken())
				end := f.under(s, s-f.lines[f.lineOf(s)]+1)
				if e.mv.Value != nil {
					if _, ve := f.span(e.mv.Value); ve > 0 {
						end = max(end, ve-1)
					}
				}
				out[p] = [2]int{f.lineOf(s) + 1, f.lineOf(max(end, s)) + 1}
				if e.mv.Value != nil {
					walk(e.mv.Value, p, depth+1)
				}
			}
			return
		}
		if sq, ok := u.(*ast.SequenceNode); ok {
			for i, it := range sq.Values {
				p := path + "[" + strconv.Itoa(i) + "]"
				s, e := f.span(it)
				if s < 0 {
					continue
				}
				e = max(e-1, f.under(s, s-f.lines[f.lineOf(s)]))
				out[p] = [2]int{f.lineOf(s) + 1, f.lineOf(max(e, s)) + 1}
				walk(it, p, depth+1)
			}
		}
	}
	walk(f.root, "", 0)
	return out
}

// under is the end of the last line after the one holding offset s that
// is indented at least ind (blank lines and comments in between count).
func (f *file) under(s, ind int) int {
	l0 := f.lineOf(s)
	end := f.lineEnd(l0)
	for l := l0 + 1; l < len(f.lines); l++ {
		if f.blank(l) {
			continue
		}
		if f.indentOf(l) < ind {
			break
		}
		end = f.lineEnd(l)
	}
	return end
}
