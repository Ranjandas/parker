package netsetup

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"

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
	return freeSubnet(24, host, taken)
}

// freeSubnet is FreeSubnet for a /bits network (bits 16 to 30).
func freeSubnet(bits int, host []HostNet, taken []netip.Prefix) (netip.Prefix, bool) {
	if bits < 16 || bits > 30 {
		return netip.Prefix{}, false
	}
	for n := 1; n < 255; n++ {
		c := netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(n), 0, 0}), bits)
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

// CheckSubnets fails if any lab network with a fixed subnet overlaps a
// subnet the host already uses, naming the clash and suggesting a free
// subnet. Networks marked auto are left out: Relocate moves them.
func CheckSubnets(lab string, f *spec.File) error {
	host, err := HostNets()
	if err != nil {
		return err
	}
	return clashError(lab, f, host)
}

func clashError(lab string, f *spec.File, host []HostNet) error {
	var clashes []Clash
	for _, c := range Clashes(lab, f, host) {
		if !f.Networks[c.Network].Auto {
			clashes = append(clashes, c)
		}
	}
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
		b.WriteString(", or leave subnet: out to let parker pick a free one")
	}
	return fmt.Errorf("%s", b.String())
}
