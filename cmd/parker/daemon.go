package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/api/parkerv1/parkerv1connect"
	"github.com/Ranjandas/parker/internal/daemon"
	"github.com/Ranjandas/parker/internal/lab"
)

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// daemonClient returns a client if a daemon answers on the socket.
func daemonClient(ctx context.Context) (parkerv1connect.ParkerServiceClient, bool) {
	c := daemon.Client(daemon.SocketPath())
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err := c.Version(ctx, connect.NewRequest(&parkerv1.VersionRequest{}))
	return c, err == nil
}

// ensureDaemon returns a client, starting `parker daemon` in the background
// if none is running. Its output goes to <run base>/daemon.log.
func ensureDaemon(ctx context.Context) (parkerv1connect.ParkerServiceClient, error) {
	if c, ok := daemonClient(ctx); ok {
		return c, nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(lab.RunBase(), 0o755); err != nil {
		return nil, lab.PrivHint(err)
	}
	logPath := filepath.Join(lab.RunBase(), "daemon.log")
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, lab.PrivHint(err)
	}
	defer lf.Close()
	cmd := exec.Command(self, "daemon")
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, ok := daemonClient(ctx); ok {
			fmt.Fprintf(os.Stderr, "started parker daemon (pid %d, log %s)\n", cmd.Process.Pid, logPath)
			return c, nil
		}
		select {
		case <-exited:
			return nil, fmt.Errorf("parker daemon exited at start; see %s", logPath)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("parker daemon did not come up within 5s; see %s", logPath)
}

// rpcErr strips connect's "code: " prefix so errors read like local ones.
func rpcErr(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return errors.New(ce.Message())
	}
	return err
}

func printLines(stream interface {
	Receive() bool
	Msg() *parkerv1.LogLine
	Err() error
}) error {
	for stream.Receive() {
		fmt.Println(stream.Msg().Line)
	}
	return rpcErr(stream.Err())
}

func upCmd() *cobra.Command {
	var keepOnFail, noFirewall, noDaemon bool
	c := &cobra.Command{
		Use:   "up FILE",
		Short: "Create the lab's network and start its processes; returns when everything is ready",
		Long: "Create the lab's network and start its processes; returns when everything is ready.\n\n" +
			"The lab is run by `parker daemon`, which is started in the background if needed and\n" +
			"keeps supervising the processes (restart: policies) after up returns.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, low, err := load(args[0])
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			if noDaemon {
				_, err := lab.Up(ctx, low, lab.Options{Log: os.Stdout, KeepOnFailure: keepOnFail, NoFirewall: noFirewall})
				if err != nil {
					return err
				}
			} else {
				c, err := ensureDaemon(ctx)
				if err != nil {
					return err
				}
				b, err := yaml.Marshal(low)
				if err != nil {
					return err
				}
				stream, err := c.Up(ctx, connect.NewRequest(&parkerv1.UpRequest{
					TopologyYaml: b, KeepOnFailure: keepOnFail, NoFirewall: noFirewall,
				}))
				if err != nil {
					return rpcErr(err)
				}
				if err := printLines(stream); err != nil {
					return err
				}
			}
			say("shell: parker exec %s %s -- consul members", args[0], firstNode(low))
			say("logs:  parker logs %s %s -f", args[0], firstNode(low))
			say("down:  parker down %s", args[0])
			return nil
		},
	}
	c.Flags().BoolVar(&keepOnFail, "keep-on-failure", false, "leave the lab running if a readiness check fails")
	c.Flags().BoolVar(&noFirewall, "no-firewall", false, "don't add iptables ACCEPT rules for the lab bridges; print them instead")
	c.Flags().BoolVar(&noDaemon, "no-daemon", false, "run in this process; nothing supervises the lab after up returns")
	return c
}

func downCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down FILE|LAB",
		Short: "Stop every process and remove the lab's namespaces, links and firewall rules (keeps logs)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := labName(args[0])
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			// Down must work with no daemon (or a dead one): fall back to
			// doing it here.
			if c, ok := daemonClient(ctx); ok {
				stream, err := c.Down(ctx, connect.NewRequest(&parkerv1.DownRequest{Lab: name}))
				if err != nil {
					return rpcErr(err)
				}
				if err := printLines(stream); err != nil {
					return err
				}
			} else if err := lab.Down(name, os.Stdout); err != nil {
				return err
			}
			say("lab %q is down", name)
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [FILE|LAB]",
		Short: "Show a lab's nodes and processes, or list labs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext()
			defer stop()
			c, viaDaemon := daemonClient(ctx)
			if len(args) == 0 {
				return listLabs(ctx, c, viaDaemon)
			}
			name, err := labName(args[0])
			if err != nil {
				return err
			}
			var st *parkerv1.StatusResponse
			if viaDaemon {
				resp, err := c.Status(ctx, connect.NewRequest(&parkerv1.StatusRequest{Lab: name}))
				if err != nil {
					return rpcErr(err)
				}
				st = resp.Msg
			} else {
				s, err := lab.GetStatus(name, nil)
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("no lab %q (no run dir under %s)", name, lab.RunBase())
				}
				if err != nil {
					return err
				}
				st = daemon.StatusToProto(s)
			}
			printStatus(os.Stdout, st)
			return nil
		},
	}
}

func listLabs(ctx context.Context, c parkerv1connect.ParkerServiceClient, viaDaemon bool) error {
	if !viaDaemon {
		names, err := lab.Labs()
		if err != nil {
			return err
		}
		for _, n := range names {
			say("%s", n)
		}
		return nil
	}
	resp, err := c.ListLabs(ctx, connect.NewRequest(&parkerv1.ListLabsRequest{}))
	if err != nil {
		return rpcErr(err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "LAB\tNAMESPACES\tSUPERVISED")
	for _, l := range resp.Msg.Labs {
		fmt.Fprintf(tw, "%s\t%d\t%v\n", l.Name, l.Namespaces, l.Supervised)
	}
	return tw.Flush()
}

func printStatus(w io.Writer, st *parkerv1.StatusResponse) {
	sup := "not supervised (no daemon owns it)"
	if st.Supervised {
		sup = "supervised by parker daemon"
	}
	fmt.Fprintf(w, "lab %s: %s; run dir %s\n", st.Lab, sup, st.RunDir)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tIP\tPROCESS\tPID\tSTATE\tRESTARTS")
	for _, n := range st.Nodes {
		ip := strings.Join(n.Ips, ",")
		if !n.Namespace {
			ip += " (no netns)"
		}
		for _, p := range n.Processes {
			state := "dead"
			if p.Alive {
				state = "running"
				if p.Ready != nil {
					if *p.Ready {
						state = "ready"
					} else {
						state = "starting"
					}
				}
			}
			pid := "-"
			if p.Pid > 0 {
				pid = fmt.Sprint(p.Pid)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n", n.Name, ip, p.Name, pid, state, p.Restarts)
		}
	}
	tw.Flush()
}

func logsCmd() *cobra.Command {
	var follow bool
	var lines int
	c := &cobra.Command{
		Use:   "logs FILE|LAB NODE[/PROCESS]",
		Short: "Print a node's process log (default: its only process, else consul)",
		Example: "  parker logs examples/minimal.yaml dc1-server-1 -f\n" +
			"  parker logs clients dc1-web-1/web -n 100",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := labName(args[0])
			if err != nil {
				return err
			}
			node, proc := args[1], ""
			// dc1/web-1 is a node; dc1-web-1/web is node/process.
			if i := strings.LastIndex(node, "/"); i >= 0 && strings.Contains(node[:i], "-") {
				node, proc = node[:i], node[i+1:]
			}
			ctx, stop := signalContext()
			defer stop()
			if c, ok := daemonClient(ctx); ok {
				stream, err := c.Logs(ctx, connect.NewRequest(&parkerv1.LogsRequest{
					Lab: name, Node: node, Process: proc, TailLines: int32(lines), Follow: follow,
				}))
				if err != nil {
					return rpcErr(err)
				}
				for stream.Receive() {
					os.Stdout.Write(stream.Msg().Data)
				}
				if err := stream.Err(); err != nil && ctx.Err() == nil {
					return rpcErr(err)
				}
				return nil
			}
			path, err := daemon.LogPath(name, node, proc)
			if err != nil {
				return err
			}
			return daemon.FollowLog(ctx, path, lines, follow, func(b []byte) error {
				_, err := os.Stdout.Write(b)
				return err
			})
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	c.Flags().IntVarP(&lines, "lines", "n", 50, "lines to print first (-1 for all)")
	return c
}

func daemonCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "daemon",
		Short: "Run the Parker daemon in the foreground (`parker up` starts one when needed)",
		Long: "Run the Parker daemon in the foreground. It supervises labs started through it and\n" +
			"serves the Parker API (gRPC and HTTP/JSON via ConnectRPC) on " + daemon.SocketPath() + ".\n" +
			"Stopping it leaves labs running; `parker down` works without it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext()
			defer stop()
			return daemon.Serve(ctx, daemon.SocketPath(), os.Stdout)
		},
	}
	c.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon (labs keep running)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cl, ok := daemonClient(ctx)
			if !ok {
				say("no parker daemon on %s", daemon.SocketPath())
				return nil
			}
			v, err := cl.Version(ctx, connect.NewRequest(&parkerv1.VersionRequest{}))
			if err != nil {
				return rpcErr(err)
			}
			if _, err := cl.Shutdown(ctx, connect.NewRequest(&parkerv1.ShutdownRequest{})); err != nil {
				return rpcErr(err)
			}
			// Wait for the socket to go away so a following start is clean.
			for range 50 {
				if _, ok := daemonClient(ctx); !ok {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			say("stopped parker daemon (pid %d)", v.Msg.Pid)
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show whether a daemon is running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cl, ok := daemonClient(ctx)
			if !ok {
				say("no parker daemon on %s", daemon.SocketPath())
				return nil
			}
			v, err := cl.Version(ctx, connect.NewRequest(&parkerv1.VersionRequest{}))
			if err != nil {
				return rpcErr(err)
			}
			say("parker daemon %s, pid %d, on %s", v.Msg.Version, v.Msg.Pid, daemon.SocketPath())
			return nil
		},
	})
	return c
}
