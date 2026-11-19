//go:build linux

package netsetup

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"github.com/Ranjandas/parker/internal/spec"
)

// cutProto marks Parker's partition routes (any unused protocol number).
const cutProto = netlink.RouteProtocol(0x50)

func nsHandle(lab, node string) (*netlink.Handle, error) {
	ns, err := netns.GetFromPath(NetnsPath(lab, node))
	if err != nil {
		return nil, fmt.Errorf("node %s: open netns: %w", node, err)
	}
	defer ns.Close()
	h, err := netlink.NewHandleAt(ns)
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", node, err)
	}
	return h, nil
}

func hostRoute(ip string) (*net.IPNet, error) {
	a := net.ParseIP(ip)
	if a == nil {
		return nil, fmt.Errorf("bad IP %q", ip)
	}
	if v4 := a.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}, nil
	}
	return &net.IPNet{IP: a, Mask: net.CIDRMask(128, 128)}, nil
}

// Partition cuts the network between the groups of nodes (see CutPlan).
// Cuts add up: an earlier partition stays until healed.
func Partition(lab string, f *spec.File, groups [][]string) error {
	plan, err := CutPlan(f, groups)
	if err != nil {
		return err
	}
	var errs []error
	for _, node := range slices.Sorted(maps.Keys(plan)) {
		h, err := nsHandle(lab, node)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, ip := range plan[node] {
			dst, err := hostRoute(ip)
			if err == nil {
				err = h.RouteReplace(&netlink.Route{Dst: dst, Type: unix.RTN_BLACKHOLE, Protocol: cutProto})
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("node %s: cut %s: %w", node, ip, err))
			}
		}
		h.Close()
	}
	return errors.Join(errs...)
}

// Heal removes cuts (see HealPlan); nil nodes heals everything. It returns
// how many routes it removed.
func Heal(lab string, f *spec.File, nodes []string) (int, error) {
	all, to := HealPlan(f, nodes)
	removed := 0
	var errs []error
	for _, node := range slices.Sorted(maps.Keys(f.Nodes)) {
		h, err := nsHandle(lab, node)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		routes, err := Cuts(h)
		if err != nil {
			errs = append(errs, fmt.Errorf("node %s: %w", node, err))
		}
		for _, r := range routes {
			if !all[node] && !to[r.Dst.IP.String()] {
				continue
			}
			if err := h.RouteDel(&r); err != nil {
				errs = append(errs, fmt.Errorf("node %s: heal %s: %w", node, r.Dst, err))
				continue
			}
			removed++
		}
		h.Close()
	}
	return removed, errors.Join(errs...)
}

// Cuts lists the partition routes in a namespace.
func Cuts(h *netlink.Handle) ([]netlink.Route, error) {
	var out []netlink.Route
	for _, fam := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		rs, err := h.RouteListFiltered(fam, &netlink.Route{Protocol: cutProto}, netlink.RT_FILTER_PROTOCOL)
		if err != nil {
			return out, err
		}
		for _, r := range rs {
			if r.Dst != nil {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// CutsOn returns the addresses node can't reach because of a partition.
func CutsOn(lab, node string) ([]string, error) {
	h, err := nsHandle(lab, node)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	rs, err := Cuts(h)
	var ips []string
	for _, r := range rs {
		ips = append(ips, r.Dst.IP.String())
	}
	slices.Sort(ips)
	return ips, err
}
