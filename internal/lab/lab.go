// Package lab brings a whole lab up and down: preflight checks, binaries,
// network, firewall, processes. `parker daemon` and the in-process CLI
// fallback both use it.
package lab

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/Ranjandas/parker/internal/fetch"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/rootless"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// TopologyFile is the expanded topology saved in each lab's run dir.
const TopologyFile = "topology.yaml"

// RunBase is the parent of every lab's run directory: PARKER_RUN_DIR, else
// /run/parker for root, else the rootless daemon's dir.
func RunBase() string {
	if b := os.Getenv("PARKER_RUN_DIR"); b != "" {
		return b
	}
	if os.Geteuid() != 0 {
		return rootless.RunBase()
	}
	return "/run/parker"
}

func RunDir(lab string) string { return filepath.Join(RunBase(), lab) }

type Options struct {
	Log           io.Writer
	KeepOnFailure bool
	NoFirewall    bool
	// Life bounds supervision (restart policies) after Up returns.
	Life context.Context
}

// Up recreates the lab from its expanded topology and returns when every
// readiness check passes. ctx cancels the bring-up itself.
func Up(ctx context.Context, low *spec.File, o Options) (*supervisor.Handle, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("labs need Linux network namespaces; on %s use a remote daemon (PARKER_HOST)", runtime.GOOS)
	}
	say := func(format string, a ...any) { fmt.Fprintf(o.Log, format+"\n", a...) }
	name := low.Name
	t0 := time.Now()
	if err := netsetup.CheckSubnets(name, low); err != nil {
		return nil, err
	}
	// Fetch before touching the network so a bad version fails fast.
	for k, v := range low.Versions {
		if Needs(low, k) {
			if _, err := fetch.Binary(k, v, o.Log); err != nil {
				return nil, err
			}
		}
	}
	if err := netsetup.Down(name, func(f string, a ...any) { say("cleanup: "+f, a...) }); err != nil {
		return nil, PrivHint(err)
	}
	dir := RunDir(name)
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if b, err := yaml.MarshalWithOptions(low, yaml.UseLiteralStyleIfMultiline(true), yaml.IndentSequence(true)); err == nil {
		_ = os.WriteFile(filepath.Join(dir, TopologyFile), b, 0o644)
	}
	say("network: %d networks, %d nodes", len(low.Networks), len(low.Nodes))
	if err := netsetup.Up(name, low); err != nil {
		_ = netsetup.Down(name, func(string, ...any) {})
		return nil, PrivHint(err)
	}
	if err := allowForwarding(name, low, o.NoFirewall, say); err != nil {
		_ = netsetup.Down(name, func(string, ...any) {})
		return nil, err
	}
	h, err := supervisor.Run(ctx, low, supervisor.Options{Lab: name, RunDir: dir, Log: o.Log, Life: o.Life})
	if err != nil {
		for _, hint := range netsetup.FirewallHints() {
			err = fmt.Errorf("%w\nhint: %s", err, hint)
		}
		if !o.KeepOnFailure {
			_ = netsetup.Down(name, func(string, ...any) {})
			return nil, fmt.Errorf("%w\n(lab torn down; rerun with --keep-on-failure to inspect it)", err)
		}
		return nil, fmt.Errorf("%w\n(lab left running; logs in %s; `parker down %s` to clean up)", err, dir, name)
	}
	say("\nlab %q is up in %s", name, time.Since(t0).Round(100*time.Millisecond))
	for _, n := range slices.Sorted(maps.Keys(low.Nodes)) {
		say("  %-24s %-14s %s", n, strings.Join(NodeIPs(low.Nodes[n]), ","), strings.Join(slices.Sorted(maps.Keys(low.Nodes[n].Processes)), ","))
	}
	say("\nlogs:  %s/nodes/<node>/", dir)
	return h, nil
}

// Down removes everything the lab created. Logs are kept.
func Down(name string, log io.Writer) error {
	return PrivHint(netsetup.Down(name, func(f string, a ...any) { fmt.Fprintf(log, f+"\n", a...) }))
}

// LoadTopology reads the expanded topology saved by Up.
func LoadTopology(name string) (*spec.File, error) {
	return spec.Load(filepath.Join(RunDir(name), TopologyFile), nil)
}

// Labs lists the labs that have a run directory.
func Labs() ([]string, error) {
	es, err := os.ReadDir(RunBase())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(RunBase(), e.Name(), TopologyFile)); err == nil {
				out = append(out, e.Name())
			}
		}
	}
	return out, nil
}

func NodeIPs(n *spec.RawNode) []string {
	var ips []string
	for _, k := range slices.Sorted(maps.Keys(n.Networks)) {
		ips = append(ips, n.Networks[k].IP)
	}
	return ips
}

func PrivHint(err error) error {
	if err == nil {
		return nil
	}
	if os.Geteuid() != 0 && (os.IsPermission(err) || strings.Contains(err.Error(), "operation not permitted")) {
		return fmt.Errorf("%w\nparker needs root (CAP_NET_ADMIN and CAP_SYS_ADMIN for named netns): try sudo", err)
	}
	return err
}

// allowForwarding lets lab traffic through a FORWARD chain that drops by
// default (Docker does this), or prints the rules when dryRun.
func allowForwarding(name string, low *spec.File, dryRun bool, say func(string, ...any)) error {
	fw := netsetup.SystemFirewall()
	if fw == nil {
		return nil
	}
	st, err := fw.State()
	if err != nil {
		say("firewall: can't read iptables, not adding rules (%v)", err)
		return nil
	}
	if !st.NeedsRules() {
		return nil
	}
	if dryRun {
		say("firewall: FORWARD would drop lab traffic; not changing it (--no-firewall). Run:")
		for _, a := range netsetup.RuleArgs(name, st.Chain(), low) {
			say("  sudo iptables %s", strings.Join(a, " "))
		}
		return nil
	}
	chain, err := fw.Allow(name, low)
	if err != nil {
		return fmt.Errorf("firewall: %w\n(rerun with --no-firewall to skip and print the rules instead)", err)
	}
	if chain != "" {
		say("firewall: allowed lab bridges in %s", chain)
	}
	return nil
}

// Needs reports whether any process runs the versioned binary product.
func Needs(f *spec.File, product string) bool {
	for _, n := range f.Nodes {
		for _, p := range n.Processes {
			if len(p.Run) > 0 && p.Run[0] == product {
				return true
			}
		}
	}
	return false
}
