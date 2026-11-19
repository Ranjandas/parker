package main

import (
	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/scenario"
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
			st, err := scenario.FaultStep(args[1], args[2:])
			if err != nil {
				return err
			}
			y, err := scenario.Inline(st)
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
