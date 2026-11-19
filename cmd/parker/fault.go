package main

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/scenario"
	"github.com/Ranjandas/parker/internal/spec"
)

func faultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fault FILE|LAB ACTION [SELECTOR...]",
		Short: "Break a running lab on purpose: partition, heal, kill, stop, start, restart, pause, resume",
		Long: "Runs one fault step on the daemon, as a scenario step would.\n\n" +
			"  partition GROUP [GROUP...]  cut the network between groups (a GROUP is selectors\n" +
			"                              joined by commas); one group is cut off from the rest\n" +
			"  heal [SELECTOR]             remove cuts to and from the nodes (default: all)\n" +
			"  kill SELECTOR               SIGKILL the nodes' processes (restart policy applies)\n" +
			"  stop|start|restart SELECTOR stop keeps them down until start\n" +
			"  pause|resume SELECTOR       SIGSTOP / SIGCONT",
		Example: "  parker fault lab.yaml kill dc1/leader\n" +
			"  parker fault lab.yaml partition dc1/server-1,dc1/server-2 dc1/server-3\n" +
			"  parker fault lab.yaml partition dc2          # dc2 alone\n" +
			"  parker fault lab.yaml heal",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := labName(args[0])
			if err != nil {
				return err
			}
			st, err := faultStep(args[1], args[2:])
			if err != nil {
				return err
			}
			y, err := yaml.Marshal(&spec.Scenario{Steps: []*spec.Step{st}})
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			cl, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			stream, err := cl.Run(ctx, connect.NewRequest(&parkerv1.RunRequest{Lab: name, Inline: string(y)}))
			if err != nil {
				return rpcErr(err)
			}
			if err := printLines(stream); err != nil {
				cmd.SilenceUsage = true
				return err
			}
			return nil
		},
	}
}

// faultStep turns `parker fault` arguments into a scenario step.
func faultStep(action string, args []string) (*spec.Step, error) {
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
		return nil, fmt.Errorf("unknown action %q (want partition, heal or %s)", action, strings.Join(scenario.ProcActions, ", "))
	}
	if err != nil {
		return nil, err
	}
	return st, nil
}
