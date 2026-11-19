// Command parker reproduces Consul topologies fast with Linux network
// namespaces: no containers, no images. Named after the Parker Solar Probe,
// the fastest human-made object, because speed is the project's main goal.
package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"
	"github.com/vishvananda/netns"

	"github.com/Ranjandas/parker/internal/echo"
	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/fetch"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

var varFlags []string

func main() {
	root := &cobra.Command{
		Use:   "parker",
		Short: "Consul repros at probe speed: Linux network namespaces, no containers",
		Long: "Parker builds Consul topologies from a YAML file in seconds, using Linux network\n" +
			"namespaces instead of containers. It is named after the Parker Solar Probe, the\n" +
			"fastest human-made object, because speed is the point.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringArrayVar(&varFlags, "var", nil, "set a variable (name=value); repeatable")
	root.AddCommand(upCmd(), downCmd(), expandCmd(), validateCmd(), execCmd(), statusCmd(), fetchCmd(), fakeServiceCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func load(path string) (*spec.File, *spec.File, error) {
	vars := map[string]string{}
	for _, kv := range varFlags {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, nil, fmt.Errorf("--var %q: want name=value", kv)
		}
		vars[k] = v
	}
	f, err := spec.Load(path, vars)
	if err != nil {
		return nil, nil, err
	}
	low, err := expand.Expand(f)
	return f, low, err
}

func runDir(lab string) string {
	base := os.Getenv("PARKER_RUN_DIR")
	if base == "" {
		base = "/run/parker"
	}
	return filepath.Join(base, lab)
}

func say(format string, a ...any) { fmt.Printf(format+"\n", a...) }

func upCmd() *cobra.Command {
	var keepOnFail bool
	c := &cobra.Command{
		Use:   "up FILE",
		Short: "Create the lab's network and start its processes; returns when everything is ready",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, low, err := load(args[0])
			if err != nil {
				return err
			}
			lab := low.Name
			t0 := time.Now()
			if err := netsetup.CheckSubnets(lab, low); err != nil {
				return err
			}
			// Fetch before touching the network so a bad version fails fast.
			for k, v := range low.Versions {
				if needs(low, k) {
					if _, err := fetch.Binary(k, v, os.Stdout); err != nil {
						return err
					}
				}
			}
			if err := netsetup.Down(lab, func(f string, a ...any) { say("cleanup: "+f, a...) }); err != nil {
				return privHint(err)
			}
			dir := runDir(lab)
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			say("network: %d networks, %d nodes", len(low.Networks), len(low.Nodes))
			if err := netsetup.Up(lab, low); err != nil {
				_ = netsetup.Down(lab, func(string, ...any) {})
				return privHint(err)
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			_, err = supervisor.Run(ctx, low, supervisor.Options{Lab: lab, RunDir: dir, Log: os.Stdout})
			if err != nil {
				if !keepOnFail {
					_ = netsetup.Down(lab, func(string, ...any) {})
					return fmt.Errorf("%w\n(lab torn down; rerun with --keep-on-failure to inspect it)", err)
				}
				return fmt.Errorf("%w\n(lab left running; logs in %s; `parker down %s` to clean up)", err, dir, args[0])
			}
			say("\nlab %q is up in %s", lab, time.Since(t0).Round(100*time.Millisecond))
			printNodes(low)
			say("\nlogs:  %s/nodes/<node>/", dir)
			say("shell: parker exec %s %s -- consul members", args[0], firstNode(low))
			say("down:  parker down %s", args[0])
			return nil
		},
	}
	c.Flags().BoolVar(&keepOnFail, "keep-on-failure", false, "leave the lab running if a readiness check fails")
	return c
}

func needs(f *spec.File, product string) bool {
	for _, n := range f.Nodes {
		for _, p := range n.Processes {
			if len(p.Run) > 0 && p.Run[0] == product {
				return true
			}
		}
	}
	return false
}

func printNodes(f *spec.File) {
	for _, name := range slices.Sorted(maps.Keys(f.Nodes)) {
		n := f.Nodes[name]
		var ips []string
		for _, k := range slices.Sorted(maps.Keys(n.Networks)) {
			ips = append(ips, n.Networks[k].IP)
		}
		say("  %-24s %-14s %s", name, strings.Join(ips, ","), strings.Join(slices.Sorted(maps.Keys(n.Processes)), ","))
	}
}

func firstNode(f *spec.File) string {
	ks := slices.Sorted(maps.Keys(f.Nodes))
	if len(ks) == 0 {
		return "NODE"
	}
	return ks[0]
}

func privHint(err error) error {
	if os.Geteuid() != 0 && (os.IsPermission(err) || strings.Contains(err.Error(), "operation not permitted")) {
		return fmt.Errorf("%w\nparker needs root (CAP_NET_ADMIN and CAP_SYS_ADMIN for named netns): try sudo", err)
	}
	return err
}

func labName(arg string) (string, error) {
	if _, err := os.Stat(arg); err == nil {
		f, err := spec.Load(arg, nil)
		if err != nil {
			return "", err
		}
		return f.Name, nil
	}
	return arg, nil
}

func downCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down FILE|LAB",
		Short: "Stop the lab's processes and delete its namespaces and links (idempotent)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lab, err := labName(args[0])
			if err != nil {
				return err
			}
			if err := netsetup.Down(lab, func(f string, a ...any) { say(f, a...) }); err != nil {
				return privHint(err)
			}
			say("lab %q is down (logs kept in %s)", lab, runDir(lab))
			return nil
		},
	}
}

func expandCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "expand FILE",
		Short: "Print the fully expanded, low-level form of a topology (itself valid input)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, low, err := load(args[0])
			if err != nil {
				return err
			}
			b, err := yaml.MarshalWithOptions(low, yaml.UseLiteralStyleIfMultiline(true), yaml.IndentSequence(true))
			if err != nil {
				return err
			}
			fmt.Print("# generated by `parker expand`; valid input for `parker up`\n")
			os.Stdout.Write(b)
			return nil
		},
	}
}

func validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate FILE",
		Short: "Check a topology file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, low, err := load(args[0])
			if err != nil {
				return err
			}
			// A host clash is only a warning here: the file may be meant
			// for another machine. `up` refuses it.
			if err := netsetup.CheckSubnets(low.Name, low); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			}
			say("ok: %d networks, %d nodes", len(low.Networks), len(low.Nodes))
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status FILE|LAB",
		Short: "List the lab's namespaces",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lab, err := labName(args[0])
			if err != nil {
				return err
			}
			ns, err := netsetup.Namespaces(lab)
			if err != nil {
				return err
			}
			if len(ns) == 0 {
				say("lab %q is not running", lab)
				return nil
			}
			for _, n := range ns {
				say("%s", strings.TrimPrefix(n, netsetup.NetnsName(lab, "")))
			}
			return nil
		},
	}
}

func execCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec FILE NODE [-- COMMAND...]",
		Short: "Run a command (default: $SHELL) inside a node's network namespace",
		Long:  "NODE is the expanded node name (dc1-server-1) or a selector (dc1/server-1).",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, low, err := load(args[0])
			if err != nil {
				return err
			}
			node := strings.ReplaceAll(args[1], "/", "-")
			if _, ok := low.Nodes[node]; !ok {
				return fmt.Errorf("no node %q (have: %s)", node, strings.Join(slices.Sorted(maps.Keys(low.Nodes)), ", "))
			}
			argv := args[2:]
			if len(argv) == 0 {
				sh := os.Getenv("SHELL")
				if sh == "" {
					sh = "/bin/sh"
				}
				argv = []string{sh}
			}
			path := os.Getenv("PATH")
			for k, v := range low.Versions {
				if needs(low, k) {
					if b, err := fetch.Binary(k, v, os.Stderr); err == nil {
						path = filepath.Dir(b) + ":" + path
					}
				}
			}
			env := append(os.Environ(), "PATH="+path, "PARKER_LAB="+low.Name, "PARKER_NODE="+node)
			bin, err := lookPath(argv[0], path)
			if err != nil {
				return err
			}
			runtime.LockOSThread()
			ns, err := netns.GetFromPath(netsetup.NetnsPath(low.Name, node))
			if err != nil {
				return fmt.Errorf("lab not running? %w", err)
			}
			if err := netns.Set(ns); err != nil {
				return privHint(err)
			}
			return syscall.Exec(bin, argv, env)
		},
	}
}

func lookPath(name, path string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, d := range filepath.SplitList(path) {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found in PATH", name)
}

func fetchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fetch FILE",
		Short: "Download the binaries a topology pins into the cache",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, low, err := load(args[0])
			if err != nil {
				return err
			}
			for k, v := range low.Versions {
				b, err := fetch.Binary(k, v, os.Stdout)
				if err != nil {
					return err
				}
				say("%s %s: %s", k, v, b)
			}
			for _, n := range low.Nodes {
				for _, p := range n.Processes {
					if p.Version != "" && len(p.Run) > 0 {
						b, err := fetch.Binary(p.Run[0], p.Version, os.Stdout)
						if err != nil {
							return err
						}
						say("%s %s: %s", p.Run[0], p.Version, b)
					}
				}
			}
			return nil
		},
	}
}

func fakeServiceCmd() *cobra.Command {
	var name string
	var port int
	c := &cobra.Command{
		Use:    "fake-service",
		Short:  "Built-in echo HTTP service",
		Hidden: true,
		RunE:   func(cmd *cobra.Command, args []string) error { return echo.Serve(name, port) },
	}
	c.Flags().StringVar(&name, "name", "fake", "service name")
	c.Flags().IntVar(&port, "port", 8080, "listen port")
	return c
}
