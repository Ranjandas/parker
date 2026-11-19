package scenario

import (
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/Ranjandas/parker/internal/spec"
)

// FaultStep turns `parker fault` (and TUI) arguments into a fault step:
// partition GROUP..., heal [SELECTOR], or a process action and one
// selector. A GROUP is selectors joined by commas.
func FaultStep(action string, args []string) (*spec.Step, error) {
	st := &spec.Step{}
	one := func(dst *string) error {
		if len(args) != 1 {
			return fmt.Errorf("%s takes one selector", action)
		}
		*dst = args[0]
		return nil
	}
	var err error
	switch action {
	case "partition":
		if len(args) == 0 {
			return nil, fmt.Errorf("partition needs at least one group (selectors joined by commas)")
		}
		for _, a := range args {
			var g spec.NodeGroup
			for _, sel := range strings.Split(a, ",") {
				if sel = strings.TrimSpace(sel); sel != "" {
					g = append(g, sel)
				}
			}
			if len(g) == 0 {
				return nil, fmt.Errorf("partition: empty group %q", a)
			}
			st.Partition = append(st.Partition, g)
		}
	case "heal":
		st.Heal = "all"
		if len(args) > 1 {
			return nil, fmt.Errorf("heal takes at most one selector")
		}
		if len(args) == 1 {
			st.Heal = args[0]
		}
	case "kill":
		err = one(&st.Kill)
	case "stop":
		err = one(&st.Stop)
	case "start":
		err = one(&st.Start)
	case "restart":
		err = one(&st.Restart)
	case "pause":
		err = one(&st.Pause)
	case "resume":
		err = one(&st.Resume)
	default:
		return nil, fmt.Errorf("unknown action %q (want partition, heal or %s)", action, strings.Join(ProcActions, ", "))
	}
	if err != nil {
		return nil, err
	}
	return st, nil
}

// Inline renders a one-step scenario for RunRequest.inline.
func Inline(steps ...*spec.Step) (string, error) {
	b, err := yaml.Marshal(&spec.Scenario{Steps: steps})
	return string(b), err
}
