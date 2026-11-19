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
