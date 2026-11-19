// Package spec defines the Parker topology file (schema v1alpha1).
//
// The file has two layers. The high-level `consul:` section speaks in
// datacenters, nodes and services. The low-level layer (`networks:` and
// `nodes:`) speaks in bridges, network namespaces and processes. `parker
// expand` turns the first into the second, and the engine only ever runs
// the low-level form.
package spec

import (
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
}

// ---------------------------------------------------------------- consul

type Consul struct {
	// Defaults apply to every datacenter unless it overrides them.
	Defaults *Inheritable `yaml:"defaults,omitempty"`
	// Datacenters keyed by name (e.g. dc1).
	Datacenters map[string]*Datacenter `yaml:"datacenters"`
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
	// Partitions are reserved for Admin Partitions (Enterprise); not
	// supported yet.
	Partitions map[string]any `yaml:"partitions,omitempty"`
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
	// Addr of the agent's HTTP API; default <node ip>:8500.
	Addr string `yaml:"addr,omitempty"`
	// Leader: a raft leader is known.
	Leader bool `yaml:"leader,omitempty"`
	// Members, e.g. ">=4": alive LAN members seen by this agent.
	Members string `yaml:"members,omitempty"`
	// ServiceHealthy: this node's instance of the service is passing in the catalog.
	ServiceHealthy string `yaml:"service_healthy,omitempty"`
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
