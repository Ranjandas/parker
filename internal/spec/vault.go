package spec

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- vault

// Vault is the vault: section: Vault clusters, each on its own network.
type Vault struct {
	// Defaults apply to every cluster unless it sets its own.
	Defaults *VaultSettings `yaml:"defaults,omitempty"`
	// Clusters keyed by name (e.g. east). Cluster names and Consul
	// datacenter names share one name space, so selectors such as
	// east/vault-2 need no product prefix.
	Clusters map[string]*VaultCluster `yaml:"clusters"`
	// Replication sets up Vault Enterprise replication between clusters:
	// performance, dr, or both.
	Replication *VaultReplication `yaml:"replication,omitempty"`
}

// VaultReplication is the lab's replication: a performance set and a
// disaster recovery (dr) set, each a primary and its secondaries.
type VaultReplication struct {
	// Performance replication: the secondaries serve reads and keep their
	// own tokens and leases, and forward writes to the primary.
	Performance *VaultReplicationSet `yaml:"performance,omitempty"`
	// DR replication: the secondaries keep a full copy, tokens and leases
	// too, and serve nothing until promoted.
	DR *VaultReplicationSet `yaml:"dr,omitempty"`
}

// VaultReplicationSet is one primary and its secondaries.
type VaultReplicationSet struct {
	// Primary is the cluster the others replicate from.
	Primary string `yaml:"primary"`
	// Secondaries are the clusters that replicate it. Activating a
	// secondary replaces its data with the primary's; from then on it
	// unseals with the primary's key.
	Secondaries []string `yaml:"secondaries"`
	// PathsFilter limits what the primary sends to a performance
	// secondary, keyed by the secondary.
	PathsFilter map[string]*VaultPathsFilter `yaml:"paths_filter,omitempty"`
}

// VaultPathsFilter is a performance secondary's paths filter.
type VaultPathsFilter struct {
	// Mode is allow (send only Paths) or deny (send all but Paths).
	Mode string `yaml:"mode" jsonschema:"enum=allow,enum=deny"`
	// Paths are mount paths or namespaces, e.g. secret/ or team-a/.
	Paths []string `yaml:"paths"`
}

// Replication modes, as Vault names them in its API.
const (
	VaultReplPerformance = "performance"
	VaultReplDR          = "dr"
)

// Set is the replication set of mode (performance or dr), or nil.
func (r *VaultReplication) Set(mode string) *VaultReplicationSet {
	if r == nil {
		return nil
	}
	switch mode {
	case VaultReplPerformance:
		return r.Performance
	case VaultReplDR:
		return r.DR
	}
	return nil
}

// Replicated are the clusters in replication, sorted.
func (v *Vault) Replicated() []string {
	var out []string
	for _, m := range []string{VaultReplPerformance, VaultReplDR} {
		s := v.Replication.Set(m)
		if s == nil {
			continue
		}
		for _, c := range append([]string{s.Primary}, s.Secondaries...) {
			if c != "" && !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	slices.Sort(out)
	return out
}

// ReplicaOf is the primary that cluster is a secondary of, and the mode;
// "" when it's no secondary.
func (v *Vault) ReplicaOf(cluster string) (primary, mode string) {
	for _, m := range []string{VaultReplPerformance, VaultReplDR} {
		if s := v.Replication.Set(m); s != nil && slices.Contains(s.Secondaries, cluster) {
			return s.Primary, m
		}
	}
	return "", ""
}

// VaultSettings are the settings a cluster can inherit from defaults.
type VaultSettings struct {
	// Storage is raft (integrated storage, the default; Vault 1.4+), file
	// (one server, no HA), or {consul: DC}: the Consul datacenter DC keeps
	// the data, through a Consul client agent beside each server.
	Storage VaultStorage `yaml:"storage,omitempty"`
	// Register registers the servers in the Consul datacenter of that name
	// as the vault service, tagged active or standby, through a Consul
	// client agent beside each server. storage: {consul: DC} registers
	// them in DC already. Dev clusters don't register.
	Register string `yaml:"register,omitempty"`
	// UI serves the Vault web UI; default true.
	UI *bool `yaml:"ui,omitempty"`
	// LogLevel of the servers: trace, debug, info, warn or error.
	LogLevel string `yaml:"log_level,omitempty" jsonschema:"enum=trace,enum=debug,enum=info,enum=warn,enum=error"`
	// TLS serves the Vault API over HTTPS with a certificate from the lab
	// CA (the one Consul's TLS uses too); default off.
	TLS *bool `yaml:"tls,omitempty"`
	// Config is merged into every server's configuration as is, in
	// Vault's JSON form (e.g. {default_lease_ttl: 1h}).
	Config map[string]any `yaml:"config,omitempty"`
}

// VaultCluster is one Vault cluster.
type VaultCluster struct {
	VaultSettings `yaml:",inline"`

	// Servers is the number of Vault servers (default 1). Parker
	// initialises the cluster and unseals every server.
	Servers int `yaml:"servers,omitempty" jsonschema:"minimum=1"`
	// Dev runs one server in dev mode: in memory, unsealed, no init.
	Dev bool `yaml:"dev,omitempty"`
	// Subnet of the cluster's LAN, e.g. 10.64.0.0/24. Leave it out and
	// Parker picks one, moving it to a free subnet when another running
	// lab or the host already uses it.
	Subnet string `yaml:"subnet,omitempty"`
	// Meta is metadata for the cluster's nodes, matched by meta:KEY=VALUE
	// selectors.
	Meta map[string]string `yaml:"meta,omitempty"`
	// Seal is how the servers unseal. Leave it out for Shamir keys, which
	// Parker keeps and unseals with; {transit: CLUSTER} auto-unseals them
	// with the Transit secrets engine of another Vault cluster of the lab.
	Seal *VaultSeal `yaml:"seal,omitempty"`
	// Secrets are secrets engines Parker sets up on the cluster.
	Secrets *VaultSecrets `yaml:"secrets,omitempty"`
}

// VaultSecrets are the secrets engines Parker mounts and configures.
type VaultSecrets struct {
	// Consul mounts the Consul secrets engine at consul/, configured with
	// a management token of the Consul datacenter of that name (which
	// needs acl: true), and a role named management that mints tokens
	// with the global-management policy.
	Consul string `yaml:"consul,omitempty"`
}

// ConsulSecrets is the datacenter of the cluster's Consul secrets
// engine; "" for none.
func (c *VaultCluster) ConsulSecrets() string {
	if c == nil || c.Secrets == nil {
		return ""
	}
	return c.Secrets.Consul
}

// VaultSeal is a cluster's auto-unseal.
type VaultSeal struct {
	// Transit is the Vault cluster whose Transit secrets engine holds the
	// key that unseals this one.
	Transit string `yaml:"transit"`
}

// Transit is the cluster's transit seal cluster; "" for none.
func (c *VaultCluster) Transit() string {
	if c == nil || c.Seal == nil {
		return ""
	}
	return c.Seal.Transit
}

// Vault storage backends.
const (
	VaultStorageRaft   = "raft"
	VaultStorageFile   = "file"
	VaultStorageConsul = "consul"
)

// VaultStorage is raft, file, or {consul: DC}.
type VaultStorage struct {
	// Kind is raft, file or consul; "" when unset.
	Kind string
	// Consul is the datacenter of consul storage.
	Consul string
}

func (s *VaultStorage) UnmarshalYAML(unmarshal func(any) error) error {
	var k string
	if err := unmarshal(&k); err == nil {
		*s = VaultStorage{Kind: k}
		return nil
	}
	var m struct {
		Consul string `yaml:"consul"`
	}
	if err := unmarshal(&m); err != nil {
		return fmt.Errorf("storage: want raft, file or {consul: DC}")
	}
	*s = VaultStorage{Kind: VaultStorageConsul, Consul: m.Consul}
	return nil
}

func (s VaultStorage) MarshalYAML() (any, error) {
	if s.Kind == VaultStorageConsul {
		return map[string]string{"consul": s.Consul}, nil
	}
	return s.Kind, nil
}

// IsZero leaves an unset storage out of marshalled lab files.
func (s VaultStorage) IsZero() bool { return s == VaultStorage{} }

// ConsulAgents is each Consul datacenter's Vault servers that run a client
// agent of it, for consul storage or to register: the servers' node keys,
// by datacenter.
func (f *File) ConsulAgents() map[string][]string {
	if f.Vault == nil {
		return nil
	}
	out := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(f.Vault.Clusters)) {
		c := f.Vault.Clusters[name]
		s := f.Vault.Settings(name)
		dc := s.ConsulDC()
		if c == nil || c.Dev || dc == "" {
			continue
		}
		for k := range c.ServerCount() {
			out[dc] = append(out[dc], VaultNodeKey(name, k+1))
		}
	}
	return out
}

// Settings is the cluster's settings over the defaults.
func (v *Vault) Settings(name string) VaultSettings {
	var s VaultSettings
	if d := v.Defaults; d != nil {
		s = *d
	}
	c := v.Clusters[name]
	if c == nil {
		return s
	}
	if c.Storage.Kind != "" {
		s.Storage = c.Storage
	}
	if c.Register != "" {
		s.Register = c.Register
	}
	if c.UI != nil {
		s.UI = c.UI
	}
	if c.LogLevel != "" {
		s.LogLevel = c.LogLevel
	}
	if c.TLS != nil {
		s.TLS = c.TLS
	}
	if c.Config != nil {
		merged := maps.Clone(s.Config)
		if merged == nil {
			merged = map[string]any{}
		}
		maps.Copy(merged, c.Config)
		s.Config = merged
	}
	if s.Storage.Kind == "" {
		s.Storage.Kind = VaultStorageRaft
	}
	return s
}

// ConsulDC is the Consul datacenter whose client agent runs beside each
// server: the storage datacenter, or the one to register in; "" for none.
func (s VaultSettings) ConsulDC() string {
	if s.Storage.Kind == VaultStorageConsul {
		return s.Storage.Consul
	}
	return s.Register
}

// TLSOn reports whether the settings turn TLS on.
func (s VaultSettings) TLSOn() bool { return s.TLS != nil && *s.TLS }

// ServerCount is the cluster's number of servers (1 when unset).
func (c *VaultCluster) ServerCount() int {
	if c == nil || c.Servers < 1 {
		return 1
	}
	return c.Servers
}

func (f *File) validateVault(add func(string, ...any)) {
	v := f.Vault
	if v == nil {
		if f.Consul != nil {
			for _, dc := range slices.Sorted(maps.Keys(f.Consul.Datacenters)) {
				if d := f.Consul.Datacenters[dc]; d != nil && d.Connect != nil && d.Connect.CA != nil && d.Connect.CA.Vault != "" {
					add("consul.datacenters.%s.connect.ca.vault: the lab has no vault: section", dc)
				}
			}
		}
		return
	}
	version := f.Versions["vault"]
	if version == "" {
		add("versions.vault: required when a vault: section is present")
	}
	if len(v.Clusters) == 0 {
		add("vault.clusters: at least one cluster is required")
	}
	f.validateVaultReplication(add)
	if _, meta, _ := strings.Cut(version, "+"); slices.Contains(strings.Split(meta, "."), "ent") && !VaultAtLeast(version, 1, 8) {
		add("versions.vault: Parker gives Vault Enterprise its license through VAULT_LICENSE_PATH, which needs Vault 1.8 or later (got %s)", version)
	}
	if f.Consul != nil {
		for _, dc := range slices.Sorted(maps.Keys(f.Consul.Datacenters)) {
			d := f.Consul.Datacenters[dc]
			if d == nil || d.Connect == nil || d.Connect.CA == nil || d.Connect.CA.Vault == "" {
				continue
			}
			if _, ok := v.Clusters[d.Connect.CA.Vault]; !ok {
				add("consul.datacenters.%s.connect.ca.vault: %q is not a Vault cluster of this lab (vault.clusters)", dc, d.Connect.CA.Vault)
			}
		}
	}
	if f.Consul != nil {
		// A dc can't keep the data of the cluster that is its Connect CA:
		// each would wait for the other to start.
		for _, dc := range slices.Sorted(maps.Keys(f.Consul.Datacenters)) {
			d := f.Consul.Datacenters[dc]
			if d == nil || d.Connect == nil || d.Connect.CA == nil || v.Clusters[d.Connect.CA.Vault] == nil {
				continue
			}
			if s := v.Settings(d.Connect.CA.Vault); s.Storage.Kind == VaultStorageConsul && s.Storage.Consul == dc {
				add("consul.datacenters.%s.connect.ca.vault: %s keeps its data in %s, so it can't be %s's Connect CA too; give it raft storage", dc, d.Connect.CA.Vault, dc, dc)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(v.Clusters)) {
		c := v.Clusters[name]
		where := "vault.clusters." + name
		if !nameRe.MatchString(name) {
			add("%s: name must be lowercase letters, digits and '-'", where)
		}
		if f.Consul != nil {
			if _, clash := f.Consul.Datacenters[name]; clash {
				add("%s: a Consul datacenter has this name too; cluster and datacenter names must be unique in a lab", where)
			}
		}
		if c == nil {
			continue
		}
		s := v.Settings(name)
		if c.Dev && s.TLSOn() {
			add("%s: tls is not supported with dev: true", where)
		}
		if c.Dev && c.Servers > 1 {
			add("%s: dev: true runs one server (got servers: %d)", where, c.Servers)
		}
		switch s.Storage.Kind {
		case VaultStorageRaft, VaultStorageFile:
		case VaultStorageConsul:
			if c.Dev {
				add("%s: dev: true keeps its data in memory; leave storage out", where)
			}
			var dc *Datacenter
			if f.Consul != nil {
				dc = f.Consul.Datacenters[s.Storage.Consul]
			}
			if s.Storage.Consul == "" || dc == nil {
				add("%s.storage.consul: %q is not a Consul datacenter of this lab (consul.datacenters)", where, s.Storage.Consul)
			}
		default:
			add("%s.storage: %q is not raft, file or {consul: DC}", where, s.Storage.Kind)
		}
		if c.Dev && c.Register != "" {
			add("%s.register: not supported with dev: true", where)
		}
		if s.Register != "" && !c.Dev {
			switch {
			case s.Storage.Kind == VaultStorageConsul:
				add("%s.register: storage: {consul: %s} registers the servers in %s already; leave register out", where, s.Storage.Consul, s.Storage.Consul)
			case f.Consul == nil || f.Consul.Datacenters[s.Register] == nil:
				add("%s.register: %q is not a Consul datacenter of this lab (consul.datacenters)", where, s.Register)
			}
		}
		if t := c.Transit(); c.Seal != nil {
			tc, ok := v.Clusters[t]
			switch {
			case c.Dev:
				add("%s.seal: not supported with dev: true", where)
			case t == "" || !ok:
				add("%s.seal.transit: %q is not a Vault cluster of this lab (vault.clusters)", where, t)
			case t == name:
				add("%s.seal.transit: a cluster can't unseal itself; name another cluster", where)
			case tc != nil && tc.Dev:
				add("%s.seal.transit: %s runs in dev mode; use a cluster that keeps its data", where, t)
			case tc.Transit() != "":
				add("%s.seal.transit: %s auto-unseals with transit itself; name a cluster that unseals with keys", where, t)
			}
			if version != "" && !VaultAtLeast(version, 1, 1) {
				add("%s.seal: transit auto-unseal needs Vault 1.1 or later (versions.vault is %s)", where, version)
			}
		}
		if dc := c.ConsulSecrets(); dc != "" {
			switch {
			case c.Dev:
				add("%s.secrets.consul: not supported with dev: true", where)
			case f.Consul == nil || f.Consul.Datacenters[dc] == nil:
				add("%s.secrets.consul: %q is not a Consul datacenter of this lab (consul.datacenters)", where, dc)
			}
		}
		if s.Storage.Kind == VaultStorageFile && c.ServerCount() > 1 {
			add("%s: storage: file has no HA, so one server only (got servers: %d); use raft, or storage: {consul: DC} for Vault before 1.4", where, c.Servers)
		}
		if s.Storage.Kind == VaultStorageRaft && !c.Dev && version != "" && !VaultAtLeast(version, 1, 4) {
			add("%s: integrated storage (raft) needs Vault 1.4 or later (versions.vault is %s); use storage: {consul: DC} for HA, or storage: file for one server", where, version)
		}
	}
}

// VaultAtLeast reports whether a versions.vault pin is at least
// major.minor. Pins it can't read (path:..., a build name) count as new.
func VaultAtLeast(version string, major, minor int) bool {
	v := strings.TrimPrefix(version, "v")
	if strings.HasPrefix(v, "path:") {
		return true
	}
	v, _, _ = strings.Cut(v, "+")
	v, _, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return true
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return true
	}
	return maj > major || maj == major && min >= minor
}

// VaultNodeKey is a Vault server's expanded node name: east-vault-1.
func VaultNodeKey(cluster string, i int) string {
	return fmt.Sprintf("%s-vault-%d", cluster, i)
}

// VaultStep acts on the Vault servers a selector matches. Set one action.
type VaultStep struct {
	// Seal seals the servers. Vault seals an active server through its
	// API; a standby is restarted, as Vault asks, and comes back sealed.
	Seal string `yaml:"seal,omitempty" json:"seal,omitempty"`
	// Unseal unseals them with the cluster's key.
	Unseal string `yaml:"unseal,omitempty" json:"unseal,omitempty"`
	// StepDown makes the active server among them give up leadership.
	StepDown string `yaml:"step_down,omitempty" json:"step_down,omitempty"`
	// Promote makes the clusters, replication secondaries, primaries.
	Promote string `yaml:"promote,omitempty" json:"promote,omitempty"`
	// Demote makes the clusters, replication primaries, secondaries that
	// replicate nothing until pointed at a primary with update_primary.
	Demote string `yaml:"demote,omitempty" json:"demote,omitempty"`
	// Disable turns replication off on the clusters.
	Disable string `yaml:"disable,omitempty" json:"disable,omitempty"`
	// UpdatePrimary points the clusters, secondaries (or demoted
	// primaries), at the cluster named by primary:.
	UpdatePrimary string `yaml:"update_primary,omitempty" json:"update_primary,omitempty"`
	// Primary is the new primary of update_primary.
	Primary string `yaml:"primary,omitempty" json:"primary,omitempty"`
	// Replication is performance or dr: which replication promote,
	// demote, disable or update_primary acts on, for a cluster in both.
	// Left out, Parker uses the one the cluster is in.
	Replication string `yaml:"replication,omitempty" json:"replication,omitempty" jsonschema:"enum=performance,enum=dr"`
}

// VaultReplActions are the vault: step actions on replication.
var VaultReplActions = []string{"promote", "demote", "disable", "update_primary"}

// Action is the step's action and selector.
func (v *VaultStep) Action() (action, sel string) {
	switch {
	case v.Seal != "":
		return "seal", v.Seal
	case v.Unseal != "":
		return "unseal", v.Unseal
	case v.StepDown != "":
		return "step_down", v.StepDown
	case v.Promote != "":
		return "promote", v.Promote
	case v.Demote != "":
		return "demote", v.Demote
	case v.Disable != "":
		return "disable", v.Disable
	case v.UpdatePrimary != "":
		return "update_primary", v.UpdatePrimary
	}
	return "", ""
}

// Actions counts the actions set.
func (v *VaultStep) Actions() int {
	n := 0
	for _, s := range []string{v.Seal, v.Unseal, v.StepDown, v.Promote, v.Demote, v.Disable, v.UpdatePrimary} {
		if s != "" {
			n++
		}
	}
	return n
}

// VaultAssert checks the Vault servers a selector matches: each must
// match every field set.
type VaultAssert struct {
	// On is a selector: east, east/vault-2, east/active, ...
	On string `yaml:"on"`
	// Sealed: the servers are sealed (true) or unsealed (false).
	Sealed *bool `yaml:"sealed,omitempty"`
	// Active: the servers are the active one (true) or standbys (false).
	Active *bool `yaml:"active,omitempty"`
	// RaftPeers, e.g. ">=3": voting raft peers, asked of the servers.
	RaftPeers string `yaml:"raft_peers,omitempty"`
	// Replication checks the replication of the servers' clusters, asked
	// of each cluster's active server.
	Replication *VaultReplicationAssert `yaml:"replication,omitempty"`
}

// VaultReplicationAssert is what a cluster's replication must be.
type VaultReplicationAssert struct {
	// Performance is the cluster's performance replication mode: primary,
	// secondary or disabled.
	Performance string `yaml:"performance,omitempty" jsonschema:"enum=primary,enum=secondary,enum=disabled"`
	// DR is its DR replication mode: primary, secondary or disabled.
	DR string `yaml:"dr,omitempty" jsonschema:"enum=primary,enum=secondary,enum=disabled"`
	// State is the state of the modes set above, as Vault reports it:
	// running on a primary, stream-wals on a secondary in sync.
	State string `yaml:"state,omitempty"`
}

// validateVaultReplication checks vault.replication.
func (f *File) validateVaultReplication(add func(string, ...any)) {
	v := f.Vault
	if v.Replication == nil {
		return
	}
	if _, meta, _ := strings.Cut(f.Versions["vault"], "+"); !slices.Contains(strings.Split(meta, "."), "ent") {
		add("vault.replication: needs Vault Enterprise: add +ent to versions.vault (got %q)", f.Versions["vault"])
	}
	// Clusters whose data a secondary's activation would wipe, or that
	// can't unseal with the primary's key.
	caOf := map[string]string{}
	if f.Consul != nil {
		for _, dc := range slices.Sorted(maps.Keys(f.Consul.Datacenters)) {
			if d := f.Consul.Datacenters[dc]; d != nil && d.Connect != nil && d.Connect.CA != nil && d.Connect.CA.Vault != "" {
				caOf[d.Connect.CA.Vault] = dc
			}
		}
	}
	transitFor := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(v.Clusters)) {
		if t := v.Clusters[name].Transit(); t != "" {
			transitFor[t] = name
		}
	}
	seen := map[string]string{}
	primaries := map[string]bool{}
	for _, m := range []string{VaultReplPerformance, VaultReplDR} {
		s := v.Replication.Set(m)
		if s == nil {
			continue
		}
		where := "vault.replication." + m
		cluster := func(field, name string) *VaultCluster {
			c, ok := v.Clusters[name]
			switch {
			case name == "":
				add("%s.%s: name a cluster", where, field)
			case !ok:
				add("%s.%s: %q is not a Vault cluster of this lab (vault.clusters)", where, field, name)
			case c != nil && c.Dev:
				add("%s.%s: %s runs in dev mode; replication needs a cluster that keeps its data", where, field, name)
			}
			return c
		}
		cluster("primary", s.Primary)
		primaries[s.Primary] = true
		if len(s.Secondaries) == 0 {
			add("%s.secondaries: at least one is required", where)
		}
		for _, sec := range s.Secondaries {
			c := cluster("secondaries", sec)
			switch {
			case sec == s.Primary:
				add("%s.secondaries: %s is the primary", where, sec)
			case seen[sec] != "":
				add("%s.secondaries: %s is a %s secondary already; a cluster is the secondary of one primary", where, sec, seen[sec])
			}
			seen[sec] = m
			if c == nil {
				continue
			}
			// Activation replaces the secondary's data with the primary's.
			if dc := caOf[sec]; dc != "" {
				add("%s.secondaries: %s is %s's Connect CA, whose keys activating it as a secondary would wipe; use the primary", where, sec, dc)
			}
			if t := transitFor[sec]; t != "" {
				add("%s.secondaries: %s unseals %s with transit, whose keys activating it as a secondary would wipe; use the primary", where, sec, t)
			}
			if c.Transit() != "" {
				add("%s.secondaries: %s auto-unseals with transit; Parker supports secondaries that unseal with keys only", where, sec)
			}
			if c.ConsulSecrets() != "" {
				add("%s.secondaries: %s has secrets.consul, which activating it as a secondary would wipe; set it on the primary", where, sec)
			}
		}
		for _, sec := range slices.Sorted(maps.Keys(s.PathsFilter)) {
			pf := s.PathsFilter[sec]
			switch {
			case m != VaultReplPerformance:
				add("%s.paths_filter: only performance replication filters paths", where)
			case !slices.Contains(s.Secondaries, sec):
				add("%s.paths_filter: %q is not one of its secondaries", where, sec)
			case pf == nil || pf.Mode != "allow" && pf.Mode != "deny":
				add("%s.paths_filter.%s.mode: want allow or deny", where, sec)
			case len(pf.Paths) == 0:
				add("%s.paths_filter.%s.paths: at least one is required", where, sec)
			}
		}
	}
	for _, p := range slices.Sorted(maps.Keys(primaries)) {
		if m := seen[p]; m != "" {
			add("vault.replication: %s is a %s secondary and a primary too; Parker doesn't chain replication yet", p, m)
		}
	}
}
