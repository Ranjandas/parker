package netsetup

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/Ranjandas/parker/internal/spec"
)

// HostNet is a subnet the host already uses: an address on an interface, or
// a route.
type HostNet struct {
	Prefix netip.Prefix
	Iface  string // interface name, "" if unknown
	Lab    string // set when the interface belongs to a Parker lab
	Route  bool   // true for a route, false for an interface address
}

// Clash is a lab network that overlaps something on the host.
type Clash struct {
	Network string
	Subnet  netip.Prefix
	Host    HostNet
}

func (c Clash) String() string {
	what := "address"
	if c.Host.Route {
		what = "route"
	}
	where := c.Host.Iface
	if where == "" {
		where = "the host"
	}
	if c.Host.Lab != "" {
		return fmt.Sprintf("network %s (%s) overlaps %s used by running lab %q (%s); `parker down %s` or pick another subnet",
			c.Network, c.Subnet, c.Host.Prefix, c.Host.Lab, where, c.Host.Lab)
	}
	return fmt.Sprintf("network %s (%s) overlaps %s %s on %s", c.Network, c.Subnet, what, c.Host.Prefix, where)
}

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

func toPrefix(ip []byte, mask []byte) (netip.Prefix, bool) {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Prefix{}, false
	}
	a = a.Unmap()
	ones := 0
	for _, b := range mask {
		for ; b != 0; b <<= 1 {
			if b&0x80 == 0 {
				break
			}
			ones++
		}
	}
	return netip.PrefixFrom(a, ones), true
}

// Clashes returns the lab networks that overlap a host subnet. Subnets that
// belong to this lab itself (left over from a previous run, which `up` cleans
// up) are ignored. At most one clash is reported per network and interface.
func Clashes(lab string, f *spec.File, host []HostNet) []Clash {
	var out []Clash
	for _, name := range slices.Sorted(maps.Keys(f.Networks)) {
		p, err := netip.ParsePrefix(f.Networks[name].Subnet)
		if err != nil {
			continue // reported by validation
		}
		p = p.Masked()
		seen := map[string]bool{}
		for _, h := range host {
			if h.Lab == lab || !p.Overlaps(h.Prefix) {
				continue
			}
			key := h.Iface + "|" + h.Lab
			if h.Iface == "" {
				key = h.Prefix.String()
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Clash{Network: name, Subnet: p, Host: h})
		}
	}
	return out
}

// FreeSubnet returns the first 10.N.0.0/24 that overlaps neither the host nor
// any of the given subnets, or false if there is none.
func FreeSubnet(host []HostNet, taken []netip.Prefix) (netip.Prefix, bool) {
	for n := 1; n < 255; n++ {
		c := netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(n), 0, 0}), 24)
		ok := true
		for _, h := range host {
			if c.Overlaps(h.Prefix) {
				ok = false
				break
			}
		}
		for _, t := range taken {
			if c.Overlaps(t) {
				ok = false
				break
			}
		}
		if ok {
			return c, true
		}
	}
	return netip.Prefix{}, false
}

// CheckSubnets fails if any lab network overlaps a subnet the host already
// uses, naming the clash and suggesting a free subnet.
func CheckSubnets(lab string, f *spec.File) error {
	host, err := HostNets()
	if err != nil {
		return err
	}
	return clashError(lab, f, host)
}

func clashError(lab string, f *spec.File, host []HostNet) error {
	clashes := Clashes(lab, f, host)
	if len(clashes) == 0 {
		return nil
	}
	var taken []netip.Prefix
	for _, n := range f.Networks {
		if p, err := netip.ParsePrefix(n.Subnet); err == nil {
			taken = append(taken, p.Masked())
		}
	}
	var b strings.Builder
	b.WriteString("subnet clash:")
	for _, c := range clashes {
		b.WriteString("\n  " + c.String())
	}
	if free, ok := FreeSubnet(host, taken); ok {
		c := clashes[0]
		b.WriteString("\nhint: move the network to a free subnet, e.g. ")
		if dc, ok := strings.CutSuffix(c.Network, "-lan"); ok {
			fmt.Fprintf(&b, "consul.datacenters.%s.subnet: %s", dc, free)
		} else {
			fmt.Fprintf(&b, "networks.%s.subnet: %s", c.Network, free)
		}
	}
	return fmt.Errorf("%s", b.String())
}
