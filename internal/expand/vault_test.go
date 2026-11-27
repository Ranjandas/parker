package expand

import (
	"encoding/json"
	"strconv"
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
	if len(m.Addrs) != 3 || m.Addrs[0] != "https://10.64.0.10:8200" || m.RootToken == "" || m.CAFile == "" {
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

// TLS: every east server serves a lab-CA certificate for its names and
// IP, joins over HTTPS trusting the CA, and is checked over HTTPS; west
// stays plain HTTP.
func TestVaultTLS(t *testing.T) {
	low, err := Expand(load(t, "../../examples/vault.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	n := low.Nodes["east-vault-2"]
	cert, err := parseCertPEM(n.Files[CertFile])
	if err != nil {
		t.Fatal(err)
	}
	ca, err := parseCertPEM(n.Files[CAFile])
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.CheckSignatureFrom(ca); err != nil {
		t.Error(err)
	}
	if err := cert.VerifyHostname("10.64.0.11"); err != nil {
		t.Error(err)
	}
	if err := cert.VerifyHostname("vault-2.east.vault.parker"); err != nil {
		t.Error(err)
	}
	cfg := vaultConfigOf(t, n.Files)
	tcp := cfg["listener"].(map[string]any)["tcp"].(map[string]any)
	if tcp["tls_disable"] != false || tcp["tls_cert_file"] == nil || tcp["tls_key_file"] == nil {
		t.Errorf("listener %v", tcp)
	}
	for _, j := range cfg["storage"].(map[string]any)["raft"].(map[string]any)["retry_join"].([]any) {
		j := j.(map[string]any)
		if !strings.HasPrefix(j["leader_api_addr"].(string), "https://") || j["leader_ca_cert_file"] == nil {
			t.Errorf("retry_join %v", j)
		}
	}
	if r := n.Processes["vault"].ReadyWhen.HTTP; !strings.HasPrefix(r.URL, "https://") || r.CAFile != CAFile {
		t.Errorf("ready %+v", r)
	}
	w := low.Nodes["west-vault-1"]
	if _, ok := w.Files[CertFile]; ok || !strings.HasPrefix(w.Processes["vault"].ReadyWhen.HTTP.URL, "http://") {
		t.Error("west has TLS")
	}
}

// One lab CA: Consul's TLS and Vault's chain to the same root, and moving
// the lab re-signs the Vault certificates too.
func TestVaultTLSSharesCA(t *testing.T) {
	low, err := Expand(loadText(t, `parker: v1alpha1
versions: { consul: 1.22.7, vault: 1.21.1 }
consul:
  defaults: { tls: true }
  datacenters:
    dc1: { servers: 1 }
vault:
  defaults: { tls: true }
  clusters:
    east: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	if low.Nodes["dc1-server-1"].Files[CAFile] != low.Nodes["east-vault-1"].Files[CAFile] {
		t.Error("Consul and Vault have different CAs")
	}
	fix := func(ip string) string { return strings.Replace(ip, "10.64.0.", "10.99.0.", 1) }
	if err := ReissueServerCerts(low, fix); err != nil {
		t.Fatal(err)
	}
	cert, _ := parseCertPEM(low.Nodes["east-vault-1"].Files[CertFile])
	if err := cert.VerifyHostname("10.99.0.10"); err != nil {
		t.Error(err)
	}
}

// Consul storage: each Vault server is also a client agent of the
// storage dc, placed after the dc's own nodes, and keeps its data through
// it with the agent's token and TLS.
func TestVaultConsulStorage(t *testing.T) {
	low, err := Expand(loadText(t, `parker: v1alpha1
versions: { consul: 1.22.7, vault: 1.3.10 }
consul:
  datacenters:
    dc1:
      servers: 1
      acl: true
      tls: true
      nodes: { web: {} }
vault:
  clusters:
    east: { servers: 2, storage: { consul: dc1 } }
`))
	if err != nil {
		t.Fatal(err)
	}
	if low.Nodes["dc1-web"].Networks["dc1-lan"].IP != "10.1.0.100" {
		t.Error("the Vault agents moved the dc's own node")
	}
	if _, ok := low.Nodes["dc1-east-vault-1"]; ok {
		t.Error("the agent kept its Consul node key")
	}
	n := low.Nodes["east-vault-2"]
	if n.Labels.Product != ProductVault || n.Labels.DC != "east" {
		t.Errorf("labels %+v", n.Labels)
	}
	if n.Networks["dc1-lan"] == nil || n.Networks["east-lan"] == nil || n.Processes["consul"] == nil {
		t.Fatalf("not an agent of dc1: %+v", n.Networks)
	}
	if d := n.Processes["vault"].DependsOn["east-vault-2/consul"]; d == nil || d.Condition != "ready" {
		t.Error("vault doesn't wait for its agent")
	}
	c := vaultConfigOf(t, n.Files)["storage"].(map[string]any)["consul"].(map[string]any)
	if c["address"] != "127.0.0.1:8501" || c["scheme"] != "https" || c["token"] == nil || c["path"] != "vault-east/" {
		t.Errorf("storage %v", c)
	}
}

// Vault as the Connect CA: dc1's servers join east's LAN from .200, set
// the vault CA provider, start after vault-init, and with TLS on the lab
// CA becomes the root in Vault's PKI.
func TestVaultConnectCA(t *testing.T) {
	low, err := Expand(loadText(t, `parker: v1alpha1
versions: { consul: 1.22.7, vault: 1.21.1 }
vault:
  clusters:
    east: { servers: 3, tls: true }
consul:
  datacenters:
    dc1: { servers: 2, tls: true, connect: { ca: { vault: east } } }
`))
	if err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"dc1-server-1", "dc1-server-2"} {
		n := low.Nodes[key]
		if a := n.Networks["east-lan"]; a == nil || a.IP != "10.64.0."+strconv.Itoa(200+i) {
			t.Errorf("%s east-lan %+v", key, a)
		}
		var cfg map[string]any
		if err := json.Unmarshal([]byte(n.Files["consul.json"]), &cfg); err != nil {
			t.Fatal(err)
		}
		conn := cfg["connect"].(map[string]any)
		cc := conn["ca_config"].(map[string]any)
		if conn["ca_provider"] != "vault" || conn["enabled"] != true || cc["address"] != "https://10.64.0.10:8200" ||
			cc["token"] == "" || cc["intermediate_pki_path"] != "connect-dc1-intermediate/" || cc["ca_file"] == nil || cc["private_key"] != nil {
			t.Errorf("%s connect %v", key, conn)
		}
		if n.Files[VaultCAFile] == "" || n.Files[CAKeyFile] == "" {
			t.Errorf("%s: missing the Vault CA or the lab CA key", key)
		}
		if d := n.Processes["consul"].DependsOn["east-vault-1/"+VaultInit]; d == nil || d.Condition != "ready" {
			t.Errorf("%s doesn't wait for vault-init", key)
		}
	}
	var is VaultInitSpec
	if err := json.Unmarshal([]byte(low.Nodes["east-vault-1"].Files[VaultInitManifest]), &is); err != nil {
		t.Fatal(err)
	}
	if len(is.PKIRoots) != 1 || is.PKIRoots[0].Path != "connect-root/" || !strings.Contains(is.PKIRoots[0].Bundle, "PRIVATE KEY") {
		t.Errorf("pki roots %+v", is.PKIRoots)
	}
	// Moving the lab re-signs the servers' certificates with the kept key.
	fix := func(ip string) string { return strings.Replace(ip, "10.1.0.", "10.98.0.", 1) }
	if err := ReissueServerCerts(low, fix); err != nil {
		t.Fatal(err)
	}

	// A dev server can't hold the lab CA.
	if _, err := Expand(loadText(t, `parker: v1alpha1
versions: { consul: 1.22.7, vault: 1.21.1 }
vault:
  clusters:
    east: { dev: true }
consul:
  datacenters:
    dc1: { servers: 1, tls: true, connect: { ca: { vault: east } } }
`)); err == nil || !strings.Contains(err.Error(), "dev-mode") {
		t.Errorf("dev with tls: %v", err)
	}
}

// Transit auto-unseal: the transit cluster's vault-init makes the key,
// and each server joins its LAN and names it in its seal stanza.
func TestVaultTransit(t *testing.T) {
	low, err := Expand(load(t, "../../examples/vault-transit.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var is, own VaultInitSpec
	if err := json.Unmarshal([]byte(low.Nodes["unsealer-vault-1"].Files[VaultInitManifest]), &is); err != nil {
		t.Fatal(err)
	}
	if strings.Join(is.TransitKeys, ",") != "autounseal-east" || is.Recovery {
		t.Errorf("unsealer init %+v", is)
	}
	if err := json.Unmarshal([]byte(low.Nodes["east-vault-1"].Files[VaultInitManifest]), &own); err != nil {
		t.Fatal(err)
	}
	if !own.Recovery {
		t.Error("east doesn't init with a recovery key")
	}
	for i := range 3 {
		key := "east-vault-" + strconv.Itoa(i+1)
		n := low.Nodes[key]
		if a := n.Networks["unsealer-lan"]; a == nil || a.IP != "10.65.0."+strconv.Itoa(150+i) {
			t.Errorf("%s unsealer-lan %+v", key, a)
		}
		tr := vaultConfigOf(t, n.Files)["seal"].(map[string]any)["transit"].(map[string]any)
		if tr["address"] != "http://10.65.0.10:8200" || tr["key_name"] != "autounseal-east" || tr["token"] == "" {
			t.Errorf("%s transit %v", key, tr)
		}
		if d := n.Processes["vault"].DependsOn["unsealer-vault-1/"+VaultInit]; d == nil || d.Condition != "ready" {
			t.Errorf("%s doesn't wait for the unsealer", key)
		}
	}
}

// The Consul secrets engine: vault-init configures it with the dc's
// management token; it needs ACLs.
func TestVaultConsulSecrets(t *testing.T) {
	low, err := Expand(load(t, "../../examples/vault-consul-secrets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	n := low.Nodes["east-vault-1"]
	var is VaultInitSpec
	if err := json.Unmarshal([]byte(n.Files[VaultInitManifest]), &is); err != nil {
		t.Fatal(err)
	}
	e := is.ConsulEngine
	if e == nil || e.Address != "10.1.0.10:8500" || e.Token == "" || e.Role != "management" || e.PolicyField != "consul_policies" {
		t.Errorf("engine %+v", e)
	}
	// It doesn't wait for dc1, which may wait for it (connect.ca.vault).
	if d := n.Processes[VaultInit].DependsOn["dc1-server-1/consul"]; d != nil {
		t.Error("vault-init waits for dc1")
	}
	if a := n.Networks["dc1-lan"]; a == nil || a.IP != "10.1.0.200" {
		t.Errorf("dc1-lan %+v", a)
	}
	if _, err := Expand(loadText(t, `parker: v1alpha1
versions: { consul: 1.22.7, vault: 1.21.1 }
consul:
  datacenters:
    dc1: { servers: 1 }
vault:
  clusters:
    east: { secrets: { consul: dc1 } }
`)); err == nil || !strings.Contains(err.Error(), "turn on acl: true for dc1") {
		t.Errorf("no acl: %v", err)
	}
}

// register: each server gets a client agent of the dc and registers
// through it.
func TestVaultRegister(t *testing.T) {
	low, err := Expand(load(t, "../../examples/vault-register.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		key := "east-vault-" + strconv.Itoa(i+1)
		n := low.Nodes[key]
		if n.Processes["consul"] == nil || n.Networks["dc1-lan"] == nil {
			t.Errorf("%s has no dc1 agent", key)
		}
		sr, _ := vaultConfigOf(t, n.Files)["service_registration"].(map[string]any)
		if c, _ := sr["consul"].(map[string]any); c["address"] != "127.0.0.1:8500" {
			t.Errorf("%s service_registration %v", key, sr)
		}
		if _, ok := vaultConfigOf(t, n.Files)["storage"].(map[string]any)["raft"]; !ok {
			t.Errorf("%s isn't on raft", key)
		}
	}
}
