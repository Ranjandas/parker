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
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Pass 1: just the vars block.
	var head struct {
		Vars map[string]any `yaml:"vars"`
	}
	if err := yaml.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("%s:\n%s", path, yaml.FormatError(err, false, true))
	}
	vars := map[string]string{}
	for k, v := range head.Vars {
		vars[k] = fmt.Sprint(v)
	}
	for k, v := range overrides {
		vars[k] = v
	}

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

	if errs := CheckKeys(text); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return nil, fmt.Errorf("%s:\n  - %s", path, strings.Join(msgs, "\n  - "))
	}
	var f File
	if err := yaml.NewDecoder(bytes.NewReader(text), yaml.Strict()).Decode(&f); err != nil {
		return nil, fmt.Errorf("%s:\n%s", path, yaml.FormatError(err, false, true))
	}
	if f.Name == "" {
		f.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if l := f.License; l != nil && l.File != "" {
		if rest, ok := strings.CutPrefix(l.File, "~/"); ok {
			if home, err := os.UserHomeDir(); err == nil {
				l.File = filepath.Join(home, rest)
			}
		}
		if !filepath.IsAbs(l.File) {
			l.File = filepath.Join(filepath.Dir(path), l.File)
		}
	}
	if err := resolveConfigEntries(&f, text, filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
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
	if f.License != nil && f.License.File == "" {
		add("license.file: required (or drop license: and set CONSUL_LICENSE_PATH)")
	}
	if !nameRe.MatchString(f.Name) || len(f.Name) > 24 {
		add("name: %q must be lowercase letters, digits and '-', at most 24 chars", f.Name)
	}
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
					add("%s.agent: dataplane is reserved and not implemented yet", np)
				default:
					add("%s.agent: must be client or none", np)
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
		if f.Consul.FederationMode() != "" || len(f.Consul.Peering) > 0 {
			add("consul: partitions and namespaces don't work with federation or peering in Parker yet")
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
