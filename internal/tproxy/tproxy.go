// Package tproxy runs a Connect sidecar for Parker: it asks `consul connect
// envoy` for Envoy's bootstrap, makes Envoy mark the connections it opens,
// and (for a transparent proxy) redirects the node's TCP traffic through
// Envoy with iptables before exec'ing it.
//
// Consul's own `redirect-traffic` tells Envoy's traffic apart by its UID.
// A Parker lab often runs in a user namespace that maps one UID, where
// every process is root, so Parker matches a socket mark (SO_MARK) instead:
// Envoy sets it on every upstream socket through the bootstrap's
// cluster_manager.upstream_bind_config.
package tproxy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Mark is the SO_MARK Envoy puts on its upstream sockets (0x5041 is "PA").
const Mark = 0x5041

// Consul agent ports a transparent proxy must leave alone: server RPC and
// serf (outbound, to other agents), plus the agent's own listeners
// (inbound).
var (
	AgentOutboundPorts = []int{8300, 8301, 8302}
	AgentInboundPorts  = []int{8300, 8301, 8302, 8500, 8501, 8502, 8503, 8600}
)

// Redirect says how to steer a node's traffic through Envoy.
type Redirect struct {
	InboundPort  int   // Envoy's public listener (the sidecar's port)
	OutboundPort int   // Envoy's outbound listener (Consul default 15001)
	DNSPort      int   // local Consul DNS; 0 leaves DNS alone
	ExcludeIn    []int // inbound ports not redirected
	ExcludeOut   []int // outbound destination ports not redirected
}

// Chain names; each is flushed and refilled, so rules can be applied again
// when the sidecar restarts.
const (
	ChainOut = "PARKER_TPROXY_OUT"
	ChainIn  = "PARKER_TPROXY_IN"
	ChainDNS = "PARKER_TPROXY_DNS"
)

// Rules is the iptables commands (nat table) for r, in order. The jumps
// from OUTPUT and PREROUTING are the last commands and come in -C/-A
// pairs: Apply adds each only if its check fails.
func Rules(r Redirect) [][]string {
	mark := "0x" + strconv.FormatInt(Mark, 16)
	nat := func(a ...string) []string { return append([]string{"-t", "nat"}, a...) }
	var out [][]string
	for _, c := range []string{ChainDNS, ChainOut, ChainIn} {
		out = append(out, nat("-N", c), nat("-F", c))
	}
	if r.DNSPort > 0 {
		for _, proto := range []string{"udp", "tcp"} {
			out = append(out, nat("-A", ChainDNS, "-p", proto, "--dport", "53", "-m", "mark", "!", "--mark", mark,
				"-j", "REDIRECT", "--to-ports", strconv.Itoa(r.DNSPort)))
		}
	}
	out = append(out,
		nat("-A", ChainOut, "-m", "mark", "--mark", mark, "-j", "RETURN"),
		nat("-A", ChainOut, "-d", "127.0.0.0/8", "-j", "RETURN"))
	if len(r.ExcludeOut) > 0 {
		out = append(out, nat("-A", ChainOut, "-p", "tcp", "-m", "multiport", "--dports", ports(r.ExcludeOut), "-j", "RETURN"))
	}
	out = append(out, nat("-A", ChainOut, "-p", "tcp", "-j", "REDIRECT", "--to-ports", strconv.Itoa(r.OutboundPort)))
	if len(r.ExcludeIn) > 0 {
		out = append(out, nat("-A", ChainIn, "-p", "tcp", "-m", "multiport", "--dports", ports(r.ExcludeIn), "-j", "RETURN"))
	}
	out = append(out, nat("-A", ChainIn, "-p", "tcp", "-j", "REDIRECT", "--to-ports", strconv.Itoa(r.InboundPort)))
	for _, j := range [][]string{
		{"OUTPUT", "-j", ChainDNS},
		{"OUTPUT", "-p", "tcp", "-j", ChainOut},
		{"PREROUTING", "-p", "tcp", "-j", ChainIn},
	} {
		out = append(out, nat(append([]string{"-C"}, j...)...), nat(append([]string{"-A"}, j...)...))
	}
	return out
}

// Apply runs Rules(r) through ipt (`iptables ARGS...`). Creating a chain
// that exists is not an error, and a jump is added only once.
func Apply(r Redirect, ipt func(args ...string) error) error {
	rules := Rules(r)
	for i := 0; i < len(rules); i++ {
		a := rules[i]
		switch a[2] {
		case "-N":
			_ = ipt(a...) // exists after a restart; -F next empties it
			continue
		case "-C":
			if ipt(a...) == nil {
				i++ // already there: skip the -A that follows
			}
			continue
		}
		if err := ipt(a...); err != nil {
			return err
		}
	}
	return nil
}

func ports(ps []int) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ",")
}

// PatchBootstrap makes every upstream connection Envoy opens carry Mark,
// so the redirect rules let Envoy's own traffic through.
func PatchBootstrap(b []byte) ([]byte, error) {
	var boot map[string]any
	if err := json.Unmarshal(b, &boot); err != nil {
		return nil, fmt.Errorf("envoy bootstrap: %w", err)
	}
	cm, _ := boot["cluster_manager"].(map[string]any)
	if cm == nil {
		cm = map[string]any{}
	}
	bind, _ := cm["upstream_bind_config"].(map[string]any)
	if bind == nil {
		bind = map[string]any{"source_address": map[string]any{"address": "0.0.0.0", "port_value": 0}}
	}
	opts, _ := bind["socket_options"].([]any)
	bind["socket_options"] = append(opts, map[string]any{
		"description": "parker tproxy mark",
		"level":       1,  // SOL_SOCKET
		"name":        36, // SO_MARK
		"int_value":   Mark,
		"state":       "STATE_PREBIND",
	})
	cm["upstream_bind_config"] = bind
	boot["cluster_manager"] = cm
	return json.MarshalIndent(boot, "", "  ")
}

// SplitArgs splits `consul connect envoy ARGS -- ENVOY_ARGS` at its "--".
func SplitArgs(argv []string) (consul, envoy []string) {
	for i, a := range argv {
		if a == "--" {
			return argv[:i], argv[i+1:]
		}
	}
	return argv, nil
}
