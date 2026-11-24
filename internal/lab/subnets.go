package lab

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
)

// relocate moves the lab's auto networks off subnets that are already in
// use (by another running lab, the host, or a stopped lab that is kept),
// preferring the subnets its last run used, and says what moved. A lab
// that resumes keeps its addresses, so for it a move is an error.
func relocate(name string, low *spec.File, resume bool, say func(string, ...any)) error {
	host, err := netsetup.HostNets()
	if err != nil {
		return err
	}
	host = append(host, stoppedNets(host)...)
	prev := map[string]netip.Prefix{}
	if old, err := LoadTopology(name); err == nil {
		for k, n := range old.Networks {
			if p, err := netip.ParsePrefix(n.Subnet); err == nil {
				prev[k] = p
			}
		}
	}
	moves, err := move(name, low, host, prev)
	if err != nil {
		return err
	}
	if resume && len(moves) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "lab %q can't come back on its addresses:", name)
		for _, m := range moves {
			fmt.Fprintf(&b, "\n  %s", m.Because)
		}
		fmt.Fprintf(&b, "\nfree the subnet, or rebuild the lab with fresh data on a free one: `parker up %s --fresh`", name)
		return errors.New(b.String())
	}
	for _, m := range moves {
		say("%s", m)
	}
	return nil
}

// stoppedNets are the subnets of labs that are down but kept: they have a
// saved topology and no network on the host. They hold their subnets so
// that `parker up LAB` can bring them back on the same addresses.
func stoppedNets(host []netsetup.HostNet) []netsetup.HostNet {
	running := map[string]bool{}
	for _, h := range host {
		if h.Lab != "" {
			running[h.Lab] = true
		}
	}
	names, _ := Labs()
	var out []netsetup.HostNet
	for _, n := range names {
		if running[n] {
			continue
		}
		f, err := LoadTopology(n)
		if err != nil {
			continue
		}
		for _, k := range slices.Sorted(maps.Keys(f.Networks)) {
			if p, err := netip.ParsePrefix(f.Networks[k].Subnet); err == nil {
				out = append(out, netsetup.HostNet{Prefix: p.Masked(), Lab: n, Stopped: true})
			}
		}
	}
	return out
}

// move relocates the lab's auto networks and re-signs the server
// certificates for their new addresses.
func move(name string, low *spec.File, host []netsetup.HostNet, prev map[string]netip.Prefix) ([]netsetup.Move, error) {
	moves, err := netsetup.Relocate(name, low, host, prev)
	if err != nil || len(moves) == 0 {
		return moves, err
	}
	fix := func(s string) string { return netsetup.MoveAddrs(s, moves) }
	if err := expand.ReissueServerCerts(low, fix); err != nil {
		return nil, fmt.Errorf("moving the lab's TLS certificates: %w", err)
	}
	return moves, nil
}

// lockSubnets serialises subnet picking across every parker on this host
// that shares the run base: the daemon and --no-daemon runs alike.
func lockSubnets() (func(), error) {
	if err := os.MkdirAll(RunBase(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(RunBase(), ".subnets.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := flock(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil // closing drops the lock
}
