package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/protobuf/encoding/protojson"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/api/parkerv1/parkerv1connect"
	"github.com/Ranjandas/parker/internal/compat"
	"github.com/Ranjandas/parker/internal/daemon"
	"github.com/Ranjandas/parker/internal/fetch"
	"github.com/Ranjandas/parker/internal/lab"
	"github.com/Ranjandas/parker/internal/license"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/rootless"
	"github.com/Ranjandas/parker/internal/scenario"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/tui"
)

// isRootless: this CLI runs as a normal user (not inside the rootless
// daemon), so it uses, and starts, the per-user rootless daemon.
func isRootless() bool {
	return os.Geteuid() != 0 && !rootless.InChild() && os.Getenv("PARKER_RUN_DIR") == ""
}

func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(no log at " + path + ")"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

var hostFlag string

// errNotLinux: off Linux, parker is a client for a daemon on a Linux host.
var errNotLinux = errors.New("labs need Linux (network namespaces); from " + runtime.GOOS +
	" point parker at a daemon on a Linux host:\n" +
	"  PARKER_HOST=https://HOST:7443 PARKER_TOKEN=... PARKER_TLS_CA=cert.pem parker status\n" +
	"(on that host: parker daemon --listen :7443, then parker daemon credentials)")

// target is where the CLI talks to a daemon: --host / PARKER_HOST, with
// PARKER_TOKEN, PARKER_TLS_CA and PARKER_TLS_INSECURE=1 for https.
func target() daemon.Target {
	h := hostFlag
	if h == "" {
		h = os.Getenv("PARKER_HOST")
	}
	return daemon.Target{
		Host:     h,
		Token:    os.Getenv("PARKER_TOKEN"),
		CAFile:   os.Getenv("PARKER_TLS_CA"),
		Insecure: os.Getenv("PARKER_TLS_INSECURE") == "1",
	}
}

// connectDaemon returns a client if a daemon answers. With no host set and
// no local daemon it returns ok=false so callers can work locally; with a
// host set, failing to reach it is an error.
func connectDaemon(ctx context.Context) (parkerv1connect.ParkerServiceClient, bool, error) {
	t := target()
	c, err := t.Dial()
	if err != nil {
		return nil, false, err
	}
	vctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := c.Version(vctx, connect.NewRequest(&parkerv1.VersionRequest{})); err != nil {
		if t.Host != "" {
			return nil, false, fmt.Errorf("parker daemon at %s: %w", t.Host, rpcErr(err))
		}
		return nil, false, nil
	}
	return c, true, nil
}

// localDaemon is connectDaemon for callers that only care about the local
// socket answering (daemon stop/status with no host set).
func localDaemon(ctx context.Context) (parkerv1connect.ParkerServiceClient, bool) {
	c, ok, _ := connectDaemon(ctx)
	return c, ok
}

// ensureDaemon returns a client, starting `parker daemon` in the background
// if no host is set and none is running. Its output goes to
// <run base>/daemon.log.
func ensureDaemon(ctx context.Context) (parkerv1connect.ParkerServiceClient, error) {
	c, ok, err := connectDaemon(ctx)
	if err != nil {
		return nil, err
	}
	if ok {
		return c, checkBuild(ctx, c)
	}
	if runtime.GOOS != "linux" {
		return nil, errNotLinux
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if isRootless() {
		if err := os.MkdirAll(rootless.Dir(), 0o700); err != nil {
			return nil, err
		}
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if isRootless() {
		cmd = rootless.Command(self, "daemon")
	}
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		if isRootless() {
			return nil, rootless.Explain(err)
		}
		return nil, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, ok := localDaemon(ctx); ok {
			mode := ""
			if isRootless() {
				mode = " in rootless mode; reach nodes with parker exec/proxy/port-forward"
			}
			fmt.Fprintf(os.Stderr, "started parker daemon%s (pid %d, log %s)\n", mode, cmd.Process.Pid, logPath)
			return c, nil
		}
		select {
		case <-exited:
			return nil, fmt.Errorf("parker daemon exited at start:\n%s", tailFile(logPath, 8))
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("parker daemon did not come up within 5s; see %s", logPath)
}

// checkBuild refuses a local daemon from another build: the client
// expands the topology, so an older daemon rejects what a newer client
// sends (unknown keys). A remote daemon only gets a warning, since
// restarting it is its host's call.
func checkBuild(ctx context.Context, c parkerv1connect.ParkerServiceClient) error {
	r, err := c.Version(ctx, connect.NewRequest(&parkerv1.VersionRequest{}))
	if err != nil {
		return rpcErr(err)
	}
	return buildMismatch(r.Msg.Version, int(r.Msg.Pid), target().Host)
}

func buildMismatch(daemonBuild string, pid int, host string) error {
	if daemonBuild == buildID() {
		return nil
	}
	if host != "" {
		fmt.Fprintf(os.Stderr, "warning: parker daemon at %s is %s, this client is %s\n", host, daemonBuild, buildID())
		return nil
	}
	return fmt.Errorf("the running parker daemon (pid %d) is %s, but this client is %s;\n"+
		"stop it with `parker daemon stop` and run the command again to start this build's daemon\n(labs keep running, except rootless ones, which go down with their daemon)", pid, daemonBuild, buildID())
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
	return writeLines(os.Stdout, stream)
}

func writeLines(w io.Writer, stream interface {
	Receive() bool
	Msg() *parkerv1.LogLine
	Err() error
}) error {
	for stream.Receive() {
		fmt.Fprintln(w, stream.Msg().Line)
	}
	return rpcErr(stream.Err())
}

type upOptions struct {
	keepOnFail, noFirewall, noDaemon bool
	// out takes progress (stdout when nil); quiet leaves out the
	// compatibility warnings (the caller has shown them).
	out   io.Writer
	quiet bool
}

// bringUp starts the lab, through the daemon unless noDaemon.
func bringUp(ctx context.Context, low *spec.File, o upOptions) error {
	out := o.out
	if out == nil {
		out = os.Stdout
	}
	if !o.quiet {
		for _, w := range compat.Warnings(low) {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
	}
	// The license, checked before anything is fetched or started.
	var targets []string
	for _, pv := range upBinaries(low) {
		if !slices.Contains(slices.Collect(maps.Values(low.Versions)), pv[1]) {
			targets = append(targets, pv[1]) // scenario upgrade targets
		}
	}
	lic, err := license.For(low, os.Getenv, targets...)
	if err != nil {
		return err
	}
	// Fetch here: a rootless daemon has no network of its own, and the
	// cache is shared. Scenario upgrade targets too, for the same reason.
	if !target().IsRemote() {
		for _, pv := range upBinaries(low) {
			if _, err := fetch.Binary(pv[0], pv[1], out); err != nil {
				return err
			}
		}
	}
	if o.noDaemon {
		_, err := lab.Up(ctx, low, lab.Options{Log: out, KeepOnFailure: o.keepOnFail, NoFirewall: o.noFirewall, License: lic})
		return err
	}
	c, err := ensureDaemon(ctx)
	if err != nil {
		return err
	}
	b, err := yaml.Marshal(low)
	if err != nil {
		return err
	}
	stream, err := c.Up(ctx, connect.NewRequest(&parkerv1.UpRequest{
		TopologyYaml: b, KeepOnFailure: o.keepOnFail, NoFirewall: o.noFirewall, ConsulLicense: lic,
	}))
	if err != nil {
		return rpcErr(err)
	}
	return writeLines(out, stream)
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
			if err := bringUp(ctx, low, upOptions{keepOnFail: keepOnFail, noFirewall: noFirewall, noDaemon: noDaemon}); err != nil {
				return err
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
	var purge bool
	c := &cobra.Command{
		Use:   "down FILE|LAB",
		Short: "Stop every process and remove the lab's namespaces, links and firewall rules (keeps logs; --purge removes them too)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := labName(args[0])
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			c, ok, err := connectDaemon(ctx)
			if err != nil {
				return err
			}
			if err := downLab(ctx, c, ok, name, purge); err != nil {
				return err
			}
			if purge {
				say("lab %q is down and purged", name)
			} else {
				say("lab %q is down", name)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&purge, "purge", false, "also delete the lab's run dir: logs, configs and data, so status no longer lists it")
	return c
}

// downLab downs (and optionally purges) a lab through the daemon, or here
// when there is none: down must work with no daemon or a dead one.
func downLab(ctx context.Context, c parkerv1connect.ParkerServiceClient, viaDaemon bool, name string, purge bool) error {
	if viaDaemon {
		stream, err := c.Down(ctx, connect.NewRequest(&parkerv1.DownRequest{Lab: name, Purge: purge}))
		if err != nil {
			return rpcErr(err)
		}
		return printLines(stream)
	}
	if err := lab.Down(name, os.Stdout); err != nil {
		return err
	}
	if purge {
		return lab.Purge(name, os.Stdout)
	}
	return nil
}

func pruneCmd() *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "prune",
		Short: "Purge every lab that isn't running: delete their run dirs (logs, configs, data)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext()
			defer stop()
			c, viaDaemon, err := connectDaemon(ctx)
			if err != nil {
				return err
			}
			labs, err := labStates(ctx, c, viaDaemon)
			if err != nil {
				return err
			}
			var stopped []string
			for _, l := range labs {
				if !l.running {
					stopped = append(stopped, l.name)
				}
			}
			if len(stopped) == 0 {
				say("nothing to prune: no stopped labs")
				return nil
			}
			say("stopped labs: %s", strings.Join(stopped, ", "))
			if !yes {
				ok, err := confirm(cmd.InOrStdin(), "purge them? [y/N] ")
				if err != nil {
					return err
				}
				if !ok {
					say("left them alone")
					return nil
				}
			}
			for _, n := range stopped {
				if err := downLab(ctx, c, viaDaemon, n, true); err != nil {
					return err
				}
			}
			return nil
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask first")
	return c
}

// confirm asks a yes/no question on a terminal; with no terminal it
// refuses rather than guess.
func confirm(in io.Reader, q string) (bool, error) {
	if f, ok := in.(*os.File); ok && !term.IsTerminal(int(f.Fd())) {
		return false, errors.New("not a terminal: pass -y to confirm")
	}
	fmt.Print(q)
	line, _ := bufio.NewReader(in).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}

type labState struct {
	name       string
	namespaces int
	supervised bool
	running    bool
}

func labStates(ctx context.Context, c parkerv1connect.ParkerServiceClient, viaDaemon bool) ([]labState, error) {
	var out []labState
	if !viaDaemon {
		names, err := lab.Labs()
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			ns, _ := netsetup.Namespaces(n)
			out = append(out, labState{name: n, namespaces: len(ns), running: len(ns) > 0})
		}
		return out, nil
	}
	resp, err := c.ListLabs(ctx, connect.NewRequest(&parkerv1.ListLabsRequest{}))
	if err != nil {
		return nil, rpcErr(err)
	}
	for _, l := range resp.Msg.Labs {
		out = append(out, labState{name: l.Name, namespaces: int(l.Namespaces), supervised: l.Supervised, running: l.Namespaces > 0 || l.Supervised})
	}
	return out, nil
}

func statusCmd() *cobra.Command {
	var output string
	c := &cobra.Command{
		Use:   "status [FILE|LAB]",
		Short: "Show a lab's nodes and processes, or list labs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext()
			defer stop()
			c, viaDaemon, err := connectDaemon(ctx)
			if err != nil {
				return err
			}
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
			switch output {
			case "json":
				return printStatusJSON(os.Stdout, st)
			case "", "table":
				printStatus(os.Stdout, st)
			default:
				return fmt.Errorf("-o %s: want table or json", output)
			}
			return nil
		},
	}
	c.Flags().StringVarP(&output, "output", "o", "table", "table or json")
	return c
}

func listLabs(ctx context.Context, c parkerv1connect.ParkerServiceClient, viaDaemon bool) error {
	labs, err := labStates(ctx, c, viaDaemon)
	if err != nil {
		return err
	}
	printLabs(os.Stdout, labs, viaDaemon)
	return nil
}

func printLabs(w io.Writer, labs []labState, viaDaemon bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	hdr := "LAB\tSTATE\tNAMESPACES"
	if viaDaemon {
		hdr += "\tSUPERVISED"
	}
	fmt.Fprintln(tw, hdr)
	var stopped int
	for _, l := range labs {
		state := "running"
		if !l.running {
			state, stopped = "down", stopped+1
		}
		row := fmt.Sprintf("%s\t%s\t%d", l.name, state, l.namespaces)
		if viaDaemon {
			row += fmt.Sprintf("\t%v", l.supervised)
		}
		fmt.Fprintln(tw, row)
	}
	tw.Flush()
	if stopped > 0 {
		fmt.Fprintf(w, "\n%d down (logs kept); parker down LAB --purge or parker prune removes them\n", stopped)
	}
}

// printStatusJSON writes st as indented JSON. protojson varies its
// whitespace on purpose; re-indenting keeps the output stable for scripts.
func printStatusJSON(w io.Writer, st *parkerv1.StatusResponse) error {
	b, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(st)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	_, err = w.Write(out.Bytes())
	return err
}

func printStatus(w io.Writer, st *parkerv1.StatusResponse) {
	sup := "not supervised (no daemon owns it)"
	if st.Supervised {
		sup = "supervised by parker daemon"
	}
	fmt.Fprintf(w, "lab %s: %s; run dir %s\n", st.Lab, sup, st.RunDir)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tIP\tPROCESS\tPID\tSTATE\tRESTARTS\tROLE")
	for _, n := range st.Nodes {
		ip := strings.Join(n.Ips, ",")
		if !n.Namespace {
			ip += " (no netns)"
		}
		for _, p := range n.Processes {
			state := tui.ProcState(p)
			pid := "-"
			if p.Pid > 0 {
				pid = fmt.Sprint(p.Pid)
			}
			role := "-"
			if p.Name == "consul" && n.Role != "" {
				role = n.Role
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", n.Name, ip, p.Name, pid, state, p.Restarts, role)
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
			c, ok, err := connectDaemon(ctx)
			if err != nil {
				return err
			}
			if ok {
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
	var listen string
	var sans []string
	c := &cobra.Command{
		Use:   "daemon",
		Short: "Run the Parker daemon in the foreground (`parker up` starts one when needed)",
		Long: "Run the Parker daemon in the foreground. It supervises labs started through it and\n" +
			"serves the Parker API (gRPC and HTTP/JSON via ConnectRPC) on " + daemon.SocketPath() + ".\n" +
			"Stopping it leaves labs running; `parker down` works without it.\n\n" +
			"--listen also serves the API over TCP for remote clients (TLS with a self-signed\n" +
			"certificate, and a bearer token), so another machine can run every parker command,\n" +
			"exec included, against this one. See `parker daemon credentials`.",
		Example: "  sudo parker daemon --listen :7443\n" +
			"  # or keep it local and tunnel the socket:\n" +
			"  ssh -N -L /tmp/parker.sock:/run/parker/parker.sock vm & PARKER_HOST=unix:///tmp/parker.sock parker status",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS != "linux" {
				return errNotLinux
			}
			ctx, stop := signalContext()
			defer stop()
			if isRootless() {
				// Re-run this command inside fresh user, network and mount
				// namespaces, where we are root over a private network.
				self, err := os.Executable()
				if err != nil {
					return err
				}
				child := rootless.Command(self, os.Args[1:]...)
				child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
				child.SysProcAttr.Setsid = false
				if err := child.Start(); err != nil {
					return rootless.Explain(err)
				}
				go func() { <-ctx.Done(); _ = child.Process.Signal(syscall.SIGTERM) }()
				return child.Wait()
			}
			opts := daemon.ServeOptions{Socket: daemon.SocketPath(), Listen: listen, SANs: sans}
			if rootless.InChild() {
				if err := rootless.Setup(); err != nil {
					return err
				}
				opts.DownLabsOnExit = true
				say("rootless mode: labs live in this daemon's namespaces and stop with it")
			}
			return daemon.Serve(ctx, opts, os.Stdout)
		},
	}
	c.Flags().StringVar(&listen, "listen", "", "also serve remote clients on this TCP address (e.g. :7443); TLS + token")
	c.Flags().StringSliceVar(&sans, "tls-san", nil, "extra DNS names or IPs for the self-signed certificate (e.g. the VM's public IP)")
	c.AddCommand(&cobra.Command{
		Use:   "credentials",
		Short: "Print what a remote client needs: token and certificate (creates them if missing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			tok, err := daemon.Token()
			if err != nil {
				return lab.PrivHint(err)
			}
			say("# on the client (copy %s to ./parker-ca.pem first):", daemon.CertPath())
			say("export PARKER_HOST=https://<this-host>:7443")
			say("export PARKER_TOKEN=%s", tok)
			say("export PARKER_TLS_CA=$PWD/parker-ca.pem")
			if _, err := os.Stat(daemon.CertPath()); err != nil {
				say("# no certificate yet: it is created when `parker daemon --listen` first starts")
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon (labs keep running)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cl, ok := localDaemon(ctx)
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
				if _, ok := localDaemon(ctx); !ok {
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
			cl, ok := localDaemon(ctx)
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

// upBinaries is what up must have cached: the pinned binaries a process
// runs (and their per-process versions), and scenario upgrade targets.
func upBinaries(low *spec.File) [][2]string {
	up := map[string]bool{}
	for _, sc := range allScenarios(low) {
		for _, u := range scenario.Upgrades(sc) {
			up[u.Product()+"@"+u.To] = true
		}
	}
	var out [][2]string
	for _, pv := range wantedBinaries(low) {
		if lab.Needs(low, pv[0]) || up[pv[0]+"@"+pv[1]] {
			out = append(out, pv)
		}
	}
	return out
}
