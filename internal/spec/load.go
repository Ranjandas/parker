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

	var f File
	if err := yaml.NewDecoder(bytes.NewReader(text), yaml.Strict()).Decode(&f); err != nil {
		return nil, fmt.Errorf("%s:\n%s", path, yaml.FormatError(err, false, true))
	}
	if f.Name == "" {
		f.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
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
		for name, dc := range f.Consul.Datacenters {
			p := "consul.datacenters." + name
			if !nameRe.MatchString(name) {
				add("%s: invalid datacenter name", p)
			}
			if dc == nil {
				add("%s: empty datacenter (set servers:)", p)
				continue
			}
			if len(dc.Partitions) > 0 {
				add("%s.partitions: not supported by this prototype yet (Enterprise)", p)
			}
			if dc.Servers.Count < 1 {
				add("%s.servers: at least 1 server is required", p)
			}
			for o := range dc.Servers.Overrides {
				if !strings.HasPrefix(o, "server-") {
					add("%s.servers.overrides.%s: keys are server names like server-2", p, o)
				}
			}
			for nn, n := range dc.Nodes {
				np := p + ".nodes." + nn
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
	for nn, n := range f.Nodes {
		if n == nil {
			add("nodes.%s: empty", nn)
			continue
		}
		for net := range n.Networks {
			if _, ok := f.Networks[net]; !ok {
				add("nodes.%s.networks.%s: no such network", nn, net)
			}
		}
		for pn, p := range n.Processes {
			if len(p.Run) == 0 {
				add("nodes.%s.processes.%s.run: required", nn, pn)
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
