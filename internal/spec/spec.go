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
	// Vars are substituted wherever ${vars.NAME} appears; override with
	// --var NAME=VALUE. ${env.NAME} reads the environment.
	Vars map[string]any `yaml:"vars,omitempty"`

	// Consul is the high-level description: datacenters, nodes, services.
	Consul *Consul `yaml:"consul,omitempty"`

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
	// Peers, e.g. ">=3": raft peers (voters and non-voters).
	Peers string `yaml:"peers,omitempty"`
	// ServiceHealthy: at least one instance of the service passes.
	ServiceHealthy string `yaml:"service_healthy,omitempty"`
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
	// Datacenters keyed by name (e.g. dc1).
	Datacenters map[string]*Datacenter `yaml:"datacenters"`
	// Federation joins the datacenters' servers over a generated `wan`
	// network. Without it, datacenters are independent clusters.
	Federation *Federation `yaml:"federation,omitempty"`
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
	if c.FederationMode() != "" {
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
	// the datacenters are federated.
	WAN *bool `yaml:"wan,omitempty"`
}

// GatewayKinds are the gateway kinds, with their config entry kinds.
var GatewayKinds = map[string]string{
	"mesh":        "", // configured by proxy-defaults / mesh entries
	"ingress":     "ingress-gateway",
	"terminating": "terminating-gateway",
	"api":         "api-gateway",
}

// OnWAN reports whether the gateway joins the wan network. Only mesh
// gateways do.
func (g *Gateway) OnWAN(c *Consul) bool {
	if g.Kind != "" && g.Kind != "mesh" {
		return false
	}
	if g.WAN != nil {
		return *g.WAN
	}
	return c.FederationMode() != ""
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
	// ServiceDefaults are merged into every service.
	ServiceDefaults *Service `yaml:"service_defaults,omitempty"`
	// ACL turns on ACLs (default deny) and gossip encryption. Parker
	// generates the management and agent tokens and the gossip key; `parker
	// exec` sets CONSUL_HTTP_TOKEN. Federated datacenters must agree.
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
	// Subnet of the datacenter LAN, e.g. 10.1.0.0/24; picked automatically
	// if empty.
	Subnet string `yaml:"subnet,omitempty"`
	// UI enables the Consul web UI on the servers.
	UI *bool `yaml:"ui,omitempty"`
	// Servers is the number of Consul servers, or {count, overrides}.
	Servers Servers `yaml:"servers"`

	// Nodes besides the servers, keyed by name. A node runs a client agent
	// by default, plus its services and processes.
	Nodes map[string]*Node `yaml:"nodes,omitempty"`
	// Gateways are nodes that run a Consul agent and a gateway Envoy,
	// keyed by node name: mesh: true, or {kind, count, wan}.
	Gateways map[string]*Gateway `yaml:"gateways,omitempty"`
	// Partitions are reserved for Admin Partitions (Enterprise); not
	// supported yet.
	Partitions map[string]any `yaml:"partitions,omitempty"`
	// ConfigEntries are applied in order once the datacenter's servers
	// (and ACLs) are ready. Each is an entry in Consul's own casing, pasted
	// from the docs, or {file: path} to an HCL or JSON file relative to
	// this YAML file.
	ConfigEntries []ConfigEntry `yaml:"config_entries,omitempty"`

	// EntrySources say where each config entry came from; Load fills it.
	EntrySources []EntrySource `yaml:"-" json:"-"`
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
	// Count makes N copies of the node, named <name>-1 ... <name>-N.
	Count int `yaml:"count,omitempty" jsonschema:"minimum=1"`
	// Agent is client (default) or none.
	Agent string `yaml:"agent,omitempty" jsonschema:"enum=client,enum=none"`
	// Config is merged into this node's agent configuration.
	Config map[string]any `yaml:"config,omitempty"`
	// Meta is the node's Consul node metadata.
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
	// Run is fake-service (default) or a command.
	Run Command `yaml:"run,omitempty"`
	// Env for the service process.
	Env map[string]string `yaml:"env,omitempty"`
	// Check is the health check: http, tcp or none.
	Check string `yaml:"check,omitempty" jsonschema:"enum=http,enum=tcp,enum=none"`
	// Register is merged into the Consul service registration as is.
	Register map[string]any `yaml:"register,omitempty"`
	// Connect puts the service in the mesh: sidecar registers a sidecar
	// proxy and runs Envoy for it (needs versions.envoy); tproxy does the
	// same as a transparent proxy, redirecting the node's traffic through
	// Envoy (one per node, needs iptables); none (default) leaves it out.
	Connect string `yaml:"connect,omitempty" jsonschema:"enum=none,enum=sidecar,enum=tproxy"`
	// Upstreams the sidecar binds on 127.0.0.1, keyed by destination
	// service: api: 9091, or {port, dc, peer, partition, namespace}.
	Upstreams map[string]*Upstream `yaml:"upstreams,omitempty"`
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

// NodeLabels place a node in the consul: tree.
type NodeLabels struct {
	DC string `yaml:"dc,omitempty"`
	// Name of the node within its dc, e.g. server-2 or web-1.
	Name string `yaml:"name,omitempty"`
	// Role is server, client, gateway or none (no agent).
	Role string `yaml:"role,omitempty" jsonschema:"enum=server,enum=client,enum=gateway,enum=none"`
	// Group is the node's entry under nodes: (web for web-1, web-2, ...).
	Group string `yaml:"group,omitempty"`
	// Services the node runs.
	Services []string `yaml:"services,omitempty"`
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
