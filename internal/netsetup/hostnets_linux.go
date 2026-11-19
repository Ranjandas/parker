package netsetup

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// HostNets lists the IPv4 subnets in use in the current network namespace:
// interface addresses and unicast routes in every table, except default
// routes and loopback.
func HostNets() ([]HostNet, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	type info struct{ name, lab string }
	byIndex := map[int]info{}
	for _, l := range links {
		a := l.Attrs()
		lab, _ := strings.CutPrefix(a.Alias, "parker:")
		if lab == a.Alias {
			lab = ""
		}
		byIndex[a.Index] = info{a.Name, lab}
	}
	var out []HostNet
	add := func(p netip.Prefix, idx int, route bool) {
		p = p.Masked()
		if p.Bits() == 0 || p.Addr().IsLoopback() || !p.Addr().Is4() {
			return
		}
		i := byIndex[idx]
		out = append(out, HostNet{Prefix: p, Iface: i.name, Lab: i.lab, Route: route})
	}

	addrs, err := netlink.AddrList(nil, netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("list addresses: %w", err)
	}
	for _, a := range addrs {
		if p, ok := toPrefix(a.IPNet.IP, a.IPNet.Mask); ok {
			add(p, a.LinkIndex, false)
		}
	}

	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4,
		&netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}
	for _, r := range routes {
		if r.Dst == nil || r.Type != unix.RTN_UNICAST {
			continue
		}
		if p, ok := toPrefix(r.Dst.IP, r.Dst.Mask); ok {
			add(p, r.LinkIndex, true)
		}
	}
	return out, nil
}
