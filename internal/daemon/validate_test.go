package daemon

import (
	"os"
	"strings"
	"testing"
)

func TestValidateYAML(t *testing.T) {
	raw, err := os.ReadFile("../../examples/mesh-federation.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r := ValidateYAML(string(raw))
	if !r.Ok || len(r.Errors) != 0 || r.Nodes != 6 || r.Networks != 3 {
		t.Errorf("meshfed: %+v", r)
	}

	for name, c := range map[string]struct {
		yaml, want, path string
		line             int32
	}{
		"bad yaml":    {"parker: v1alpha1\nname: x\nconsul: [\n", "", "", 3},
		"unknown key": {"parker: v1alpha1\nname: x\nversions: { consul: 1.22.7 }\nconsul:\n  datacenters:\n    dc1:\n      servers: 1\n      nope: 2\n", "nope", "", 8},
		"expand": {"parker: v1alpha1\nname: x\nversions: { consul: 1.22.7, envoy: 1.35.12 }\nconsul:\n  federation: { mode: mesh-gateways }\n  datacenters:\n    dc1: { servers: 1 }\n    dc2: { servers: 1 }\n",
			"mesh-gateways needs tls", "consul.federation", 0},
		// Several problems come under a heading, which is no problem itself.
		"several": {"parker: v1alpha1\nname: x\nversions: { consul: 1.22.7 }\nconsul:\n  federation: { mode: wan }\n  datacenters:\n    dc1: { servers: 1 }\n    dc2:\n      servers: 1\n      partitions: { team-a: {} }\n",
			"Enterprise", "versions.consul", 0},
	} {
		r := ValidateYAML(c.yaml)
		if r.Ok || len(r.Errors) == 0 {
			t.Errorf("%s: ok %+v", name, r)
			continue
		}
		e := r.Errors[0]
		t.Logf("%s: %q line %d path %q", name, e.Message, e.Line, e.Path)
		if !strings.Contains(e.Message, c.want) || strings.Contains(e.Message, os.TempDir()) || (c.line != 0 && e.Line != c.line) || (c.path != "" && !strings.HasPrefix(e.Path, c.path)) {
			t.Errorf("%s: %+v", name, e)
		}
	}
}
