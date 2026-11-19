package main

import (
	"fmt"
	"os"
	"os/exec"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/daemon"
	"github.com/Ranjandas/parker/internal/tui"
)

func tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui [FILE|LAB]",
		Short: "Terminal UI: browse labs and nodes, open shells, follow logs",
		Long: "A k9s-style terminal UI over the daemon API, local or remote (--host).\n\n" +
			"Keys: enter open · s shell into the node · l follow the process log\n" +
			"(f follow, w wrap, / filter) · d describe the node · / filter rows\n" +
			"· : commands (:labs, :lab NAME, :dc NAME, :q) · esc back · q quit",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext()
			defer stop()
			c, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			o := tui.Options{Client: c, Where: daemonWhere(), Shell: shellCommand}
			if len(args) == 1 {
				if o.Lab, err = labName(args[0]); err != nil {
					return err
				}
			}
			return tui.Run(o)
		},
	}
}

func daemonWhere() string {
	if h := target().Host; h != "" {
		return h
	}
	return daemon.SocketPath()
}

// shellCommand runs `parker exec LAB NODE` as a child with the terminal,
// against the same daemon as this process.
func shellCommand(labName, node string) *exec.Cmd {
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	var args []string
	if hostFlag != "" {
		args = append(args, "--host", hostFlag)
	}
	args = append(args, "exec", labName, node)
	cmd := exec.Command(self, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

func describeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe FILE|LAB NODE",
		Short: "Print a node's expanded topology: networks, rendered files, processes",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext()
			defer stop()
			name, err := labName(args[0])
			if err != nil {
				return err
			}
			c, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			r, err := c.Describe(ctx, connect.NewRequest(&parkerv1.DescribeRequest{Lab: name, Node: args[1]}))
			if err != nil {
				return rpcErr(err)
			}
			fmt.Print(r.Msg.Yaml)
			return nil
		},
	}
}
