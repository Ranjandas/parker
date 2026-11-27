package expand

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"

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
	// RootToken is the root token id Parker generated; vault-init creates
	// it with the initial root token.
	RootToken string `json:"root_token"`
	// KeysFile is where the unseal keys are kept, so a resumed lab can
	// unseal again.
	KeysFile string `json:"keys_file"`
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
		ips := make([]string, n)
		addrs := make([]string, n)
		keys := make([]string, n)
		for k := range n {
			ips[k] = addrAt(base, 10+k)
			addrs[k] = "http://" + ips[k] + ":" + strconv.Itoa(VaultPort)
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
			ready := &spec.Ready{
				HTTP:    &spec.HTTPCheck{URL: fmt.Sprintf("http://127.0.0.1:%d/v1/sys/health?standbyok=true&perfstandbyok=true", VaultPort)},
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
			node.Files[VaultConfigFile] = mustJSON(vaultConfig(s, ips[k], node.Labels.Name, addrs, k))
			// Vault wants its storage directory to exist.
			node.Files["data/.keep"] = ""
			node.Processes["vault"] = &spec.Process{
				Run:       spec.Command{"vault", "server", "-config=" + VarNodeDir + "/" + VaultConfigFile},
				ReadyWhen: ready,
			}
			if k == 0 {
				deps := map[string]*spec.Dep{}
				for _, key := range keys {
					deps[key+"/vault"] = &spec.Dep{Condition: "started"}
				}
				node.Files[VaultInitManifest] = mustJSON(VaultInitSpec{Addrs: addrs, RootToken: token, KeysFile: VarNodeDir + "/" + VaultInitKeys})
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

// vaultConfig is server k's configuration, in Vault's JSON form.
func vaultConfig(s spec.VaultSettings, ip, nodeID string, addrs []string, k int) map[string]any {
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
	switch s.Storage {
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
