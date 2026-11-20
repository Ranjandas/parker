package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func doc(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := yaml.UnmarshalWithOptions([]byte(s), &v, yaml.UseOrderedMap()); err != nil {
		t.Fatal(err)
	}
	return dedupe(v)
}

func text(t *testing.T, v any) string {
	t.Helper()
	b, err := yaml.MarshalWithOptions(v, yaml.Flow(true))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestMerge(t *testing.T) {
	for _, c := range []struct{ base, overlay, want string }{
		{"{a: 1, b: {c: 2, d: 3}}", "{b: {d: 4, e: 5}, f: 6}", "{a: 1, b: {c: 2, d: 4, e: 5}, f: 6}"},
		{"{a: 1, b: 2}", "{a: null}", "{b: 2}"},
		{"{a: {web: null}}", "{a: {cache: null}}", "{a: {web: null, cache: null}}"},
		{"{l: [1, 2]}", "{l: [3]}", "{l: [3]}"},
		{"{l: [1, 2]}", `{l: [{$patch: append}, 3]}`, "{l: [1, 2, 3]}"},
		{"{}", `{l: [{$patch: append}, 3]}`, "{l: [3]}"},
		{"{a: {b: 1}}", "{a: 5}", "{a: 5}"},
		{"{<<: {a: 1, b: 2}, b: 3}", "{}", "{a: 1, b: 3}"},
	} {
		if got := text(t, Merge(doc(t, c.base), doc(t, c.overlay))); got != c.want {
			t.Errorf("%s + %s = %s, want %s", c.base, c.overlay, got, c.want)
		}
	}
}

const setBase = `
consul:
  datacenters:
    dc1:
      servers: 3
      nodes:
        web: { service: { port: 8080 } }
        api:
      partitions:
        team-a: { nodes: { billing: {} }, gateways: { mesh: true } }
        team-b: { nodes: { billing: {} } }
`

func TestApplySet(t *testing.T) {
	for set, want := range map[string]string{
		"consul.datacenters.dc1.servers=5":  "servers: 5",
		"dc1:servers=5":                     "servers: 5",
		"dc1/web:service.port=9000":         "web: {service: {port: 9000}}",
		"dc1/api:service.env.MESSAGE=hi":    "api: {service: {env: {MESSAGE: hi}}}",
		"dc1/team-b/billing:count=2":        "team-b: {nodes: {billing: {count: 2}}}",
		"dc1/mesh:count=2":                  "gateways: {mesh: {count: 2}}",
		"dc1/web:service={port: 1, run: x}": "web: {service: {port: 1, run: x}}",
		"dc1/web:service.check=null":        "web: {service: {port: 8080}}",
		"dc1/web:meta.tags=[a, b]":          "meta: {tags: [a, b]}",
		"versions.consul=1.22.7":            "versions: {consul: 1.22.7}",
		"dc1/web:service=null":              "web: {}",
	} {
		got, err := ApplySet(doc(t, setBase), set)
		if err != nil {
			t.Errorf("%s: %v", set, err)
			continue
		}
		if s := text(t, got); !strings.Contains(s, want) {
			t.Errorf("%s: %s, want %s in it", set, s, want)
		}
	}
	for set, want := range map[string]string{
		"servers":                  "want PATH=VALUE",
		"dc9:servers=1":            `no datacenter "dc9"`,
		"dc1/nope:count=1":         `no node or gateway "nope" in dc1`,
		"dc1/billing:count=1":      "name the partition",
		"dc1/team-a/web:count=1":   `no node or gateway "web" in partition team-a`,
		"dc1/a/b/c:x=1":            "want dc, dc/name or dc/partition/name",
		"dc1/web:service.port.x=1": "is not a map",
		"dc1/web:service..port=1":  "empty key",
	} {
		if _, err := ApplySet(doc(t, setBase), set); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", set, err, want)
		}
	}
}

func TestLoadWith(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := write("base.yaml", `parker: v1alpha1
vars: { n: 3 }
versions: { consul: 1.22.7 }
consul:
  datacenters:
    dc1:
      servers: ${vars.n}
      nodes:
        web: { service: { port: 8080 } }
      config_entries:
        - { Kind: service-defaults, Name: web, Protocol: http }
`)
	acl := write("acl.yaml", `vars: { n: 5 }
consul:
  defaults: { acl: true }
  datacenters:
    dc1:
      servers: ${vars.n}
      config_entries:
        - $patch: append
        - { Kind: service-defaults, Name: api, Protocol: grpc }
`)
	f, err := LoadWith(base, Layers{Overlays: []string{acl}, Sets: []string{"dc1/web:service.port=9000"}})
	if err != nil {
		t.Fatal(err)
	}
	dc := f.Consul.Datacenters["dc1"]
	if f.Name != "base" || dc.Servers.Count != 5 || f.Consul.Defaults.ACL == nil || !*f.Consul.Defaults.ACL ||
		dc.Nodes["web"].Service.Port != 9000 || len(dc.ConfigEntries) != 2 {
		t.Fatalf("merged: name %s servers %d acl %v port %d entries %d", f.Name, dc.Servers.Count, f.Consul.Defaults.ACL != nil, dc.Nodes["web"].Service.Port, len(dc.ConfigEntries))
	}
	// Lines in merged text would point nowhere, so labels drop them.
	if l := dc.EntryLabel("dc1", 1); l != "consul.datacenters.dc1.config_entries[1]" {
		t.Errorf("label %q", l)
	}
	// --var still beats every file.
	if f, err := LoadWith(base, Layers{Overlays: []string{acl}, Vars: map[string]string{"n": "1"}}); err != nil || f.Consul.Datacenters["dc1"].Servers.Count != 1 {
		t.Errorf("--var: %v", err)
	}
	// Without layers, the base loads as before, lines and all.
	if f, err := LoadWith(base, Layers{}); err != nil || f.Consul.Datacenters["dc1"].EntryLabel("dc1", 0) != "consul.datacenters.dc1.config_entries[0] (line 11)" {
		t.Errorf("plain load: %v", err)
	}
	// Unknown keys name the overlay they're in; the merged result is
	// validated as a whole.
	typo := write("typo.yaml", "consul:\n  datacenters:\n    dc1:\n      nodez: {}\n")
	if _, err := LoadWith(base, Layers{Overlays: []string{typo}}); err == nil || !strings.Contains(err.Error(), "typo.yaml:\n  - line 4: consul.datacenters.dc1.nodez: unknown key") {
		t.Errorf("typo: %v", err)
	}
	if _, err := LoadWith(base, Layers{Sets: []string{"dc1:servers=0"}}); err == nil || !strings.Contains(err.Error(), "base.yaml (with overlays and --set)") {
		t.Errorf("invalid merge: %v", err)
	}
	list := write("list.yaml", "- a\n")
	if _, err := LoadWith(base, Layers{Overlays: []string{list}}); err == nil || !strings.Contains(err.Error(), "mapping is expected") {
		t.Errorf("list overlay: %v", err)
	}
	text, err := Merged(base, Layers{Overlays: []string{acl}})
	if err != nil || !strings.Contains(string(text), "servers: 5") || strings.Contains(string(text), "$patch") {
		t.Errorf("merged text %s %v", text, err)
	}
}

// An example with anchors and merge keys survives a round trip through
// the merge unchanged.
func TestMergedRoundTrip(t *testing.T) {
	for _, ex := range []string{"partitions", "gateways", "scenario", "mesh-federation"} {
		p := "../../examples/" + ex + ".yaml"
		a, err := Load(p, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, err := LoadWith(p, Layers{Sets: []string{"parker=" + SchemaVersion}})
		if err != nil {
			t.Fatalf("%s: %v", ex, err)
		}
		ya, _ := yaml.Marshal(a)
		yb, _ := yaml.Marshal(b)
		if string(ya) != string(yb) {
			t.Errorf("%s changed in the round trip", ex)
		}
	}
}
