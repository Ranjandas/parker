package main

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Ranjandas/parker/internal/daemon"
	"github.com/Ranjandas/parker/internal/proxy"
)

func proxyCmd() *cobra.Command {
	var listen, scope string
	c := &cobra.Command{
		Use:   "proxy",
		Short: "Open a local SOCKS5/HTTP proxy into the labs (like kubectl proxy)",
		Long: "Open a local proxy that tunnels every connection through the daemon, so a browser or\n" +
			"curl on this machine reaches any node IP of any running lab, locally or on a remote\n" +
			"daemon (--host). Names resolve on the daemon side: <node>.<lab>.parker\n" +
			"(dc1-server-1.minimal.parker), <name>.<dc>.<lab>.parker (server-1.dc1.minimal.parker),\n" +
			"or <node>.parker when unique. Only addresses inside lab networks are reachable.",
		Example: "  parker proxy &\n" +
			"  curl --proxy socks5h://127.0.0.1:1080 http://server-1.dc1.minimal.parker:8500/v1/status/leader\n" +
			"  curl --proxy http://127.0.0.1:1080 http://10.1.0.10:8500/ui/",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext()
			defer stop()
			cl, ok, err := connectDaemon(ctx)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no parker daemon on %s (run `parker up` or `parker daemon`)", daemon.SocketPath())
			}
			ln, err := net.Listen("tcp", listen)
			if err != nil {
				return err
			}
			say("proxy on %s (SOCKS5 and HTTP); e.g. curl --proxy socks5h://%s http://<node>.<lab>.parker:8500/", ln.Addr(), ln.Addr())
			return proxy.Serve(ctx, ln, proxy.APIDialer(cl, scope), func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
		},
	}
	c.Flags().StringVar(&listen, "listen", "127.0.0.1:1080", "local address to listen on")
	c.Flags().StringVar(&scope, "lab", "", "lab for bare node names (dc1-server-1)")
	return c
}

func portForwardCmd() *cobra.Command {
	var addr string
	c := &cobra.Command{
		Use:   "port-forward FILE|LAB NODE [LOCAL:]REMOTE",
		Short: "Forward a local port to a port on a node",
		Example: "  parker port-forward examples/minimal.yaml dc1/server-1 8500\n" +
			"  parker port-forward minimal dc1-server-1 18500:8500",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := labName(args[0])
			if err != nil {
				return err
			}
			node := strings.ReplaceAll(args[1], "/", "-")
			local, remote, ok := strings.Cut(args[2], ":")
			if !ok {
				local, remote = args[2], args[2]
			}
			ctx, stop := signalContext()
			defer stop()
			cl, ok, err := connectDaemon(ctx)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no parker daemon on %s (run `parker up` or `parker daemon`)", daemon.SocketPath())
			}
			target := net.JoinHostPort(node+"."+name+".parker", remote)
			if _, err := daemon.ResolveAddress(target, name); err != nil && !hostSet() {
				return err
			}
			ln, err := net.Listen("tcp", net.JoinHostPort(addr, local))
			if err != nil {
				return err
			}
			say("forwarding %s -> %s/%s:%s", ln.Addr(), name, node, remote)
			return proxy.Forward(ctx, ln, target, proxy.APIDialer(cl, name), func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
		},
	}
	c.Flags().StringVar(&addr, "address", "127.0.0.1", "local address to listen on")
	return c
}

// hostSet reports whether a host is set, in which case local
// topology files may not exist and resolution is left to the daemon.
func hostSet() bool { return target().Host != "" }
