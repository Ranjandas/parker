package main

import (
	"fmt"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestWantedBinaries(t *testing.T) {
	low := &spec.File{
		Versions: map[string]string{"consul": "1.22.6", "nomad": "1.9.0"},
		Nodes: map[string]*spec.RawNode{
			"a": {Processes: map[string]*spec.Process{
				"consul": {Run: []string{"consul", "agent"}, Version: "1.21.5"},
				"web":    {Run: []string{"web"}},
			}},
			"b": {Processes: map[string]*spec.Process{"consul": {Run: []string{"consul"}, Version: "1.22.6"}}},
		},
		Scenario: &spec.Scenario{Steps: []*spec.Step{{Upgrade: &spec.UpgradeStep{On: "dc1", To: "1.22.7"}}}},
		Scenarios: map[string]*spec.Scenario{
			"x": {Steps: []*spec.Step{{Upgrade: &spec.UpgradeStep{On: "dc1", To: "1.22.7"}}, {Upgrade: &spec.UpgradeStep{On: "dc1", To: "2.0.0", Binary: "nomad"}}}},
		},
	}
	got := fmt.Sprint(wantedBinaries(low))
	want := "[[consul 1.22.6] [nomad 1.9.0] [consul 1.21.5] [consul 1.22.7] [nomad 2.0.0]]"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
