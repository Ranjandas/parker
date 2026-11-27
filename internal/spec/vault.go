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
}

// VaultSettings are the settings a cluster can inherit from defaults.
type VaultSettings struct {
	// Storage is raft (integrated storage, the default; Vault 1.4+) or
	// file (one server, no HA).
	Storage string `yaml:"storage,omitempty" jsonschema:"enum=raft,enum=file"`
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
}

// Vault storage backends.
const (
	VaultStorageRaft = "raft"
	VaultStorageFile = "file"
)

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
	if c.Storage != "" {
		s.Storage = c.Storage
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
	if s.Storage == "" {
		s.Storage = VaultStorageRaft
	}
	return s
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
		return
	}
	version := f.Versions["vault"]
	if version == "" {
		add("versions.vault: required when a vault: section is present")
	}
	if len(v.Clusters) == 0 {
		add("vault.clusters: at least one cluster is required")
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
		if s.Storage == VaultStorageFile && c.ServerCount() > 1 {
			add("%s: storage: file has no HA, so one server only (got servers: %d); use raft, or consul storage for Vault before 1.4", where, c.Servers)
		}
		if s.Storage == VaultStorageRaft && !c.Dev && version != "" && !VaultAtLeast(version, 1, 4) {
			add("%s: integrated storage (raft) needs Vault 1.4 or later (versions.vault is %s); use storage: file for one server", where, version)
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
}

// Action is the step's action and selector.
func (v *VaultStep) Action() (action, sel string) {
	switch {
	case v.Seal != "":
		return "seal", v.Seal
	case v.Unseal != "":
		return "unseal", v.Unseal
	case v.StepDown != "":
		return "step_down", v.StepDown
	}
	return "", ""
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
}
