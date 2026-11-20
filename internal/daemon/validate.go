package daemon

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/compat"
	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
)

// maxValidate bounds an inline topology.
const maxValidate = 1 << 20

var (
	// goccy/go-yaml positions look like [12:5]; Parker's own errors name
	// a line as "(line 12)".
	lineRe = regexp.MustCompile(`\[(\d+):\d+\]|\(line (\d+)\)|line (\d+)`)
	// goccy/go-yaml quotes the source under its errors: "   3 | key: x".
	excerptRe = regexp.MustCompile(`\n\s*>?\s*\d+ \|`)
	// The first dotted path into the file that an error names.
	pathRe = regexp.MustCompile(`\b((?:consul|networks|nodes|versions|scenario)(?:\.[A-Za-z0-9_\-]+|\[\d+\])+)`)
)

// Validate checks inline YAML as `parker validate` does. A file that
// doesn't load or expand is a normal answer (ok false, with errors), not
// an RPC error.
func (s *Server) Validate(_ context.Context, req *connect.Request[parkerv1.ValidateRequest]) (*connect.Response[parkerv1.ValidateResponse], error) {
	return connect.NewResponse(ValidateYAML(req.Msg.Yaml)), nil
}

// ValidateYAML loads, expands and checks a topology given as text.
func ValidateYAML(text string) *parkerv1.ValidateResponse {
	out := &parkerv1.ValidateResponse{}
	fail := func(err error) *parkerv1.ValidateResponse {
		out.Errors = problems(err)
		return out
	}
	if len(text) > maxValidate {
		out.Errors = []*parkerv1.ValidateProblem{{Message: "the file is over 1 MiB"}}
		return out
	}
	dir, err := os.MkdirTemp("", "parker-validate-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "lab.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return fail(err)
	}
	f, err := spec.Load(path, nil)
	if err != nil {
		return fail(clean(err, dir))
	}
	low, err := expand.Expand(f)
	if err != nil {
		return fail(clean(err, dir))
	}
	if err := netsetup.CheckSubnets(low.Name, low); err != nil {
		out.Warnings = append(out.Warnings, err.Error())
	}
	out.Warnings = append(out.Warnings, compat.Warnings(low)...)
	out.Ok, out.Networks, out.Nodes = true, int32(len(low.Networks)), int32(len(low.Nodes))
	return out
}

type cleaned struct{ msg string }

func (c cleaned) Error() string { return c.msg }

// clean drops the temporary directory from an error's text.
func clean(err error, dir string) error {
	return cleaned{strings.ReplaceAll(err.Error(), dir+string(filepath.Separator), "")}
}

// problems splits an error into one problem per line of its first
// paragraph (Parker joins several errors with newlines), each with the
// line and path it names.
func problems(err error) []*parkerv1.ValidateProblem {
	text := strings.TrimSpace(err.Error())
	text = strings.TrimSpace(strings.TrimPrefix(text, "lab.yaml:"))
	lines := strings.Split(text, "\n")
	if excerptRe.MatchString(text) {
		lines = []string{text} // a YAML error with its source excerpt stays whole
	}
	var out []*parkerv1.ValidateProblem
	for _, l := range lines {
		if l = strings.TrimPrefix(strings.TrimSpace(l), "- "); l == "" || (len(lines) > 1 && strings.HasSuffix(l, ":")) {
			continue // a heading over the list ("invalid topology:")
		}
		p := &parkerv1.ValidateProblem{Message: l}
		if m := lineRe.FindStringSubmatch(l); m != nil {
			for _, g := range m[1:] {
				if n, err := strconv.Atoi(g); err == nil {
					p.Line = int32(n)
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
