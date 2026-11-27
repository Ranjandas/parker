package expand

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/spec"
)

// ProductConsul is Consul's product name: its lab file key and the
// product label of its nodes.
const ProductConsul = "consul"

func init() { Register(consulProduct{}) }

// consulProduct expands the consul: section (expandConsul and the files
// next to it).
type consulProduct struct{}

func (consulProduct) Name() string { return ProductConsul }

func (consulProduct) In(in *spec.File) bool { return in.Consul != nil }

func (consulProduct) Expand(in, out *spec.File, facts Facts) error {
	wanSubnet, wanAuto := WANSubnet, true
	if w := in.Networks[WANNetwork]; w != nil && w.Subnet != "" {
		wanSubnet, wanAuto = w.Subnet, false
	}
	if err := expandConsul(in.Consul, wanSubnet, out, facts, in.ConsulAgents()); err != nil {
		return err
	}
	// A Vault server's client agent goes by the server's node name: the
	// Vault product puts the server on it.
	for dc, keys := range in.ConsulAgents() {
		for _, k := range keys {
			from := spec.NodeKey(dc, "", k)
			out.Nodes[k] = out.Nodes[from]
			delete(out.Nodes, from)
		}
	}
	if w := out.Networks[WANNetwork]; w != nil && wanAuto {
		w.Auto = true
	}
	return nil
}

// Wire makes a Vault cluster the Connect CA of the datacenters that ask
// for one (connect.ca.vault).
func (consulProduct) Wire(in, out *spec.File, facts Facts) error {
	vf, _ := facts[ProductVault].(VaultFacts)
	used := map[string]int{} // addresses taken on each Vault LAN
	defaults := in.Consul.Defaults
	if defaults == nil {
		defaults = &spec.Inheritable{}
	}
	for _, dcName := range slices.Sorted(maps.Keys(in.Consul.Datacenters)) {
		dc := in.Consul.Datacenters[dcName]
		if dc == nil || dc.Connect == nil || dc.Connect.CA == nil || dc.Connect.CA.Vault == "" {
			continue
		}
		cluster := dc.Connect.CA.Vault
		v, ok := vf[cluster]
		if !ok || len(v.Nodes) == 0 {
			return fmt.Errorf("consul.datacenters.%s.connect.ca.vault: no Vault cluster %q", dcName, cluster)
		}
		if err := vaultConnectCA(out, facts, dcName, cluster, tlsOn(defaults, dc), v, used); err != nil {
			return fmt.Errorf("consul.datacenters.%s.connect.ca.vault: %w", dcName, err)
		}
	}
	return nil
}

// vaultConnectCA points dc's servers at the Vault cluster's PKI: each
// server joins the cluster's LAN (from .200 up), its agent config sets
// the vault CA provider with the cluster's root token, and it starts once
// the cluster is unsealed. Consul mounts the PKI paths itself.
func vaultConnectCA(out *spec.File, facts Facts, dcName, cluster string, tlsDC bool, v VaultClusterFacts, used map[string]int) error {
	lan := cluster + "-lan"
	net := out.Networks[lan]
	if net == nil {
		return fmt.Errorf("no network %s", lan)
	}
	pfx, err := netip.ParsePrefix(net.Subnet)
	if err != nil {
		return err
	}
	first := out.Nodes[v.Nodes[0]]
	labCA, _ := facts[factCA].(*ca)
	ready := v.Nodes[0] + "/vault"
	if _, ok := first.Processes[VaultInit]; ok {
		ready = v.Nodes[0] + "/" + VaultInit
	}
	if labCA != nil {
		// With TLS on, auto-encrypt client certificates come from the
		// Connect CA: make the lab CA its root in Vault, so they chain to
		// the CA everything else trusts.
		var is VaultInitSpec
		if m, ok := first.Files[VaultInitManifest]; ok && json.Unmarshal([]byte(m), &is) == nil {
			if !slices.ContainsFunc(is.PKIRoots, func(r VaultPKIRoot) bool { return r.Path == "connect-root/" }) {
				is.PKIRoots = append(is.PKIRoots, VaultPKIRoot{Path: "connect-root/", Bundle: labCA.certPEM + labCA.keyPEM})
				first.Files[VaultInitManifest] = mustJSON(is)
			}
		} else if tlsDC {
			return fmt.Errorf("a dev-mode cluster can't hold the lab CA that TLS needs; drop dev: true from %s", cluster)
		}
	}
	var vaultCA string
	if strings.HasPrefix(v.Addrs[0], "https://") {
		vaultCA = first.Files[CAFile]
	}
	for _, key := range slices.Sorted(maps.Keys(out.Nodes)) {
		rn := out.Nodes[key]
		if l := rn.Labels; l == nil || l.Product != ProductConsul || l.DC != dcName || l.Role != "server" {
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal([]byte(rn.Files["consul.json"]), &cfg); err != nil {
			return fmt.Errorf("%s: consul.json: %w", key, err)
		}
		conn, _ := cfg["connect"].(map[string]any)
		if conn == nil {
			conn = map[string]any{}
		}
		if old, _ := conn["ca_config"].(map[string]any); old["private_key"] != nil && labCA != nil {
			// The lab CA was the Connect root, and the only copy of its
			// key on this node; keep it for re-signing certificates.
			rn.Files[CAKeyFile] = labCA.keyPEM
		}
		caCfg := map[string]any{
			"address":               v.Addrs[0],
			"token":                 v.RootToken,
			"root_pki_path":         "connect-root/",
			"intermediate_pki_path": "connect-" + dcName + "-intermediate/",
		}
		if vaultCA != "" {
			rn.Files[VaultCAFile] = vaultCA
			caCfg["ca_file"] = VarNodeDir + "/" + VaultCAFile
		}
		conn["enabled"] = true
		conn["ca_provider"] = "vault"
		conn["ca_config"] = caCfg
		cfg["connect"] = conn
		rn.Files["consul.json"] = mustJSON(cfg)
		rn.Networks[lan] = &spec.Attachment{IP: addrAt(pfx.Masked().Addr(), 200+used[lan])}
		used[lan]++
		p := rn.Processes["consul"]
		if p.DependsOn == nil {
			p.DependsOn = map[string]*spec.Dep{}
		}
		p.DependsOn[ready] = &spec.Dep{Condition: "ready"}
	}
	return nil
}
