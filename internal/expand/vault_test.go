package expand

import (
	"encoding/json"
	"strings"
	"testing"
)

func vaultConfigOf(t *testing.T, files map[string]string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(files[VaultConfigFile]), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestVaultCluster(t *testing.T) {
	low, err := Expand(load(t, "../../examples/vault.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if n := low.Networks["east-lan"]; n == nil || n.Subnet != "10.64.0.0/24" || !n.Auto {
		t.Fatalf("east-lan: %+v", n)
	}
	for i, key := range []string{"east-vault-1", "east-vault-2", "east-vault-3"} {
		n := low.Nodes[key]
		if n == nil {
			t.Fatalf("no %s", key)
		}
		l := n.Labels
		if l.Product != ProductVault || l.DC != "east" || l.Role != "server" || l.Group != "vault" || l.Name != "vault-"+string(rune('1'+i)) {
			t.Errorf("%s labels %+v", key, l)
		}
		cfg := vaultConfigOf(t, n.Files)
		raft := cfg["storage"].(map[string]any)["raft"].(map[string]any)
		if raft["node_id"] != l.Name || len(raft["retry_join"].([]any)) != 2 {
			t.Errorf("%s raft %v", key, raft)
		}
		if cfg["disable_mlock"] != true || cfg["ui"] != true {
			t.Errorf("%s config %v", key, cfg)
		}
		if p := n.Processes["vault"]; p == nil || p.Run[0] != "vault" || p.ReadyWhen.HTTP == nil {
			t.Errorf("%s vault process %+v", key, p)
		}
		_, hasInit := n.Processes[VaultInit]
		if hasInit != (i == 0) {
			t.Errorf("%s: vault-init %v", key, hasInit)
		}
	}
	init := low.Nodes["east-vault-1"].Processes[VaultInit]
	if len(init.DependsOn) != 3 || init.ReadyWhen.Log != VaultReadyLine {
		t.Errorf("vault-init %+v", init)
	}
	var m VaultInitSpec
	if err := json.Unmarshal([]byte(low.Nodes["east-vault-1"].Files[VaultInitManifest]), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Addrs) != 3 || m.Addrs[0] != "http://10.64.0.10:8200" || m.RootToken == "" {
		t.Errorf("manifest %+v", m)
	}
}

func TestVaultDevAndFile(t *testing.T) {
	low, err := Expand(loadText(t, `parker: v1alpha1
versions: { vault: 1.3.10 }
vault:
  defaults: { log_level: debug, config: { default_lease_ttl: 1h } }
  clusters:
    dev: { dev: true }
    old: { storage: file, subnet: 192.168.9.0/24, meta: { region: us-east } }
`))
	if err != nil {
		t.Fatal(err)
	}
	dev := low.Nodes["dev-vault-1"]
	run := strings.Join(dev.Processes["vault"].Run, " ")
	if !strings.Contains(run, "-dev ") || !strings.Contains(run, "-dev-root-token-id=") || !strings.Contains(run, "-log-level=debug") {
		t.Errorf("dev run %q", run)
	}
	if _, ok := dev.Processes[VaultInit]; ok {
		t.Error("a dev server needs no vault-init")
	}
	old := low.Nodes["old-vault-1"]
	cfg := vaultConfigOf(t, old.Files)
	if _, ok := cfg["storage"].(map[string]any)["file"]; !ok || cfg["default_lease_ttl"] != "1h" || cfg["log_level"] != "debug" {
		t.Errorf("old config %v", cfg)
	}
	if old.Networks["old-lan"].IP != "192.168.9.10" || old.Labels.Meta["region"] != "us-east" {
		t.Errorf("old node %+v %+v", old.Networks, old.Labels)
	}
}

func TestVaultBesideConsul(t *testing.T) {
	low, err := Expand(loadText(t, `parker: v1alpha1
versions: { consul: 1.22.7, vault: 1.21.1 }
consul:
  datacenters:
    dc1: { servers: 1 }
vault:
  clusters:
    east: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	if low.Nodes["dc1-server-1"].Labels.Product != ProductConsul || low.Nodes["east-vault-1"].Labels.Product != ProductVault {
		t.Error("product labels")
	}
	if low.Networks["dc1-lan"].Subnet == low.Networks["east-lan"].Subnet {
		t.Error("the dc and the cluster share a subnet")
	}
}
