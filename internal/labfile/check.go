// Package labfile is what the web UI's builder does with a topology file
// as text: check it as `parker validate` does (with its merged and
// expanded forms), and patch it in place so that comments, anchors and
// key order survive an edit made on the canvas.
package labfile

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/Ranjandas/parker/internal/compat"
	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
)

// MaxSize bounds a topology given as text.
const MaxSize = 1 << 20

// Problem is one error in a file, with the line and path it names.
type Problem struct {
	Message string `json:"message"`
	Line    int    `json:"line,omitempty"`
	Path    string `json:"path,omitempty"`
}

// Result is a checked file.
type Result struct {
	OK       bool       `json:"ok"`
	Problems []Problem  `json:"problems,omitempty"`
	Warnings []string   `json:"warnings,omitempty"`
	Name     string     `json:"name,omitempty"`
	Nodes    int        `json:"nodes"`
	Networks int        `json:"networks"`
	Merged   string     `json:"merged,omitempty"`   // imports and templates applied, vars substituted
	Expanded string     `json:"expanded,omitempty"` // the low-level form `parker up` runs
	Low      *spec.File `json:"-"`
}

// Check loads, expands and checks text as the file at path (which places
// relative imports and file: refs; it need not exist). A file that
// doesn't load is a normal result, with problems.
func Check(text, path string) Result {
	var r Result
	fail := func(err error) Result {
		r.Problems = Problems(clean(err, path))
		return r
	}
	if len(text) > MaxSize {
		return fail(errors.New("the file is over 1 MiB"))
	}
	if path == "" {
		path = filepath.Join(os.TempDir(), "lab.yaml")
	}
	l := spec.Layers{Base: []byte(text)}
	f, err := spec.LoadWith(path, l)
	if err != nil {
		return fail(err)
	}
	merged, err := spec.Merged(path, l)
	if err != nil {
		return fail(err)
	}
	low, err := expand.Expand(f)
	if err != nil {
		return fail(err)
	}
	ex, err := yaml.MarshalWithOptions(low, yaml.UseLiteralStyleIfMultiline(true), yaml.IndentSequence(true))
	if err != nil {
		return fail(err)
	}
	if err := netsetup.CheckSubnets(low.Name, low); err != nil {
		r.Warnings = append(r.Warnings, err.Error())
	}
	r.Warnings = append(r.Warnings, compat.Warnings(low)...)
	r.OK, r.Name, r.Low = true, low.Name, low
	r.Nodes, r.Networks = len(low.Nodes), len(low.Networks)
	r.Merged, r.Expanded = string(merged), string(ex)
	return r
}

var (
	// goccy/go-yaml positions look like [12:5]; Parker's own errors name
	// a line as "(line 12)".
	lineRe = regexp.MustCompile(`\[(\d+):\d+\]|\(line (\d+)\)|line (\d+)`)
	// goccy/go-yaml quotes the source under its errors: "   3 | key: x".
	excerptRe = regexp.MustCompile(`\n\s*>?\s*\d+ \|`)
	// The first dotted path into the file that an error names.
	pathRe = regexp.MustCompile(`\b((?:consul|networks|nodes|versions|scenario|imports|vars)(?:\.[A-Za-z0-9_\-]+|\[\d+\])+)`)
)

type cleaned struct{ msg string }

func (c cleaned) Error() string { return c.msg }

// clean drops the file's own path from an error's text: problems are
// about the text the builder shows.
func clean(err error, path string) error {
	msg := err.Error()
	if path != "" {
		msg = strings.ReplaceAll(msg, path+" (merged):", "")
		msg = strings.ReplaceAll(msg, path+":", "")
		msg = strings.ReplaceAll(msg, filepath.Dir(path)+string(filepath.Separator), "")
	}
	return cleaned{strings.TrimSpace(msg)}
}

// Problems splits an error into one problem per line (Parker joins
// several errors with newlines), each with the line and path it names.
func Problems(err error) []Problem {
	text := strings.TrimSpace(err.Error())
	text = strings.TrimSpace(strings.TrimPrefix(text, "lab.yaml:"))
	lines := strings.Split(text, "\n")
	if excerptRe.MatchString(text) {
		lines = []string{text} // a YAML error with its source excerpt stays whole
	}
	var out []Problem
	for _, l := range lines {
		if l = strings.TrimPrefix(strings.TrimSpace(l), "- "); l == "" || (len(lines) > 1 && strings.HasSuffix(l, ":")) {
			continue // a heading over the list ("invalid topology:")
		}
		p := Problem{Message: l}
		if m := lineRe.FindStringSubmatch(l); m != nil {
			for _, g := range m[1:] {
				if n, err := strconv.Atoi(g); err == nil {
					p.Line = n
					break
				}
			}
		}
		if m := pathRe.FindStringSubmatch(l); m != nil {
			p.Path = m[1]
		}
		out = append(out, p)
	}
	return out
}
