// Package spec defines the Parker topology file (schema v1alpha1).
//
// The file has two layers. The high-level `consul:` section speaks in
// datacenters, nodes and services. The low-level layer (`networks:` and
// `nodes:`) speaks in bridges, network namespaces and processes. `parker
// expand` turns the first into the second, and the engine only ever runs
// the low-level form.
package spec

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

const SchemaVersion = "v1alpha1"

//go:generate go run ../../hack/genschema parker.schema.json

// File is a whole Parker topology file.
type File struct {
	// Parker is the schema version of this file.
	Parker string `yaml:"parker" jsonschema:"enum=v1alpha1"`
	// Name of the lab; defaults to the file name. Lowercase letters, digits
	// and '-', at most 24 characters.
	Name string `yaml:"name,omitempty" jsonschema:"pattern=^[a-z0-9]([a-z0-9-]*[a-z0-9])?$,maxLength=24"`
	// Versions of the binaries to fetch, e.g. {consul: 1.22.7}. Add "+ent"
	// for Enterprise builds.
	Versions map[string]string `yaml:"versions,omitempty"`
	// License is the Consul Enterprise license for "+ent" versions. Without
	// it Parker reads CONSUL_LICENSE_PATH, then CONSUL_LICENSE. The license
	// goes to the agents only; it never appears in expand output or logs.
	License *License `yaml:"license,omitempty"`
	// Vars are substituted wherever ${vars.NAME} appears; override with
	// --var NAME=VALUE. ${env.NAME} reads the environment.
	Vars map[string]any `yaml:"vars,omitempty"`
	// Imports merge module files under this one: a module's provides:
	// comes first, so this file's own values win.
	Imports []*Import `yaml:"imports,omitempty"`

	// Consul is the high-level description: datacenters, nodes, services.
	Consul *Consul `yaml:"consul,omitempty"`
	// Vault describes Vault clusters, each on its own network.
	Vault *Vault `yaml:"vault,omitempty"`

	// Networks are extra low-level bridges, keyed by name.
	Networks map[string]*Network `yaml:"networks,omitempty"`
	// Nodes are low-level network namespaces running arbitrary processes,
	// keyed by name. They may join generated <dc>-lan networks.
	Nodes map[string]*RawNode `yaml:"nodes,omitempty"`

	// Scenario is the steps `parker run FILE` plays against the lab.
	Scenario *Scenario `yaml:"scenario,omitempty"`
	// Scenarios are named ones: `parker run FILE NAME`.
	Scenarios map[string]*Scenario `yaml:"scenarios,omitempty"`
}

// License points at a Consul Enterprise license file.
type License struct {
	// File is the license file: absolute, ~/..., or relative to this file.
	File string `yaml:"file"`
}

// ---------------------------------------------------------------- scenario

type Scenario struct {
	// Steps run in order; a failing step ends the scenario.
	Steps []*Step `yaml:"steps"`
	// OnFailure: keep (default) leaves the lab running for a look, teardown
	// takes it down.
	OnFailure string `yaml:"on_failure,omitempty" jsonschema:"enum=keep,enum=teardown"`
}

// Step is one action, optionally delayed with at:.
type Step struct {
	// At delays the step: +30s after the previous step finished, or T+30s
	// after the scenario started.
	At string `yaml:"at,omitempty" jsonschema:"pattern=^T?\\+[0-9].*$"`
	// Name labels the step in the output.
	Name string `yaml:"name,omitempty"`

	// Wait for nodes to be ready: "dc1 ready", or any selector.
	Wait string `yaml:"wait,omitempty"`
	// Sleep for a duration, e.g. 10s.
	Sleep string `yaml:"sleep,omitempty"`
	// Exec runs a command on one or more nodes; a non-zero exit fails the
	// step unless allow_failure is set.
	Exec *ExecStep `yaml:"exec,omitempty"`
	// Assert checks a condition, retried until within: runs out.
	Assert *Assert `yaml:"assert,omitempty"`
	// Snapshot copies state aside for later: logs.
	Snapshot string `yaml:"snapshot,omitempty" jsonschema:"enum=logs"`

	// Partition cuts the network between groups of nodes, each a selector
	// or a list of them: [dc1, dc2], or [[dc1/server-1, dc1/server-2],
	// [dc1/server-3]]. One group is cut off from the rest of the lab.
	// Packets between the sides are dropped until heal.
	Partition []NodeGroup `yaml:"partition,omitempty"`
	// Heal removes partitions: all, or a selector (every cut to or from
	// its nodes).
	Heal string `yaml:"heal,omitempty"`
	// Kill SIGKILLs the processes of the nodes a selector matches; the
	// restart policy applies, as to a crash.
	Kill string `yaml:"kill,omitempty"`
	// Stop stops the nodes' processes (SIGTERM, then SIGKILL after 10s)
	// and keeps them down until start.
	Stop string `yaml:"stop,omitempty"`
	// Start starts the nodes' processes that aren't running and waits for
	// their readiness checks.
	Start string `yaml:"start,omitempty"`
	// Restart is stop then start.
	Restart string `yaml:"restart,omitempty"`
	// Pause freezes the nodes' processes (SIGSTOP) until resume.
	Pause string `yaml:"pause,omitempty"`
	// Resume continues paused processes (SIGCONT).
	Resume string `yaml:"resume,omitempty"`
	// Upgrade moves the nodes to another version of a pinned binary, one
	// node at a time: servers before clients, the leader last, each one
	// healthy again before the next.
	Upgrade *UpgradeStep `yaml:"upgrade,omitempty"`
	// Fake changes the faults of running fake services (run:
	// fake-service) on the nodes a selector matches, without a restart.
	Fake *FakeStep `yaml:"fake,omitempty"`
	// LogLevel changes the log level of the Consul agents, Envoys and
	// fake services on the nodes a selector matches, without a restart.
	LogLevel *LogLevelStep `yaml:"log_level,omitempty"`
	// Vault acts on Vault servers: seal, unseal or step_down, each a
	// selector.
	Vault *VaultStep `yaml:"vault,omitempty"`
}

// LogLevelStep changes running processes' log levels. With no level,
// loggers or reset it shows the levels in force.
type LogLevelStep struct {
	// On is a selector: dc1/web, dc1, ...
	On string `yaml:"on" json:"on"`
	// Level is trace, debug, info, warn or error (critical and off for
	// Envoy only).
	Level string `yaml:"level,omitempty" json:"level,omitempty" jsonschema:"enum=trace,enum=debug,enum=info,enum=warn,enum=error,enum=critical,enum=off"`
	// Only picks one kind of process: consul, envoy or fake; default all.
	Only string `yaml:"only,omitempty" json:"only,omitempty" jsonschema:"enum=consul,enum=envoy,enum=fake"`
	// Process picks one process on the nodes by name (web-sidecar,
	// consul, web); default all of them.
	Process string `yaml:"process,omitempty" json:"process,omitempty"`
	// Loggers sets single Envoy loggers: {upstream: trace, http: debug}.
	Loggers map[string]string `yaml:"loggers,omitempty" json:"loggers,omitempty"`
	// Reset goes back to the levels the lab started with.
	Reset bool `yaml:"reset,omitempty" json:"reset,omitempty"`
}

// Empty reports a step that changes nothing (it shows the levels).
func (l *LogLevelStep) Empty() bool { return l.Level == "" && len(l.Loggers) == 0 && !l.Reset }

// FakeStep changes a running fake service's faults. The fields set are
// changed; the rest stay as they are.
type FakeStep struct {
	// On is a selector: dc1/api, dc2, ...
	On string `yaml:"on" json:"on"`
	// Service picks one fake service on the nodes; default: all of them.
	Service string `yaml:"service,omitempty" json:"service,omitempty"`
	// Reset goes back to the faults the lab started with.
	Reset bool `yaml:"reset,omitempty" json:"reset,omitempty"`
	// Health sets what /health answers: 503 fails the Consul check while
	// the process keeps running.
	Health *FakeHealth `yaml:"health,omitempty" json:"health,omitempty"`
	// Latency, as under fake:. Set a percentile to 0s to clear it.
	Latency *FakeLatency `yaml:"latency,omitempty" json:"latency,omitempty"`
	// Errors, as under fake:. rate: 0 stops them.
	Errors *FakeErrors `yaml:"errors,omitempty" json:"errors,omitempty"`
	// RateLimit, as under fake:. rps: 0 turns it off.
	RateLimit *FakeRateLimit `yaml:"rate_limit,omitempty" json:"rate_limit,omitempty"`
	// Limits, as under fake:. Set a limit to 0 to turn it off.
	Limits *FakeLimits `yaml:"limits,omitempty" json:"limits,omitempty"`
}

// Change is the step's changes in a fake: block's shape.
func (f *FakeStep) Change() *Fake {
	return &Fake{Health: f.Health, Latency: f.Latency, Errors: f.Errors, RateLimit: f.RateLimit, Limits: f.Limits}
}

// Empty reports a step that changes nothing (it shows the current faults).
func (f *FakeStep) Empty() bool {
	return !f.Reset && f.Health == nil && f.Latency == nil && f.Errors == nil && f.RateLimit == nil && f.Limits == nil
}

type UpgradeStep struct {
	// On is a selector: dc1/servers, dc1, ...
	On string `yaml:"on"`
	// To is the version to run, e.g. 1.22.8.
	To string `yaml:"to"`
	// Binary is the pinned binary to swap; default consul.
	Binary string `yaml:"binary,omitempty"`
}

// Product is the binary an upgrade swaps.
func (u *UpgradeStep) Product() string {
	if u.Binary == "" {
		return "consul"
	}
	return u.Binary
}

// NodeGroup is one side of a partition: a selector or a list of them.
type NodeGroup []string

func (g *NodeGroup) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		*g = []string{s}
		return nil
	}
	var l []string
	if err := unmarshal(&l); err != nil {
		return fmt.Errorf("partition: each group is a selector or a list of selectors")
	}
	*g = l
	return nil
}

func (g NodeGroup) MarshalYAML() (any, error) {
	if len(g) == 1 {
		return g[0], nil
	}
	return []string(g), nil
}

type ExecStep struct {
	// On is a selector; the command runs on every node it matches.
	On string `yaml:"on"`
	// Run is the command: a string runs with sh -c (pipes and quotes
	// work), a list runs as is.
	Run Script `yaml:"run"`
	// AllowFailure keeps going when the command exits non-zero.
	AllowFailure bool `yaml:"allow_failure,omitempty"`
}

type Assert struct {
	// Consul checks the cluster through the dc's servers.
	Consul *ConsulAssert `yaml:"consul,omitempty"`
	// Exec passes when the command exits 0 (and its output contains
	// output:, if set).
	Exec *ExecAssert `yaml:"exec,omitempty"`
	// Call sends one request to a fake service (run: fake-service) and
	// checks the answer and the hops it went through.
	Call *CallAssert `yaml:"call,omitempty"`
	// Vault checks the state of Vault servers.
	Vault *VaultAssert `yaml:"vault,omitempty"`
	// Within is how long to keep retrying, e.g. 20s; default once.
	Within string `yaml:"within,omitempty"`
}

type ConsulAssert struct {
	// DC to check; default: the only datacenter.
	DC string `yaml:"dc,omitempty"`
	// Leader: the dc has a raft leader.
	Leader bool `yaml:"leader,omitempty"`
	// Members, e.g. ">=5": alive LAN members.
	Members string `yaml:"members,omitempty"`
	// Peers, e.g. ">=3": voting raft peers. A server that rejoined after a
	// long outage stays a non-voter for a few seconds and doesn't count.
	Peers string `yaml:"peers,omitempty"`
	// ServiceHealthy: at least one instance of the service passes.
	ServiceHealthy string `yaml:"service_healthy,omitempty"`
}

type CallAssert struct {
	// On is the node whose fake service answers (one node), e.g. dc1/web.
	On string `yaml:"on"`
	// Service picks one fake when the node runs several.
	Service string `yaml:"service,omitempty"`
	// Path to request; default /.
	Path string `yaml:"path,omitempty"`
	// Header adds request headers, e.g. X-Parker-Fault.
	Header map[string]string `yaml:"header,omitempty"`
	// Code the answer must have; default 200.
	Code int `yaml:"code,omitempty" jsonschema:"minimum=100,maximum=599"`
	// Reaches: some hop of the chain is this service: api, dc2/api, or a
	// node name (dc2-api).
	Reaches string `yaml:"reaches,omitempty"`
	// Body: the answer contains this text.
	Body string `yaml:"body,omitempty"`
}

type ExecAssert struct {
	On     string `yaml:"on"`
	Run    Script `yaml:"run"`
	Output string `yaml:"output,omitempty"`
}

// ---------------------------------------------------------------- consul

type Consul struct {
	// Defaults apply to every datacenter unless it overrides them.
	Defaults *Inheritable `yaml:"defaults,omitempty"`
	// Templates are reusable node shapes, keyed by name; a node takes one
	// with from: NAME and overrides fields after it.
	Templates map[string]*Node `yaml:"templates,omitempty"`
	// Datacenters keyed by name (e.g. dc1).
	Datacenters map[string]*Datacenter `yaml:"datacenters"`
	// Federation joins the datacenters' servers over a generated `wan`
	// network. Without it, datacenters are independent clusters.
	Federation *Federation `yaml:"federation,omitempty"`
	// Peering connects datacenters (or admin partitions in different
	// datacenters) as cluster peers, keyed by peering name (the peer's name
	// on both sides, e.g. dc1-dc2). Needs Consul 1.14+ and a mesh gateway
	// on each side (in the side's partition) for service traffic.
	Peering map[string]*Peering `yaml:"peering,omitempty"`
	// External services run outside Consul, keyed by service name: one
	// node, on the LAN of every datacenter that registers it, with no
	// agent. Each place under in: registers it in its catalog on an
	// external node (node meta external-node) whose health Parker probes
	// from server-1, as consul-esm would. Reach it from the mesh through a
	// terminating gateway.
	External map[string]*External `yaml:"external,omitempty"`
}

// External is a service outside Consul, registered in the catalog.
type External struct {
	// Service is what runs: port, run (fake-service or a command), fake,
	// env, check and namespace. It takes consul.defaults.service_template
	// but stays out of the mesh: connect and upstreams don't apply.
	Service *Service `yaml:"service"`
	// In lists where it is registered: a datacenter (dc1) or an admin
	// partition of one (dc1/part-a).
	In []string `yaml:"in"`
}

// Peering is one cluster peering between two datacenters, or between
// admin partitions of two datacenters.
type Peering struct {
	// Acceptor generates the peering token: a dc, or dc/partition
	// (Enterprise; dc alone is dc/default).
	Acceptor string `yaml:"acceptor"`
	// Dialer establishes the peering with the token: a dc, or
	// dc/partition.
	Dialer string `yaml:"dialer"`
	// ViaMeshGateways sends the peering's control plane through the mesh
	// gateways (mesh config entry PeerThroughMeshGateways); otherwise the
	// dialer's servers reach the acceptor's on the wan network.
	ViaMeshGateways bool `yaml:"via_mesh_gateways,omitempty"`
	// Exports lists, per side (acceptor or dialer, written as there), the
	// services it exports to the other side, as service or
	// namespace/service (sugar for the partition's exported-services entry).
	Exports map[string][]string `yaml:"exports,omitempty"`
}

type Federation struct {
	// Mode is wan (serf WAN gossip between servers on the wan network) or
	// mesh-gateways (servers reach each other through the datacenters'
	// mesh gateways; needs tls and a mesh gateway per datacenter).
	Mode string `yaml:"mode" jsonschema:"enum=wan,enum=mesh-gateways"`
}

// Federation modes.
const (
	FederationWAN          = "wan"
	FederationMeshGateways = "mesh-gateways"
)

// FederationMode is the federation mode, or "" for independent
// datacenters.
func (c *Consul) FederationMode() string {
	if c == nil || c.Federation == nil {
		return ""
	}
	return c.Federation.Mode
}

// Federated reports whether the datacenters are WAN-federated over serf
// WAN on the wan network.
func (c *Consul) Federated() bool { return c.FederationMode() == FederationWAN }

// UsesWAN reports whether anything joins the generated wan network: servers
// under wan federation, or mesh gateways with wan on.
func (c *Consul) UsesWAN() bool {
	if c == nil {
		return false
	}
	if c.FederationMode() != "" || len(c.Peering) > 0 {
		return true
	}
	for _, dc := range c.Datacenters {
		for _, g := range dc.Gateways {
			if g != nil && g.OnWAN(c) {
				return true
			}
		}
	}
	return false
}

// Gateway is a gateway node (or count of them).
type Gateway struct {
	// Kind is mesh, ingress, terminating or api. Default: the gateway's
	// name when that is a kind.
	Kind string `yaml:"kind,omitempty"`
	// Entry is the gateway's config entry in Consul's casing, without Kind
	// and Name (Parker sets them): Listeners for ingress and api gateways,
	// Services for terminating ones. Applied after the dc's config_entries,
	// so service-defaults there set the protocols its listeners need.
	Entry map[string]any `yaml:"entry,omitempty"`
	// Count of gateway nodes (default 1); several are named <name>-1, ...
	Count int `yaml:"count,omitempty"`
	// WAN attaches a mesh gateway to the wan network too. Default: on when
	// the datacenters are federated or peered.
	WAN *bool `yaml:"wan,omitempty"`
	// Namespace the gateway registers in (Consul Enterprise), for
	// terminating, ingress and API gateways; Parker creates it. Mesh
	// gateways always sit in their partition's default namespace.
	Namespace string `yaml:"namespace,omitempty"`
}

// GatewayKinds are the gateway kinds, with their config entry kinds.
var GatewayKinds = map[string]string{
	"mesh":        "", // configured by proxy-defaults / mesh entries
	"ingress":     "ingress-gateway",
	"terminating": "terminating-gateway",
	"api":         "api-gateway",
}

// OnWAN reports whether the gateway joins the wan network. Only mesh
// gateways do; by default when the datacenters are federated or peered.
func (g *Gateway) OnWAN(c *Consul) bool {
	if g.Kind != "" && g.Kind != "mesh" {
		return false
	}
	if g.WAN != nil {
		return *g.WAN
	}
	return c.FederationMode() != "" || len(c.Peering) > 0
}

// UnmarshalYAML takes true (a gateway with defaults) or an object.
func (g *Gateway) UnmarshalYAML(unmarshal func(any) error) error {
	var on bool
	if err := unmarshal(&on); err == nil {
		if !on {
			return errors.New("gateway: false is not allowed; leave the gateway out")
		}
		*g = Gateway{}
		return nil
	}
	type plain Gateway
	var p plain
	if err := unmarshal(&p); err != nil {
		return err
	}
	*g = Gateway(p)
	return nil
}

// Inheritable holds the settings that flow down the tree.
type Inheritable struct {
	// Agents settings apply to servers and clients alike.
	Agents *AgentSettings `yaml:"agents,omitempty"`
	// Server settings apply to Consul servers only.
	Server *AgentSettings `yaml:"server,omitempty"`
	// Client settings apply to client agents only.
	Client *AgentSettings `yaml:"client,omitempty"`
	// Service template: settings merged into every service, field by
	// field; a service's own settings win. It is a Parker shortcut and unrelated to
	// Consul's service-defaults config entry.
	ServiceTemplate *Service `yaml:"service_template,omitempty"`
	// ACL turns on ACLs (default deny) and gossip encryption. Parker
	// generates the management token and the gossip key, and gives every
	// agent, service and gateway its own identity token; `parker exec`
	// sets CONSUL_HTTP_TOKEN. Federated datacenters must agree.
	ACL *bool `yaml:"acl,omitempty"`
	// TLS turns on mutual TLS for RPC and HTTPS-only APIs (port 8501),
	// with a CA Parker generates per lab. Clients get their certificates
	// through auto_encrypt. `parker exec` sets CONSUL_HTTP_ADDR and
	// CONSUL_CACERT. Federated datacenters must agree.
	TLS *bool `yaml:"tls,omitempty"`
}

type AgentSettings struct {
	// Config is passed through to the Consul agent configuration as is.
	Config map[string]any `yaml:"config,omitempty"`
	// LogLevel of the agent: trace, debug, info, warn or error.
	LogLevel string `yaml:"log_level,omitempty"`
}

type Datacenter struct {
	Inheritable `yaml:",inline"`

	// Primary marks the primary datacenter (for federation and ACLs).
	Primary *bool `yaml:"primary,omitempty"`
	// Subnet of the datacenter LAN, e.g. 10.1.0.0/24. Leave it out and
	// Parker uses 10.<n>.0.0/24, or a free subnet when another running lab
	// or the host already uses that one. Set it when addresses matter to
	// the repro: an explicit subnet never moves.
	Subnet string `yaml:"subnet,omitempty"`
	// UI enables the Consul web UI on the servers.
	UI *bool `yaml:"ui,omitempty"`
	// Meta is metadata for every node of the datacenter, servers and
	// gateways too: Consul node metadata, and labels the meta:KEY=VALUE
	// selector matches. A partition's or node's own meta wins per key.
	Meta map[string]string `yaml:"meta,omitempty"`
	// Servers is the number of Consul servers, or {count, overrides}.
	Servers Servers `yaml:"servers"`
	// Connect sets up the service mesh's certificate authority.
	Connect *DCConnect `yaml:"connect,omitempty"`

	// Nodes besides the servers, keyed by name. A node runs a client agent
	// by default, plus its services and processes.
	Nodes map[string]*Node `yaml:"nodes,omitempty"`
	// Gateways are nodes that run a Consul agent and a gateway Envoy,
	// keyed by node name: mesh: true, or {kind, count, wan}.
	Gateways map[string]*Gateway `yaml:"gateways,omitempty"`
	// Partitions are Admin Partitions (Consul Enterprise), keyed by name.
	// A dc has either nodes: and gateways: (the default partition) or
	// partitions: (put default-partition nodes under partitions.default).
	Partitions map[string]*Partition `yaml:"partitions,omitempty"`
	// ConfigEntries are applied in order once the datacenter's servers
	// (and ACLs) are ready. Each is an entry in Consul's own casing, pasted
	// from the docs, or {file: path} to an HCL or JSON file relative to
	// this YAML file.
	ConfigEntries []ConfigEntry `yaml:"config_entries,omitempty"`

	// EntrySources say where each config entry came from; Load fills it.
	EntrySources []EntrySource `yaml:"-" json:"-"`
}

// DCConnect is a datacenter's Connect (service mesh) settings.
type DCConnect struct {
	// CA is the certificate authority that signs the mesh's certificates.
	CA *ConnectCA `yaml:"ca,omitempty"`
}

// ConnectCA picks the Connect CA provider.
type ConnectCA struct {
	// Vault makes a Vault cluster of the lab the CA (Vault's PKI secrets
	// engine): Consul's servers join the cluster's network and mount
	// their root and intermediate PKI paths in it.
	Vault string `yaml:"vault,omitempty"`
}

// Partition is an Admin Partition (Consul Enterprise): Parker creates
// it, joins its nodes' client agents to it and writes its config entries
// into it.
type Partition struct {
	// Meta is metadata for every node of the partition, over the
	// datacenter's: Consul node metadata, and labels the meta:KEY=VALUE
	// selector matches.
	Meta map[string]string `yaml:"meta,omitempty"`
	// Client settings apply to this partition's client agents.
	Client *AgentSettings `yaml:"client,omitempty"`
	// Service template: settings merged into this partition's services,
	// field by field; a service's own settings win. It is a Parker shortcut and
	// unrelated to Consul's service-defaults config entry.
	ServiceTemplate *Service `yaml:"service_template,omitempty"`
	// Nodes in this partition, keyed by name. Node names are unique
	// across a datacenter's partitions.
	Nodes map[string]*Node `yaml:"nodes,omitempty"`
	// Gateways in this partition (mesh gateways are per partition).
	Gateways map[string]*Gateway `yaml:"gateways,omitempty"`
	// ConfigEntries are written into this partition, after the dc's.
	ConfigEntries []ConfigEntry `yaml:"config_entries,omitempty"`

	// EntrySources say where each config entry came from; Load fills it.
	EntrySources []EntrySource `yaml:"-" json:"-"`
}

// DefaultPartition is the partition every Consul cluster has.
const DefaultPartition = "default"

// NodeKey is a node's expanded name: dc1-web-1, or dc1-team-a-billing-1
// inside a partition other than the default one. Consul scopes node names
// to a partition, so the same name can sit in two partitions.
func NodeKey(dc, partition, name string) string {
	if partition != "" && partition != DefaultPartition {
		return dc + "-" + partition + "-" + name
	}
	return dc + "-" + name
}

// PlacedNode is a node of a datacenter with the partition it's in (""
// for nodes: directly under the dc).
type PlacedNode struct {
	Name      string
	Node      *Node
	Partition string
	Part      *Partition // nil outside partitions:
}

// AllNodes lists a datacenter's nodes, in its partitions too, by name.
func (dc *Datacenter) AllNodes() []PlacedNode {
	var out []PlacedNode
	for n, node := range dc.Nodes {
		out = append(out, PlacedNode{Name: n, Node: node})
	}
	for pn, part := range dc.Partitions {
		if part == nil {
			continue
		}
		for n, node := range part.Nodes {
			out = append(out, PlacedNode{Name: n, Node: node, Partition: pn, Part: part})
		}
	}
	slices.SortFunc(out, func(a, b PlacedNode) int {
		return strings.Compare(a.Name+"\x00"+a.Partition, b.Name+"\x00"+b.Partition)
	})
	return out
}

// PlacedGateway is a gateway with its partition ("" outside partitions:).
type PlacedGateway struct {
	Name      string
	Gateway   *Gateway
	Partition string
}

// AllGateways lists a datacenter's gateways, in its partitions too, by
// name.
func (dc *Datacenter) AllGateways() []PlacedGateway {
	var out []PlacedGateway
	for n, g := range dc.Gateways {
		out = append(out, PlacedGateway{Name: n, Gateway: g})
	}
	for pn, part := range dc.Partitions {
		if part == nil {
			continue
		}
		for n, g := range part.Gateways {
			out = append(out, PlacedGateway{Name: n, Gateway: g, Partition: pn})
		}
	}
	slices.SortFunc(out, func(a, b PlacedGateway) int {
		return strings.Compare(a.Name+"\x00"+a.Partition, b.Name+"\x00"+b.Partition)
	})
	return out
}

// UsesTenancy reports whether the lab uses partitions or namespaces, both
// Enterprise features.
func (c *Consul) UsesTenancy() bool {
	if c == nil {
		return false
	}
	for _, dc := range c.Datacenters {
		if dc != nil && (len(dc.Partitions) > 0 || len(c.Namespaces(dc)) > 0) {
			return true
		}
	}
	return false
}

// Namespaces lists the namespaces a datacenter's services use, as
// "partition/namespace" ("" partition for the default), sorted. A
// service's namespace comes from the service, else the partition's, dc's
// or lab's service_template.
func (c *Consul) Namespaces(dc *Datacenter) []string {
	seen := map[string]bool{}
	for _, pn := range dc.AllNodes() {
		if pn.Node == nil {
			continue
		}
		svcs := pn.Node.Services
		if pn.Node.Service != nil {
			svcs = map[string]*Service{pn.Name: pn.Node.Service}
		}
		for _, s := range svcs {
			if ns := ServiceNamespace(c, dc, pn.Part, s); ns != "" && ns != DefaultNamespace {
				part := pn.Partition
				if part == DefaultPartition {
					part = ""
				}
				seen[part+"/"+ns] = true
			}
		}
	}
	for _, pg := range dc.AllGateways() {
		if g := pg.Gateway; g != nil && g.Namespace != "" && g.Namespace != DefaultNamespace {
			part := pg.Partition
			if part == DefaultPartition {
				part = ""
			}
			seen[part+"/"+g.Namespace] = true
		}
	}
	for _, x := range c.ExternalsIn(dc) {
		if ns := x.Namespace; ns != "" && ns != DefaultNamespace {
			seen[x.Partition+"/"+ns] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// PlacedExternal is an external service as one datacenter registers it.
type PlacedExternal struct {
	Name      string
	External  *External
	Partition string // "" for the default partition
	Namespace string // from the service or consul.defaults.service_template
}

// ExternalsIn lists the external services dc registers, by name then
// partition. Places under in: that don't parse are left out (expand
// reports them).
func (c *Consul) ExternalsIn(dc *Datacenter) []PlacedExternal {
	if c == nil || dc == nil {
		return nil
	}
	dcName := ""
	for n, d := range c.Datacenters {
		if d == dc {
			dcName = n
		}
	}
	var out []PlacedExternal
	for _, name := range slices.Sorted(maps.Keys(c.External)) {
		x := c.External[name]
		if x == nil {
			continue
		}
		ns := ""
		if c.Defaults != nil && c.Defaults.ServiceTemplate != nil {
			ns = c.Defaults.ServiceTemplate.Namespace
		}
		if x.Service != nil && x.Service.Namespace != "" {
			ns = x.Service.Namespace
		}
		for _, where := range x.In {
			d, part, _ := strings.Cut(where, "/")
			if d != dcName {
				continue
			}
			if part == DefaultPartition {
				part = ""
			}
			out = append(out, PlacedExternal{Name: name, External: x, Partition: part, Namespace: ns})
		}
	}
	return out
}

// DefaultNamespace is the namespace every partition has.
const DefaultNamespace = "default"

// ServiceNamespace is the namespace a service registers in ("" for the
// default), from the most specific layer that sets one.
func ServiceNamespace(c *Consul, dc *Datacenter, part *Partition, s *Service) string {
	ns := ""
	var layers []*Service
	if c != nil && c.Defaults != nil {
		layers = append(layers, c.Defaults.ServiceTemplate)
	}
	if dc != nil {
		layers = append(layers, dc.ServiceTemplate)
	}
	if part != nil {
		layers = append(layers, part.ServiceTemplate)
	}
	for _, l := range append(layers, s) {
		if l != nil && l.Namespace != "" {
			ns = l.Namespace
		}
	}
	return ns
}

// ConfigEntry is a Consul config entry, passed through as is.
type ConfigEntry map[string]any

// EntrySource is where a config entry came from, for error messages.
type EntrySource struct {
	Line int    // line in the YAML file
	File string // the file: it was read from, if any
	HCL  string // the file's text when it is HCL
}

// Servers accepts either an int or {count, overrides}.
type Servers struct {
	Count     int                        `yaml:"count"`
	Overrides map[string]*ServerOverride `yaml:"overrides,omitempty"`
}

func (s *Servers) UnmarshalYAML(unmarshal func(any) error) error {
	var n int
	if err := unmarshal(&n); err == nil {
		s.Count = n
		return nil
	}
	type plain Servers
	var p plain
	if err := unmarshal(&p); err != nil {
		return err
	}
	*s = Servers(p)
	return nil
}

type ServerOverride struct {
	// Version of Consul for this server, e.g. for upgrade tests.
	Version string `yaml:"version,omitempty"`
	// Config is merged into this server's agent configuration.
	Config map[string]any `yaml:"config,omitempty"`
}

type Node struct {
	// From names a template under consul.templates; the node's own fields
	// win over the template's.
	From string `yaml:"from,omitempty"`
	// Count makes N copies of the node, named <name>-1 ... <name>-N.
	Count int `yaml:"count,omitempty" jsonschema:"minimum=1"`
	// Agent is client (default), dataplane (no agent: services are
	// registered in the catalog and each sidecar is consul-dataplane, which
	// talks to the servers) or none.
	Agent string `yaml:"agent,omitempty" jsonschema:"enum=client,enum=dataplane,enum=none"`
	// Config is merged into this node's agent configuration.
	Config map[string]any `yaml:"config,omitempty"`
	// Meta is the node's Consul node metadata, over its partition's and
	// datacenter's; the meta:KEY=VALUE selector matches it too.
	Meta map[string]string `yaml:"meta,omitempty"`
	// Service is shorthand for one service named after the node.
	Service *Service `yaml:"service,omitempty"`
	// Services run on this node, keyed by service name.
	Services map[string]*Service `yaml:"services,omitempty"`
	// Processes are extra processes on this node, keyed by name.
	Processes map[string]*Process `yaml:"processes,omitempty"`
}

type Service struct {
	// Port the service listens on.
	Port int `yaml:"port,omitempty" jsonschema:"minimum=1,maximum=65535"`
	// Run is fake-service (Parker's built-in fake service) or a command.
	Run Command `yaml:"run,omitempty"`
	// Fake configures run: fake-service.
	Fake *Fake `yaml:"fake,omitempty"`
	// Env for the service process.
	Env map[string]string `yaml:"env,omitempty"`
	// Check is the health check: http, tcp, grpc or none.
	Check string `yaml:"check,omitempty" jsonschema:"enum=http,enum=tcp,enum=grpc,enum=none"`
	// Register is merged into the Consul service registration as is.
	Register map[string]any `yaml:"register,omitempty"`
	// Namespace the service registers in (Consul Enterprise); Parker
	// creates it. Default: the default namespace.
	Namespace string `yaml:"namespace,omitempty"`
	// Connect puts the service in the mesh: sidecar registers a sidecar
	// proxy and runs Envoy for it (needs versions.envoy); tproxy does the
	// same as a transparent proxy, redirecting the node's traffic through
	// Envoy (one per node, needs iptables); none (default) leaves it out.
	Connect string `yaml:"connect,omitempty" jsonschema:"enum=none,enum=sidecar,enum=tproxy"`
	// Upstreams the sidecar binds on 127.0.0.1, keyed by destination
	// service: api: 9091, or {port, dc, peer, partition, namespace}.
	Upstreams map[string]*Upstream `yaml:"upstreams,omitempty"`
}

// Fake configures Parker's built-in fake service. Each field matches a
// nicholasjackson/fake-service env var, which works too; the YAML wins.
type Fake struct {
	// Protocol is http (default); grpc, which also serves fake-service's
	// FakeService.Handle and the gRPC health check on the same port (h2c),
	// with check: grpc by default; or tcp, a line echo server that greets
	// with its name, node and dc, with check: tcp.
	Protocol string `yaml:"protocol,omitempty" json:"protocol,omitempty" jsonschema:"enum=http,enum=grpc,enum=tcp"`
	// Message is the body of every response (MESSAGE).
	Message string `yaml:"message,omitempty" json:"message,omitempty"`
	// Version labels responses, e.g. v2 for a canary subset (VERSION).
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// Upstreams the service calls on each request, as URIs
	// (UPSTREAM_URIS). Default: the service's upstreams: through its
	// sidecar, http://127.0.0.1:<port>.
	Upstreams []string `yaml:"upstreams,omitempty" json:"upstreams,omitempty"`
	// Workers is how many upstreams are called at once (UPSTREAM_WORKERS);
	// default 1, in order.
	Workers int `yaml:"workers,omitempty" json:"workers,omitempty" jsonschema:"minimum=1"`
	// UpstreamFailure is fail (default: answer 500 when an upstream
	// fails) or ignore.
	UpstreamFailure string `yaml:"upstream_failure,omitempty" json:"upstream_failure,omitempty" jsonschema:"enum=fail,enum=ignore"`
	// ForwardHeaders are headers passed on from each incoming request to
	// every upstream call, such as [Cookie, Authorization], the way an app
	// passes a session cookie or token down a chain.
	ForwardHeaders []string `yaml:"forward_headers,omitempty" json:"forward_headers,omitempty"`
	// UpstreamRequest shapes the requests sent to the upstreams: method,
	// extra headers, a large header and a body, to reach an upstream's
	// limits.
	UpstreamRequest *FakeUpstreamRequest `yaml:"upstream_request,omitempty" json:"upstream_request,omitempty"`
	// Health sets what /health answers; Parker's http check uses it.
	Health *FakeHealth `yaml:"health,omitempty" json:"health,omitempty"`
	// Ready sets what /ready answers; Parker waits on it at up.
	Ready *FakeReady `yaml:"ready,omitempty" json:"ready,omitempty"`
	// Latency adds a delay to each answer, drawn from these percentiles.
	Latency *FakeLatency `yaml:"latency,omitempty" json:"latency,omitempty"`
	// Errors fails a share of requests.
	Errors *FakeErrors `yaml:"errors,omitempty" json:"errors,omitempty"`
	// RateLimit answers requests over a rate with an error code.
	RateLimit *FakeRateLimit `yaml:"rate_limit,omitempty" json:"rate_limit,omitempty"`
	// Limits refuses requests over a size, the way an app server does
	// (Tomcat answers 400 to headers over 8 KB, for example). Default: no
	// limits.
	Limits *FakeLimits `yaml:"limits,omitempty" json:"limits,omitempty"`
	// Seed makes the random draws (errors, latency) repeatable
	// (RAND_SEED); default: a new seed each start.
	Seed int64 `yaml:"seed,omitempty" json:"seed,omitempty"`
	// LogLevel is debug (adds headers and upstream calls), info (every
	// request; the default), warn (failed or faulted requests) or error
	// (LOG_LEVEL).
	LogLevel string `yaml:"log_level,omitempty" json:"log_level,omitempty" jsonschema:"enum=trace,enum=debug,enum=info,enum=warn,enum=error"`
	// RequestFaults lets a caller ask for faults on one request
	// (?error-rate=50&delay=2s, or the X-Parker-Fault header); default
	// true.
	RequestFaults *bool `yaml:"request_faults,omitempty" json:"request_faults,omitempty"`
	// Facts adds a short space fact to each answer (default true; the
	// set depends on the version). false keeps output clean for sharing.
	Facts *bool `yaml:"facts,omitempty" json:"facts,omitempty"`
}

type FakeLatency struct {
	// P50 is the median delay, e.g. 20ms (TIMING_50_PERCENTILE).
	P50 string `yaml:"p50,omitempty" json:"p50,omitempty"`
	// P90 (TIMING_90_PERCENTILE); default p50.
	P90 string `yaml:"p90,omitempty" json:"p90,omitempty"`
	// P99 (TIMING_99_PERCENTILE); default p90.
	P99 string `yaml:"p99,omitempty" json:"p99,omitempty"`
	// Variance spreads each draw by up to this percentage either way
	// (TIMING_VARIANCE).
	Variance int `yaml:"variance,omitempty" json:"variance,omitempty" jsonschema:"minimum=0,maximum=100"`
}

type FakeErrors struct {
	// Rate is the share of requests that fail, 0 to 1 (ERROR_RATE).
	Rate *float64 `yaml:"rate,omitempty" json:"rate,omitempty" jsonschema:"minimum=0,maximum=1"`
	// Code a failed request answers (ERROR_CODE); default 500.
	Code int `yaml:"code,omitempty" json:"code,omitempty" jsonschema:"minimum=100,maximum=599"`
	// Type is http_error (default: answer Code) or delay (answer late,
	// after Delay) (ERROR_TYPE).
	Type string `yaml:"type,omitempty" json:"type,omitempty" jsonschema:"enum=http_error,enum=delay"`
	// Delay for type: delay, e.g. 5s (ERROR_DELAY).
	Delay string `yaml:"delay,omitempty" json:"delay,omitempty"`
}

type FakeRateLimit struct {
	// RPS is the requests per second allowed (RATE_LIMIT); 0 is off.
	RPS *float64 `yaml:"rps,omitempty" json:"rps,omitempty" jsonschema:"minimum=0"`
	// Code for requests over the limit (RATE_LIMIT_CODE); default 503.
	Code int `yaml:"code,omitempty" json:"code,omitempty" jsonschema:"minimum=100,maximum=599"`
}

// FakeUpstreamRequest shapes each upstream call.
type FakeUpstreamRequest struct {
	// Method is GET (default), or POST when body_bytes is set; PUT, PATCH
	// and DELETE work too.
	Method string `yaml:"method,omitempty" json:"method,omitempty" jsonschema:"enum=GET,enum=POST,enum=PUT,enum=PATCH,enum=DELETE"`
	// Headers added to each call.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	// PadHeader adds a header of this size to each call, filled with x.
	PadHeader *FakePadHeader `yaml:"pad_header,omitempty" json:"pad_header,omitempty"`
	// BodyBytes sends a body of this size with each call (8KB, 2MB).
	BodyBytes *ByteSize `yaml:"body_bytes,omitempty" json:"body_bytes,omitempty"`
}

// FakePadHeader is a header of a given size.
type FakePadHeader struct {
	// Name of the header; default X-Parker-Pad. Cookie makes it look like
	// a big session cookie.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Bytes is the size of the whole "Name: value" line, as limits count
	// it (9KB).
	Bytes ByteSize `yaml:"bytes" json:"bytes"`
}

// FakeLimits are request size limits. Sizes are bytes, or a string like
// 8KB or 1MB (KB is 1024 bytes). 0 turns a limit off.
type FakeLimits struct {
	// Profile starts from a server's stock limits and status codes:
	// tomcat, nginx, apache, node, envoy, go, alb, kestrel or gunicorn.
	// Keys set beside it win. none clears every limit.
	Profile string `yaml:"profile,omitempty" json:"profile,omitempty" jsonschema:"enum=tomcat,enum=nginx,enum=apache,enum=node,enum=envoy,enum=go,enum=alb,enum=kestrel,enum=gunicorn,enum=none"`
	// MaxHeaderBytes bounds the request line and all headers together,
	// as Tomcat's maxHttpHeaderSize and Node's max header size do.
	MaxHeaderBytes *ByteSize `yaml:"max_header_bytes,omitempty" json:"max_header_bytes,omitempty"`
	// MaxHeaderLineBytes bounds any one header ("Name: value"), as
	// Nginx's large_client_header_buffers and Apache's
	// LimitRequestFieldSize do.
	MaxHeaderLineBytes *ByteSize `yaml:"max_header_line_bytes,omitempty" json:"max_header_line_bytes,omitempty"`
	// MaxHeaderCount bounds the number of headers, Host included
	// (Tomcat's maxHeaderCount).
	MaxHeaderCount *int `yaml:"max_header_count,omitempty" json:"max_header_count,omitempty" jsonschema:"minimum=0"`
	// MaxURLBytes bounds the path and query (Nginx and Apache answer 414).
	MaxURLBytes *ByteSize `yaml:"max_url_bytes,omitempty" json:"max_url_bytes,omitempty"`
	// MaxBodyBytes bounds the request body (Nginx's
	// client_max_body_size). With protocol: grpc it bounds the request
	// message, answered RESOURCE_EXHAUSTED.
	MaxBodyBytes *ByteSize `yaml:"max_body_bytes,omitempty" json:"max_body_bytes,omitempty"`
	// MaxFormParams bounds the query and form body parameters together
	// (Tomcat's maxParameterCount).
	MaxFormParams *int `yaml:"max_form_params,omitempty" json:"max_form_params,omitempty" jsonschema:"minimum=0"`
	// MaxConcurrent is how many requests are handled at once, like a
	// server's worker threads (Tomcat's maxThreads). Health checks and the
	// fake's page don't count.
	MaxConcurrent *int `yaml:"max_concurrent,omitempty" json:"max_concurrent,omitempty" jsonschema:"minimum=0"`
	// Queue is how many requests wait for a free worker beyond
	// max_concurrent (Tomcat's acceptCount); the rest are refused.
	Queue *int `yaml:"queue,omitempty" json:"queue,omitempty" jsonschema:"minimum=0"`
	// WhenFull is what a refused request gets: refuse (default: the
	// connection is closed unanswered, as a full accept queue does; over
	// HTTP/2 it answers codes.full) or answer (codes.full).
	WhenFull string `yaml:"when_full,omitempty" json:"when_full,omitempty" jsonschema:"enum=refuse,enum=answer"`
	// MaxRequestsPerConn closes a keep-alive connection after this many
	// requests (Tomcat's maxKeepAliveRequests); HTTP/1.1 only.
	MaxRequestsPerConn *int `yaml:"max_requests_per_conn,omitempty" json:"max_requests_per_conn,omitempty" jsonschema:"minimum=0"`
	// IdleTimeout closes a keep-alive connection idle this long, e.g. 20s.
	// Set at start only.
	IdleTimeout string `yaml:"idle_timeout,omitempty" json:"idle_timeout,omitempty"`
	// HeaderTimeout closes a connection that hasn't sent its headers in
	// this long (a slow client), e.g. 60s. Set at start only.
	HeaderTimeout string `yaml:"header_timeout,omitempty" json:"header_timeout,omitempty"`
	// H2MaxStreams is HTTP/2's SETTINGS_MAX_CONCURRENT_STREAMS, for
	// protocol: grpc (default 100 or more, Go's own). Set at start only.
	H2MaxStreams *int `yaml:"h2_max_streams,omitempty" json:"h2_max_streams,omitempty" jsonschema:"minimum=0"`
	// UnderscoreHeaders is what happens to a request header with an
	// underscore in its name, such as X_Tenant_Id: allow (default), drop
	// (Nginx's default: the app never sees it) or reject (400). A dropped
	// header is named in the answer's dropped_headers.
	UnderscoreHeaders string `yaml:"underscore_headers,omitempty" json:"underscore_headers,omitempty" jsonschema:"enum=allow,enum=drop,enum=reject"`
	// StrictCookies drops cookies whose name or value breaks RFC 6265
	// (a space, comma, backslash or quote inside the value), as Tomcat's
	// cookie processor does. They're named in the answer's
	// dropped_cookies.
	StrictCookies *bool `yaml:"strict_cookies,omitempty" json:"strict_cookies,omitempty"`
	// StrictPathChars answers 400 to a request whose path or query has
	// one of " < > [ \ ] ^ ` { | } unencoded, as Tomcat does unless
	// relaxedPathChars or relaxedQueryChars allow it.
	StrictPathChars *bool `yaml:"strict_path_chars,omitempty" json:"strict_path_chars,omitempty"`
	// Codes are the status codes a refused request answers.
	Codes *FakeLimitCodes `yaml:"codes,omitempty" json:"codes,omitempty"`
}

// FakeLimitCodes are the codes for each kind of limit.
type FakeLimitCodes struct {
	// Header is for header size and count; default 431. Tomcat, Nginx
	// and Apache answer 400.
	Header int `yaml:"header,omitempty" json:"header,omitempty" jsonschema:"minimum=100,maximum=599"`
	// URL; default 414.
	URL int `yaml:"url,omitempty" json:"url,omitempty" jsonschema:"minimum=100,maximum=599"`
	// Body; default 413.
	Body int `yaml:"body,omitempty" json:"body,omitempty" jsonschema:"minimum=100,maximum=599"`
	// Form is for form parameters; default 400.
	Form int `yaml:"form,omitempty" json:"form,omitempty" jsonschema:"minimum=100,maximum=599"`
	// Full is for a request refused because every worker and queue slot
	// is busy, with when_full: answer or over HTTP/2; default 503.
	Full int `yaml:"full,omitempty" json:"full,omitempty" jsonschema:"minimum=100,maximum=599"`
}

type FakeHealth struct {
	// Code /health answers (HEALTH_CHECK_RESPONSE_CODE); default 200. A
	// code of 400 or more fails the Consul check while the process keeps
	// running.
	Code int `yaml:"code,omitempty" json:"code,omitempty" jsonschema:"minimum=100,maximum=599"`
}

type FakeReady struct {
	// Delay before /ready passes, from the start of the process
	// (READY_CHECK_RESPONSE_DELAY), e.g. 5s: a slow-starting service.
	Delay string `yaml:"delay,omitempty" json:"delay,omitempty"`
	// Code /ready answers once ready (READY_CHECK_RESPONSE_SUCCESS_CODE);
	// default 200.
	Code int `yaml:"code,omitempty" json:"code,omitempty" jsonschema:"minimum=100,maximum=599"`
	// FailureCode /ready answers before then
	// (READY_CHECK_RESPONSE_FAILURE_CODE); default 503.
	FailureCode int `yaml:"failure_code,omitempty" json:"failure_code,omitempty" jsonschema:"minimum=100,maximum=599"`
}

// Upstream is a destination reached through the sidecar on a local port.
// A bare number is the port.
type Upstream struct {
	// Port to bind on 127.0.0.1.
	Port int `yaml:"port" jsonschema:"minimum=1,maximum=65535"`
	// DC of the destination, when not the caller's.
	DC string `yaml:"dc,omitempty"`
	// Peer the destination is imported from (cluster peering).
	Peer string `yaml:"peer,omitempty"`
	// Partition of the destination (Enterprise).
	Partition string `yaml:"partition,omitempty"`
	// Namespace of the destination (Enterprise).
	Namespace string `yaml:"namespace,omitempty"`
}

func (u *Upstream) UnmarshalYAML(unmarshal func(any) error) error {
	var port int
	if err := unmarshal(&port); err == nil {
		*u = Upstream{Port: port}
		return nil
	}
	type plain Upstream
	var p plain
	if err := unmarshal(&p); err != nil {
		return err
	}
	*u = Upstream(p)
	return nil
}

// ---------------------------------------------------------------- low level

type Network struct {
	// Subnet of the bridge, e.g. 10.9.0.0/24.
	Subnet string `yaml:"subnet"`
	// Gateway is the host-side bridge address; default the subnet's .1.
	Gateway string `yaml:"gateway,omitempty"`
	// Auto lets `parker up` move the network to a free subnet when this
	// one is already in use, by another running lab or by the host. The
	// lab's addresses move with it. Expansion sets it on the networks whose
	// subnet the file didn't choose.
	Auto bool `yaml:"auto,omitempty"`
}

type RawNode struct {
	// Networks the node joins, keyed by network name.
	Networks map[string]*Attachment `yaml:"networks"`
	// Files to write before start: path (relative to the node dir) to
	// content.
	Files map[string]string `yaml:"files,omitempty"`
	// Processes run in the node's namespace, keyed by name.
	Processes map[string]*Process `yaml:"processes,omitempty"`
	// Labels say where an expanded node came from; selectors such as
	// dc1/servers and svc:api match on them.
	Labels *NodeLabels `yaml:"labels,omitempty"`
}

// NodeLabels say where a node sits in the lab: which product it belongs
// to and its region in that product.
type NodeLabels struct {
	// Product the node belongs to: consul ("" for a node of no product).
	Product string `yaml:"product,omitempty"`
	// DC is the node's region: its Consul datacenter. Region names are
	// unique across a lab's products, so selectors need no product prefix.
	DC string `yaml:"dc,omitempty"`
	// Name of the node within its dc, e.g. server-2 or web-1.
	Name string `yaml:"name,omitempty"`
	// Role is server, client, gateway or none (no agent).
	Role string `yaml:"role,omitempty" jsonschema:"enum=server,enum=client,enum=gateway,enum=none"`
	// Group is the node's entry under nodes: (web for web-1, web-2, ...).
	Group string `yaml:"group,omitempty"`
	// Services the node runs.
	Services []string `yaml:"services,omitempty"`
	// Partition the node's agent is in (Enterprise; "" outside
	// partitions:).
	Partition string `yaml:"partition,omitempty"`
	// Namespaces of the node's services, besides the default (Enterprise).
	Namespaces []string `yaml:"namespaces,omitempty"`
	// Meta is the node's metadata, matched by meta:KEY=VALUE selectors.
	Meta map[string]string `yaml:"meta,omitempty"`
}

type Attachment struct {
	// IP address of the node on this network.
	IP string `yaml:"ip"`
}

type Process struct {
	// Run is the command. The first word may name a versioned binary
	// (e.g. "consul"), which is resolved from `versions:` or Version.
	Run Command `yaml:"run"`
	// Version overrides versions.<binary> for this process.
	Version string `yaml:"version,omitempty"`
	// Uses names other pinned binaries the process runs (consul connect
	// envoy starts envoy); they are fetched and put on PATH.
	Uses []string `yaml:"uses,omitempty"`
	// Env for the process.
	Env map[string]string `yaml:"env,omitempty"`
	// DependsOn lists processes to wait for, keyed by "<node>/<process>"
	// (or "<process>" on the same node).
	DependsOn map[string]*Dep `yaml:"depends_on,omitempty"`
	// ReadyWhen says when the process counts as ready.
	ReadyWhen *Ready `yaml:"ready_when,omitempty"`
	// Restart is no (default), on-failure or always. It applies once the lab
	// is up, and only while `parker daemon` supervises the lab.
	Restart string `yaml:"restart,omitempty" jsonschema:"enum=no,enum=on-failure,enum=always"`
}

type Dep struct {
	// Condition is started (default) or ready.
	Condition string `yaml:"condition,omitempty" jsonschema:"enum=started,enum=ready"`
}

type Ready struct {
	// Port: ready once this TCP port accepts connections.
	Port int `yaml:"port,omitempty" jsonschema:"minimum=1,maximum=65535"`
	// HTTP: ready once this URL answers with the expected status.
	HTTP *HTTPCheck `yaml:"http,omitempty"`
	// Log: ready once the process log contains this text.
	Log string `yaml:"log,omitempty"`
	// Consul: ready once the agent reports this state.
	Consul *ConsulCheck `yaml:"consul,omitempty"`
	// Timeout for readiness, e.g. 30s.
	Timeout string `yaml:"timeout,omitempty"`
}

type HTTPCheck struct {
	// URL to request from inside the node's namespace.
	URL string `yaml:"url"`
	// Status expected; default 200.
	Status int `yaml:"status,omitempty"`
	// CAFile verifies an https:// URL's server with this CA; a relative
	// path is in the node's directory.
	CAFile string `yaml:"ca_file,omitempty"`
}

type ConsulCheck struct {
	// Addr of the agent's HTTP API inside the node; default 127.0.0.1:8500
	// (127.0.0.1:8501 with ca_file).
	Addr string `yaml:"addr,omitempty"`
	// Leader: a raft leader is known.
	Leader bool `yaml:"leader,omitempty"`
	// Members, e.g. ">=4": alive LAN members seen by this agent.
	Members string `yaml:"members,omitempty"`
	// WANMembers, e.g. ">=6": alive WAN members (servers of every
	// federated datacenter) seen by this server.
	WANMembers string `yaml:"wan_members,omitempty"`
	// ServiceHealthy: this node's instance of the service is passing in the catalog.
	ServiceHealthy string `yaml:"service_healthy,omitempty"`
	// Reaches: the agent can read this datacenter's catalog (cross-dc RPC
	// works from the agent's datacenter to it).
	Reaches string `yaml:"reaches,omitempty"`
	// Partition and Namespace scope ServiceHealthy (Consul Enterprise).
	Partition string `yaml:"partition,omitempty"`
	Namespace string `yaml:"namespace,omitempty"`
	// Token for the agent API when ACLs are on.
	Token string `yaml:"token,omitempty"`
	// CAFile turns on HTTPS (default address 127.0.0.1:8501) and verifies
	// the agent with this CA; a relative path is in the node's directory.
	CAFile string `yaml:"ca_file,omitempty"`
}

// Command is a string (split on whitespace) or an argv list.
type Command []string

func (c *Command) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		*c = strings.Fields(s)
		return nil
	}
	var l []string
	if err := unmarshal(&l); err != nil {
		return fmt.Errorf("run: want a string or a list of strings")
	}
	*c = l
	return nil
}

// Script is a shell command line (run with sh -c) or an argv list.
type Script []string

func (c *Script) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		*c = []string{"sh", "-c", s}
		return nil
	}
	var l []string
	if err := unmarshal(&l); err != nil {
		return fmt.Errorf("run: want a string or a list of strings")
	}
	*c = l
	return nil
}

func (c Script) MarshalYAML() (any, error) {
	if len(c) == 3 && c[0] == "sh" && c[1] == "-c" {
		return c[2], nil
	}
	return []string(c), nil
}

// ParseMembers parses ">=N" / "N".
func ParseMembers(s string) (int, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), ">="))
	return strconv.Atoi(s)
}

// Cfg returns the passthrough config, nil-safe.
func (a *AgentSettings) Cfg() map[string]any {
	if a == nil {
		return nil
	}
	return a.Config
}

// Import is one module to merge in.
type Import struct {
	// Module is a module file's path, relative to the importing file.
	Module string `yaml:"module"`
	// With sets the module's inputs.
	With map[string]any `yaml:"with,omitempty"`
}

// Module is a module file: a fragment of a topology with inputs, merged
// in by imports:.
type Module struct {
	Parker string `yaml:"parker" jsonschema:"enum=v1alpha1"`
	// Kind is Module.
	Kind string `yaml:"kind" jsonschema:"enum=Module"`
	// Description says what the module is for.
	Description string `yaml:"description,omitempty"`
	// Inputs the module takes, keyed by name; ${inputs.NAME} reads one.
	Inputs map[string]*ModuleInput `yaml:"inputs,omitempty"`
	// Provides is what the module adds: any part of a topology file.
	Provides *File `yaml:"provides"`
}

// ModuleInput is one input of a module.
type ModuleInput struct {
	// Type is string (default), number or bool.
	Type string `yaml:"type,omitempty" jsonschema:"enum=string,enum=number,enum=bool"`
	// Enum lists the values allowed.
	Enum []any `yaml:"enum,omitempty"`
	// Default is used when the importer doesn't set the input; without one
	// the input is required.
	Default any `yaml:"default,omitempty"`
	// Description says what the input does.
	Description string `yaml:"description,omitempty"`
}
