package main

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func suite(t *testing.T) []string {
	f, err := os.Open("../e2e/suite.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return readLines(f)
}

func TestPlan(t *testing.T) {
	s := suite(t)
	all := append(slices.Clone(s), Live)
	for _, c := range []struct {
		name    string
		changed []string
		want    []string
	}{
		{"docs only", []string{"README.md", "docs/x.png"}, nil},
		{"unit tests only", []string{"internal/lab/lab_test.go"}, nil},
		{"tui", []string{"internal/tui/model.go", "cmd/parker/tui.go"}, nil},
		{"tproxy", []string{"internal/tproxy/tproxy.go"}, []string{"examples/minimal.yaml", "examples/tproxy.yaml", "hack/e2e/tproxy-secure.yaml"}},
		{"peering", []string{"internal/expand/peering.go", "internal/expand/peering_test.go"}, []string{"examples/minimal.yaml", "examples/peering.yaml", "hack/e2e/peering-secure.yaml"}},
		{"a lab file", []string{"examples/gateways.yaml"}, []string{"examples/gateways.yaml"}},
		{"rootless mode script", []string{"hack/rootless-mode-e2e.sh"}, []string{Live}},
		{"core", []string{"internal/lab/lab.go"}, all},
		{"core with docs", []string{"README.md", "internal/supervisor/supervisor.go"}, all},
		{"workflow", []string{".github/workflows/ci.yml"}, all},
		{"expand core", []string{"internal/expand/expand.go"}, all},
	} {
		if got := Plan(c.changed, s); !slices.Equal(got, c.want) {
			t.Errorf("%s: Plan = %v, want %v", c.name, got, c.want)
		}
	}
}

// Every lab a rule names is in the suite, so a renamed file can't drop
// out of CI silently.
func TestRulesNameSuiteLabs(t *testing.T) {
	s := suite(t)
	for _, r := range rules {
		for _, l := range r.labs {
			if l != Live && !slices.Contains(s, l) {
				t.Errorf("rule %s names %s, which is not in suite.txt", r.prefix, l)
			}
		}
	}
	for _, l := range s {
		if _, err := os.Stat("../../" + l); err != nil {
			t.Errorf("suite.txt: %v", err)
		}
	}
}

func TestReadLines(t *testing.T) {
	got := readLines(strings.NewReader("a\n\n# comment\n b \n"))
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("readLines = %v", got)
	}
}
