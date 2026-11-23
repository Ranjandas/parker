package lab

import (
	"net/netip"
	"os"
	"path/filepath"

	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
)

// relocate moves the lab's auto networks off subnets that are already in
// use (by another running lab or the host), preferring the subnets its
// last run used, and says what moved.
func relocate(name string, low *spec.File, say func(string, ...any)) error {
	host, err := netsetup.HostNets()
	if err != nil {
		return err
	}
	prev := map[string]netip.Prefix{}
	if old, err := LoadTopology(name); err == nil {
		for k, n := range old.Networks {
			if p, err := netip.ParsePrefix(n.Subnet); err == nil {
				prev[k] = p
			}
		}
	}
	moves, err := netsetup.Relocate(name, low, host, prev)
	if err != nil {
		return err
	}
	for _, m := range moves {
		say("%s", m)
	}
	return nil
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
