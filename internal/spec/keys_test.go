package spec

import (
	"strings"
	"testing"
)

func TestCheckKeys(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		want       []string // substrings, one per error
	}{
		{"clean", `
parker: v1alpha1
versions: {consul: 1.22.7}
consul:
  defaults:
    agents: {config: {anything: [1, 2]}, log_level: debug}
  datacenters:
    dc1:
      servers: {count: 3, overrides: {server-2: {version: 1.21.0}}}
      nodes:
        web:
          service: {port: 9090, register: {free: form}}
          processes:
            x: {run: sleep 1, ready_when: {consul: {leader: true}}}
`, nil},
		{"typo", `
parker: v1alpha1
consul:
  datacenters:
    dc1: {sever: 3}
`, []string{`line 5: consul.datacenters.dc1.sever: unknown key (did you mean "server" or "servers"?)`}},
		{"wrong level", `
parker: v1alpha1
consul:
  datacenters:
    dc1:
      servers: 1
      nodes:
        web: {servers: 3}
`, []string{"consul.datacenters.dc1.nodes.web.servers: unknown key (servers belongs under consul.datacenters.<dc>)"}},
		{"top-level typo", "parker: v1alpha1\nverions: {}\n", []string{`line 2: verions: unknown key (did you mean "versions"?)`}},
		{"no idea", "parker: v1alpha1\nzzzzzz: 1\n", []string{"line 2: zzzzzz: unknown key"}},
		{"low level", `
parker: v1alpha1
nodes:
  a:
    networks: {n: {ip: 10.0.0.2, ipp: x}}
    processes:
      p: {run: x, restrat: always}
`, []string{`nodes.a.networks.n.ipp: unknown key (did you mean "ip"?)`, `nodes.a.processes.p.restrat: unknown key (did you mean "restart"?)`}},
		{"anchors and merge keys", `
parker: v1alpha1
x-common: &c {count: 2}
`, []string{"x-common: unknown key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := CheckKeys([]byte(tc.yaml))
			if len(errs) != len(tc.want) {
				t.Fatalf("got %d errors %v, want %d", len(errs), errs, len(tc.want))
			}
			for i, e := range errs {
				if !strings.Contains(e.Error(), tc.want[i]) {
					t.Errorf("error %d = %q, want it to contain %q", i, e.Error(), tc.want[i])
				}
			}
		})
	}
}

func TestEditDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		d    int
	}{{"", "abc", 3}, {"sever", "servers", 2}, {"kitten", "sitting", 3}, {"same", "same", 0}} {
		if got := editDistance(c.a, c.b); got != c.d {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.a, c.b, got, c.d)
		}
	}
}
