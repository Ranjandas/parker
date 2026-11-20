package spec

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, name, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadVarsEnvAndOverrides(t *testing.T) {
	t.Setenv("PARKER_TEST_LEVEL", "debug")
	p := write(t, "my-lab.yaml", `
parker: v1alpha1
vars: {v: 1.22.7, n: 2}
versions: {consul: "${vars.v}"}
consul:
  datacenters:
    dc1:
      servers: ${vars.n}
      agents: {log_level: "${env.PARKER_TEST_LEVEL}"}
`)
	f, err := Load(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "my-lab" {
		t.Errorf("name from file name: %q", f.Name)
	}
	dc := f.Consul.Datacenters["dc1"]
	if f.Versions["consul"] != "1.22.7" || dc.Servers.Count != 2 || dc.Agents.LogLevel != "debug" {
		t.Errorf("substitution: versions=%v servers=%d level=%q", f.Versions, dc.Servers.Count, dc.Agents.LogLevel)
	}
	f, err = Load(p, map[string]string{"n": "5"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Consul.Datacenters["dc1"].Servers.Count != 5 {
		t.Error("--var override ignored")
	}
}

func TestLoadUndefinedVariables(t *testing.T) {
	p := write(t, "x.yaml", "parker: v1alpha1\nname: \"${vars.nope}-${env.PARKER_TEST_UNSET_XYZ}\"\n")
	_, err := Load(p, nil)
	if err == nil || !strings.Contains(err.Error(), "undefined variables: ${vars.nope}, ${env.PARKER_TEST_UNSET_XYZ}") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadLeavesRuntimeVars(t *testing.T) {
	p := write(t, "x.yaml", `
parker: v1alpha1
networks: {n: {subnet: 10.9.0.0/24}}
nodes:
  a:
    networks: {n: {ip: 10.9.0.2}}
    processes:
      p: {run: "ls ${node_dir} ${run_dir}"}
`)
	f, err := Load(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Nodes["a"].Processes["p"].Run; !slices.Equal(got, Command{"ls", "${node_dir}", "${run_dir}"}) {
		t.Errorf("run = %v", got)
	}
}

func TestLoadSyntaxAndTypeErrors(t *testing.T) {
	for name, text := range map[string]string{
		"syntax": "parker: v1alpha1\nconsul: [\n",
		"type":   "parker: v1alpha1\nversions: {consul: 1.22.7}\nconsul:\n  datacenters:\n    dc1: {servers: lots}\n",
	} {
		if _, err := Load(write(t, "x.yaml", text), nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestServersAndCommandShapes(t *testing.T) {
	p := write(t, "x.yaml", `
parker: v1alpha1
versions: {consul: 1.22.7}
consul:
  datacenters:
    dc1:
      servers: {count: 3, overrides: {server-2: {version: 1.21.0}}}
      nodes:
        web: {service: {run: [fake-service, --listen, ":8080"]}}
`)
	f, err := Load(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	dc := f.Consul.Datacenters["dc1"]
	if dc.Servers.Count != 3 || dc.Servers.Overrides["server-2"].Version != "1.21.0" {
		t.Errorf("servers: %+v", dc.Servers)
	}
	if got := dc.Nodes["web"].Service.Run; !slices.Equal(got, Command{"fake-service", "--listen", ":8080"}) {
		t.Errorf("run list: %v", got)
	}
}

func TestValidate(t *testing.T) {
	ok := func() *File {
		return &File{
			Parker:   SchemaVersion,
			Name:     "lab",
			Versions: map[string]string{"consul": "1.22.7"},
			Consul: &Consul{Datacenters: map[string]*Datacenter{
				"dc1": {Servers: Servers{Count: 1}, Nodes: map[string]*Node{"web": {}}},
			}},
			Networks: map[string]*Network{"n": {Subnet: "10.9.0.0/24"}},
			Nodes: map[string]*RawNode{"box": {
				Networks: map[string]*Attachment{"n": {IP: "10.9.0.2"}, "dc1-lan": {IP: "10.1.0.200"}},
				Processes: map[string]*Process{
					"a": {Run: Command{"sleep", "1"}, Restart: "always"},
					"b": {Run: Command{"sleep", "1"}, DependsOn: map[string]*Dep{"a": {Condition: "ready"}, "box/a": nil}},
				},
			}},
		}
	}
	if err := ok().Validate(); err != nil {
		t.Fatalf("valid file: %v", err)
	}
	for _, c := range []struct {
		name string
		mod  func(f *File)
		want string
	}{
		{"schema version", func(f *File) { f.Parker = "v0" }, `parker: must be "v1alpha1"`},
		{"lab name", func(f *File) { f.Name = "Bad_Name" }, "name:"},
		{"long name", func(f *File) { f.Name = strings.Repeat("a", 25) }, "at most 24"},
		{"consul version", func(f *File) { delete(f.Versions, "consul") }, "versions.consul: required"},
		{"no dcs", func(f *File) { f.Consul.Datacenters = nil }, "at least one datacenter"},
		{"dc name", func(f *File) { f.Consul.Datacenters["DC_1"] = &Datacenter{Servers: Servers{Count: 1}} }, "invalid datacenter name"},
		{"empty dc", func(f *File) { f.Consul.Datacenters["dc2"] = nil }, "empty datacenter"},
		{"zero servers", func(f *File) { f.Consul.Datacenters["dc1"].Servers.Count = 0 }, "at least 1 server"},
		{"override key", func(f *File) {
			f.Consul.Datacenters["dc1"].Servers.Overrides = map[string]*ServerOverride{"s2": {}}
		}, "keys are server names"},
		{"partitions beside nodes", func(f *File) {
			f.Versions["consul"] = "1.22.7+ent"
			f.Consul.Datacenters["dc1"].Partitions = map[string]*Partition{"team-a": {}}
		}, "move them under partitions.default"},
		{"partition name", func(f *File) {
			f.Versions["consul"] = "1.22.7+ent"
			dc := f.Consul.Datacenters["dc1"]
			dc.Partitions, dc.Nodes = map[string]*Partition{"Team_A": {}}, nil
		}, "partition names are lowercase"},
		{"partitions need ent", func(f *File) {
			dc := f.Consul.Datacenters["dc1"]
			dc.Partitions, dc.Nodes = map[string]*Partition{"team-a": {Nodes: dc.Nodes}}, nil
		}, "pin versions.consul to an Enterprise build such as 1.22.7+ent"},
		{"namespace needs ent", func(f *File) {
			f.Consul.Datacenters["dc1"].Nodes["web"].Service = &Service{Namespace: "payments"}
		}, "partitions and namespaces need Consul Enterprise"},
		{"node name", func(f *File) { f.Consul.Datacenters["dc1"].Nodes["Web"] = &Node{} }, "invalid node name"},
		{"reserved node", func(f *File) { f.Consul.Datacenters["dc1"].Nodes["server-9"] = &Node{} }, "reserved for servers"},
		{"agent", func(f *File) { f.Consul.Datacenters["dc1"].Nodes["web"].Agent = "bogus" }, "must be client or none"},
		{"dataplane", func(f *File) { f.Consul.Datacenters["dc1"].Nodes["web"].Agent = "dataplane" }, "dataplane is reserved"},
		{"service and services", func(f *File) {
			n := f.Consul.Datacenters["dc1"].Nodes["web"]
			n.Service, n.Services = &Service{}, map[string]*Service{"x": {}}
		}, "not both"},
		{"empty raw node", func(f *File) { f.Nodes["x"] = nil }, "nodes.x: empty"},
		{"unknown network", func(f *File) { f.Nodes["box"].Networks["nope"] = &Attachment{} }, "networks.nope: no such network"},
		{"unknown dc lan", func(f *File) { f.Nodes["box"].Networks["dc9-lan"] = &Attachment{} }, "networks.dc9-lan: no such network"},
		{"no run", func(f *File) { f.Nodes["box"].Processes["a"].Run = nil }, "processes.a.run: required"},
		{"restart", func(f *File) { f.Nodes["box"].Processes["a"].Restart = "sometimes" }, "must be no, on-failure or always"},
		{"dep target", func(f *File) { f.Nodes["box"].Processes["b"].DependsOn["zzz/a"] = nil }, "depends_on.zzz/a: no such process"},
		{"dep condition", func(f *File) { f.Nodes["box"].Processes["b"].DependsOn["a"].Condition = "soon" }, "started or ready"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := ok()
			c.mod(f)
			err := f.Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestParseMembers(t *testing.T) {
	for in, want := range map[string]int{">=4": 4, "3": 3, " >= 2 ": 2} {
		if got, err := ParseMembers(in); err != nil || got != want {
			t.Errorf("ParseMembers(%q) = %d, %v", in, got, err)
		}
	}
	if _, err := ParseMembers(">=many"); err == nil {
		t.Error("bad count accepted")
	}
}

func TestValidateFederation(t *testing.T) {
	base := func(mode string, dcs ...string) *File {
		f := &File{Parker: SchemaVersion, Name: "x", Versions: map[string]string{"consul": "1.22.7"},
			Consul: &Consul{Federation: &Federation{Mode: mode}, Datacenters: map[string]*Datacenter{}}}
		for _, d := range dcs {
			f.Consul.Datacenters[d] = &Datacenter{Servers: Servers{Count: 1}}
		}
		return f
	}
	if err := base("wan", "dc1", "dc2").Validate(); err != nil {
		t.Errorf("valid: %v", err)
	}
	for name, c := range map[string]struct {
		f    *File
		want string
	}{
		"one dc":                {base("wan", "dc1"), "needs at least two datacenters"},
		"mesh gateways, one dc": {base("mesh-gateways", "dc1"), "needs at least two datacenters"},
		"bad mode":              {base("carrier-pigeon", "dc1", "dc2"), "must be wan"},
	} {
		if err := c.f.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	two := base("wan", "dc1", "dc2")
	yes := true
	two.Consul.Datacenters["dc1"].Primary, two.Consul.Datacenters["dc2"].Primary = &yes, &yes
	if err := two.Validate(); err == nil || !strings.Contains(err.Error(), "only one datacenter can be primary") {
		t.Errorf("two primaries: %v", err)
	}
}

func TestLicensePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lab.yaml")
	os.WriteFile(p, []byte("parker: v1alpha1\nversions: { consul: 1.22.7+ent }\nlicense: { file: keys/consul.hclic }\n"), 0o644)
	f, err := Load(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.License.File != filepath.Join(dir, "keys", "consul.hclic") {
		t.Errorf("relative license.file: %q", f.License.File)
	}
	home, _ := os.UserHomeDir()
	os.WriteFile(p, []byte("parker: v1alpha1\nlicense: { file: ~/consul.hclic }\n"), 0o644)
	if f, err := Load(p, nil); err != nil || f.License.File != filepath.Join(home, "consul.hclic") {
		t.Errorf("~ license.file: %v %v", f, err)
	}
	os.WriteFile(p, []byte("parker: v1alpha1\nlicense: {}\n"), 0o644)
	if _, err := Load(p, nil); err == nil || !strings.Contains(err.Error(), "license.file: required") {
		t.Errorf("empty license: %v", err)
	}
}
