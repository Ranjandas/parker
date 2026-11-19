package netsetup

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func lab(subnets map[string]string) *spec.File {
	f := &spec.File{Networks: map[string]*spec.Network{}}
	for n, s := range subnets {
		f.Networks[n] = &spec.Network{Subnet: s}
	}
	return f
}

func hn(p, iface, lab string, route bool) HostNet {
	return HostNet{Prefix: netip.MustParsePrefix(p), Iface: iface, Lab: lab, Route: route}
}

func TestClashes(t *testing.T) {
	host := []HostNet{
		hn("192.168.1.0/24", "wlp2s0", "", false),
		hn("10.1.0.0/16", "tun0", "", true),          // VPN route covering dc1-lan
		hn("10.1.0.0/24", "tun0", "", false),         // same iface: reported once
		hn("10.2.0.0/24", "pk-aaaa", "other", false), // another running lab
		hn("10.3.0.0/24", "pk-bbbb", "me", false),    // this lab, left over: ignored
	}
	f := lab(map[string]string{"dc1-lan": "10.1.0.0/24", "dc2-lan": "10.2.0.0/24", "dc3-lan": "10.3.0.0/24"})
	got := Clashes("me", f, host)
	if len(got) != 2 {
		t.Fatalf("want 2 clashes, got %d: %v", len(got), got)
	}
	if got[0].Network != "dc1-lan" || got[0].Host.Iface != "tun0" || !got[0].Host.Route {
		t.Errorf("first clash: %v", got[0])
	}
	if got[1].Network != "dc2-lan" || got[1].Host.Lab != "other" {
		t.Errorf("second clash: %v", got[1])
	}
	if !strings.Contains(got[1].String(), `running lab "other"`) {
		t.Errorf("lab clash message: %s", got[1])
	}
}

func TestNoClash(t *testing.T) {
	host := []HostNet{hn("172.17.0.0/16", "docker0", "", false)}
	if err := clashError("me", lab(map[string]string{"dc1-lan": "10.1.0.0/24"}), host); err != nil {
		t.Fatal(err)
	}
}

func TestClashErrorHint(t *testing.T) {
	host := []HostNet{hn("10.0.0.0/14", "eth0", "", true)} // covers 10.0-10.3
	f := lab(map[string]string{"dc1-lan": "10.1.0.0/24", "dc2-lan": "10.4.0.0/24"})
	err := clashError("me", f, host)
	if err == nil {
		t.Fatal("want clash error")
	}
	msg := err.Error()
	for _, want := range []string{"dc1-lan (10.1.0.0/24) overlaps route 10.0.0.0/14 on eth0", "consul.datacenters.dc1.subnet: 10.5.0.0/24"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

func TestFreeSubnetRawNetworkHint(t *testing.T) {
	host := []HostNet{hn("10.255.0.0/24", "eth1", "", false)}
	err := clashError("me", lab(map[string]string{"wan": "10.255.0.0/24"}), host)
	if err == nil || !strings.Contains(err.Error(), "networks.wan.subnet: 10.1.0.0/24") {
		t.Fatalf("got %v", err)
	}
}

func TestToPrefix(t *testing.T) {
	p, ok := toPrefix([]byte{10, 1, 0, 0}, []byte{255, 255, 240, 0})
	if !ok || p != netip.MustParsePrefix("10.1.0.0/20") {
		t.Fatalf("got %v %v", p, ok)
	}
}

func TestHostNetsSmoke(t *testing.T) {
	if _, err := HostNets(); err != nil {
		t.Skipf("netlink unavailable: %v", err)
	}
}
