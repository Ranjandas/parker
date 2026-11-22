package expand

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/selector"
	"github.com/Ranjandas/parker/internal/spec"
)

func TestMeshGatewayFederation(t *testing.T) {
	low, err := Expand(load(t, "../../examples/mesh-federation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	gw := low.Nodes["dc1-mesh"]
	if gw == nil || gw.Labels.Role != "gateway" || gw.Labels.Group != "mesh" {
		t.Fatalf("dc1-mesh: %+v", gw)
	}
	if gw.Networks[WANNetwork] == nil || gw.Networks[WANNetwork].IP != "10.255.0.200" {
		t.Errorf("dc1 gateway wan: %v", gw.Networks)
	}
	run := strings.Join(gw.Processes["mesh-gateway"].Run, " ")
	for _, want := range []string{"-gateway=mesh -register -service mesh-gateway", "-address 10.1.0.100:8443",
		"-bind-address default=0.0.0.0:8443", "-wan-address 10.255.0.200:8443", "-expose-servers"} {
		if !strings.Contains(run, want) {
			t.Errorf("gateway run lacks %q: %s", want, run)
		}
	}
	if p := agentConfig(t, gw)["ports"].(map[string]any); p["grpc_tls"] != float64(GRPCTLSPort) {
		t.Errorf("gateway agent ports: %v", p)
	}
	if low.Nodes["dc2-mesh"].Networks[WANNetwork].IP != "10.255.0.210" {
		t.Errorf("dc2 gateway wan: %v", low.Nodes["dc2-mesh"].Networks)
	}

	s1 := low.Nodes["dc1-server-1"]
	c1 := agentConfig(t, s1)
	if c1["connect"].(map[string]any)["enable_mesh_gateway_wan_federation"] != true || c1["primary_gateways"] != nil || c1["retry_join_wan"] != nil {
		t.Errorf("primary server: connect %v primary_gateways %v retry_join_wan %v", c1["connect"], c1["primary_gateways"], c1["retry_join_wan"])
	}
	if c1["bind_addr"] != "0.0.0.0" || c1["serf_wan"] != "10.1.0.10" || s1.Networks[WANNetwork] == nil {
		t.Errorf("primary server binds: %v %v, networks %v", c1["bind_addr"], c1["serf_wan"], s1.Networks)
	}
	if r := s1.Processes["consul"].ReadyWhen.Consul; r.WANMembers != "" {
		t.Errorf("the primary can't wait for secondaries that wait for its gateway: %+v", r)
	}

	s2 := low.Nodes["dc2-server-1"]
	c2 := agentConfig(t, s2)
	if !reflect.DeepEqual(c2["primary_gateways"], []any{"10.255.0.200:8443"}) || c2["primary_datacenter"] != "dc1" {
		t.Errorf("secondary server: primary_gateways %v primary_datacenter %v", c2["primary_gateways"], c2["primary_datacenter"])
	}
	p2 := s2.Processes["consul"]
	if p2.DependsOn["dc1-mesh/mesh-gateway"] == nil || p2.ReadyWhen.Consul.WANMembers != ">=2" {
		t.Errorf("secondary server: deps %v ready %+v", p2.DependsOn, p2.ReadyWhen.Consul)
	}

	g2 := low.Nodes["dc2-mesh"]
	if d := g2.Processes["consul"].DependsOn["dc2-server-1/consul"]; d == nil || d.Condition != "started" {
		t.Errorf("secondary gateway agent waits for federation it carries: %+v", d)
	}
	if r := g2.Processes["mesh-gateway"].ReadyWhen.Consul; r == nil || r.Reaches != "dc2" || r.Addr != "10.255.0.10:8501" {
		t.Errorf("secondary gateway ready: %+v", r)
	}
	if low.Nodes["dc1-mesh"].Processes["mesh-gateway"].ReadyWhen.Consul != nil {
		t.Error("the primary gateway checks only Envoy")
	}
	if got, err := selector.Resolve(low, "dc2/gateways", nil); err != nil || !slices.Equal(got, []string{"dc2-mesh"}) {
		t.Errorf("dc2/gateways = %v, %v", got, err)
	}
}

// Only mesh gateways take -expose-servers; Envoy for an API gateway
// refuses to start with it.
func TestMeshFederationOtherGateways(t *testing.T) {
	low, err := Expand(load(t, "../../hack/e2e/mesh-federation-gateways.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if run := strings.Join(low.Nodes["dc1-mesh"].Processes["mesh-gateway"].Run, " "); !strings.Contains(run, "-expose-servers") {
		t.Errorf("mesh gateway: %s", run)
	}
	edge := low.Nodes["dc1-edge"]
	if edge == nil {
		t.Fatal("no dc1-edge")
	}
	run := strings.Join(edge.Processes["api-gateway"].Run, " ")
	if !strings.Contains(run, "-gateway=api") || strings.Contains(run, "-expose-servers") || strings.Contains(run, "-wan-address") {
		t.Errorf("api gateway: %s", run)
	}
	if _, ok := edge.Networks[WANNetwork]; ok {
		t.Error("the api gateway joins the wan")
	}
}

func TestMeshGatewayWithoutFederation(t *testing.T) {
	f := load(t, "../../examples/mesh.yaml")
	f.Consul.Datacenters["dc1"].Gateways = map[string]*spec.Gateway{"edge": {Kind: "mesh", Count: 2}}
	low, err := Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"dc1-edge-1", "dc1-edge-2"} {
		gw := low.Nodes[n]
		if gw == nil {
			t.Fatalf("no %s", n)
		}
		if _, ok := gw.Networks[WANNetwork]; ok {
			t.Errorf("%s joins the wan without federation", n)
		}
		run := strings.Join(gw.Processes["mesh-gateway"].Run, " ")
		if strings.Contains(run, "-wan-address") || strings.Contains(run, "-expose-servers") {
			t.Errorf("%s: %s", n, run)
		}
	}
	if _, ok := low.Networks[WANNetwork]; ok {
		t.Error("wan network generated with nothing on it")
	}
	if a, b := low.Nodes["dc1-edge-1"].Networks["dc1-lan"].IP, low.Nodes["dc1-edge-2"].Networks["dc1-lan"].IP; a == b {
		t.Errorf("gateways share %s", a)
	}
}

func TestGatewayErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*spec.File)
		want string
	}{
		{"no tls", func(f *spec.File) { f.Consul.Defaults.TLS = nil }, "mesh-gateways needs tls: true"},
		{"no gateway", func(f *spec.File) { f.Consul.Datacenters["dc2"].Gateways = nil }, "needs a mesh gateway on the wan network in every datacenter; add gateways: { mesh: true } to dc2"},
		{"off the wan", func(f *spec.File) {
			off := false
			f.Consul.Datacenters["dc2"].Gateways["mesh"].WAN = &off
		}, "to dc2"},
		{"bad kind", func(f *spec.File) {
			f.Consul.Datacenters["dc1"].Gateways = map[string]*spec.Gateway{"edge": {}, "mesh": {}}
		}, "gateways.edge: kind: want mesh"},
		{"clash", func(f *spec.File) {
			f.Consul.Datacenters["dc1"].Gateways = map[string]*spec.Gateway{"web": {Kind: "mesh"}, "mesh": {}}
		}, "gateways.web: a node is already named web"},
	} {
		f := load(t, "../../examples/mesh-federation.yaml")
		c.edit(f)
		if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

func TestGatewayYAML(t *testing.T) {
	var dc spec.Datacenter
	if err := unmarshalStrict("servers: 1\ngateways: { mesh: true, edge: { kind: mesh, count: 2, wan: false } }", &dc); err != nil {
		t.Fatal(err)
	}
	if g := dc.Gateways["mesh"]; g == nil || g.Kind != "" || g.Count != 0 {
		t.Errorf("mesh: true = %+v", g)
	}
	if g := dc.Gateways["edge"]; g.Kind != "mesh" || g.Count != 2 || g.WAN == nil || *g.WAN {
		t.Errorf("edge = %+v", g)
	}
	if err := unmarshalStrict("servers: 1\ngateways: { mesh: false }", &dc); err == nil || !strings.Contains(err.Error(), "leave the gateway out") {
		t.Errorf("mesh: false: %v", err)
	}
}

func TestIngressTerminatingAPIGateways(t *testing.T) {
	low, err := Expand(load(t, "../../examples/gateways.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for node, want := range map[string]string{
		"dc1-ingress":     "consul connect envoy -gateway=ingress -register -service ingress-gateway -address ",
		"dc1-terminating": "consul connect envoy -gateway=terminating -register -service terminating-gateway -address ",
		"dc1-edge":        "consul connect envoy -gateway=api -register -service edge -address ",
	} {
		n := low.Nodes[node]
		if n == nil || n.Labels.Role != "gateway" {
			t.Fatalf("%s: %+v", node, n)
		}
		var run string
		for pn, p := range n.Processes {
			if strings.HasSuffix(pn, "-gateway") {
				run = strings.Join(p.Run, " ")
			}
		}
		if !strings.HasPrefix(run, want) || strings.Contains(run, "-bind-address") || strings.Contains(run, "-wan-address") {
			t.Errorf("%s runs %q", node, run)
		}
	}

	srv := low.Nodes["dc1-server-1"]
	var refs []EntryRef
	if err := json.Unmarshal([]byte(srv.Files[ConfigEntriesManifest]), &refs); err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, r := range refs {
		labels = append(labels, r.Label)
	}
	if len(labels) != 5 || !strings.HasPrefix(labels[0], "consul.datacenters.dc1.config_entries[0]") ||
		labels[2] != "consul.datacenters.dc1.gateways.edge.entry" || labels[4] != "consul.datacenters.dc1.gateways.terminating.entry" {
		t.Errorf("entries in order: %v", labels)
	}
	var edge map[string]any
	if err := json.Unmarshal([]byte(srv.Files["gateway-entry-0.json"]), &edge); err != nil {
		t.Fatal(err)
	}
	if edge["Kind"] != "api-gateway" || edge["Name"] != "edge" || edge["Listeners"] == nil {
		t.Errorf("api gateway entry: %v", edge)
	}
}

func TestOtherGatewayErrors(t *testing.T) {
	on := true
	for _, c := range []struct {
		name string
		gw   *spec.Gateway
		want string
	}{
		{"ingress on the wan", &spec.Gateway{Kind: "ingress", WAN: &on}, "only mesh gateways join the wan"},
		{"mesh with an entry", &spec.Gateway{Kind: "mesh", Entry: map[string]any{"Listeners": nil}}, "mesh gateways have no config entry"},
		{"unknown kind", &spec.Gateway{Kind: "egress"}, "want mesh, ingress, terminating or api"},
	} {
		f := load(t, "../../examples/gateways.yaml")
		f.Consul.Datacenters["dc1"].Gateways["x"] = c.gw
		if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
