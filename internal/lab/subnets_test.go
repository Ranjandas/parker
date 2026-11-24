package lab

import (
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
)

// A real lab that collides with another running lab on every default
// subnet moves wholesale: no address from the old subnets is left.
func TestRelocateExpandedExamples(t *testing.T) {
	for _, ex := range []string{"minimal.yaml", "federation.yaml", "mesh-federation.yaml", "acl.yaml", "tls.yaml", "gateways.yaml", "dataplane.yaml", "peering.yaml"} {
		t.Run(ex, func(t *testing.T) {
			in, err := spec.Load("../../examples/"+ex, nil)
			if err != nil {
				t.Fatal(err)
			}
			low, err := expand.Expand(in)
			if err != nil {
				t.Fatal(err)
			}
			var host []netsetup.HostNet
			var old []netip.Prefix
			for name, n := range low.Networks {
				if !n.Auto {
					t.Fatalf("%s: network %s isn't auto", ex, name)
				}
				p := netip.MustParsePrefix(n.Subnet)
				old = append(old, p)
				host = append(host, netsetup.HostNet{Prefix: p, Iface: "pk-x", Lab: "other"})
			}
			moves, err := netsetup.Relocate(low.Name, low, host, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(moves) != len(low.Networks) {
				t.Fatalf("moved %d of %d networks", len(moves), len(low.Networks))
			}
			b, err := yaml.Marshal(low)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`).FindAllString(string(b), -1) {
				ip, err := netip.ParseAddr(a)
				if err != nil {
					continue
				}
				for _, p := range old {
					if p.Contains(ip) {
						t.Errorf("%s still in the lab after the move", a)
					}
				}
			}
		})
	}
}

func TestExplicitSubnetIsFixed(t *testing.T) {
	in, err := spec.Load("../../examples/terminating-failover.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	low, err := expand.Expand(in)
	if err != nil {
		t.Fatal(err)
	}
	if low.Networks["dc1-lan"].Auto || low.Networks["dc2-lan"].Auto {
		t.Error("an explicit dc subnet must not be auto")
	}
}

func TestStoppedNets(t *testing.T) {
	base := t.TempDir()
	t.Setenv("PARKER_RUN_DIR", base)
	for name, subnet := range map[string]string{"down": "10.7.0.0/24", "up": "10.8.0.0/24"} {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		topo := "parker: v1alpha1\nname: " + name + "\nnetworks:\n  lan: { subnet: " + subnet + ", auto: true }\nnodes:\n  a:\n    networks: { lan: { ip: " + strings.TrimSuffix(subnet, "0/24") + "10 } }\n"
		if err := os.WriteFile(filepath.Join(dir, TopologyFile), []byte(topo), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	running := []netsetup.HostNet{{Prefix: netip.MustParsePrefix("10.8.0.0/24"), Iface: "pk-x", Lab: "up"}}
	got := stoppedNets(running)
	if len(got) != 1 || got[0].Lab != "down" || got[0].Prefix.String() != "10.7.0.0/24" || !got[0].Stopped {
		t.Fatalf("stopped nets: %+v", got)
	}
}
