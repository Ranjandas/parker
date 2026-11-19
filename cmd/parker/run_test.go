package main

import (
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestHasScenario(t *testing.T) {
	sc := &spec.Scenario{}
	for _, c := range []struct {
		low  *spec.File
		name string
		err  string
	}{
		{&spec.File{Name: "x", Scenario: sc}, "", ""},
		{&spec.File{Name: "x", Scenarios: map[string]*spec.Scenario{"a": sc}}, "", ""},
		{&spec.File{Name: "x", Scenarios: map[string]*spec.Scenario{"a": sc}}, "a", ""},
		{&spec.File{Name: "x"}, "", "no scenario: (or scenarios:)"},
		{&spec.File{Name: "x", Scenarios: map[string]*spec.Scenario{"a": sc, "b": sc}}, "", "name one of: a, b"},
		{&spec.File{Name: "x", Scenarios: map[string]*spec.Scenario{"a": sc}}, "z", `no scenario "z" (have: a)`},
		{&spec.File{Name: "x", Scenario: sc}, "z", `drop "z" to run scenario:`},
	} {
		err := hasScenario(c.low, c.name)
		if (c.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%q: err %v, want %q", c.name, err, c.err)
		}
	}
}
