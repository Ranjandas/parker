package expand

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/Ranjandas/parker/internal/spec"
)

// ProductVault is Vault's product name: its lab file key, versions: key
// and the product label of its nodes.
const ProductVault = "vault"

// Vault ports and files.
const (
	VaultPort        = 8200
	VaultClusterPort = 8201
	VaultConfigFile  = "vault.json"
	// VaultInit is the one-shot process on a cluster's first server that
	// initialises the cluster, unseals every server and creates the lab's
	// root token.
	VaultInit         = "vault-init"
	VaultInitManifest = "vault-init.json"
	VaultInitKeys     = "vault-keys.json" // unseal keys, in the first server's dir
	// VaultInitDone records the supervision run vault-init finished in.
	VaultInitDone = "vault-init.run"
	// VaultReadyLine is what vault-init logs once every server is unsealed.
	VaultReadyLine = "parker: vault initialised and unsealed"
	// vaultSubnetBase: cluster LANs default to 10.<base+i>.0.0/24, clear
	// of the Consul datacenters' 10.<n>.
	vaultSubnetBase = 64
)

// VaultInitSpec is vault-init's manifest.
type VaultInitSpec struct {
	// Addrs are the API addresses of the cluster's servers, first server
	// first.
	Addrs []string `json:"addrs"`
	// CAFile verifies https:// addresses.
	CAFile string `json:"ca_file,omitempty"`
	// RootToken is the root token id Parker generated; vault-init creates
	// it with the initial root token.
	RootToken string `json:"root_token"`
	// KeysFile is where the unseal keys are kept, so a resumed lab can
	// unseal again.
	KeysFile string `json:"keys_file"`
	// PKIRoots are PKI secrets engines to mount with a given root CA
	// before anything else uses the cluster: the lab CA as the root of a
	// Consul datacenter's Connect CA.
	PKIRoots []VaultPKIRoot `json:"pki_roots,omitempty"`
}

// VaultPKIRoot is a PKI mount and its root CA.
type VaultPKIRoot struct {
	// Path of the mount, e.g. connect-root/.
	Path string `json:"path"`
	// Bundle is the CA certificate and key, PEM.
	Bundle string `json:"bundle"`
}

// VaultFacts are what the vault product tells the others, keyed by cluster.
type VaultFacts map[string]VaultClusterFacts

// VaultClusterFacts describe one cluster.
type VaultClusterFacts struct {
	// Addrs are its servers' API addresses (http://IP:8200).
	Addrs []string
	// RootToken is the lab's root token for the cluster.
	RootToken string
	// Nodes are its servers' expanded node names.
	Nodes []string
}

func init() { Register(vaultProduct{}) }

type vaultProduct struct{}

func (vaultProduct) Name() string { return ProductVault }

func (vaultProduct) In(in *spec.File) bool { return in.Vault != nil }

// Wire: Vault reads nothing from other products yet.
func (vaultProduct) Wire(_, _ *spec.File, _ Facts) error { return nil }

func (vaultProduct) Expand(in, out *spec.File, facts Facts) error {
	v := in.Vault
	vf := VaultFacts{}
	for i, name := range slices.Sorted(maps.Keys(v.Clusters)) {
		c := v.Clusters[name]
		if c == nil {
			c = &spec.VaultCluster{}
		}
		s := v.Settings(name)
		subnet, auto := c.Subnet, c.Subnet == ""
		if auto {
			subnet = fmt.Sprintf("10.%d.0.0/24", vaultSubnetBase+i)
		}
		pfx, err := netip.ParsePrefix(subnet)
		if err != nil {
			return fmt.Errorf("vault.clusters.%s.subnet: %w", name, err)
		}
		base := pfx.Masked().Addr()
		lan := name + "-lan"
		out.Networks[lan] = &spec.Network{Subnet: subnet, Auto: auto}

		token, err := uuid()
		if err != nil {
			return err
		}
		n := c.ServerCount()
		tlsOn := s.TLSOn() && !c.Dev
		scheme := "http"
		var labCA *ca
		if tlsOn {
			scheme = "https"
			if labCA, err = labCAOf(facts, out.Name); err != nil {
				return err
			}
		}
		ips := make([]string, n)
		addrs := make([]string, n)
		keys := make([]string, n)
		for k := range n {
			ips[k] = addrAt(base, 10+k)
			addrs[k] = scheme + "://" + ips[k] + ":" + strconv.Itoa(VaultPort)
			keys[k] = spec.VaultNodeKey(name, k+1)
		}
		for k := range n {
			node := &spec.RawNode{
				Networks:  map[string]*spec.Attachment{lan: {IP: ips[k]}},
				Files:     map[string]string{},
				Processes: map[string]*spec.Process{},
				Labels: &spec.NodeLabels{Product: ProductVault, DC: name, Name: "vault-" + strconv.Itoa(k+1),
					Role: "server", Group: "vault", Meta: c.Meta},
			}
			// A sealed server is up too: it is sealed after every restart,
			// and vault-init or the user unseals it.
			ready := &spec.Ready{
				HTTP:    &spec.HTTPCheck{URL: fmt.Sprintf("%s://127.0.0.1:%d/v1/sys/health?standbyok=true&perfstandbyok=true&sealedcode=200&uninitcode=200", scheme, VaultPort)},
				Timeout: "90s",
			}
			if c.Dev {
				run := spec.Command{"vault", "server", "-dev", "-dev-root-token-id=" + token,
					"-dev-listen-address=0.0.0.0:" + strconv.Itoa(VaultPort)}
				if s.LogLevel != "" {
					run = append(run, "-log-level="+s.LogLevel)
				}
				node.Processes["vault"] = &spec.Process{Run: run, ReadyWhen: ready}
				out.Nodes[keys[k]] = node
				continue
			}
			cfg := vaultConfig(s, name, ips[k], node.Labels.Name, addrs, k)
			if s.Storage.Kind == spec.VaultStorageConsul {
				if err := vaultOnConsul(out, keys[k], node, cfg); err != nil {
					return err
				}
			}
			if tlsOn {
				short := node.Labels.Name
				cert, key, err := labCA.leafCert(keys[k], []string{
					"localhost", short, keys[k],
					short + "." + name + "." + out.Name + ".parker", keys[k] + "." + out.Name + ".parker",
				}, []string{ips[k]})
				if err != nil {
					return fmt.Errorf("%s: %w", keys[k], err)
				}
				node.Files[CAFile] = labCA.certPEM
				node.Files[CAKeyFile] = labCA.keyPEM
				node.Files[CertFile] = cert
				node.Files[CertKeyFile] = key
				ready.HTTP.CAFile = CAFile
				vaultTLS(cfg)
			}
			node.Files[VaultConfigFile] = mustJSON(cfg)
			// Vault wants its storage directory to exist.
			node.Files["data/.keep"] = ""
			node.Processes["vault"] = &spec.Process{
				Run:       spec.Command{"vault", "server", "-config=" + VarNodeDir + "/" + VaultConfigFile},
				ReadyWhen: ready,
			}
			if _, ok := node.Processes["consul"]; ok {
				node.Processes["vault"].DependsOn = map[string]*spec.Dep{keys[k] + "/consul": {Condition: "ready"}}
			}
			if k == 0 {
				deps := map[string]*spec.Dep{}
				for _, key := range keys {
					deps[key+"/vault"] = &spec.Dep{Condition: "started"}
				}
				is := VaultInitSpec{Addrs: addrs, RootToken: token, KeysFile: VarNodeDir + "/" + VaultInitKeys}
				if tlsOn {
					is.CAFile = VarNodeDir + "/" + CAFile
				}
				node.Files[VaultInitManifest] = mustJSON(is)
				node.Processes[VaultInit] = &spec.Process{
					Run:       spec.Command{VarParker, VaultInit, VarNodeDir + "/" + VaultInitManifest},
					DependsOn: deps,
					ReadyWhen: &spec.Ready{Log: VaultReadyLine, Timeout: "90s"},
				}
			}
			out.Nodes[keys[k]] = node
		}
		vf[name] = VaultClusterFacts{Addrs: addrs, RootToken: token, Nodes: keys}
	}
	facts[ProductVault] = vf
	return nil
}

// vaultOnConsul puts a Vault server with consul storage on the node the
// Consul product made for it: a client agent of the storage datacenter,
// on that datacenter's LAN (see consulProduct). The server keeps its data
// through that agent, with the agent's token, over HTTPS when the
// datacenter has TLS, and starts once the agent is ready.
func vaultOnConsul(out *spec.File, key string, node *spec.RawNode, cfg map[string]any) error {
	agent := out.Nodes[key]
	if agent == nil || agent.Processes["consul"] == nil {
		return fmt.Errorf("%s: no Consul agent for its consul storage", key)
	}
	maps.Copy(node.Networks, agent.Networks)
	maps.Copy(node.Files, agent.Files)
	maps.Copy(node.Processes, agent.Processes)
	st, _ := cfg["storage"].(map[string]any)
	c, _ := st["consul"].(map[string]any)
	if c == nil {
		return nil // the user's config replaced the storage
	}
	if r := agent.Processes["consul"].ReadyWhen; r != nil && r.Consul != nil {
		if r.Consul.Token != "" {
			c["token"] = r.Consul.Token
		}
		if r.Consul.CAFile != "" {
			c["address"] = "127.0.0.1:" + strconv.Itoa(HTTPSPort)
			c["scheme"] = "https"
			c["tls_ca_file"] = VarNodeDir + "/" + r.Consul.CAFile
		}
	}
	return nil
}

// vaultTLS turns on TLS in a server's config: the API listener serves the
// node's certificate, and retry_join trusts the lab CA. deepMerge of the
// user's config ran already, so this sets only what TLS needs.
func vaultTLS(cfg map[string]any) {
	if l, ok := cfg["listener"].(map[string]any); ok {
		if t, ok := l["tcp"].(map[string]any); ok {
			t["tls_disable"] = false
			t["tls_cert_file"] = VarNodeDir + "/" + CertFile
			t["tls_key_file"] = VarNodeDir + "/" + CertKeyFile
		}
	}
	if st, ok := cfg["storage"].(map[string]any); ok {
		if r, ok := st["raft"].(map[string]any); ok {
			if js, ok := r["retry_join"].([]any); ok {
				for _, j := range js {
					if m, ok := j.(map[string]any); ok {
						m["leader_ca_cert_file"] = VarNodeDir + "/" + CAFile
					}
				}
			}
		}
	}
}

// vaultConfig is server k's configuration, in Vault's JSON form.
func vaultConfig(s spec.VaultSettings, cluster, ip, nodeID string, addrs []string, k int) map[string]any {
	cfg := map[string]any{
		// Labs run unprivileged; mlock needs CAP_IPC_LOCK.
		"disable_mlock": true,
		"ui":            s.UI == nil || *s.UI,
		"api_addr":      addrs[k],
		"cluster_addr":  "https://" + ip + ":" + strconv.Itoa(VaultClusterPort),
		"listener": map[string]any{"tcp": map[string]any{
			"address":         "0.0.0.0:" + strconv.Itoa(VaultPort),
			"cluster_address": ip + ":" + strconv.Itoa(VaultClusterPort),
			"tls_disable":     true,
		}},
	}
	if s.LogLevel != "" {
		cfg["log_level"] = s.LogLevel
	}
	switch s.Storage.Kind {
	case spec.VaultStorageConsul:
		// The node's own Consul client agent; vaultConsul adds the token
		// and TLS once the agent is known.
		cfg["storage"] = map[string]any{"consul": map[string]any{"address": "127.0.0.1:8500", "path": "vault-" + cluster + "/"}}
	case spec.VaultStorageFile:
		cfg["storage"] = map[string]any{"file": map[string]any{"path": VarNodeDir + "/data"}}
	default:
		var join []any
		for j, a := range addrs {
			if j != k {
				join = append(join, map[string]any{"leader_api_addr": a})
			}
		}
		raft := map[string]any{"path": VarNodeDir + "/data", "node_id": nodeID}
		if len(join) > 0 {
			raft["retry_join"] = join
		}
		cfg["storage"] = map[string]any{"raft": raft}
	}
	deepMerge(cfg, s.Config)
	return cfg
}

// VaultRootToken is the root token of a Vault cluster in an expanded lab:
// from vault-init's manifest, or a dev server's flag; "" when it has none.
func VaultRootToken(f *spec.File, cluster string) string {
	for _, name := range slices.Sorted(maps.Keys(f.Nodes)) {
		rn := f.Nodes[name]
		if l := rn.Labels; l == nil || l.Product != ProductVault || l.DC != cluster {
			continue
		}
		if m, ok := rn.Files[VaultInitManifest]; ok {
			var s VaultInitSpec
			if json.Unmarshal([]byte(m), &s) == nil {
				return s.RootToken
			}
		}
		if p := rn.Processes["vault"]; p != nil {
			for _, a := range p.Run {
				if t, ok := strings.CutPrefix(a, "-dev-root-token-id="); ok {
					return t
				}
			}
		}
	}
	return ""
}
