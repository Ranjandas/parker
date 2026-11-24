// Package lab brings a whole lab up and down: preflight checks, binaries,
// network, firewall, processes. `parker daemon` and the in-process CLI
// fallback both use it.
package lab

import (
	"context"
	"errors"
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
	"github.com/Ranjandas/parker/internal/license"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/rootless"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// TopologyFile is the expanded topology saved in each lab's run dir.
const TopologyFile = "topology.yaml"

// RunBase is the parent of every lab's run directory: PARKER_RUN_DIR, else
// /var/lib/parker/run for root, else the rootless daemon's state dir. Labs
// hold Consul data, so they live on disk: /run is a small tmpfs.
func RunBase() string {
	if b := os.Getenv("PARKER_RUN_DIR"); b != "" {
		return b
	}
	if os.Geteuid() != 0 {
		return rootless.RunBase()
	}
	return RootRunBase
}

// RootRunBase is root's run base without PARKER_RUN_DIR.
const RootRunBase = "/var/lib/parker/run"

func RunDir(lab string) string { return filepath.Join(RunBase(), lab) }

type Options struct {
	Log           io.Writer
	KeepOnFailure bool
	NoFirewall    bool
	// Life bounds supervision (restart policies) after Up returns.
	Life context.Context
	// License is the Consul Enterprise license, for labs that need one
	// (see license.Needed). It's written to the run dir, 0600, and nowhere
	// else.
	License []byte
	// Resume brings the lab back on its kept run dir (Consul data, logs,
	// peering tokens) instead of a fresh one, and fails rather than move
	// it to another subnet.
	Resume bool
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
	if why := license.Needed(low); len(why) > 0 && len(o.License) == 0 {
		return nil, fmt.Errorf("%s needs a Consul Enterprise license, and none came with the request: set CONSUL_LICENSE_PATH or CONSUL_LICENSE where you run parker up", strings.Join(why, ", "))
	}
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
	// Pick free subnets and create the network under one lock, so labs
	// coming up together don't pick the same one.
	unlock, err := lockSubnets()
	if err != nil {
		return nil, err
	}
	locked := true
	release := func() {
		if locked {
			unlock()
			locked = false
		}
	}
	defer release()
	if err := relocate(name, low, o.Resume, say); err != nil {
		return nil, err
	}
	if err := netsetup.Down(name, func(f string, a ...any) { say("cleanup: "+f, a...) }); err != nil {
		return nil, PrivHint(err)
	}
	dir := RunDir(name)
	if o.Resume {
		say("resuming lab %q on its data in %s", name, dir)
	} else if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	licPath := ""
	if len(o.License) > 0 {
		licPath = license.Path(dir)
		if err := license.Write(dir, o.License); err != nil {
			return nil, err
		}
	}
	if w := spaceWarning(dir, low); w != "" {
		say("%s", w)
	}
	if b, err := yaml.MarshalWithOptions(low, yaml.UseLiteralStyleIfMultiline(true), yaml.IndentSequence(true)); err == nil {
		_ = os.WriteFile(filepath.Join(dir, TopologyFile), b, 0o644)
	}
	say("network: %d networks, %d nodes", len(low.Networks), len(low.Nodes))
	err = netsetup.Up(name, low)
	release()
	if err != nil {
		_ = netsetup.Down(name, func(string, ...any) {})
		_ = license.Remove(dir)
		return nil, PrivHint(err)
	}
	if err := allowForwarding(name, low, o.NoFirewall, say); err != nil {
		_ = netsetup.Down(name, func(string, ...any) {})
		_ = license.Remove(dir)
		return nil, err
	}
	h, err := supervisor.Run(ctx, low, supervisor.Options{Lab: name, RunDir: dir, Log: o.Log, Life: o.Life, LicensePath: licPath})
	if err != nil {
		err = errors.New(license.Redact(err.Error(), o.License))
		err = spaceHint(dir, err)
		for _, hint := range netsetup.FirewallHints() {
			err = fmt.Errorf("%w\nhint: %s", err, hint)
		}
		if !o.KeepOnFailure {
			_ = netsetup.Down(name, func(string, ...any) {})
			_ = license.Remove(dir)
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
	if err := PrivHint(netsetup.Down(name, func(f string, a ...any) { fmt.Fprintf(log, f+"\n", a...) })); err != nil {
		return err
	}
	return license.Remove(RunDir(name)) // the logs stay; the license doesn't
}

// LoadTopology reads the expanded topology saved by Up.
func LoadTopology(name string) (*spec.File, error) {
	return spec.Load(filepath.Join(RunDir(name), TopologyFile), nil)
}

// SavedTopology returns the expanded topology file saved by Up, as is.
// The error wraps os.ErrNotExist when the lab has none (never up here, or
// purged).
func SavedTopology(name string) ([]byte, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return nil, fmt.Errorf("lab %q: not a lab name", name)
	}
	b, err := os.ReadFile(filepath.Join(RunDir(name), TopologyFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, noSavedTopology{name}
	}
	return b, err
}

type noSavedTopology struct{ name string }

func (e noSavedTopology) Error() string {
	return fmt.Sprintf("lab %q has no saved topology in %s (never brought up here, or removed by down --purge or prune); `parker status` lists labs, or pass the lab's file", e.name, RunBase())
}

func (noSavedTopology) Unwrap() error { return os.ErrNotExist }

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
			if len(p.Run) > 0 && p.Run[0] == product || slices.Contains(p.Uses, product) {
				return true
			}
		}
	}
	return false
}
