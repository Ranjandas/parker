package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/spec"
)

func runCmd() *cobra.Command {
	var noUp, keepOnFail, noFirewall bool
	c := &cobra.Command{
		Use:   "run FILE [SCENARIO]",
		Short: "Bring the lab up and play its scenario; exits non-zero when a step fails",
		Long: "Bring the lab up (fresh, as `parker up` does) and play the file's scenario:, or the\n" +
			"named one from scenarios:. The daemon runs the steps and streams their progress.\n" +
			"A failed step exits 1; on_failure: keep (default) leaves the lab up for a look.",
		Example: "  parker run examples/scenario.yaml\n" +
			"  parker run raft.yaml leader-loss\n" +
			"  parker run --no-up raft.yaml leader-loss   # against the running lab",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 2 {
				name = args[1]
			}
			_, low, err := load(args[0])
			if err != nil {
				return err
			}
			if err := hasScenario(low, name); err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			if !noUp {
				if err := bringUp(ctx, low, upOptions{keepOnFail: keepOnFail, noFirewall: noFirewall}); err != nil {
					return err
				}
				say("")
			}
			cl, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			stream, err := cl.Run(ctx, connect.NewRequest(&parkerv1.RunRequest{Lab: low.Name, Scenario: name}))
			if err != nil {
				return rpcErr(err)
			}
			if err := printLines(stream); err != nil {
				cmd.SilenceUsage = true
				return fmt.Errorf("scenario failed: %w", err)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&noUp, "no-up", false, "play against the running lab instead of bringing it up fresh")
	c.Flags().BoolVar(&keepOnFail, "keep-on-failure", false, "leave the lab running if a readiness check fails during up")
	c.Flags().BoolVar(&noFirewall, "no-firewall", false, "don't add iptables ACCEPT rules for the lab bridges")
	return c
}

// hasScenario checks the file has the scenario before anything starts.
func hasScenario(low *spec.File, name string) error {
	have := slices.Sorted(maps.Keys(low.Scenarios))
	switch {
	case name == "" && low.Scenario == nil && len(have) == 0:
		return fmt.Errorf("%s has no scenario: (or scenarios:) to run", low.Name)
	case name == "" && low.Scenario == nil && len(have) > 1:
		return fmt.Errorf("%s has several scenarios; name one of: %s", low.Name, strings.Join(have, ", "))
	case name != "" && low.Scenarios[name] == nil:
		if len(have) == 0 {
			return fmt.Errorf("%s has no scenarios: section (drop %q to run scenario:)", low.Name, name)
		}
		return fmt.Errorf("no scenario %q (have: %s)", name, strings.Join(have, ", "))
	}
	return nil
}
