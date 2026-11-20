package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func files(t *testing.T, fs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range fs {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const aclModule = `parker: v1alpha1
kind: Module
inputs:
  servers: { type: number, default: 3 }
  mode: { enum: [wan, mesh-gateways], default: wan }
provides:
  consul:
    defaults: { acl: true }
    datacenters:
      dc1: { servers: ${inputs.servers}, nodes: { from-module: {} } }
`

func TestTemplates(t *testing.T) {
	dir := files(t, map[string]string{"lab.yaml": `parker: v1alpha1
versions: { consul: 1.22.7+ent }
consul:
  templates:
    base: { service: { port: 9090, run: fake-service } }
    api-node: { from: base, count: 2, meta: { tier: api } }
  datacenters:
    dc1:
      servers: 1
      nodes:
        api: { from: api-node }
        solo: { from: api-node, count: 1, service: { port: 7000 } }
        plain: {}
    dc2:
      servers: 1
      partitions:
        team-a: { nodes: { api: { from: api-node } } }
`})
	f, err := Load(filepath.Join(dir, "lab.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	n := f.Consul.Datacenters["dc1"].Nodes
	if n["api"].Count != 2 || n["api"].Service.Port != 9090 || n["api"].Meta["tier"] != "api" || n["api"].From != "" {
		t.Errorf("api: %+v", n["api"])
	}
	if n["solo"].Count != 1 || n["solo"].Service.Port != 7000 || len(n["solo"].Service.Run) == 0 {
		t.Errorf("solo overrides: %+v %+v", n["solo"], n["solo"].Service)
	}
	if p := f.Consul.Datacenters["dc2"].Partitions["team-a"].Nodes["api"]; p.Count != 2 {
		t.Errorf("partition node: %+v", p)
	}
	if f.Consul.Templates != nil {
		t.Errorf("templates left in: %v", f.Consul.Templates)
	}
	for body, want := range map[string]string{
		"consul:\n  datacenters:\n    dc1:\n      nodes:\n        a: { from: nope }\n":                                               `consul.datacenters.dc1.nodes.a: no template "nope" (add it under consul.templates)`,
		"consul:\n  templates: { x: {} }\n  datacenters:\n    dc1:\n      nodes:\n        a: { from: y }\n":                          `no template "y" (templates: x)`,
		"consul:\n  templates: { x: { from: y }, y: { from: x } }\n  datacenters:\n    dc1:\n      nodes:\n        a: { from: x }\n": "x comes from itself (x -> y -> x)",
		"consul:\n  templates: { x: { cuont: 2 } }\n  datacenters:\n    dc1:\n      nodes:\n        a: { from: x }\n":                `consul.templates.x.cuont: unknown key (did you mean "count"?)`,
	} {
		d := files(t, map[string]string{"l.yaml": "parker: v1alpha1\n" + body})
		if _, err := Load(filepath.Join(d, "l.yaml"), nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
}

func TestImports(t *testing.T) {
	dir := files(t, map[string]string{
		"modules/acl.yaml": aclModule,
		"lab.yaml": `parker: v1alpha1
imports:
  - module: modules/acl.yaml
    with: { servers: 5 }
versions: { consul: 1.22.7 }
consul:
  datacenters:
    dc1:
      nodes: { web: { service: { port: 8080 } } }
`,
		"wins.yaml": `parker: v1alpha1
imports: [{ module: modules/acl.yaml }]
versions: { consul: 1.22.7 }
consul:
  datacenters:
    dc1: { servers: 1 }
`,
	})
	f, err := Load(filepath.Join(dir, "lab.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	dc := f.Consul.Datacenters["dc1"]
	if dc.Servers.Count != 5 || f.Consul.Defaults.ACL == nil || !*f.Consul.Defaults.ACL || dc.Nodes["web"] == nil || dc.Nodes["from-module"] == nil || f.Imports != nil {
		t.Errorf("imported: servers %d nodes %v imports %v", dc.Servers.Count, dc.Nodes, f.Imports)
	}
	// The importer's own values win over the module's, defaults fill in.
	if f, err := Load(filepath.Join(dir, "wins.yaml"), nil); err != nil || f.Consul.Datacenters["dc1"].Servers.Count != 1 {
		t.Errorf("importer wins: %v", err)
	}

	for name, c := range map[string]struct{ mod, imp, want string }{
		"bad input":   {aclModule, "with: { servers: many }", `input servers: want a number, got "many"`},
		"enum":        {aclModule, "with: { mode: carrier-pigeon }", `input mode: want one of wan, mesh-gateways, got "carrier-pigeon"`},
		"no input":    {aclModule, "with: { replicas: 2 }", "with.replicas: no such input (inputs: mode, servers)"},
		"required":    {strings.Replace(aclModule, ", default: 3", "", 1), "", "input servers: required"},
		"undeclared":  {strings.Replace(aclModule, "${inputs.servers}", "${inputs.count}", 1), "", "undeclared inputs: ${inputs.count}"},
		"not module":  {strings.Replace(aclModule, "kind: Module", "kind: Lab", 1), "", "a module starts with parker: v1alpha1 and kind: Module"},
		"key in mod":  {strings.Replace(aclModule, "defaults:", "defualts:", 1), "", "mod.yaml:\n  - line 8: provides.consul.defualts: unknown key"},
		"self import": {strings.Replace(aclModule, "provides:\n", "provides:\n  imports: [{ module: mod.yaml }]\n", 1), "", "imports itself"},
	} {
		d := files(t, map[string]string{
			"mod.yaml": c.mod,
			"l.yaml":   "parker: v1alpha1\nversions: { consul: 1.22.7 }\nimports:\n  - module: mod.yaml\n    " + c.imp + "\n",
		})
		if _, err := Load(filepath.Join(d, "l.yaml"), nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// Overlays may import modules and use templates the base defines.
func TestImportsWithOverlays(t *testing.T) {
	dir := files(t, map[string]string{
		"acl.yaml": aclModule,
		"base.yaml": `parker: v1alpha1
versions: { consul: 1.22.7 }
consul:
  templates: { svc: { service: { port: 9000 } } }
  datacenters: { dc1: { servers: 3 } }
`,
		"over.yaml": `imports: [{ module: acl.yaml, with: { servers: 3 } }]
consul:
  datacenters:
    dc1: { nodes: { api: { from: svc } } }
`,
	})
	f, err := LoadWith(filepath.Join(dir, "base.yaml"), Layers{Overlays: []string{filepath.Join(dir, "over.yaml")}, Sets: []string{"dc1/api:service.port=9001"}})
	if err != nil {
		t.Fatal(err)
	}
	if p := f.Consul.Datacenters["dc1"].Nodes["api"].Service.Port; p != 9001 || f.Consul.Defaults.ACL == nil {
		t.Errorf("port %d acl %v", p, f.Consul.Defaults.ACL)
	}
}

func TestTemplatesExample(t *testing.T) {
	f, err := Load("../../examples/templates.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Consul.Datacenters["dc1"].Servers.Count != 3 || f.Consul.Datacenters["dc2"].Nodes["api"].Count != 1 || f.Consul.Datacenters["dc1"].Nodes["api"].Count != 2 {
		t.Errorf("%+v", f.Consul.Datacenters)
	}
}
