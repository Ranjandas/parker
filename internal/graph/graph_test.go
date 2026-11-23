package graph

import (
	"maps"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/spec"
)

func build(t *testing.T, example string) *Graph {
	t.Helper()
	f, err := spec.Load(filepath.Join("..", "..", "examples", example), nil)
	if err != nil {
		t.Fatal(err)
	}
	low, err := expand.Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	return Build(low)
}

func node(t *testing.T, g *Graph, name string) Node {
	t.Helper()
	for _, n := range g.Nodes {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("no node %s", name)
	return Node{}
}

// edges lists a graph's edges of one kind as "from>to label via".
func edges(g *Graph, kind string) []string {
	var out []string
	for _, e := range g.Edges {
		if e.Kind == kind {
			s := e.From + ">" + e.To + " " + e.Label
			for _, v := range e.Via {
				s += " ~" + v
			}
			out = append(out, s)
		}
	}
	return out
}

func want(t *testing.T, what string, got []string, w ...string) {
	t.Helper()
	if !slices.Equal(got, w) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, w)
	}
}

func TestMesh(t *testing.T) {
	g := build(t, "mesh.yaml")
	if g.Federation != "" || len(g.Nodes) != 5 {
		t.Errorf("federation %q, %d nodes", g.Federation, len(g.Nodes))
	}
	web := node(t, g, "dc1-web")
	if web.DC != "dc1" || web.Role != "client" || !slices.Equal(web.Services, []string{"web"}) || len(web.IPs) != 1 {
		t.Errorf("web: %+v", web)
	}
	if s := node(t, g, "dc1-server-1"); s.Role != "server" || len(s.Services) != 0 {
		t.Errorf("server: %+v", s)
	}
	want(t, "upstreams", edges(g, Upstream), "dc1-web>dc1-api api :9091")
}

func TestMeshGatewayFederation(t *testing.T) {
	g := build(t, "mesh-federation.yaml")
	if g.Federation != "mesh-gateways" {
		t.Errorf("federation %q", g.Federation)
	}
	if gw := node(t, g, "dc1-mesh"); gw.Role != "gateway" || !slices.Equal(gw.Gateways, []string{"mesh"}) || len(gw.IPs) != 2 {
		t.Errorf("gateway: %+v", gw)
	}
	want(t, "upstreams", edges(g, Upstream), "dc1-web>dc2-api api :9091 ~dc1-mesh ~dc2-mesh")
	want(t, "wan", edges(g, WAN), "dc1-server-1>dc1-mesh ", "dc2-server-1>dc2-mesh ", "dc1-mesh>dc2-mesh wan")
}

func TestWANFederation(t *testing.T) {
	g := build(t, "federation.yaml")
	if g.Federation != "wan" {
		t.Errorf("federation %q", g.Federation)
	}
	want(t, "wan", edges(g, WAN), "dc1-server-1>dc2-server-1 wan")
}

func TestPeering(t *testing.T) {
	g := build(t, "peering.yaml")
	if g.Federation != "" {
		t.Errorf("federation %q", g.Federation)
	}
	// via_mesh_gateways: the control plane goes through each dc's gateway.
	want(t, "peering", edges(g, Peering), "dc1-server-1>dc2-server-1 dc1-dc2 ~dc1-mesh ~dc2-mesh")
	want(t, "upstreams", edges(g, Upstream), "dc1-web>dc2-api api :9091 ~dc1-mesh ~dc2-mesh")
}

func TestPeeringPartitions(t *testing.T) {
	g := build(t, "peering-partitions.yaml")
	// The control plane goes through the default partitions' gateways,
	// service traffic through the peered partitions' own.
	want(t, "peering", edges(g, Peering), "dc1-server-1>dc2-server-1 billing ~dc1-mesh ~dc2-mesh")
	want(t, "upstreams", edges(g, Upstream), "dc1-team-a-frontend>dc2-team-b-billing billing :9091 (ns payments) ~dc1-team-a-mesh ~dc2-team-b-mesh")
}

func TestDataplane(t *testing.T) {
	g := build(t, "dataplane.yaml")
	if n := node(t, g, "dc1-api"); n.Role != expand.DataplaneRole {
		t.Errorf("api: %+v", n)
	}
	// The dataplane's upstreams come from the manifest on server-1.
	got := edges(g, Upstream)
	if !slices.Contains(got, "dc1-api>dc1-web web :9092") || !slices.Contains(got, "dc1-web>dc1-api api :9091") {
		t.Errorf("upstreams %q", got)
	}
}

func TestPartitions(t *testing.T) {
	g := build(t, "partitions.yaml")
	if n := node(t, g, "dc1-team-a-billing-1"); n.Partition != "team-a" {
		t.Errorf("partition: %+v", n)
	}
	// frontend calls billing in team-a, not the billing in team-b.
	want(t, "upstreams", edges(g, Upstream),
		"dc1-frontend>dc1-team-a-billing-1 billing :9091 (ns payments) ~dc1-mesh ~dc1-team-a-mesh",
		"dc1-frontend>dc1-team-a-billing-2 billing :9091 (ns payments) ~dc1-mesh ~dc1-team-a-mesh")
	// Each service and gateway carries its namespace where a partition has
	// more than default (#134).
	wantNS(t, g, "dc1-team-a-billing-1", map[string]string{"billing": "payments"})
	wantNS(t, g, "dc1-team-a-mesh", map[string]string{"mesh-gateway": "default"})
	// The default partition has only default: no chips.
	wantNS(t, g, "dc1-frontend", nil)
	wantNS(t, g, "dc1-mesh", nil)
}

func wantNS(t *testing.T, g *Graph, name string, w map[string]string) {
	t.Helper()
	if got := node(t, g, name).Namespaces; !maps.Equal(got, w) {
		t.Errorf("%s namespaces: got %v, want %v", name, got, w)
	}
}

func TestGateways(t *testing.T) {
	g := build(t, "gateways.yaml")
	// The terminating gateway's hop to legacy is the last hop of web's
	// upstream path, so it isn't a route of its own as well.
	want(t, "routes", edges(g, Route), "dc1-edge>dc1-api api", "dc1-ingress>dc1-api api :8080",
		// Clients outside the lab call the ingress and API gateways' listeners.
		"internet>dc1-edge :8081 http", "internet>dc1-ingress :8080 http")
	if n := node(t, g, OutsideNode); n.Role != OutsideRole || n.DC != "" {
		t.Errorf("outside node: %+v", n)
	}
	want(t, "upstreams", edges(g, Upstream), "dc1-web>dc1-legacy legacy :9092 ~dc1-terminating")
}

func TestMinimal(t *testing.T) {
	g := build(t, "minimal.yaml")
	if len(g.Nodes) != 3 || len(g.Edges) != 0 || g.Federation != "" {
		t.Errorf("%+v", g)
	}
}

func linksOf(t *testing.T, g *Graph, name string) []string {
	t.Helper()
	var out []string
	for _, l := range node(t, g, name).Links {
		s := l.Name + " " + l.Scheme + ":" + itoa(l.Port) + l.Path
		if l.Token != "" {
			s += " token"
		}
		out = append(out, s)
	}
	return out
}

func TestLinks(t *testing.T) {
	g := build(t, "mesh.yaml")
	want(t, "server", linksOf(t, g, "dc1-server-1"), "Consul UI http:8500/ui/")
	// A fake service links to its own page.
	want(t, "service", linksOf(t, g, "dc1-web"), "web (fake service) http:8080/ui")
	// TLS turns the HTTP port off: the UI is on 8501 (and the example has ACLs).
	want(t, "tls server", linksOf(t, build(t, "tls.yaml"), "dc1-server-1"), "Consul UI https:8501/ui/ token")
	// With ACLs the UI link carries the management token to sign in with.
	want(t, "acl server", linksOf(t, build(t, "acl.yaml"), "dc1-server-1"), "Consul UI http:8500/ui/ token")
	// Gateways register no service ports of their own here.
	if l := linksOf(t, build(t, "gateways.yaml"), "dc1-ingress"); len(l) != 0 {
		t.Errorf("gateway links %q", l)
	}
}

// An external service is on several dcs' LANs: web reaches it through its
// own dc's terminating gateway, and the resolver's failover to the peer
// goes through both mesh gateways and the peer's terminating gateway.
func TestTerminatingFailover(t *testing.T) {
	g := build(t, "terminating-failover.yaml")
	want(t, "upstreams", edges(g, Upstream), "dc1-web>api api :9091 ~dc1-terminating")
	// Each terminating gateway's hop to api is drawn once, by the path
	// that uses it: dc2's only carries the failover.
	want(t, "routes", edges(g, Route))
	want(t, "failovers", edges(g, Failover), "dc1-web>api api failover (peer dc1-dc2) ~dc1-mesh ~dc2-mesh ~dc2-terminating")
}

func TestTerminatingFailoverPartitions(t *testing.T) {
	t.Setenv("CONSUL_LICENSE", "test")
	g := build(t, "terminating-failover-partitions.yaml")
	want(t, "upstreams", edges(g, Upstream), "dc1-part-a-web>api api :9091 ~dc1-part-a-terminating")
	want(t, "routes", edges(g, Route))
	want(t, "failovers", edges(g, Failover),
		"dc1-part-a-web>api api failover (peer dc1-dc2) ~dc1-part-a-mesh ~dc2-part-b-mesh ~dc2-part-b-terminating")
	// web, its terminating gateway and the external api sit in app; the
	// mesh gateway in default.
	wantNS(t, g, "dc1-part-a-web", map[string]string{"web": "app"})
	wantNS(t, g, "dc1-part-a-terminating", map[string]string{"terminating-gateway": "app"})
	wantNS(t, g, "dc1-part-a-mesh", map[string]string{"mesh-gateway": "default"})
	wantNS(t, g, "api", map[string]string{"api": "app"})
}

func TestFailoverDatacentersAndPartitions(t *testing.T) {
	want(t, "failovers", edges(build(t, "fake-chain.yaml"), Failover), "dc1-web>dc2-api api failover (dc2) ~dc1-mesh ~dc2-mesh")
	t.Setenv("CONSUL_LICENSE", "test")
	want(t, "failovers", edges(build(t, "partitions.yaml"), Failover),
		"dc1-frontend>dc1-team-b-billing billing failover (dc1/team-b, ns payments) ~dc1-mesh ~dc1-team-b-mesh")
}

func TestPeeringDirect(t *testing.T) {
	g := build(t, "terminating-failover.yaml")
	// No via_mesh_gateways: the servers peer directly.
	for _, e := range g.Edges {
		if e.Kind == Peering && len(e.Via) > 0 {
			t.Errorf("peering %s goes via %v", e.Label, e.Via)
		}
	}
	if len(edges(g, Peering)) == 0 {
		t.Error("no peering edge")
	}
}
