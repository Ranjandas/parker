package graph

import (
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
	want(t, "peering", edges(g, Peering), "dc1-server-1>dc2-server-1 dc1-dc2")
	want(t, "upstreams", edges(g, Upstream), "dc1-web>dc2-api api :9091 ~dc1-mesh ~dc2-mesh")
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
		"dc1-frontend>dc1-team-a-billing-1 billing :9091 ~dc1-mesh ~dc1-team-a-mesh",
		"dc1-frontend>dc1-team-a-billing-2 billing :9091 ~dc1-mesh ~dc1-team-a-mesh")
}

func TestGateways(t *testing.T) {
	g := build(t, "gateways.yaml")
	want(t, "routes", edges(g, Route), "dc1-edge>dc1-api api", "dc1-ingress>dc1-api api :8080")
	want(t, "upstreams", edges(g, Upstream), "dc1-web>dc1-legacy legacy :9092 ~dc1-terminating")
}

func TestMinimal(t *testing.T) {
	g := build(t, "minimal.yaml")
	if len(g.Nodes) != 3 || len(g.Edges) != 0 || g.Federation != "" {
		t.Errorf("%+v", g)
	}
}
