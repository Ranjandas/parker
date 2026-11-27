package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/daemon"
	"github.com/Ranjandas/parker/internal/proxy"
	"github.com/Ranjandas/parker/internal/tui"
)

func tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui [FILE|LAB]",
		Short: "Terminal UI: browse labs and nodes, open shells, follow logs, inject faults",
		Long: "A k9s-style terminal UI over the daemon API, local or remote (--host).\n\n" +
			"Keys: enter open · s shell into the node · l follow the process log\n" +
			"(f follow, w wrap, / filter) · d describe the node · / filter rows\n" +
			"· esc back · q quit\n\n" +
			"Faults on the selected node: K kill · R restart · S stop/start · P pause/resume\n" +
			"· x partition it from the rest · H heal all (disruptive ones ask y/n first).\n\n" +
			"Labs with several datacenters (or admin partitions) open on them: enter one for its nodes.\n" +
			"The HEALTH column shows each Consul agent's members (alive/known), each service's catalog\n" +
			"health and each Vault server's raft voters; ROLE shows raft roles and Vault's active, standby\n" +
			"or sealed.\n\n" +
			"Commands: :labs · :lab NAME · :dc [NAME[/PARTITION]] · :svc (services and their instances)\n" +
			"· :run [SCENARIO] (watch it live; esc stops it)\n" +
			"· :kill|:stop|:start|:restart|:pause|:resume [SELECTOR] · :partition GROUP... · :heal [SELECTOR]\n" +
			"· :pf [LOCAL:]REMOTE (port-forward from the selected node; :pf lists, :pf stop) · :q",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext()
			defer stop()
			c, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			o := tui.Options{Client: c, Where: daemonWhere(), Shell: shellCommand,
				Static: os.Getenv("NO_COLOR") != "" || os.Getenv("PARKER_TUI_STATIC") != "",
				Run: func(ctx context.Context, lab, sc, inline string, line func(string)) error {
					s, err := c.Run(ctx, connect.NewRequest(&parkerv1.RunRequest{Lab: lab, Scenario: sc, Inline: inline}))
					if err != nil {
						return rpcErr(err)
					}
					defer s.Close()
					for s.Receive() {
						line(s.Msg().Line)
					}
					return rpcErr(s.Err())
				},
				Forward: func(ctx context.Context, lab, node, local, remote string) (string, error) {
					ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", local))
					if err != nil {
						return "", err
					}
					target := net.JoinHostPort(node+"."+lab+".parker", remote)
					go func() { _ = proxy.Forward(ctx, ln, target, proxy.APIDialer(c, lab), func(string, ...any) {}) }()
					return ln.Addr().String(), nil
				},
			}
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
