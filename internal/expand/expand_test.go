package expand

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/Ranjandas/parker/internal/selector"
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
	if cfg["retry_interval"] != RetryInterval {
		t.Errorf("retry_interval: %v", cfg["retry_interval"])
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

// zeros makes generated secrets stable.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestACL(t *testing.T) {
	Rand = zeros{}
	defer func() { Rand = rand.Reader }()
	low, err := Expand(load(t, "../../examples/acl.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const mgmt = "00000000-0000-4000-8000-000000000000"
	srv := agentConfig(t, low.Nodes["dc1-server-1"])
	acl, _ := srv["acl"].(map[string]any)
	if acl["enabled"] != true || acl["default_policy"] != "deny" {
		t.Errorf("server acl: %v", acl)
	}
	tokens, _ := acl["tokens"].(map[string]any)
	if tokens["initial_management"] != mgmt || tokens["agent"] != mgmt {
		t.Errorf("server tokens: %v", tokens)
	}
	if srv["encrypt"] != base64.StdEncoding.EncodeToString(make([]byte, 32)) {
		t.Errorf("encrypt: %v", srv["encrypt"])
	}
	cli := agentConfig(t, low.Nodes["dc1-web"])
	ctokens := cli["acl"].(map[string]any)["tokens"].(map[string]any)
	if _, ok := ctokens["initial_management"]; ok {
		t.Error("clients must not get the management token")
	}
	if cli["encrypt"] != srv["encrypt"] {
		t.Error("clients need the gossip key")
	}
	if reg := cli["services"].([]any)[0].(map[string]any); reg["token"] != mgmt {
		t.Errorf("service registration token: %v", reg)
	}

	setup := low.Nodes["dc1-server-1"].Processes[ACLSetup]
	if setup == nil || setup.Env["CONSUL_HTTP_TOKEN"] != mgmt || setup.ReadyWhen.Log != ACLReadyLine {
		t.Fatalf("acl-setup: %+v", setup)
	}
	if len(setup.DependsOn) != 3 {
		t.Errorf("acl-setup deps: %v", setup.DependsOn)
	}
	if _, ok := low.Nodes["dc1-server-2"].Processes[ACLSetup]; ok {
		t.Error("acl-setup runs once per primary dc")
	}
	if d := low.Nodes["dc1-web"].Processes["consul"].DependsOn["dc1-server-1/"+ACLSetup]; d == nil || d.Condition != "ready" {
		t.Error("clients wait for acl-setup")
	}
	for _, ref := range []struct{ node, proc string }{{"dc1-server-2", "consul"}, {"dc1-web", "consul"}, {"dc1-web", "web"}} {
		if r := low.Nodes[ref.node].Processes[ref.proc].ReadyWhen.Consul; r == nil || r.Token != mgmt {
			t.Errorf("%s/%s readiness token: %+v", ref.node, ref.proc, r)
		}
	}
}

func TestACLOffByDefault(t *testing.T) {
	low, err := Expand(load(t, "../../examples/minimal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, n := range low.Nodes {
		if _, ok := n.Processes[ACLSetup]; ok {
			t.Errorf("%s has acl-setup", name)
		}
		if cfg := agentConfig(t, n); cfg["acl"] != nil || cfg["encrypt"] != nil {
			t.Errorf("%s: acl %v encrypt %v", name, cfg["acl"], cfg["encrypt"])
		}
	}
}

func TestACLFederated(t *testing.T) {
	f := load(t, "../../examples/federation.yaml")
	on := true
	f.Consul.Defaults = &spec.Inheritable{ACL: &on}
	low, err := Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := low.Nodes["dc2-server-1"].Processes[ACLSetup]; ok {
		t.Error("secondary dc runs acl-setup")
	}
	sec := agentConfig(t, low.Nodes["dc2-server-1"])["acl"].(map[string]any)
	if sec["enable_token_replication"] != true || sec["tokens"].(map[string]any)["replication"] == nil {
		t.Errorf("secondary acl: %v", sec)
	}
	if d := low.Nodes["dc2-api"].Processes["consul"].DependsOn["dc1-server-1/"+ACLSetup]; d == nil {
		t.Error("dc2 clients wait for the primary's acl-setup")
	}

	off := false
	f = load(t, "../../examples/federation.yaml")
	f.Consul.Defaults = &spec.Inheritable{ACL: &on}
	f.Consul.Datacenters["dc2"].ACL = &off
	if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), "all have acl on or all off") {
		t.Errorf("mixed acl: %v", err)
	}
}

func TestPrimaryPerDCWithoutFederation(t *testing.T) {
	f := load(t, "../../examples/federation.yaml")
	f.Consul.Federation = nil
	low, err := Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	if p := agentConfig(t, low.Nodes["dc2-server-1"])["primary_datacenter"]; p != "dc2" {
		t.Errorf("primary_datacenter %v", p)
	}
}

func TestTLS(t *testing.T) {
	low, err := Expand(load(t, "../../examples/tls.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	srv := low.Nodes["dc1-server-2"]
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(srv.Files[CAFile])) {
		t.Fatal("no CA in server files")
	}
	block, _ := pem.Decode([]byte(srv.Files[CertFile]))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"server.dc1.consul", "localhost", "server-2.dc1.tls.parker", "dc1-server-2.tls.parker"} {
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
			t.Errorf("verify %s: %v", name, err)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.1.0.11"} {
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: ip}); err != nil {
			t.Errorf("verify %s: %v", ip, err)
		}
	}
	if _, err := tlsKeyPair(srv); err != nil {
		t.Errorf("server key pair: %v", err)
	}
	if low.Nodes["dc1-server-1"].Files[CAFile] != srv.Files[CAFile] {
		t.Error("one CA per lab")
	}

	cfg := agentConfig(t, srv)
	if p := cfg["ports"].(map[string]any); p["http"] != -1.0 || p["https"] != float64(HTTPSPort) {
		t.Errorf("server ports: %v", p)
	}
	if cfg["auto_encrypt"].(map[string]any)["allow_tls"] != true {
		t.Error("servers allow auto_encrypt")
	}
	if cfg["connect"].(map[string]any)["ca_config"].(map[string]any)["root_cert"] != srv.Files[CAFile] {
		t.Error("lab CA is the Connect root")
	}
	if r := srv.Processes["consul"].ReadyWhen.Consul; r.CAFile != CAFile {
		t.Errorf("server check: %+v", r)
	}

	web := low.Nodes["dc1-web"]
	if _, ok := web.Files[CertKeyFile]; ok {
		t.Error("clients get their certificate through auto_encrypt, not files")
	}
	ccfg := agentConfig(t, web)
	if ccfg["auto_encrypt"].(map[string]any)["tls"] != true {
		t.Error("client auto_encrypt")
	}
	for _, p := range []string{"consul", "web"} {
		if r := web.Processes[p].ReadyWhen.Consul; r == nil || r.CAFile != CAFile || r.Token == "" {
			t.Errorf("%s check: %+v", p, r)
		}
	}
	env := low.Nodes["dc1-server-1"].Processes[ACLSetup].Env
	if env["CONSUL_HTTP_ADDR"] != "https://127.0.0.1:8501" || env["CONSUL_CACERT"] != VarNodeDir+"/"+CAFile {
		t.Errorf("acl-setup env: %v", env)
	}
}

func tlsKeyPair(n *spec.RawNode) (tls.Certificate, error) {
	return tls.X509KeyPair([]byte(n.Files[CertFile]), []byte(n.Files[CertKeyFile]))
}

func TestTLSFederated(t *testing.T) {
	on, off := true, false
	f := load(t, "../../examples/federation.yaml")
	f.Consul.Defaults = &spec.Inheritable{TLS: &on}
	low, err := Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(low.Nodes["dc2-server-1"].Files[CertFile]))
	cert, _ := x509.ParseCertificate(block.Bytes)
	if !slices.ContainsFunc(cert.IPAddresses, func(ip net.IP) bool { return ip.String() == "10.255.0.30" }) {
		t.Errorf("wan IP missing from SANs: %v", cert.IPAddresses)
	}
	if !slices.Contains(cert.DNSNames, "server.dc2.consul") {
		t.Errorf("SANs: %v", cert.DNSNames)
	}

	f = load(t, "../../examples/federation.yaml")
	f.Consul.Defaults = &spec.Inheritable{TLS: &on}
	f.Consul.Datacenters["dc2"].TLS = &off
	if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), "all have tls on or all off") {
		t.Errorf("mixed tls: %v", err)
	}
}

func TestConfigEntries(t *testing.T) {
	f := load(t, "../../examples/config-entries.yaml")
	on := true
	f.Consul.Defaults = &spec.Inheritable{ACL: &on, TLS: &on}
	low, err := Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	srv := low.Nodes["dc1-server-1"]
	p := srv.Processes[ConfigEntries]
	if p == nil || p.ReadyWhen.Log != ConfigEntriesReadyLine {
		t.Fatalf("config-entries process: %+v", p)
	}
	if p.DependsOn["dc1-server-1/consul"] == nil || p.DependsOn["dc1-server-1/"+ACLSetup] == nil {
		t.Errorf("deps: %v", p.DependsOn)
	}
	if p.Env["CONSUL_HTTP_TOKEN"] == "" || p.Env["CONSUL_CACERT"] == "" {
		t.Errorf("env: %v", p.Env)
	}
	var refs []EntryRef
	if err := json.Unmarshal([]byte(srv.Files[ConfigEntriesManifest]), &refs); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 || refs[0].File != VarNodeDir+"/config-entry-0.json" || refs[2].File != VarNodeDir+"/config-entry-2.hcl" {
		t.Fatalf("manifest: %+v", refs)
	}
	if !strings.Contains(refs[2].Label, "config_entries[2] (line ") || !strings.Contains(refs[2].Label, "web-intentions.hcl") {
		t.Errorf("label %q", refs[2].Label)
	}
	var e0 map[string]any
	json.Unmarshal([]byte(srv.Files["config-entry-0.json"]), &e0)
	if e0["Kind"] != "proxy-defaults" || e0["Config"].(map[string]any)["protocol"] != "http" {
		t.Errorf("entry 0: %v", e0)
	}
	if !strings.Contains(srv.Files["config-entry-2.hcl"], `Kind = "service-intentions"`) {
		t.Errorf("hcl entry: %q", srv.Files["config-entry-2.hcl"])
	}
	if _, ok := low.Nodes["dc1-web"].Processes[ConfigEntries]; ok {
		t.Error("config entries run on the first server only")
	}
}

func TestNodeLabels(t *testing.T) {
	low, err := Expand(load(t, "../../examples/clients.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for node, want := range map[string]spec.NodeLabels{
		"dc1-server-2": {DC: "dc1", Name: "server-2", Role: "server"},
		"dc1-web-2":    {DC: "dc1", Name: "web-2", Role: "client", Group: "web", Services: []string{"web"}},
		"dc1-client":   {DC: "dc1", Name: "client", Role: "client", Group: "client"},
	} {
		if got := low.Nodes[node].Labels; got == nil || !reflect.DeepEqual(*got, want) {
			t.Errorf("%s labels %+v, want %+v", node, got, want)
		}
	}
}

func TestScenarioCarriedAndChecked(t *testing.T) {
	low, err := Expand(load(t, "../../examples/scenario.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if low.Scenario == nil || len(low.Scenario.Steps) != 6 || low.Scenarios["wrong-peers"] == nil {
		t.Fatalf("scenarios not carried: %+v", low.Scenario)
	}
	if got := low.Scenario.Steps[1].Exec.Run; !reflect.DeepEqual(got, spec.Script{"sh", "-c", "consul operator raft list-peers"}) {
		t.Errorf("exec run %q", got)
	}
	// The saved topology reads back the same (Script marshals as a string).
	b, _ := yaml.Marshal(low)
	if !strings.Contains(string(b), `run: consul operator raft list-peers`) {
		t.Errorf("script not marshalled as a string:\n%s", b)
	}
	p := filepath.Join(t.TempDir(), "low.yaml")
	os.WriteFile(p, b, 0o644)
	again, err := Expand(load(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Scenario, low.Scenario) {
		t.Error("scenario changed in the round trip")
	}

	f := load(t, "../../examples/scenario.yaml")
	f.Scenario.Steps[1].Exec.On = "dc1/nope"
	f.Scenarios["wrong-peers"].Steps[0].Assert.Within = "soon"
	_, err = Expand(f)
	if err == nil || !strings.Contains(err.Error(), `scenario: selector "dc1/nope" matches no nodes`) ||
		!strings.Contains(err.Error(), "scenarios.wrong-peers: step 1: assert.within") {
		t.Errorf("err %v", err)
	}
}

func TestFaultScenarioCarriedAndChecked(t *testing.T) {
	low, err := Expand(load(t, "../../examples/faults.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "low.yaml")
	b, err := yaml.Marshal(low)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := Expand(load(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Scenario, low.Scenario) {
		t.Error("fault scenario changed in the round trip")
	}

	// Fault selectors are checked up front (raft roles wait for the lab).
	f := load(t, "../../examples/faults.yaml")
	f.Scenario.Steps[2].Partition = []spec.NodeGroup{{"dc1/server-1", "dc9"}}
	f.Scenario.Steps[7].Kill = "dc1/followers"
	f.Scenario.Steps[10].Start = "dc1/nope"
	_, err = Expand(f)
	if err == nil || !strings.Contains(err.Error(), `"dc9" matches no nodes`) || !strings.Contains(err.Error(), `"dc1/nope" matches no nodes`) ||
		strings.Contains(err.Error(), "followers") {
		t.Errorf("err %v", err)
	}
}

func TestUpgradeNeedsPinnedBinary(t *testing.T) {
	if _, err := Expand(load(t, "../../examples/upgrade.yaml")); err != nil {
		t.Fatal(err)
	}
	f := load(t, "../../examples/upgrade.yaml")
	f.Scenario.Steps[2].Upgrade.Binary = "nomad"
	if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), "upgrade: nomad is not pinned under versions:") {
		t.Errorf("err = %v", err)
	}
}

func TestPartitionsAndNamespaces(t *testing.T) {
	low, err := Expand(load(t, "../../examples/partitions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// billing sits in team-a and team-b under the same name; frontend
	// stays in the default partition.
	b1 := low.Nodes["dc1-team-a-billing-1"]
	if b1 == nil || b1.Labels.Partition != "team-a" || b1.Labels.Name != "billing-1" || !slices.Equal(b1.Labels.Namespaces, []string{"payments"}) {
		t.Fatalf("dc1-team-a-billing-1: %+v", b1)
	}
	bb := low.Nodes["dc1-team-b-billing"]
	if bb == nil || bb.Labels.Partition != "team-b" || bb.Labels.Name != "billing" || agentConfig(t, bb)["node_name"] != "billing" {
		t.Fatalf("dc1-team-b-billing: %+v", bb)
	}
	cfg := agentConfig(t, b1)
	if cfg["partition"] != "team-a" {
		t.Errorf("billing partition: %v", cfg["partition"])
	}
	if cfg["retry_interval"] != RetryInterval {
		t.Errorf("client retry_interval: %v", cfg["retry_interval"])
	}
	fe := agentConfig(t, low.Nodes["dc1-frontend"])
	if _, ok := fe["partition"]; ok {
		t.Errorf("frontend has a partition: %v", fe["partition"])
	}
	// team-a's agents carry their own partition's token, not the default one.
	tok := cfg["acl"].(map[string]any)["tokens"].(map[string]any)["agent"]
	feTok := fe["acl"].(map[string]any)["tokens"].(map[string]any)["agent"]
	if tok == feTok {
		t.Error("team-a agents use the default partition's token")
	}
	// The service registers in its namespace, its health check and sidecar
	// look there too.
	reg := cfg["services"].([]any)[0].(map[string]any)
	if reg["namespace"] != "payments" || reg["token"] != tok {
		t.Errorf("billing registration: %v", reg)
	}
	sc := b1.Processes["billing"+SidecarSuffix]
	if sc.Env["CONSUL_PARTITION"] != "team-a" || sc.Env["CONSUL_NAMESPACE"] != "payments" {
		t.Errorf("sidecar env: %v", sc.Env)
	}
	if b1.Processes["billing"] == nil {
		t.Error("billing's service_defaults run: gave no process")
	}
	// Each partition has its own mesh gateway, all named mesh.
	gw := low.Nodes["dc1-team-a-mesh"]
	if gw == nil || gw.Labels.Partition != "team-a" || agentConfig(t, gw)["partition"] != "team-a" ||
		gw.Processes["mesh-gateway"].Env["CONSUL_PARTITION"] != "team-a" {
		t.Fatalf("dc1-team-a-mesh: %+v", gw)
	}
	if low.Nodes["dc1-mesh"] == nil || low.Nodes["dc1-team-b-mesh"] == nil {
		t.Error("default or team-b mesh gateway missing")
	}
	tokB := agentConfig(t, bb)["acl"].(map[string]any)["tokens"].(map[string]any)["agent"]

	// The tenancy job creates team-a and both payments namespaces, and the
	// token team-a's agents use, before any client starts.
	s1 := low.Nodes["dc1-server-1"]
	var ten TenancySpec
	if err := json.Unmarshal([]byte(s1.Files[TenancyManifest]), &ten); err != nil {
		t.Fatal(err)
	}
	want := TenancySpec{
		Partitions:  []string{"team-a", "team-b"},
		Namespaces:  []NamespaceRef{{Partition: "team-a", Name: "payments"}, {Partition: "team-b", Name: "payments"}},
		AgentTokens: []AgentToken{{Partition: "team-a", SecretID: tok.(string)}, {Partition: "team-b", SecretID: tokB.(string)}},
	}
	if !reflect.DeepEqual(ten, want) {
		t.Errorf("tenancy %+v\nwant %+v", ten, want)
	}
	p := s1.Processes[Tenancy]
	if p == nil || p.DependsOn["dc1-server-1/"+ACLSetup] == nil {
		t.Fatalf("tenancy process: %+v", p)
	}
	for _, n := range []string{"dc1-team-a-billing-1", "dc1-team-b-billing", "dc1-frontend", "dc1-mesh"} {
		if d := low.Nodes[n].Processes["consul"].DependsOn["dc1-server-1/"+Tenancy]; d == nil || d.Condition != "ready" {
			t.Errorf("%s doesn't wait for tenancy", n)
		}
	}
	if !slices.Contains(s1.Processes[ACLSetup].Run, "--enterprise") {
		t.Errorf("acl-setup: %v", s1.Processes[ACLSetup].Run)
	}
	// team-a's config entries are written into team-a, after tenancy.
	var refs []EntryRef
	if err := json.Unmarshal([]byte(s1.Files[ConfigEntriesManifest]), &refs); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 8 || refs[1].Partition != "team-a" || !strings.Contains(refs[1].Label, "partitions.team-a.config_entries[0]") ||
		refs[7].Partition != "team-b" {
		t.Errorf("entries: %+v", refs)
	}
	if s1.Processes[ConfigEntries].DependsOn["dc1-server-1/"+Tenancy] == nil {
		t.Error("config entries don't wait for tenancy")
	}
	// Selectors see partitions and namespaces.
	for sel, want := range map[string]string{
		"part:team-a":          "dc1-team-a-billing-1 dc1-team-a-billing-2 dc1-team-a-mesh",
		"dc1/billing":          "dc1-team-a-billing-1 dc1-team-a-billing-2 dc1-team-b-billing",
		"dc1/team-b/billing":   "dc1-team-b-billing",
		"dc1/team-a/billing-2": "dc1-team-a-billing-2",
		"dc1/default/frontend": "dc1-frontend",
	} {
		if got, err := selector.Resolve(low, sel, nil); err != nil || strings.Join(got, " ") != want {
			t.Errorf("%s = %v %v, want %s", sel, got, err, want)
		}
	}
}

func TestPartitionChecks(t *testing.T) {
	f := load(t, "../../examples/partitions.yaml")
	dc := f.Consul.Datacenters["dc1"]
	dc.Partitions["team-a"].Gateways = map[string]*spec.Gateway{"billing": {Kind: "mesh"}}
	if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), "partitions.team-a.gateways.billing: a node is already named billing") {
		t.Errorf("gateway named like a node: %v", err)
	}
	f = load(t, "../../examples/partitions.yaml")
	dc = f.Consul.Datacenters["dc1"]
	dc.Partitions["default"].Nodes["team-b-billing"] = &spec.Node{}
	if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), "two nodes expand to dc1-team-b-billing") {
		t.Errorf("expanded name clash: %v", err)
	}
}
