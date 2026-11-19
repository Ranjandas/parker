package expand

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func entriesOn(t *testing.T, n *spec.RawNode) map[string]map[string]any {
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
		out[e["Kind"].(string)] = e
	}
	return out
}

func TestPeeringViaMeshGateways(t *testing.T) {
	low, err := Expand(load(t, "../../examples/peering.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	acc, dial := low.Nodes["dc2-server-1"], low.Nodes["dc1-server-1"]
	tok := acc.Processes["peering-dc1-dc2-token"]
	if tok == nil {
		t.Fatalf("no token process on dc2-server-1: %v", acc.Processes)
	}
	script := strings.Join(tok.Run, " ")
	if !strings.Contains(script, "consul peering generate-token -name dc1-dc2 >") || strings.Contains(script, "-server-external-addresses") {
		t.Errorf("token script: %s", script)
	}
	for _, d := range []string{"dc2-mesh/mesh-gateway", "dc2-server-1/" + ConfigEntries, "dc2-server-1/consul"} {
		if tok.DependsOn[d] == nil {
			t.Errorf("token process doesn't wait for %s: %v", d, tok.DependsOn)
		}
	}
	est := dial.Processes["peering-dc1-dc2"]
	if est == nil || est.DependsOn["dc2-server-1/peering-dc1-dc2-token"] == nil || est.DependsOn["dc1-mesh/mesh-gateway"] == nil {
		t.Fatalf("establish process: %+v", est)
	}
	if !strings.Contains(strings.Join(est.Run, " "), `until consul peering establish -name dc1-dc2 -peering-token "$(cat ${run_dir}/peering-dc1-dc2.token)"; do sleep 2; done`) {
		t.Errorf("establish script: %v", est.Run)
	}
	if est.ReadyWhen.Log != PeeringActiveLine("dc1-dc2") {
		t.Errorf("establish ready: %+v", est.ReadyWhen)
	}

	ae := entriesOn(t, acc)
	if m := ae["mesh"]; m == nil || m["Peering"].(map[string]any)["PeerThroughMeshGateways"] != true {
		t.Errorf("dc2 mesh entry: %v", ae["mesh"])
	}
	exp := ae["exported-services"]
	if exp == nil {
		t.Fatal("no exported-services entry in dc2")
	}
	b, _ := json.Marshal(exp["Services"])
	if string(b) != `[{"Consumers":[{"Peer":"dc1-dc2"}],"Name":"api"}]` {
		t.Errorf("exports: %s", b)
	}
	if de := entriesOn(t, dial); de["mesh"] == nil || de["exported-services"] != nil || de["proxy-defaults"] == nil {
		t.Errorf("dc1 entries: %v", de)
	}
	if p := agentConfig(t, acc)["ports"].(map[string]any); p["grpc"] != float64(GRPCPort) {
		t.Errorf("acceptor server ports: %v", p)
	}
	if _, ok := acc.Networks[WANNetwork]; ok {
		t.Error("servers stay off the wan when peering through gateways")
	}
	if _, ok := low.Nodes["dc2-mesh"].Networks[WANNetwork]; !ok {
		t.Error("peered mesh gateways join the wan")
	}
}

func TestPeeringDirect(t *testing.T) {
	low, err := Expand(load(t, "../../hack/e2e/peering-secure.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	acc := low.Nodes["dc2-server-1"]
	if !strings.Contains(strings.Join(acc.Processes["peering-dc1-dc2-token"].Run, " "), "-server-external-addresses 10.255.0.30:8503") {
		t.Errorf("token script: %v", acc.Processes["peering-dc1-dc2-token"].Run)
	}
	for _, n := range []string{"dc1-server-1", "dc2-server-1"} {
		node := low.Nodes[n]
		cfg := agentConfig(t, node)
		if node.Networks[WANNetwork] == nil || cfg["bind_addr"] != "0.0.0.0" {
			t.Errorf("%s: networks %v bind %v", n, node.Networks, cfg["bind_addr"])
		}
		if p := cfg["ports"].(map[string]any); p["grpc_tls"] != float64(GRPCTLSPort) || p["https"] != float64(HTTPSPort) {
			t.Errorf("%s ports: %v", n, p)
		}
	}
	if _, ok := entriesOn(t, acc)["mesh"]; ok {
		t.Error("no PeerThroughMeshGateways without via_mesh_gateways")
	}
}

func TestPeeringErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*spec.File)
		want string
	}{
		{"old consul", func(f *spec.File) { f.Versions["consul"] = "1.13.9" }, "needs Consul 1.14 or later"},
		{"federated", func(f *spec.File) {
			f.Consul.Federation = &spec.Federation{Mode: "wan"}
		}, "can't also be federated"},
		{"same dc", func(f *spec.File) { f.Consul.Peering["dc1-dc2"].Dialer = "dc2" }, "acceptor and dialer are both dc2"},
		{"unknown dc", func(f *spec.File) { f.Consul.Peering["dc1-dc2"].Acceptor = "dc9" }, `acceptor: no datacenter "dc9"`},
		{"partition", func(f *spec.File) { f.Consul.Peering["dc1-dc2"].Dialer = "dc1/web" }, "partitions (dc1/web) are not supported yet"},
		{"no dialer", func(f *spec.File) { f.Consul.Peering["dc1-dc2"].Dialer = "" }, "dialer: required"},
		{"exports elsewhere", func(f *spec.File) {
			f.Consul.Datacenters["dc3"] = &spec.Datacenter{Servers: spec.Servers{Count: 1}}
			f.Consul.Peering["dc1-dc2"].Exports["dc3"] = []string{"x"}
		}, "exports.dc3: not a side of this peering"},
		{"no gateway", func(f *spec.File) { f.Consul.Datacenters["dc1"].Gateways = nil }, "dc1 needs a mesh gateway for peered traffic"},
	} {
		f := load(t, "../../examples/peering.yaml")
		c.edit(f)
		if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
