package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/vishvananda/netns"
	"golang.org/x/term"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/api/parkerv1/parkerv1connect"
	"github.com/Ranjandas/parker/internal/lab"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
)

func execCmd() *cobra.Command {
	var noTTY, printEnv bool
	c := &cobra.Command{
		Use:   "exec FILE|LAB NODE [-- COMMAND...]",
		Short: "Run a command (default: $SHELL) inside a node's network namespace",
		Long: "NODE is a selector that names one node: dc1/server-1, dc1/web-2, dc1/leader,\n" +
			"svc:api (when one node runs it), a top-level node, or the expanded name dc1-server-1.\n\n" +
			"Locally the command runs directly in the namespace. With --host / PARKER_HOST it runs\n" +
			"on that daemon, streamed over the API with a terminal when stdin is one.\n\n" +
			"--env prints, instead, the export lines that point the consul CLI on this machine at\n" +
			"the node's agent: its address, the lab's management token and CA. eval them where the\n" +
			"host can reach the node's address (not under a rootless daemon); inside the node,\n" +
			"`parker exec` already sets them.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			argv := args[2:]
			if printEnv {
				return execEnv(cmd.OutOrStdout(), args[0], args[1])
			}
			// Remote daemons, and a rootless one (whose namespaces this
			// process can't enter), get exec over the API.
			if target().Host != "" || isRootless() {
				name, err := labName(args[0])
				if err != nil {
					return err
				}
				code, err := remoteExec(name, args[1], argv, !noTTY)
				if err != nil {
					return err
				}
				os.Exit(code)
			}
			low, err := topologyFor(args[0])
			if err != nil {
				return err
			}
			return localExec(low, args[1], argv)
		},
	}
	c.Flags().BoolVarP(&noTTY, "no-tty", "T", false, "don't allocate a terminal (remote exec)")
	c.Flags().BoolVar(&printEnv, "env", false, "print export lines for the consul CLI on this machine (address, management token, CA) instead of running a command")
	return c
}

// execEnv prints the consul CLI's environment for a node, to eval on the
// host.
func execEnv(w io.Writer, arg, nodeArg string) error {
	if target().Host != "" {
		return errors.New("--env needs the lab on this machine (no --host)")
	}
	low, err := topologyFor(arg)
	if err != nil {
		return err
	}
	node, err := lab.ResolveNode(low, nodeArg)
	if err != nil {
		return err
	}
	env, err := lab.ConsulEnv(low, node)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "# %s in lab %s; eval \"$(parker exec %s %s --env)\"\n", node, low.Name, arg, nodeArg)
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(w, "export %s=%s\n", k, shellQuote(v))
	}
	return nil
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// topologyFor returns the running lab's saved topology, for FILE or LAB.
// A FILE is only expanded when its lab isn't running: expanding again would
// generate new tokens, not the ones the lab runs with.
func topologyFor(arg string) (*spec.File, error) {
	if _, err := os.Stat(arg); err == nil {
		name, err := labName(arg)
		if err != nil {
			return nil, err
		}
		if low, err := lab.LoadTopology(name); err == nil {
			return low, nil
		}
		_, low, err := load(arg)
		return low, err
	}
	low, err := lab.LoadTopology(arg)
	if err != nil {
		return nil, fmt.Errorf("lab %q: %w", arg, err)
	}
	return low, nil
}

// localExec replaces this process with argv inside the node's netns.
func localExec(low *spec.File, nodeArg string, argv []string) error {
	node, err := lab.ResolveNode(low, nodeArg)
	if err != nil {
		return err
	}
	if len(argv) == 0 {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		argv = []string{sh}
	}
	path, env := lab.ExecEnv(low, node, os.Environ(), os.Stderr)
	bin, err := lab.LookPath(argv[0], path)
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	ns, err := netns.GetFromPath(netsetup.NetnsPath(low.Name, node))
	if err != nil {
		return fmt.Errorf("lab not running? %w", err)
	}
	if err := netns.Set(ns); err != nil {
		return lab.PrivHint(err)
	}
	return syscall.Exec(bin, argv, env)
}

// remoteExec runs argv on the daemon and returns its exit code.
func remoteExec(labName, node string, argv []string, wantTTY bool) (int, error) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	c, ok, err := connectDaemon(ctx)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.New("no parker daemon")
	}
	return execStream(ctx, c, labName, node, argv, wantTTY, os.Stdin, os.Stdout, os.Stderr)
}

func execStream(ctx context.Context, c parkerv1connect.ParkerServiceClient, labName, node string, argv []string,
	wantTTY bool, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	inFd, outFd := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	tty := wantTTY && stdin == os.Stdin && term.IsTerminal(inFd) && term.IsTerminal(outFd)

	start := &parkerv1.ExecStart{Lab: labName, Node: node, Argv: argv, Tty: tty}
	if tty {
		if t := os.Getenv("TERM"); t != "" {
			start.Env = append(start.Env, "TERM="+t)
		}
		if w, h, err := term.GetSize(outFd); err == nil {
			start.Size = &parkerv1.TermSize{Rows: uint32(h), Cols: uint32(w)}
		}
	}
	stream := c.Exec(ctx)
	if err := stream.Send(&parkerv1.ExecRequest{Msg: &parkerv1.ExecRequest_Start{Start: start}}); err != nil {
		return 0, rpcErr(err)
	}
	if tty {
		old, err := term.MakeRaw(inFd)
		if err == nil {
			defer term.Restore(inFd, old)
		}
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				if w, h, err := term.GetSize(outFd); err == nil {
					_ = stream.Send(&parkerv1.ExecRequest{Msg: &parkerv1.ExecRequest_Resize{Resize: &parkerv1.TermSize{Rows: uint32(h), Cols: uint32(w)}}})
				}
			}
		}()
	}
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				if stream.Send(&parkerv1.ExecRequest{Msg: &parkerv1.ExecRequest_Stdin{Stdin: append([]byte(nil), buf[:n]...)}}) != nil {
					return
				}
			}
			if err != nil {
				if !tty {
					_ = stream.Send(&parkerv1.ExecRequest{Msg: &parkerv1.ExecRequest_StdinEof{StdinEof: true}})
				}
				return
			}
		}
	}()
	for {
		msg, err := stream.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, errors.New("exec stream ended without an exit code")
			}
			return 0, rpcErr(err)
		}
		switch m := msg.Msg.(type) {
		case *parkerv1.ExecResponse_Stdout:
			_, _ = stdout.Write(m.Stdout)
		case *parkerv1.ExecResponse_Stderr:
			_, _ = stderr.Write(m.Stderr)
		case *parkerv1.ExecResponse_ExitCode:
			_ = stream.CloseRequest()
			_ = stream.CloseResponse()
			return int(m.ExitCode), nil
		}
	}
}
