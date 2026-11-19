package lab

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/Ranjandas/parker/internal/fetch"
	"github.com/Ranjandas/parker/internal/selector"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// ResolveNode maps a selector that names one node (dc1/server-1,
// dc1/leader, svc:api, dc1-server-1, ...) to the expanded node name.
// Raft roles are asked of the running lab's servers.
func ResolveNode(low *spec.File, sel string) (string, error) {
	n, err := selector.One(low, sel, func(dc string) (string, error) { return RaftLeader(low, dc) })
	if err == nil {
		return n, nil
	}
	// Topologies saved before labels existed: dc1/server-1 -> dc1-server-1.
	if alt := strings.ReplaceAll(sel, "/", "-"); low.Nodes[alt] != nil {
		return alt, nil
	}
	return "", err
}

// ResolveNodes is ResolveNode for selectors that may match many nodes.
func ResolveNodes(low *spec.File, sel string) ([]string, error) {
	return selector.Resolve(low, sel, func(dc string) (string, error) { return RaftLeader(low, dc) })
}

// ExecEnv returns PATH (with the lab's pinned binaries first) and the
// environment for a command run inside a node.
func ExecEnv(low *spec.File, node string, base []string, log io.Writer) (path string, env []string) {
	path = os.Getenv("PATH")
	for _, k := range slices.Sorted(maps.Keys(low.Versions)) {
		v := low.Versions[k]
		// What the lab runs is fetched if need be; other pins (envoy before
		// any sidecar runs it) join PATH once cached.
		if Needs(low, k) {
			if b, err := fetch.Binary(k, v, log); err == nil {
				path = filepath.Dir(b) + ":" + path
			}
		} else if b, ok := fetch.Cached(k, v); ok {
			path = filepath.Dir(b) + ":" + path
		}
	}
	env = append(slices.Clone(base), "PATH="+path, "PARKER_LAB="+low.Name, "PARKER_NODE="+node)
	if n := low.Nodes[node]; n != nil {
		if ips := NodeIPs(n); len(ips) > 0 {
			env = append(env, "PARKER_IP="+ips[0]) // its LAN address (networks sorted by name)
		}
	}
	if tok := ManagementToken(low); tok != "" {
		env = append(env, "CONSUL_HTTP_TOKEN="+tok)
	}
	if cc := AgentCheck(low, node); cc.CAFile != "" {
		ca := cc.CAFile
		if !filepath.IsAbs(ca) {
			ca = filepath.Join(supervisor.NodeDir(RunDir(low.Name), node), ca)
		}
		env = append(env, "CONSUL_HTTP_ADDR=https://127.0.0.1:8501", "CONSUL_CACERT="+ca)
	}
	return path, env
}

// AgentCheck is the readiness check of the node's consul agent, which says
// how to reach its API (token, CA); an empty check when it has none.
func AgentCheck(low *spec.File, node string) *spec.ConsulCheck {
	cc := &spec.ConsulCheck{Token: ManagementToken(low)}
	if n := low.Nodes[node]; n != nil {
		if p := n.Processes["consul"]; p != nil && p.ReadyWhen != nil && p.ReadyWhen.Consul != nil {
			cc.CAFile = p.ReadyWhen.Consul.CAFile
			cc.Addr = p.ReadyWhen.Consul.Addr
		}
	}
	return cc
}

// ManagementToken is the lab's Consul management token when ACLs are on,
// as expand put it in the readiness checks; "" otherwise.
func ManagementToken(low *spec.File) string {
	for _, n := range slices.Sorted(maps.Keys(low.Nodes)) {
		for _, p := range low.Nodes[n].Processes {
			if r := p.ReadyWhen; r != nil && r.Consul != nil && r.Consul.Token != "" {
				return r.Consul.Token
			}
		}
	}
	return ""
}

// LookPath finds name in a PATH string.
func LookPath(name, path string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, d := range filepath.SplitList(path) {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found in PATH", name)
}

// Describe returns a node's expanded topology as YAML: networks, rendered
// files and processes.
func Describe(labName, node string) (string, string, error) {
	low, err := LoadTopology(labName)
	if err != nil {
		return "", "", fmt.Errorf("lab %q: %w", labName, err)
	}
	name, err := ResolveNode(low, node)
	if err != nil {
		return "", "", err
	}
	b, err := yaml.MarshalWithOptions(map[string]*spec.RawNode{name: low.Nodes[name]},
		yaml.UseLiteralStyleIfMultiline(true), yaml.IndentSequence(true))
	if err != nil {
		return "", "", err
	}
	return name, string(b), nil
}
