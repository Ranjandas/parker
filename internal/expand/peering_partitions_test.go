package expand

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

// entriesIn lists a server's generated config entries with the partition
// each is written into.
func entriesIn(t *testing.T, n *spec.RawNode) map[string]map[string]any {
	t.Helper()
	var refs []EntryRef
	if err := json.Unmarshal([]byte(n.Files[ConfigEntriesManifest]), &refs); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, r := range refs {
		var e map[string]any
		if err := json.Unmarshal([]byte(n.Files[strings.TrimPrefix(r.File, VarNodeDir+"/")]), &e); err != nil {
			t.Fatalf("%s: %v", r.File, err)
		}
		out[r.Partition+"/"+e["Kind"].(string)] = e
	}
	return out
}

func TestPeeringPartitions(t *testing.T) {
	low, err := Expand(load(t, "../../examples/peering-partitions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	acc, dial := low.Nodes["dc2-server-1"], low.Nodes["dc1-server-1"]
	tok := acc.Processes["peering-billing-token"]
	if tok == nil || tok.Env["CONSUL_PARTITION"] != "team-b" {
		t.Fatalf("token process: %+v", tok)
	}
	// After the partitions exist, through the default partition's gateway.
	for _, d := range []string{"dc2-server-1/" + Tenancy, "dc2-mesh/mesh-gateway", "dc2-server-1/" + ConfigEntries} {
		if tok.DependsOn[d] == nil {
			t.Errorf("token process doesn't wait for %s: %v", d, tok.DependsOn)
		}
	}
	est := dial.Processes["peering-billing"]
	if est == nil || est.Env["CONSUL_PARTITION"] != "team-a" || est.DependsOn["dc2-server-1/peering-billing-token"] == nil ||
		est.DependsOn["dc1-mesh/mesh-gateway"] == nil || est.DependsOn["dc1-server-1/"+Tenancy] == nil {
		t.Fatalf("establish process: %+v", est)
	}

	ae := entriesIn(t, acc)
	if m := ae["/mesh"]; m == nil || m["Peering"].(map[string]any)["PeerThroughMeshGateways"] != true {
		t.Errorf("PeerThroughMeshGateways must be on the default partition's mesh entry: %v", ae)
	}
	ex := ae["team-b/exported-services"]
	if ex == nil || ex["Name"] != "team-b" {
		t.Fatalf("team-b exported-services: %v", ae)
	}
	svc := ex["Services"].([]any)[0].(map[string]any)
	if svc["Name"] != "billing" || svc["Namespace"] != "payments" || svc["Consumers"].([]any)[0].(map[string]any)["Peer"] != "billing" {
		t.Errorf("exported billing: %v", svc)
	}

	// Every mesh gateway, in every partition, registers in the default
	// namespace.
	for key, n := range low.Nodes {
		if n.Labels.Role != "gateway" {
			continue
		}
		p := n.Processes["mesh-gateway"]
		if p == nil || p.Env["CONSUL_NAMESPACE"] != spec.DefaultNamespace {
			t.Errorf("%s: gateway env %v", key, p)
		}
		if n.Labels.Partition != "default" && p.Env["CONSUL_PARTITION"] != n.Labels.Partition {
			t.Errorf("%s: gateway partition %v", key, p.Env)
		}
		// The peer's gateways dial it on the wan network, so a
		// partition's gateway is on it too and advertises that address.
		wan := n.Networks[WANNetwork]
		if wan == nil || !strings.Contains(strings.Join(p.Run, " "), "-wan-address "+wan.IP+":") {
			t.Errorf("%s: mesh gateway not on the wan network: %v %v", key, n.Networks, p.Run)
		}
	}
	if low.Nodes["dc2-team-b-mesh"] == nil || low.Nodes["dc1-team-a-mesh"] == nil {
		t.Error("each peered partition gets its own mesh gateway")
	}
}

func TestPeeringPartitionErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*spec.File)
		want string
	}{
		{"same dc", func(f *spec.File) {
			f.Consul.Datacenters["dc1"].Partitions["team-b"] = f.Consul.Datacenters["dc2"].Partitions["team-b"]
			f.Consul.Peering["billing"].Acceptor = "dc1/team-b"
			f.Consul.Peering["billing"].Exports = nil
		}, "Consul can't peer two partitions of the same datacenter (dc1/team-b, dc1/team-a); export the services to the other partition instead, with an exported-services entry whose Consumers are [{ Partition: team-a }]"},
		{"no partition", func(f *spec.File) { f.Consul.Peering["billing"].Dialer = "dc1/team-z" }, `dialer: no partition "team-z" in dc1`},
		{"empty partition", func(f *spec.File) { f.Consul.Peering["billing"].Dialer = "dc1/" }, "name the partition after the slash"},
		{"no partition gateway", func(f *spec.File) { f.Consul.Datacenters["dc2"].Partitions["team-b"].Gateways = nil },
			"dc2/team-b needs a mesh gateway for peered traffic; add gateways: { mesh: true } to consul.datacenters.dc2.partitions.team-b"},
		{"no default gateway with via", func(f *spec.File) { f.Consul.Datacenters["dc1"].Partitions["default"].Gateways = nil },
			"via_mesh_gateways sends the control plane through dc1's default partition, which needs a mesh gateway too"},
		{"exports other partition", func(f *spec.File) {
			f.Consul.Peering["billing"].Exports = map[string][]string{"dc2": {"billing"}}
		}, "exports.dc2: not a side of this peering (dc2/team-b, dc1/team-a)"},
		{"peer from another partition", func(f *spec.File) {
			fe := f.Consul.Datacenters["dc1"].Partitions["team-a"].Nodes["frontend"]
			f.Consul.Datacenters["dc1"].Partitions["default"].Nodes = map[string]*spec.Node{"frontend": fe}
			delete(f.Consul.Datacenters["dc1"].Partitions["team-a"].Nodes, "frontend")
		}, "dc1: service frontend, upstream billing: peering billing connects dc2/team-b and dc1/team-a, not dc1"},
		{"unknown peer", func(f *spec.File) {
			f.Consul.Datacenters["dc1"].Partitions["team-a"].Nodes["frontend"].Service.Upstreams["billing"].Peer = "nope"
		}, "peer nope is not under consul.peering"},
		{"ambiguous namespace", func(f *spec.File) {
			part := f.Consul.Datacenters["dc2"].Partitions["team-b"]
			part.Nodes["billing-2"] = &spec.Node{Services: map[string]*spec.Service{"billing": {Port: 9000, Namespace: "other"}}}
			f.Consul.Peering["billing"].Exports["dc2/team-b"] = []string{"billing"}
		}, "billing runs in namespaces other, payments; write it as <namespace>/billing"},
	} {
		f := load(t, "../../examples/peering-partitions.yaml")
		c.edit(f)
		if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

func TestPeeringExportsInferNamespace(t *testing.T) {
	f := load(t, "../../examples/peering-partitions.yaml")
	f.Consul.Peering["billing"].Exports["dc2/team-b"] = []string{"billing"}
	low, err := Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	svc := entriesIn(t, low.Nodes["dc2-server-1"])["team-b/exported-services"]["Services"].([]any)[0].(map[string]any)
	if svc["Namespace"] != "payments" {
		t.Errorf("billing's namespace comes from team-b's service_template: %v", svc)
	}
}

func TestPeeringExportsMergeIntoUserEntry(t *testing.T) {
	f := load(t, "../../examples/peering-partitions.yaml")
	part := f.Consul.Datacenters["dc2"].Partitions["team-b"]
	part.ConfigEntries = append(part.ConfigEntries, spec.ConfigEntry{
		"Kind": "exported-services", "Name": "team-b",
		"Services": []any{
			map[string]any{"Name": "billing", "Namespace": "payments", "Consumers": []any{map[string]any{"Partition": "default"}}},
			map[string]any{"Name": "audit", "Consumers": []any{map[string]any{"Partition": "default"}}},
		},
	})
	low, err := Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	srv := low.Nodes["dc2-server-1"]
	var refs []EntryRef
	if err := json.Unmarshal([]byte(srv.Files[ConfigEntriesManifest]), &refs); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range refs {
		var e map[string]any
		_ = json.Unmarshal([]byte(srv.Files[strings.TrimPrefix(r.File, VarNodeDir+"/")]), &e)
		if e["Kind"] == "exported-services" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d exported-services entries for team-b, want one", n)
	}
	ex := entriesIn(t, srv)["team-b/exported-services"]
	svcs := ex["Services"].([]any)
	if len(svcs) != 2 {
		t.Fatalf("services: %v", svcs)
	}
	cs := svcs[0].(map[string]any)["Consumers"].([]any)
	if len(cs) != 2 || cs[1].(map[string]any)["Peer"] != "billing" {
		t.Errorf("billing consumers: %v", cs)
	}
}

func TestGatewayNamespace(t *testing.T) {
	if errs := spec.CheckKeys([]byte("parker: v1alpha1\nconsul:\n  datacenters:\n    dc1:\n      gateways:\n        terminating: { namespace: payments }\n")); len(errs) != 0 {
		t.Errorf("namespace is a gateway key: %v", errs)
	}
	f := load(t, "../../examples/peering-partitions.yaml")
	f.Consul.Datacenters["dc1"].Partitions["team-a"].Gateways["mesh"] = &spec.Gateway{Namespace: "payments"}
	if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), "mesh gateways sit in their partition's default namespace") {
		t.Errorf("mesh gateway in a namespace: %v", err)
	}
}
