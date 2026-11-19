package compat

import (
	"slices"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestMinor(t *testing.T) {
	for in, want := range map[string]string{
		"1.22.7": "1.22", "1.22.7+ent": "1.22", "v1.35.3": "1.35", "1.38.7+fips1403": "1.38",
		"1.22": "1.22", "path:/x/consul": "", "latest": "", "1": "",
	} {
		if got := minor(in); got != want {
			t.Errorf("minor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckEnvoy(t *testing.T) {
	for _, c := range []struct{ consul, envoy, want string }{
		{"1.22.7", "1.35.9", ""},
		{"1.22.7+ent", "1.32.12", ""},
		{"1.22.7", "1.38.7", "envoy 1.38.7 is not supported by consul 1.22.7, which supports envoy 1.35, 1.34, 1.33, 1.32"},
		{"1.19.2", "1.30.1", "supports envoy 1.29, 1.28, 1.27, 1.26"},
		{"1.99.0", "1.10.0", ""},      // Consul newer than the table: can't judge
		{"path:/x", "1.10.0", ""},     // local build
		{"1.22.7", "path:/envoy", ""}, // local build
	} {
		err := CheckEnvoy(c.consul, c.envoy)
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("CheckEnvoy(%s, %s) = %v, want %q", c.consul, c.envoy, err, c.want)
		}
	}
	if e, ok := EnvoyFor("1.22.1"); !ok || e[0] != "1.35" {
		t.Errorf("EnvoyFor = %v %v", e, ok)
	}
	// The table must not be reversed by EnvoyFor.
	if !slices.Equal(envoyByConsul["1.22"], []string{"1.32", "1.33", "1.34", "1.35"}) {
		t.Error("EnvoyFor changed the table")
	}
}

func TestWarnings(t *testing.T) {
	f := &spec.File{
		Versions: map[string]string{"consul": "1.22.7", "envoy": "1.35.9"},
		Nodes: map[string]*spec.RawNode{
			"a": {Processes: map[string]*spec.Process{"consul": {Run: []string{"consul"}, Version: "1.19.2"}}},
			"b": {Processes: map[string]*spec.Process{"envoy": {Run: []string{"envoy"}, Version: "1.38.7"}}},
			"c": {Processes: map[string]*spec.Process{"consul": {Run: []string{"consul"}}}},
		},
	}
	got := Warnings(f)
	if len(got) != 2 || !strings.Contains(got[0], "envoy 1.35.9 is not supported by consul 1.19.2") ||
		!strings.Contains(got[1], "envoy 1.38.7 is not supported by consul 1.22.7") {
		t.Errorf("Warnings = %q", got)
	}
	if w := Warnings(&spec.File{Versions: map[string]string{"consul": "1.22.7"}}); len(w) != 0 {
		t.Errorf("no envoy: %q", w)
	}
}
