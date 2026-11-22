package labfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func op(o string, path []any, v any, key ...string) Op {
	out := Op{Op: o, Path: path}
	if v != nil {
		b, _ := json.Marshal(v)
		out.Value = b
	}
	if len(key) > 0 {
		out.Key = key[0]
	}
	return out
}

// raw is a JSON value with its key order kept.
func raw(s string) any { return json.RawMessage(s) }

func TestPatch(t *testing.T) {
	src := `# A lab.
name: x # the name
versions: { consul: 1.22.7, envoy: 1.35.12 }
consul:
  defaults:
    service_template: &sd { run: fake-service, connect: sidecar }
  datacenters:
    dc1:
      servers: 1 # one is enough
      gateways: { mesh: true }
      nodes:
        # the front end
        web:
          service:
            port: 8080
            upstreams: { api: 9091 }
        api:
          service: { port: 9090 }
      config_entries:
        - Kind: proxy-defaults
          Name: global
        - { Kind: service-defaults, Name: api, Protocol: http }
    dc2:
      service_template: *sd
      servers: 3
`
	cases := []struct {
		name string
		op   Op
		want []string // lines the result has
		not  []string // lines it doesn't
	}{
		{"scalar keeps comment", op("set", []any{"consul", "datacenters", "dc1", "servers"}, 3), []string{"      servers: 3 # one is enough"}, nil},
		{"flow key", op("set", []any{"versions", "consul"}, "1.21.0"), []string{"versions: { consul: 1.21.0, envoy: 1.35.12 }"}, nil},
		{"add to flow map", op("set", []any{"consul", "datacenters", "dc1", "nodes", "web", "service", "upstreams", "db"}, 9092), []string{"            upstreams: { api: 9091, db: 9092 }"}, nil},
		{"add to block map", op("set", []any{"consul", "datacenters", "dc1", "nodes", "db"}, raw(`{"service":{"port":5432}}`)), []string{"        db:", "          service: { port: 5432 }", "        # the front end"}, nil},
		{"new parent", op("set", []any{"consul", "datacenters", "dc2", "nodes", "web"}, raw(`{}`)), []string{"      nodes:", "        web: {}"}, nil},
		{"delete block key", op("delete", []any{"consul", "datacenters", "dc1", "nodes", "api"}, nil), []string{"        web:"}, []string{"        api:", "          service: { port: 9090 }"}},
		{"delete flow key", op("delete", []any{"versions", "envoy"}, nil), []string{"versions: { consul: 1.22.7 }"}, nil},
		{"rename keeps body and comment", op("rename", []any{"consul", "datacenters", "dc1", "nodes", "web"}, nil, "front"), []string{"        # the front end", "        front:", "            port: 8080"}, nil},
		{"append block item", op("append", []any{"consul", "datacenters", "dc1", "config_entries"}, raw(`{"Kind":"exported-services","Name":"default","Services":[{"Name":"api","Consumers":[{"Peer":"p"}]}]}`)),
			[]string{"        - Kind: exported-services", "          Name: default"}, nil},
		{"remove item", op("remove", []any{"consul", "datacenters", "dc1", "config_entries", 0}, nil), []string{"        - { Kind: service-defaults, Name: api, Protocol: http }"}, []string{"          Name: global"}},
		{"set to block", op("set", []any{"consul", "datacenters", "dc1", "nodes", "api", "service"}, raw(`{"port":9090,"upstreams":{"db":{"port":9093,"dc":"dc2"}}}`)),
			[]string{"          service:", "            port: 9090", "            upstreams: { db: { port: 9093, dc: dc2 } }"}, nil},
	}
	for _, c := range cases {
		got, err := Patch(src, []Op{c.op})
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		lines := strings.Split(got, "\n")
		has := func(l string) bool {
			for _, x := range lines {
				if x == l {
					return true
				}
			}
			return false
		}
		for _, w := range c.want {
			if !has(w) {
				t.Errorf("%s: no line %q in\n%s", c.name, w, got)
			}
		}
		for _, w := range c.not {
			if has(w) {
				t.Errorf("%s: line %q still in\n%s", c.name, w, got)
			}
		}
		if !strings.HasPrefix(got, "# A lab.\nname: x # the name\n") {
			t.Errorf("%s: the head changed:\n%s", c.name, got)
		}
	}

	// An anchored value keeps its anchor.
	if got, err := Patch(src, []Op{op("set", []any{"consul", "defaults", "tls"}, true)}); err != nil || !strings.Contains(got, "service_template: &sd {") {
		t.Errorf("anchor: %v\n%s", err, got)
	}
	// A value shared through an anchor, or reached through an alias: refused.
	if _, err := Patch(src, []Op{op("set", []any{"consul", "defaults", "service_template", "connect"}, "none")}); err == nil {
		t.Error("edit of a shared anchor went through")
	}
	if _, err := Patch(src, []Op{op("set", []any{"consul", "datacenters", "dc2", "service_template", "run"}, "x")}); err == nil {
		t.Error("edit through an alias went through")
	}
}

func TestPatchExamples(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*.yaml")
	if len(files) < 15 {
		t.Fatalf("%d examples", len(files))
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		text := string(b)
		// No ops: the text, byte for byte.
		if got, err := Patch(text, nil); err != nil || got != text {
			t.Errorf("%s: a no-op patch changed it (%v)", f, err)
		}
		dcs := datacenters(t, text)
		if len(dcs) == 0 {
			continue
		}
		dc := dcs[0]
		ops := []Op{
			op("set", []any{"consul", "datacenters", dc, "nodes", "added"}, raw(`{"service":{"port":7000,"upstreams":{"x":7001}}}`)),
			op("set", []any{"consul", "datacenters", dc, "nodes", "added", "count"}, 2),
			op("rename", []any{"consul", "datacenters", dc, "nodes", "added"}, nil, "renamed"),
			op("set", []any{"consul", "datacenters", dc, "ui"}, true),
			op("delete", []any{"consul", "datacenters", dc, "nodes", "renamed"}, nil),
			op("delete", []any{"consul", "datacenters", dc, "ui"}, nil),
		}
		got, err := Patch(text, ops)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		// Every comment survives.
		for _, l := range strings.Split(text, "\n") {
			if c := strings.TrimSpace(l); strings.HasPrefix(c, "#") && !strings.Contains(got, c) {
				t.Errorf("%s: lost comment %q", filepath.Base(f), c)
			}
		}
		if r := Check(got, f); !r.OK {
			t.Errorf("%s: patched file doesn't check: %v\n%s", filepath.Base(f), r.Problems, got)
		}
	}
}

func datacenters(t *testing.T, text string) []string {
	t.Helper()
	tree, err := Tree(text)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	if c, ok := get(tree, "consul", "datacenters"); ok {
		for _, it := range c.([]kv) {
			out = append(out, it.K)
		}
	}
	return out
}

func get(v any, path ...string) (any, bool) {
	for _, p := range path {
		m, ok := v.([]kv)
		if !ok {
			return nil, false
		}
		found := false
		for _, it := range m {
			if it.K == p {
				v, found = it.V, true
			}
		}
		if !found {
			return nil, false
		}
	}
	return v, true
}
