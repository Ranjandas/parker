package daemon

import (
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestPickScenario(t *testing.T) {
	one := &spec.Scenario{}
	two := &spec.Scenario{}
	for _, c := range []struct {
		low  *spec.File
		name string
		want *spec.Scenario
		err  string
	}{
		{&spec.File{Name: "x", Scenario: one}, "", one, ""},
		{&spec.File{Name: "x", Scenarios: map[string]*spec.Scenario{"a": two}}, "", two, ""},
		{&spec.File{Name: "x", Scenarios: map[string]*spec.Scenario{"a": one, "b": two}}, "b", two, ""},
		{&spec.File{Name: "x", Scenarios: map[string]*spec.Scenario{"a": one, "b": two}}, "", nil, "name one of: a, b"},
		{&spec.File{Name: "x", Scenarios: map[string]*spec.Scenario{"a": one}}, "c", nil, `no scenario "c" (have: a)`},
		{&spec.File{Name: "x"}, "", nil, "has no scenario"},
		{&spec.File{Name: "x", Scenario: one}, "a", nil, "has no scenarios: section"},
	} {
		got, err := pickScenario(c.low, c.name)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: err %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: %p %v", c.name, got, err)
		}
	}
}

func TestInlineScenario(t *testing.T) {
	sc, err := inlineScenario("steps:\n  - kill: dc1/leader\n")
	if err != nil || len(sc.Steps) != 1 || sc.Steps[0].Kill != "dc1/leader" {
		t.Fatalf("%+v %v", sc, err)
	}
	for y, want := range map[string]string{
		"steps:\n  - explode: dc1\n":          "explode",
		"steps: []\n":                         "no steps",
		"steps:\n  - kill: a\n    pause: b\n": "one action per step",
	} {
		if _, err := inlineScenario(y); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", y, err, want)
		}
	}
}
