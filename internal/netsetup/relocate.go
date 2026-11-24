package netsetup

import (
	"fmt"
	"maps"
	"net/netip"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/spec"
)

// Move is a network that Relocate moved off a subnet already in use.
type Move struct {
	Network  string
	From, To netip.Prefix
	Because  Clash
}

func (m Move) String() string {
	why := ""
	switch h := m.Because.Host; {
	case h.Stopped:
		why = fmt.Sprintf("stopped lab %q holds it", h.Lab)
	case h.Lab != "":
		why = fmt.Sprintf("running lab %q uses it", h.Lab)
	case h.Route:
		why = fmt.Sprintf("it overlaps route %s on %s", h.Prefix, ifaceOrHost(h.Iface))
	default:
		why = fmt.Sprintf("it overlaps %s on %s", h.Prefix, ifaceOrHost(h.Iface))
	}
	return fmt.Sprintf("network %s: %s is taken (%s); using %s", m.Network, m.From, why, m.To)
}

func ifaceOrHost(s string) string {
	if s == "" {
		return "the host"
	}
	return s
}

// Relocate moves every auto network (one whose subnet the lab file didn't
// choose) that clashes with the host onto a free subnet of the same size,
// and rewrites every address in the lab that falls in the old subnet to the
// same offset in the new one: node IPs, gateways, generated configs and the
// file's own commands. A network keeps its subnet from prev (the lab's last
// run) when that one is free, so addresses stay put across re-ups. A clash
// on a fixed subnet fails, as CheckSubnets does.
func Relocate(lab string, f *spec.File, host []HostNet, prev map[string]netip.Prefix) ([]Move, error) {
	if err := clashError(lab, f, host); err != nil {
		return nil, err
	}
	var others []HostNet // the lab's own leftovers are about to go
	for _, h := range host {
		if h.Lab != lab {
			others = append(others, h)
		}
	}
	clashing := map[string]Clash{}
	for _, c := range Clashes(lab, f, host) {
		if !f.Networks[c.Network].Auto {
			continue // a fixed subnet past clashError overlaps only a stopped lab
		}
		if _, ok := clashing[c.Network]; !ok {
			clashing[c.Network] = c
		}
	}
	if len(clashing) == 0 {
		return nil, nil
	}
	var taken []netip.Prefix
	for name, n := range f.Networks {
		if _, moving := clashing[name]; moving {
			continue
		}
		if p, err := netip.ParsePrefix(n.Subnet); err == nil {
			taken = append(taken, p.Masked())
		}
	}
	free := func(p netip.Prefix) bool {
		for _, h := range others {
			if p.Overlaps(h.Prefix) {
				return false
			}
		}
		for _, t := range taken {
			if p.Overlaps(t) {
				return false
			}
		}
		return true
	}
	var moves []Move
	for _, name := range slices.Sorted(maps.Keys(clashing)) {
		c := clashing[name]
		to, ok := prev[name]
		if !ok || to.Bits() != c.Subnet.Bits() || !free(to) {
			if to, ok = freeSubnet(c.Subnet.Bits(), others, taken); !ok {
				return nil, fmt.Errorf("subnet clash:\n  %s\nand no free 10.x subnet of that size is left; set the network's subnet: to one that is free", c)
			}
		}
		to = to.Masked()
		taken = append(taken, to)
		moves = append(moves, Move{Network: name, From: c.Subnet, To: to, Because: c})
	}
	rewriteStrings(reflect.ValueOf(f), func(s string) string { return moveAddrs(s, moves) })
	return moves, nil
}

// addrRun is a run of digits and dots: an IPv4 address when it parses as one.
var addrRun = regexp.MustCompile(`[0-9][0-9.]*`)

// moveAddrs rewrites each IPv4 address in s that falls in a moved subnet to
// the same offset in its new subnet.
func moveAddrs(s string, moves []Move) string {
	if !strings.Contains(s, ".") {
		return s
	}
	return addrRun.ReplaceAllStringFunc(s, func(run string) string {
		trimmed := strings.TrimRight(run, ".")
		a, err := netip.ParseAddr(trimmed)
		if err != nil || !a.Is4() {
			return run
		}
		for _, m := range moves {
			if m.From.Contains(a) {
				return translate(a, m.From, m.To).String() + run[len(trimmed):]
			}
		}
		return run
	})
}

func translate(a netip.Addr, from, to netip.Prefix) netip.Addr {
	x, f, t := a.As4(), from.Masked().Addr().As4(), to.Masked().Addr().As4()
	off := be32(x) - be32(f)
	v := be32(t) + off
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func be32(b [4]byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// rewriteStrings applies fn to every string reachable from v: struct
// fields, pointers, slices, map values and interfaces.
func rewriteStrings(v reflect.Value, fn func(string) string) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return
		}
		if v.Kind() == reflect.Interface {
			e := v.Elem()
			c := reflect.New(e.Type()).Elem()
			c.Set(e)
			rewriteStrings(c, fn)
			if v.CanSet() {
				v.Set(c)
			}
			return
		}
		rewriteStrings(v.Elem(), fn)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				rewriteStrings(v.Field(i), fn)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			rewriteStrings(v.Index(i), fn)
		}
	case reflect.Map:
		if v.IsNil() {
			return
		}
		it := v.MapRange()
		type kv struct{ k, v reflect.Value }
		var set []kv
		for it.Next() {
			c := reflect.New(v.Type().Elem()).Elem()
			c.Set(it.Value())
			rewriteStrings(c, fn)
			set = append(set, kv{it.Key(), c})
		}
		for _, e := range set {
			v.SetMapIndex(e.k, e.v)
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(fn(v.String()))
		}
	}
}
