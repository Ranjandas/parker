package spec

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
)

var varRef = regexp.MustCompile(`\$\{(vars|env)\.([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads a topology file, applies ${vars.x} / ${env.X} substitution
// (with overrides from --var), decodes it strictly and validates it.
// Runtime variables such as ${run_dir} are left in place.
func Load(path string, overrides map[string]string) (*File, error) {
	return LoadWith(path, Layers{Vars: overrides})
}

// Layers are what goes on top of a base file: overlay files merged in
// order, --var values and --set paths, applied last.
type Layers struct {
	Overlays []string
	Vars     map[string]string
	Sets     []string
	// Base is the base file's text, when it isn't on disk (the web UI's
	// builder): the path still places relative imports and file: refs.
	Base []byte
}

func (l Layers) empty() bool { return len(l.Overlays) == 0 && len(l.Sets) == 0 }

// LoadWith is Load with overlays and --set.
func LoadWith(path string, l Layers) (*File, error) {
	text, merged, err := merge(path, l)
	if err != nil {
		return nil, err
	}
	label := path
	if merged {
		label = path + " (merged)"
	}
	return decode(path, label, text)
}

// Merged is the base file with vars substituted, imports, overlays,
// --set and templates applied: the input LoadWith decodes. Without any of
// those it is the base file's own text.
func Merged(path string, l Layers) ([]byte, error) {
	text, _, err := merge(path, l)
	return text, err
}

// merge is Merged, and whether anything was merged (so lines in the text
// match no file).
func merge(path string, l Layers) ([]byte, bool, error) {
	paths := append([]string{path}, l.Overlays...)
	raws := make([][]byte, len(paths))
	// Pass 1: the vars blocks; a later file's value wins, --var over all.
	vars := map[string]string{}
	for i, p := range paths {
		raw, err := l.Base, error(nil)
		if i > 0 || raw == nil {
			raw, err = os.ReadFile(p)
		}
		if err != nil {
			return nil, false, err
		}
		raws[i] = raw
		var head struct {
			Vars map[string]any `yaml:"vars"`
		}
		if err := yaml.Unmarshal(raw, &head); err != nil {
			return nil, false, fmt.Errorf("%s:\n%s", p, yaml.FormatError(err, false, true))
		}
		for k, v := range head.Vars {
			vars[k] = fmt.Sprint(v)
		}
	}
	for k, v := range l.Vars {
		vars[k] = v
	}
	texts := make([][]byte, len(paths))
	for i, raw := range raws {
		t, err := substitute(paths[i], raw, vars)
		if err != nil {
			return nil, false, err
		}
		texts[i] = t
	}
	trees := make([]any, len(texts))
	tree := !l.empty()
	for i, t := range texts {
		var v any
		if err := yaml.UnmarshalWithOptions(t, &v, yaml.UseOrderedMap()); err != nil {
			return nil, false, fmt.Errorf("%s:\n%s", paths[i], yaml.FormatError(err, false, true))
		}
		trees[i] = dedupe(v)
		tree = tree || needsTree(trees[i])
	}
	if !tree {
		return texts[0], false, nil
	}
	var doc any
	for i, t := range texts {
		// Unknown keys are reported against the file that has them.
		if errs := CheckKeys(t); len(errs) > 0 {
			msgs := make([]string, len(errs))
			for j, e := range errs {
				msgs[j] = e.Error()
			}
			return nil, false, fmt.Errorf("%s:\n  - %s", paths[i], strings.Join(msgs, "\n  - "))
		}
		v := trees[i]
		if v == nil {
			continue
		}
		if _, ok := v.(yaml.MapSlice); !ok {
			return nil, false, fmt.Errorf("%s: want a YAML map at the top", paths[i])
		}
		v, err := resolveImports(v, filepath.Dir(paths[i]), vars, nil)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", paths[i], err)
		}
		doc = Merge(doc, v)
	}
	for _, set := range l.Sets {
		var err error
		if doc, err = ApplySet(doc, set); err != nil {
			return nil, false, err
		}
	}
	doc, err := resolveTemplates(doc)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	out, err := yaml.MarshalWithOptions(doc, yaml.UseLiteralStyleIfMultiline(true), yaml.IndentSequence(true))
	return out, true, err
}

func substitute(path string, raw []byte, vars map[string]string) ([]byte, error) {
	var missing []string
	text := varRef.ReplaceAllFunc(raw, func(m []byte) []byte {
		sub := varRef.FindSubmatch(m)
		kind, name := string(sub[1]), string(sub[2])
		switch kind {
		case "vars":
			if v, ok := vars[name]; ok {
				return []byte(v)
			}
		case "env":
			if v, ok := os.LookupEnv(name); ok {
				return []byte(v)
			}
		}
		missing = append(missing, string(m))
		return m
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s: undefined variables: %s", path, strings.Join(missing, ", "))
	}
	return text, nil
}

// decode checks, decodes and validates text; path places relative files
// and names the lab, label prefixes errors.
func decode(path, label string, text []byte) (*File, error) {
	if errs := CheckKeys(text); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return nil, fmt.Errorf("%s:\n  - %s", label, strings.Join(msgs, "\n  - "))
	}
	var f File
	if err := yaml.NewDecoder(bytes.NewReader(text), yaml.Strict()).Decode(&f); err != nil {
		return nil, fmt.Errorf("%s:\n%s", label, yaml.FormatError(err, false, true))
	}
	if f.Name == "" {
		f.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if l := f.License; l != nil {
		resolve := func(p *string) {
			if *p == "" {
				return
			}
			if rest, ok := strings.CutPrefix(*p, "~/"); ok {
				if home, err := os.UserHomeDir(); err == nil {
					*p = filepath.Join(home, rest)
				}
			}
			if !filepath.IsAbs(*p) {
				*p = filepath.Join(filepath.Dir(path), *p)
			}
		}
		resolve(&l.File)
		for _, lf := range []*LicenseFile{l.Consul, l.Vault} {
			if lf != nil {
				resolve(&lf.File)
			}
		}
	}
	if err := resolveConfigEntries(&f, text, filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	if label != path && f.Consul != nil {
		// Lines in merged text match no file the user wrote.
		drop := func(srcs []EntrySource) {
			for i := range srcs {
				if srcs[i].File == "" {
					srcs[i].Line = 0
				}
			}
		}
		for _, dc := range f.Consul.Datacenters {
			if dc != nil {
				drop(dc.EntrySources)
				for _, p := range dc.Partitions {
					if p != nil {
						drop(p.EntrySources)
					}
				}
			}
		}
	}
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return &f, nil
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Validate does the structural and semantic checks the prototype supports.
func (f *File) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if f.Parker != SchemaVersion {
		add("parker: must be %q (got %q)", SchemaVersion, f.Parker)
	}
	if l := f.License; l != nil {
		switch {
		case l.File != "" && l.Consul != nil:
			add("license: file is Consul's license; set it or consul: {file: ...}, not both")
		case l.File == "" && l.Consul == nil && l.Vault == nil:
			add("license: give {consul: {file: PATH}} or {vault: {file: PATH}} (or drop license: and set CONSUL_LICENSE_PATH or VAULT_LICENSE_PATH)")
		}
		for p, lf := range map[string]*LicenseFile{"consul": l.Consul, "vault": l.Vault} {
			if lf != nil && lf.File == "" {
				add("license.%s.file: required (or drop it and set %s_LICENSE_PATH)", p, strings.ToUpper(p))
			}
		}
	}
	if !nameRe.MatchString(f.Name) || len(f.Name) > 24 {
		add("name: %q must be lowercase letters, digits and '-', at most 24 chars", f.Name)
	}
	f.validateVault(add)
	if f.Consul != nil {
		if f.Versions["consul"] == "" {
			add("versions.consul: required when a consul: section is present")
		}
		if len(f.Consul.Datacenters) == 0 {
			add("consul.datacenters: at least one datacenter is required")
		}
		if fed := f.Consul.Federation; fed != nil {
			switch fed.Mode {
			case FederationWAN, FederationMeshGateways:
				if len(f.Consul.Datacenters) < 2 {
					add("consul.federation: needs at least two datacenters")
				}
			default:
				add("consul.federation.mode: must be wan or mesh-gateways")
			}
		}
		primaries := 0
		for _, dc := range f.Consul.Datacenters {
			if dc != nil && dc.Primary != nil && *dc.Primary {
				primaries++
			}
		}
		if primaries > 1 {
			add("consul.datacenters: only one datacenter can be primary: true")
		}
		for name, dc := range f.Consul.Datacenters {
			p := "consul.datacenters." + name
			if !nameRe.MatchString(name) {
				add("%s: invalid datacenter name", p)
			}
			if dc == nil {
				add("%s: empty datacenter (set servers:)", p)
				continue
			}
			for i, e := range dc.ConfigEntries {
				if _, isFile := e["file"]; !isFile && entryKind(e) == "" {
					add("%s: needs Kind (or file:)", dc.EntryLabel(name, i))
				}
			}
			if len(dc.Partitions) > 0 && (len(dc.Nodes) > 0 || len(dc.Gateways) > 0) {
				add("%s: nodes: and gateways: can't sit beside partitions:; move them under partitions.default", p)
			}
			for pn, part := range dc.Partitions {
				pp := p + ".partitions." + pn
				if !nameRe.MatchString(pn) || len(pn) > 32 {
					add("%s: partition names are lowercase letters, digits and '-', at most 32 chars", pp)
				}
				if part == nil {
					add("%s: empty partition (add nodes:)", pp)
					continue
				}
				for i, e := range part.ConfigEntries {
					if _, isFile := e["file"]; !isFile && entryKind(e) == "" {
						add("%s: needs Kind (or file:)", part.EntryLabel(name, pn, i))
					}
				}
			}
			if dc.Servers.Count < 1 {
				add("%s.servers: at least 1 server is required", p)
			}
			for o := range dc.Servers.Overrides {
				if !strings.HasPrefix(o, "server-") {
					add("%s.servers.overrides.%s: keys are server names like server-2", p, o)
				}
			}
			for _, placed := range dc.AllNodes() {
				nn, n := placed.Name, placed.Node
				np := p + ".nodes." + nn
				if placed.Part != nil {
					np = p + ".partitions." + placed.Partition + ".nodes." + nn
				}
				if !nameRe.MatchString(nn) {
					add("%s: invalid node name", np)
				}
				if strings.HasPrefix(nn, "server-") {
					add("%s: the name server-* is reserved for servers", np)
				}
				if n == nil {
					continue
				}
				switch n.Agent {
				case "", "client", "none":
				case "dataplane":
					if v := f.Versions["consul-dataplane"]; v == "" {
						add("%s.agent: dataplane needs versions.consul-dataplane (e.g. 1.9.12) and versions.envoy", np)
					}
					if len(n.Config) > 0 {
						add("%s.config: a dataplane node has no agent to configure", np)
					}
				default:
					add("%s.agent: must be client, dataplane or none", np)
				}
				if n.Service != nil && len(n.Services) > 0 {
					add("%s: use service: or services:, not both", np)
				}
			}
		}
	}
	if f.Consul.UsesTenancy() {
		if v := f.Versions["consul"]; !strings.Contains(v, "+ent") && !strings.HasPrefix(v, "path:") {
			add("partitions and namespaces need Consul Enterprise: pin versions.consul to an Enterprise build such as %s+ent", strings.TrimPrefix(v, "v"))
		}
		if f.Consul.FederationMode() != "" {
			add("consul: partitions and namespaces don't work with federation in Parker yet; use consul.peering between partitions instead")
		}
	}
	for nn, n := range f.Nodes {
		if n == nil {
			add("nodes.%s: empty", nn)
			continue
		}
		for net := range n.Networks {
			// <dc>-lan networks are generated from consul.datacenters.
			if dc, ok := strings.CutSuffix(net, "-lan"); ok && f.Consul != nil && f.Consul.Datacenters[dc] != nil {
				continue
			}
			if net == "wan" && f.Consul.UsesWAN() {
				continue
			}
			if _, ok := f.Networks[net]; !ok {
				add("nodes.%s.networks.%s: no such network", nn, net)
			}
		}
		for pn, p := range n.Processes {
			if len(p.Run) == 0 {
				add("nodes.%s.processes.%s.run: required", nn, pn)
			}
			switch p.Restart {
			case "", "no", "on-failure", "always":
			default:
				add("nodes.%s.processes.%s.restart: must be no, on-failure or always", nn, pn)
			}
			for dep, d := range p.DependsOn {
				dn, dp, ok := strings.Cut(dep, "/")
				if !ok {
					dn, dp = nn, dep
				}
				if tn, ok := f.Nodes[dn]; !ok || tn.Processes[dp] == nil {
					add("nodes.%s.processes.%s.depends_on.%s: no such process", nn, pn, dep)
				}
				if d != nil && d.Condition != "" && d.Condition != "started" && d.Condition != "ready" {
					add("nodes.%s.processes.%s.depends_on.%s.condition: started or ready", nn, pn, dep)
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid topology:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}
