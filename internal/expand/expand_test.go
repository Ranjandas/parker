package expand

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/Ranjandas/parker/internal/spec"
)

func load(t *testing.T, path string) *spec.File {
	t.Helper()
	f, err := spec.Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func agentConfig(t *testing.T, n *spec.RawNode) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(n.Files["consul.json"]), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMinimal(t *testing.T) {
	low, err := Expand(load(t, "../../examples/minimal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(low.Nodes); got != 3 {
		t.Fatalf("nodes: got %d, want 3", got)
	}
	if low.Networks["dc1-lan"].Subnet != "10.1.0.0/24" || low.Networks["dc1-lan"].Gateway != "10.1.0.1" {
		t.Fatalf("network: %+v", low.Networks["dc1-lan"])
	}
	cfg := agentConfig(t, low.Nodes["dc1-server-2"])
	if cfg["bootstrap_expect"].(float64) != 3 || cfg["bind_addr"] != "10.1.0.11" {
		t.Fatalf("server-2 config: %v", cfg)
	}
	if !reflect.DeepEqual(cfg["retry_join"], []any{"10.1.0.10", "10.1.0.12"}) {
		t.Fatalf("retry_join: %v", cfg["retry_join"])
	}
}

func TestClientsInheritAndDepend(t *testing.T) {
	low, err := Expand(load(t, "../../examples/clients.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	web := low.Nodes["dc1-web-2"]
	if web == nil {
		t.Fatal("dc1-web-2 missing (count: 2 should give web-1, web-2)")
	}
	cfg := agentConfig(t, web)
	if cfg["log_level"] != "DEBUG" {
		t.Errorf("log_level not inherited from dc agents: %v", cfg["log_level"])
	}
	if len(web.Processes["consul"].DependsOn) != 3 {
		t.Errorf("client agent should wait for the 3 servers: %v", web.Processes["consul"].DependsOn)
	}
	if web.Processes["web"].DependsOn["dc1-web-2/consul"] == nil {
		t.Errorf("service should depend on its local agent")
	}
	if _, ok := low.Nodes["dc1-client"]; !ok {
		t.Errorf("count 1 node should keep its bare name")
	}
}

// expand output must be valid input and expand to itself.
func TestRoundTrip(t *testing.T) {
	for _, ex := range []string{"minimal", "clients"} {
		low, err := Expand(load(t, "../../examples/"+ex+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := yaml.MarshalWithOptions(low, yaml.UseLiteralStyleIfMultiline(true))
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), ex+".yaml")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		again, err := Expand(load(t, p))
		if err != nil {
			t.Fatalf("%s: re-expand: %v", ex, err)
		}
		b2, _ := yaml.MarshalWithOptions(again, yaml.UseLiteralStyleIfMultiline(true))
		if string(b) != string(b2) {
			t.Errorf("%s: expand is not a fixed point", ex)
		}
	}
}

func TestOverlappingNetworksRejected(t *testing.T) {
	err := checkOverlaps(map[string]*spec.Network{
		"dc1-lan": {Subnet: "10.1.0.0/24"},
		"wan":     {Subnet: "10.1.0.128/25"},
	})
	if err == nil {
		t.Fatal("want overlap error")
	}
	if err := checkOverlaps(map[string]*spec.Network{
		"dc1-lan": {Subnet: "10.1.0.0/24"},
		"dc2-lan": {Subnet: "10.2.0.0/24"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWANFederation(t *testing.T) {
	low, err := Expand(load(t, "../../examples/federation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if w := low.Networks["wan"]; w == nil || w.Subnet != WANSubnet || w.Gateway != "10.255.0.1" {
		t.Fatalf("wan network: %+v", low.Networks["wan"])
	}
	// dc1 servers at .10-.12, dc2's first server at .30.
	for node, ip := range map[string]string{"dc1-server-1": "10.255.0.10", "dc1-server-3": "10.255.0.12", "dc2-server-1": "10.255.0.30"} {
		if got := low.Nodes[node].Networks["wan"]; got == nil || got.IP != ip {
			t.Errorf("%s wan attachment: %+v, want %s", node, got, ip)
		}
	}
	if _, ok := low.Nodes["dc1-client"].Networks["wan"]; ok {
		t.Error("clients must not join the wan network")
	}
	cfg := agentConfig(t, low.Nodes["dc2-server-1"])
	if cfg["primary_datacenter"] != "dc1" || cfg["datacenter"] != "dc2" {
		t.Errorf("dcs: %v / %v", cfg["primary_datacenter"], cfg["datacenter"])
	}
	if cfg["bind_addr"] != "0.0.0.0" || cfg["serf_lan"] != "10.2.0.10" || cfg["serf_wan"] != "10.255.0.30" || cfg["advertise_addr_wan"] != "10.255.0.30" {
		t.Errorf("addresses: %v", cfg)
	}
	if !reflect.DeepEqual(cfg["retry_join_wan"], []any{"10.255.0.10", "10.255.0.11", "10.255.0.12"}) {
		t.Errorf("retry_join_wan: %v", cfg["retry_join_wan"])
	}
	if r := low.Nodes["dc1-server-2"].Processes["consul"].ReadyWhen.Consul; !r.Leader || r.WANMembers != ">=4" {
		t.Errorf("server readiness: %+v", r)
	}
	// Expanded output is valid input and expands to itself.
	b, _ := yaml.Marshal(low)
	p := filepath.Join(t.TempDir(), "low.yaml")
	os.WriteFile(p, b, 0o644)
	again, err := Expand(load(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Nodes["dc2-server-1"], low.Nodes["dc2-server-1"]) {
		t.Error("round trip changed dc2-server-1")
	}
}

func TestWANSubnetOverride(t *testing.T) {
	f := load(t, "../../examples/federation.yaml")
	f.Networks = map[string]*spec.Network{"wan": {Subnet: "192.168.250.0/24"}}
	low, err := Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	if got := low.Nodes["dc2-server-1"].Networks["wan"].IP; got != "192.168.250.30" {
		t.Errorf("wan ip %s", got)
	}
	if low.Networks["wan"].Subnet != "192.168.250.0/24" {
		t.Errorf("subnet %s", low.Networks["wan"].Subnet)
	}
}
