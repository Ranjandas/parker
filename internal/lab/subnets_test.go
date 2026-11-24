package lab

import (
	"crypto/tls"
	"crypto/x509"
	"net/netip"
	"regexp"
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
			moves, err := move(low.Name, low, host, nil)
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
			checkServerCerts(t, low)
		})
	}
}

// checkServerCerts fails unless every server certificate chains to the lab
// CA, matches its key, and is valid for each of the node's addresses.
func checkServerCerts(t *testing.T, low *spec.File) {
	t.Helper()
	for name, n := range low.Nodes {
		if n.Files[expand.CertFile] == "" {
			continue
		}
		pair, err := tls.X509KeyPair([]byte(n.Files[expand.CertFile]), []byte(n.Files[expand.CertKeyFile]))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM([]byte(n.Files[expand.CAFile]))
		cert, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		for net, a := range n.Networks {
			if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: a.IP}); err != nil {
				t.Errorf("%s on %s: %v", name, net, err)
			}
		}
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
