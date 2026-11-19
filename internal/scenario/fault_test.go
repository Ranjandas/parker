package scenario

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestFaultStep(t *testing.T) {
	for _, c := range []struct {
		action string
		args   []string
		yaml   string
	}{
		{"kill", []string{"dc1/leader"}, "kill: dc1/leader"},
		{"stop", []string{"dc1/web"}, "stop: dc1/web"},
		{"start", []string{"dc1/web"}, "start: dc1/web"},
		{"restart", []string{"dc1"}, "restart: dc1"},
		{"pause", []string{"svc:api"}, "pause: svc:api"},
		{"resume", []string{"svc:api"}, "resume: svc:api"},
		{"heal", nil, "heal: all"},
		{"heal", []string{"dc2"}, "heal: dc2"},
		{"partition", []string{"dc1/server-1, dc1/server-2", "dc1/server-3"}, "partition:\n- - dc1/server-1\n  - dc1/server-2\n- dc1/server-3"},
	} {
		st, err := FaultStep(c.action, c.args)
		if err != nil {
			t.Errorf("%s %v: %v", c.action, c.args, err)
			continue
		}
		b, _ := yaml.Marshal(st)
		if got := strings.TrimSpace(string(b)); got != c.yaml {
			t.Errorf("%s %v:\n%s\nwant:\n%s", c.action, c.args, got, c.yaml)
		}
		// What the daemon gets must be a valid scenario.
		if err := Validate(&spec.Scenario{Steps: []*spec.Step{st}}); err != nil {
			t.Errorf("%s: %v", c.action, err)
		}
		// And survive the trip through YAML.
		var back spec.Step
		if err := yaml.UnmarshalWithOptions(b, &back, yaml.Strict()); err != nil || Label(&back) != Label(st) {
			t.Errorf("%s: round trip %v %q", c.action, err, Label(&back))
		}
	}
	for _, c := range []struct {
		action string
		args   []string
		want   string
	}{
		{"kill", nil, "kill takes one selector"},
		{"kill", []string{"a", "b"}, "kill takes one selector"},
		{"heal", []string{"a", "b"}, "at most one"},
		{"partition", nil, "at least one group"},
		{"partition", []string{","}, "empty group"},
		{"explode", nil, "unknown action"},
	} {
		if _, err := FaultStep(c.action, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: %v, want %q", c.action, c.args, err, c.want)
		}
	}
}

func TestInline(t *testing.T) {
	st, _ := FaultStep("kill", []string{"dc1/leader"})
	y, err := Inline(st)
	if err != nil || y != "steps:\n- kill: dc1/leader\n" {
		t.Errorf("%q %v", y, err)
	}
}
