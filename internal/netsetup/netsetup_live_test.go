//go:build linux

package netsetup

import (
	"os"
	"slices"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func liveLab(subnet, gw string, ips ...string) *spec.File {
	f := &spec.File{
		Networks: map[string]*spec.Network{"lan": {Subnet: subnet, Gateway: gw}},
		Nodes:    map[string]*spec.RawNode{},
	}
	for i, ip := range ips {
		f.Nodes[string(rune('a'+i))] = &spec.RawNode{Networks: map[string]*spec.Attachment{"lan": {IP: ip}}}
	}
	return f
}

// Run with PARKER_LIVE_NETNS=1 inside `unshare -Urnm` with a tmpfs on /run
// (see the CI e2e job): it creates real bridges and namespaces.
func TestNetsetupLive(t *testing.T) {
	if os.Getenv("PARKER_LIVE_NETNS") == "" {
		t.Skip("set PARKER_LIVE_NETNS=1 in a private user+net+mount namespace")
	}
	quiet := func(string, ...any) {}
	// Lab "a-b" starts with "a-": Down("a") must leave it alone.
	if err := Up("a", liveLab("10.61.0.0/24", "10.61.0.1", "10.61.0.10", "10.61.0.11")); err != nil {
		t.Fatal(err)
	}
	if err := Up("a-b", liveLab("10.62.0.0/24", "10.62.0.1", "10.62.0.10")); err != nil {
		t.Fatal(err)
	}
	ns, err := Namespaces("a")
	if err != nil || !slices.Equal(ns, []string{"parker-a_a", "parker-a_b"}) {
		t.Fatalf("Namespaces(a) = %v, %v", ns, err)
	}
	links, err := labelledLinks("a")
	if err != nil || len(links) != 3 { // bridge + 2 host-side veths
		t.Fatalf("labelled links of a: %d, %v", len(links), err)
	}
	hn, err := HostNets()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(hn, func(h HostNet) bool { return h.Lab == "a" && h.Prefix.String() == "10.61.0.0/24" }) {
		t.Errorf("HostNets doesn't attribute 10.61.0.0/24 to lab a: %+v", hn)
	}

	if err := Down("a", quiet); err != nil {
		t.Fatal(err)
	}
	if ns, _ := Namespaces("a"); len(ns) != 0 {
		t.Errorf("left after down: %v", ns)
	}
	if links, _ := labelledLinks("a"); len(links) != 0 {
		t.Errorf("links left after down: %d", len(links))
	}
	if ns, _ := Namespaces("a-b"); len(ns) != 1 {
		t.Errorf("down a touched lab a-b: %v", ns)
	}
	if err := Down("a", quiet); err != nil {
		t.Errorf("second down: %v", err)
	}
	if err := Down("a-b", quiet); err != nil {
		t.Fatal(err)
	}
}
