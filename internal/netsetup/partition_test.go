package netsetup

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func cutLab() *spec.File {
	f := &spec.File{Nodes: map[string]*spec.RawNode{}}
	for i, n := range []string{"s1", "s2", "s3", "c1"} {
		f.Nodes[n] = &spec.RawNode{Networks: map[string]*spec.Attachment{
			"lan": {IP: "10.0.0." + string(rune('1'+i))},
		}}
	}
	// c1 is on a second network too.
	f.Nodes["c1"].Networks["wan"] = &spec.Attachment{IP: "10.9.0.4"}
	return f
}

func TestCutPlanTwoGroups(t *testing.T) {
	plan, err := CutPlan(cutLab(), [][]string{{"s1", "s2"}, {"s3"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"s1": "10.0.0.3", "s2": "10.0.0.3", "s3": "10.0.0.1 10.0.0.2",
	}
	if len(plan) != len(want) {
		t.Errorf("plan covers %v, want s1 s2 s3 (c1 untouched)", slices.Sorted(maps.Keys(plan)))
	}
	for n, ips := range want {
		if got := strings.Join(plan[n], " "); got != ips {
			t.Errorf("%s cuts %q, want %q", n, got, ips)
		}
	}
}

func TestCutPlanOneGroupIsolates(t *testing.T) {
	plan, err := CutPlan(cutLab(), [][]string{{"c1"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan["c1"], " "); got != "10.0.0.1 10.0.0.2 10.0.0.3" {
		t.Errorf("c1 cuts %q", got)
	}
	// Every other node loses both of c1's addresses.
	if got := strings.Join(plan["s2"], " "); got != "10.0.0.4 10.9.0.4" {
		t.Errorf("s2 cuts %q", got)
	}
}

func TestCutPlanErrors(t *testing.T) {
	f := cutLab()
	for _, c := range []struct {
		groups [][]string
		want   string
	}{
		{nil, "no groups"},
		{[][]string{{"s1"}, {}}, "group 2 is empty"},
		{[][]string{{"s1", "s2"}, {"s2"}}, "s2 is in groups 1 and 2"},
		{[][]string{{"nope"}}, "no node nope"},
		{[][]string{{"s1", "s2", "s3", "c1"}}, "whole lab"},
	} {
		if _, err := CutPlan(f, c.groups); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("CutPlan(%v) = %v, want %q", c.groups, err, c.want)
		}
	}
}

func TestHealPlan(t *testing.T) {
	f := cutLab()
	all, to := HealPlan(f, nil)
	if len(all) != 4 || len(to) != 0 {
		t.Errorf("heal all: %v %v", all, to)
	}
	all, to = HealPlan(f, []string{"c1"})
	if !all["c1"] || all["s1"] || !to["10.0.0.4"] || !to["10.9.0.4"] || to["10.0.0.1"] {
		t.Errorf("heal c1: %v %v", all, to)
	}
}
