package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

var inputRef = regexp.MustCompile(`\$\{inputs\.([A-Za-z_][A-Za-z0-9_]*)\}`)

// maxImportDepth bounds modules importing modules.
const maxImportDepth = 8

// needsTree reports whether a file's text uses imports or templates, which
// are resolved on the merged tree.
func needsTree(doc any) bool {
	if lookup(doc, []string{"imports"}) != nil || lookup(doc, []string{"consul", "templates"}) != nil {
		return true
	}
	dcs, _ := lookup(doc, []string{"consul", "datacenters"}).(yaml.MapSlice)
	for _, dc := range dcs {
		if usesFrom(lookup(dc.Value, []string{"nodes"})) {
			return true
		}
		parts, _ := lookup(dc.Value, []string{"partitions"}).(yaml.MapSlice)
		for _, p := range parts {
			if usesFrom(lookup(p.Value, []string{"nodes"})) {
				return true
			}
		}
	}
	return false
}

func usesFrom(nodes any) bool {
	ns, _ := nodes.(yaml.MapSlice)
	return slices.ContainsFunc(ns, func(n yaml.MapItem) bool { return lookup(n.Value, []string{"from"}) != nil })
}

// resolveImports merges the modules doc imports under it (the importer's
// values win) and drops imports:. dir is the importing file's directory.
func resolveImports(doc any, dir string, vars map[string]string, chain []string) (any, error) {
	imps, _ := lookup(doc, []string{"imports"}).([]any)
	if lookup(doc, []string{"imports"}) != nil && imps == nil {
		return nil, fmt.Errorf("imports: want a list of {module: PATH, with: {...}}")
	}
	var base any
	for i, raw := range imps {
		at := fmt.Sprintf("imports[%d]", i)
		m, ok := raw.(yaml.MapSlice)
		if !ok {
			return nil, fmt.Errorf("%s: want {module: PATH, with: {...}}", at)
		}
		path, _ := lookup(m, []string{"module"}).(string)
		if path == "" {
			return nil, fmt.Errorf("%s: module: required", at)
		}
		with, _ := lookup(m, []string{"with"}).(yaml.MapSlice)
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		provided, err := loadModule(path, with, vars, chain)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		base = Merge(base, provided)
	}
	if ms, ok := doc.(yaml.MapSlice); ok {
		doc = slices.DeleteFunc(slices.Clone(ms), func(it yaml.MapItem) bool { return mapKey(it.Key) == "imports" })
	}
	if base == nil {
		return doc, nil
	}
	return Merge(base, doc), nil
}

// loadModule reads a module file, checks it, substitutes its inputs and
// returns what it provides, with its own imports resolved.
func loadModule(path string, with yaml.MapSlice, vars map[string]string, chain []string) (any, error) {
	abs, _ := filepath.Abs(path)
	if slices.Contains(chain, abs) {
		return nil, fmt.Errorf("%s: imports itself (%s)", path, strings.Join(append(chain, abs), " -> "))
	}
	if len(chain) >= maxImportDepth {
		return nil, fmt.Errorf("%s: imports nest deeper than %d", path, maxImportDepth)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if errs := CheckModuleKeys(inputRef.ReplaceAll(raw, []byte("0"))); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return nil, fmt.Errorf("%s:\n  - %s", path, strings.Join(msgs, "\n  - "))
	}
	var head struct {
		Parker string                  `yaml:"parker"`
		Kind   string                  `yaml:"kind"`
		Inputs map[string]*ModuleInput `yaml:"inputs"`
	}
	// ${inputs.x} may sit where YAML can't parse it yet (a flow map), so
	// read the header with placeholders.
	if err := yaml.Unmarshal(inputRef.ReplaceAll(raw, []byte("0")), &head); err != nil {
		return nil, fmt.Errorf("%s:\n%s", path, yaml.FormatError(err, false, true))
	}
	if head.Parker != SchemaVersion || head.Kind != "Module" {
		return nil, fmt.Errorf("%s: a module starts with parker: %s and kind: Module", path, SchemaVersion)
	}
	inputs, err := moduleInputs(head.Inputs, with)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var missing []string
	text := inputRef.ReplaceAllFunc(raw, func(m []byte) []byte {
		name := string(inputRef.FindSubmatch(m)[1])
		if v, ok := inputs[name]; ok {
			return []byte(v)
		}
		missing = append(missing, string(m))
		return m
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s: undeclared inputs: %s", path, strings.Join(missing, ", "))
	}
	if text, err = substitute(path, text, vars); err != nil {
		return nil, err
	}
	var v any
	if err := yaml.UnmarshalWithOptions(text, &v, yaml.UseOrderedMap()); err != nil {
		return nil, fmt.Errorf("%s:\n%s", path, yaml.FormatError(err, false, true))
	}
	provides := lookup(dedupe(v), []string{"provides"})
	if provides == nil {
		return nil, nil
	}
	if _, ok := provides.(yaml.MapSlice); !ok {
		return nil, fmt.Errorf("%s: provides: want a map", path)
	}
	return resolveImports(provides, filepath.Dir(path), vars, append(chain, abs))
}

// moduleInputs checks with: against the declared inputs and returns each
// input's text.
func moduleInputs(decl map[string]*ModuleInput, with yaml.MapSlice) (map[string]string, error) {
	got := map[string]any{}
	for _, it := range with {
		k := mapKey(it.Key)
		if _, ok := decl[k]; !ok {
			names := make([]string, 0, len(decl))
			for n := range decl {
				names = append(names, n)
			}
			slices.Sort(names)
			return nil, fmt.Errorf("with.%s: no such input (inputs: %s)", k, strings.Join(names, ", "))
		}
		got[k] = it.Value
	}
	out := map[string]string{}
	for name, in := range decl {
		if in == nil {
			in = &ModuleInput{}
		}
		v, ok := got[name]
		if !ok {
			if in.Default == nil {
				return nil, fmt.Errorf("input %s: required (set it under with:)", name)
			}
			v = in.Default
		}
		s := fmt.Sprint(v)
		switch in.Type {
		case "", "string":
		case "number":
			if _, err := strconv.ParseFloat(s, 64); err != nil {
				return nil, fmt.Errorf("input %s: want a number, got %q", name, s)
			}
		case "bool":
			if s != "true" && s != "false" {
				return nil, fmt.Errorf("input %s: want true or false, got %q", name, s)
			}
		default:
			return nil, fmt.Errorf("input %s: type: want string, number or bool", name)
		}
		if len(in.Enum) > 0 && !slices.ContainsFunc(in.Enum, func(e any) bool { return fmt.Sprint(e) == s }) {
			opts := make([]string, len(in.Enum))
			for i, e := range in.Enum {
				opts[i] = fmt.Sprint(e)
			}
			return nil, fmt.Errorf("input %s: want one of %s, got %q", name, strings.Join(opts, ", "), s)
		}
		out[name] = s
	}
	return out, nil
}

// resolveTemplates replaces each node's from: NAME with the template
// merged under the node, and drops consul.templates.
func resolveTemplates(doc any) (any, error) {
	tmpls, _ := lookup(doc, []string{"consul", "templates"}).(yaml.MapSlice)
	resolved := map[string]any{}
	var resolve func(name string, chain []string) (any, error)
	resolve = func(name string, chain []string) (any, error) {
		if v, ok := resolved[name]; ok {
			return v, nil
		}
		if slices.Contains(chain, name) {
			return nil, fmt.Errorf("consul.templates: %s comes from itself (%s)", name, strings.Join(append(chain, name), " -> "))
		}
		i := slices.IndexFunc(tmpls, func(it yaml.MapItem) bool { return mapKey(it.Key) == name })
		if i < 0 {
			names := make([]string, len(tmpls))
			for j, t := range tmpls {
				names[j] = mapKey(t.Key)
			}
			if len(names) == 0 {
				return nil, fmt.Errorf("no template %q (add it under consul.templates)", name)
			}
			return nil, fmt.Errorf("no template %q (templates: %s)", name, strings.Join(names, ", "))
		}
		v, err := applyFrom(tmpls[i].Value, func(n string) (any, error) { return resolve(n, append(chain, name)) })
		if err != nil {
			return nil, err
		}
		resolved[name] = v
		return v, nil
	}
	dcs, _ := lookup(doc, []string{"consul", "datacenters"}).(yaml.MapSlice)
	for di := range dcs {
		dc, _ := dcs[di].Value.(yaml.MapSlice)
		where := "consul.datacenters." + mapKey(dcs[di].Key)
		for _, at := range nodeMaps(dc) {
			nodes, _ := lookup(dc, at).(yaml.MapSlice)
			for ni := range nodes {
				v, err := applyFrom(nodes[ni].Value, func(n string) (any, error) { return resolve(n, nil) })
				if err != nil {
					return nil, fmt.Errorf("%s.%s.%s: %w", where, strings.Join(at, "."), mapKey(nodes[ni].Key), err)
				}
				nodes[ni].Value = v
			}
		}
	}
	if c, ok := lookup(doc, []string{"consul"}).(yaml.MapSlice); ok {
		i := slices.IndexFunc(c, func(it yaml.MapItem) bool { return mapKey(it.Key) == "templates" })
		if i >= 0 {
			doc, _ = setPath(doc, []string{"consul"}, slices.Delete(slices.Clone(c), i, i+1), "")
		}
	}
	return doc, nil
}

// nodeMaps are the paths of a datacenter's nodes: maps.
func nodeMaps(dc yaml.MapSlice) [][]string {
	out := [][]string{{"nodes"}}
	parts, _ := lookup(dc, []string{"partitions"}).(yaml.MapSlice)
	for _, p := range parts {
		out = append(out, []string{"partitions", mapKey(p.Key), "nodes"})
	}
	return out
}

// applyFrom merges node over the template its from: names.
func applyFrom(node any, template func(string) (any, error)) (any, error) {
	m, ok := node.(yaml.MapSlice)
	if !ok {
		return node, nil
	}
	i := slices.IndexFunc(m, func(it yaml.MapItem) bool { return mapKey(it.Key) == "from" })
	if i < 0 {
		return node, nil
	}
	name, ok := m[i].Value.(string)
	if !ok || name == "" {
		return nil, fmt.Errorf("from: want a template name")
	}
	t, err := template(name)
	if err != nil {
		return nil, err
	}
	return Merge(t, slices.Delete(slices.Clone(m), i, i+1)), nil
}
