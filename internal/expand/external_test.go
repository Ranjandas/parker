package expand

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/selector"
	"github.com/Ranjandas/parker/internal/spec"
)

func dataplaneManifest(t *testing.T, n *spec.RawNode) []DataplaneEntry {
	t.Helper()
	var es []DataplaneEntry
	if err := json.Unmarshal([]byte(n.Files[DataplaneManifest]), &es); err != nil {
		t.Fatal(err)
	}
	return es
}

func TestExternalService(t *testing.T) {
	low, err := Expand(load(t, "../../examples/terminating-failover.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	api := low.Nodes["api"]
	if api == nil || api.Labels.Role != ExternalRole {
		t.Fatalf("api node: %+v", api)
	}
	if api.Networks["dc1-lan"].IP != "10.1.0.254" || api.Networks["dc2-lan"].IP != "10.2.0.254" || len(api.Networks) != 2 {
		t.Errorf("api joins both LANs at the top of the subnet: %v", api.Networks)
	}
	p := api.Processes["api"]
	if p == nil || !slices.Contains(p.Run, "fake-service") || p.ReadyWhen.Consul != nil {
		t.Errorf("api runs the fake with no agent to ask: %+v", p)
	}
	if _, ok := api.Files["consul.json"]; ok || api.Processes["consul"] != nil {
		t.Error("an external node runs no Consul agent")
	}
	for dc, ip := range map[string]string{"dc1": "10.1.0.254", "dc2": "10.2.0.254"} {
		s1 := low.Nodes[dc+"-server-1"]
		if s1.Processes[DataplaneHealth] == nil {
			t.Fatalf("%s: no %s process to register api", dc, DataplaneHealth)
		}
		es := dataplaneManifest(t, s1)
		if len(es) != 1 {
			t.Fatalf("%s: %+v", dc, es)
		}
		e := es[0]
		if e.Node != "api" || e.Address != ip || e.NodeMeta["external-node"] != "true" || e.Service.Name != "api" || e.Service.Port != 9000 ||
			e.Probe != "http://"+ip+":9000/health" {
			t.Errorf("%s registration: %+v", dc, e)
		}
	}
	got, err := selector.Resolve(low, "api", nil)
	if err != nil || !slices.Equal(got, []string{"api"}) {
		t.Errorf("selector api: %v %v", got, err)
	}
}

func TestExternalServicePartitions(t *testing.T) {
	low, err := Expand(load(t, "../../examples/terminating-failover-partitions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	e := dataplaneManifest(t, low.Nodes["dc2-server-1"])[0]
	if e.Partition != "part-b" || e.Service.Namespace != "app" {
		t.Errorf("api registers in part-b's app namespace: %+v", e)
	}
	var ten TenancySpec
	if err := json.Unmarshal([]byte(low.Nodes["dc2-server-1"].Files[TenancyManifest]), &ten); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ten.Namespaces, NamespaceRef{Partition: "part-b", Name: "app"}) {
		t.Errorf("dc2 creates app in part-b for api and the gateway, though no node runs there: %+v", ten)
	}
	if ns := low.Nodes["api"].Labels.Namespaces; !slices.Equal(ns, []string{"app"}) {
		t.Errorf("api namespaces label: %v", ns)
	}

	tgw := low.Nodes["dc1-part-a-terminating"]
	if env := tgw.Processes["terminating-gateway"].Env; env["CONSUL_NAMESPACE"] != "app" || env["CONSUL_PARTITION"] != "part-a" {
		t.Errorf("terminating gateway registers in part-a/app: %v", env)
	}
	if mgw := low.Nodes["dc1-part-a-mesh"].Processes["mesh-gateway"].Env; mgw["CONSUL_NAMESPACE"] != "default" {
		t.Errorf("mesh gateway stays in default: %v", mgw)
	}
	tge := entriesIn(t, low.Nodes["dc1-server-1"])["part-a/terminating-gateway"]
	if tge["Namespace"] != "app" {
		t.Errorf("terminating-gateway entry: %v", tge)
	}
	got, err := selector.Resolve(low, "ns:dc1/app", nil)
	if err != nil || !slices.Equal(got, []string{"dc1-part-a-terminating", "dc1-part-a-web"}) {
		t.Errorf("ns:dc1/app: %v %v", got, err)
	}

	// exports: [api] finds api's namespace from consul.external too.
	f := load(t, "../../examples/terminating-failover-partitions.yaml")
	f.Consul.Peering["dc1-dc2"].Exports["dc2/part-b"] = []string{"api"}
	low, err = Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	svc := entriesIn(t, low.Nodes["dc2-server-1"])["part-b/exported-services"]["Services"].([]any)[0].(map[string]any)
	if svc["Namespace"] != "app" {
		t.Errorf("exported api: %v", svc)
	}
}

func TestExternalServiceErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*spec.File)
		want string
	}{
		{"no service", func(f *spec.File) { f.Consul.External["api"].Service = nil }, "consul.external.api: service: required"},
		{"no port", func(f *spec.File) { f.Consul.External["api"].Service.Port = 0 }, "consul.external.api.service.port: required"},
		{"connect", func(f *spec.File) { f.Consul.External["api"].Service.Connect = "sidecar" }, "reach it through a terminating gateway"},
		{"upstreams", func(f *spec.File) {
			f.Consul.External["api"].Service.Upstreams = map[string]*spec.Upstream{"db": {Port: 9100}}
		}, "consul.external.api.service.upstreams"},
		{"no in", func(f *spec.File) { f.Consul.External["api"].In = nil }, "consul.external.api.in: list where to register it"},
		{"unknown dc", func(f *spec.File) { f.Consul.External["api"].In = []string{"dc9"} }, `consul.external.api.in: no datacenter "dc9"`},
		{"twice", func(f *spec.File) { f.Consul.External["api"].In = []string{"dc1", "dc1/default"} }, "dc1 is listed twice"},
		{"node clash", func(f *spec.File) {
			f.Consul.Datacenters["dc1"].Nodes["api"] = &spec.Node{}
		}, "dc1 already has a node named api"},
		{"bad name", func(f *spec.File) {
			f.Consul.External["API"] = f.Consul.External["api"]
		}, "consul.external.API: names are lowercase"},
		{"no room", func(f *spec.File) {
			f.Consul.Datacenters["dc1"].Subnet = "10.1.0.0/25"
			f.Consul.Datacenters["dc1"].Nodes["web"].Count = 26
		}, "no room left on dc1's LAN"},
	} {
		f := load(t, "../../examples/terminating-failover.yaml")
		c.edit(f)
		if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
