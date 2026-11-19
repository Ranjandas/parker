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

// File is a whole Parker topology file.
type File struct {
	Parker   string            `yaml:"parker"`
	Name     string            `yaml:"name,omitempty"`
	Versions map[string]string `yaml:"versions,omitempty"`
	Vars     map[string]any    `yaml:"vars,omitempty"`

	Consul *Consul `yaml:"consul,omitempty"`

	// Low level.
	Networks map[string]*Network `yaml:"networks,omitempty"`
	Nodes    map[string]*RawNode `yaml:"nodes,omitempty"`
}

// ---------------------------------------------------------------- consul

type Consul struct {
	Defaults    *Inheritable           `yaml:"defaults,omitempty"`
	Datacenters map[string]*Datacenter `yaml:"datacenters"`
}

// Inheritable holds the settings that flow down the tree.
type Inheritable struct {
	Agents          *AgentSettings `yaml:"agents,omitempty"`
	Server          *AgentSettings `yaml:"server,omitempty"`
	Client          *AgentSettings `yaml:"client,omitempty"`
	ServiceDefaults *Service       `yaml:"service_defaults,omitempty"`
}

type AgentSettings struct {
	Config   map[string]any `yaml:"config,omitempty"`
	LogLevel string         `yaml:"log_level,omitempty"`
}

type Datacenter struct {
	Inheritable `yaml:",inline"`

	Primary *bool   `yaml:"primary,omitempty"`
	Subnet  string  `yaml:"subnet,omitempty"`
	UI      *bool   `yaml:"ui,omitempty"`
	Servers Servers `yaml:"servers"`

	Nodes      map[string]*Node `yaml:"nodes,omitempty"`
	Partitions map[string]any   `yaml:"partitions,omitempty"` // reserved, not yet supported
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
	Version string         `yaml:"version,omitempty"`
	Config  map[string]any `yaml:"config,omitempty"`
}

type Node struct {
	Count     int                 `yaml:"count,omitempty"`
	Agent     string              `yaml:"agent,omitempty"` // client | none (dataplane reserved)
	Config    map[string]any      `yaml:"config,omitempty"`
	Meta      map[string]string   `yaml:"meta,omitempty"`
	Service   *Service            `yaml:"service,omitempty"` // shorthand: service named after the node
	Services  map[string]*Service `yaml:"services,omitempty"`
	Processes map[string]*Process `yaml:"processes,omitempty"`
}

type Service struct {
	Port     int               `yaml:"port,omitempty"`
	Run      Command           `yaml:"run,omitempty"` // fake-service | command
	Env      map[string]string `yaml:"env,omitempty"`
	Check    string            `yaml:"check,omitempty"` // http | tcp | none
	Register map[string]any    `yaml:"register,omitempty"`
}

// ---------------------------------------------------------------- low level

type Network struct {
	Subnet  string `yaml:"subnet"`
	Gateway string `yaml:"gateway,omitempty"` // host-side bridge address; default .1
}

type RawNode struct {
	Networks  map[string]*Attachment `yaml:"networks"`
	Files     map[string]string      `yaml:"files,omitempty"` // relative to the node dir
	Processes map[string]*Process    `yaml:"processes,omitempty"`
}

type Attachment struct {
	IP string `yaml:"ip"`
}

type Process struct {
	// Run is the command. The first word may name a versioned binary
	// (e.g. "consul"), which is resolved from `versions:` or Version.
	Run       Command           `yaml:"run"`
	Version   string            `yaml:"version,omitempty"` // override versions.<binary>
	Env       map[string]string `yaml:"env,omitempty"`
	DependsOn map[string]*Dep   `yaml:"depends_on,omitempty"` // key: "<node>/<process>"
	ReadyWhen *Ready            `yaml:"ready_when,omitempty"`
}

type Dep struct {
	Condition string `yaml:"condition"` // started | ready
}

type Ready struct {
	Port    int          `yaml:"port,omitempty"`
	HTTP    *HTTPCheck   `yaml:"http,omitempty"`
	Log     string       `yaml:"log,omitempty"`
	Consul  *ConsulCheck `yaml:"consul,omitempty"`
	Timeout string       `yaml:"timeout,omitempty"`
}

type HTTPCheck struct {
	URL    string `yaml:"url"`
	Status int    `yaml:"status,omitempty"`
}

type ConsulCheck struct {
	Addr    string `yaml:"addr,omitempty"`    // default <node ip>:8500
	Leader  bool   `yaml:"leader,omitempty"`  // a raft leader is known
	Members string `yaml:"members,omitempty"` // e.g. ">=4": alive LAN members seen by this agent
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
