// Package selector resolves node selectors against an expanded topology:
//
//	dc1                          every node of the datacenter
//	dc1/servers|clients|gateways a role group
//	dc1/server-2, dc1/web-1      one node (dc1/default/web-1 also works)
//	dc1/web                      a node group: every copy of nodes.web
//	svc:api, svc:dc2/api         nodes that run service api
//	dc1/leader, dc1/followers    raft roles, asked of the servers when resolved
//	toxiproxy, dc1-server-1      a node by its expanded name
//
// The same selectors serve scenarios, exec, port-forward and --set.
package selector

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/spec"
)

// Raft tells which server of a dc is the raft leader, by expanded node
// name. Only leader and followers selectors call it.
type Raft func(dc string) (leader string, err error)

// ErrNoRaft is returned for leader/followers when no Raft is given.
var ErrNoRaft = errors.New("raft roles need a running lab")

// Resolve returns the expanded node names sel matches, sorted.
func Resolve(low *spec.File, sel string, raft Raft) ([]string, error) {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return nil, errors.New("empty selector")
	}
	names := slices.Sorted(maps.Keys(low.Nodes))
	label := func(n string) *spec.NodeLabels {
		if l := low.Nodes[n].Labels; l != nil {
			return l
		}
		return &spec.NodeLabels{}
	}
	filter := func(keep func(string, *spec.NodeLabels) bool) []string {
		var out []string
		for _, n := range names {
			if keep(n, label(n)) {
				out = append(out, n)
			}
		}
		return out
	}
	none := func(out []string, err error) ([]string, error) {
		if err == nil && len(out) == 0 {
			return nil, fmt.Errorf("selector %q matches no nodes (have: %s)", sel, strings.Join(names, ", "))
		}
		return out, err
	}
	dcs := map[string]bool{}
	for _, n := range names {
		if dc := label(n).DC; dc != "" {
			dcs[dc] = true
		}
	}

	if rest, ok := strings.CutPrefix(sel, "svc:"); ok {
		dc, svc, scoped := strings.Cut(rest, "/")
		if !scoped {
			dc, svc = "", rest
		}
		if dc != "" && !dcs[dc] {
			return nil, fmt.Errorf("selector %q: no datacenter %q", sel, dc)
		}
		return none(filter(func(_ string, l *spec.NodeLabels) bool {
			return (dc == "" || l.DC == dc) && slices.Contains(l.Services, svc)
		}), nil)
	}

	parts := strings.Split(sel, "/")
	if len(parts) == 1 {
		if _, ok := low.Nodes[sel]; ok {
			return []string{sel}, nil
		}
		if dcs[sel] {
			return filter(func(_ string, l *spec.NodeLabels) bool { return l.DC == sel }), nil
		}
		return none(nil, nil)
	}
	if len(parts) == 3 {
		if parts[1] != "default" {
			return nil, fmt.Errorf("selector %q: partitions are not supported yet", sel)
		}
		parts = []string{parts[0], parts[2]}
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("selector %q: want dc, dc/name, svc:name or a node name", sel)
	}
	dc, what := parts[0], parts[1]
	if !dcs[dc] {
		return nil, fmt.Errorf("selector %q: no datacenter %q", sel, dc)
	}
	inDC := func(keep func(*spec.NodeLabels) bool) []string {
		return filter(func(_ string, l *spec.NodeLabels) bool { return l.DC == dc && keep(l) })
	}
	switch what {
	case "servers":
		return none(inDC(func(l *spec.NodeLabels) bool { return l.Role == "server" }), nil)
	case "clients":
		return none(inDC(func(l *spec.NodeLabels) bool { return l.Role == "client" }), nil)
	case "gateways":
		return none(inDC(func(l *spec.NodeLabels) bool { return l.Role == "gateway" }), nil)
	case "leader", "followers":
		if raft == nil {
			return nil, fmt.Errorf("selector %q: %w", sel, ErrNoRaft)
		}
		leader, err := raft(dc)
		if err != nil {
			return nil, fmt.Errorf("selector %q: %w", sel, err)
		}
		if what == "leader" {
			return []string{leader}, nil
		}
		return none(filter(func(n string, l *spec.NodeLabels) bool { return l.DC == dc && l.Role == "server" && n != leader }), nil)
	}
	if one := inDC(func(l *spec.NodeLabels) bool { return l.Name == what }); len(one) > 0 {
		return one, nil
	}
	return none(inDC(func(l *spec.NodeLabels) bool { return l.Group == what }), nil)
}

// One resolves a selector that must name exactly one node.
func One(low *spec.File, sel string, raft Raft) (string, error) {
	ns, err := Resolve(low, sel, raft)
	if err != nil {
		return "", err
	}
	if len(ns) != 1 {
		return "", fmt.Errorf("selector %q matches %d nodes (%s); name one", sel, len(ns), strings.Join(ns, ", "))
	}
	return ns[0], nil
}

// LeaderByAddr maps a raft leader address ("10.1.0.10:8300") to the dc
// server that has that IP.
func LeaderByAddr(low *spec.File, dc, addr string) (string, error) {
	if addr == "" {
		return "", fmt.Errorf("%s has no raft leader", dc)
	}
	ip := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		ip = addr[:i]
	}
	for _, n := range slices.Sorted(maps.Keys(low.Nodes)) {
		rn := low.Nodes[n]
		if l := rn.Labels; l == nil || l.DC != dc || l.Role != "server" {
			continue
		}
		for _, a := range rn.Networks {
			if a.IP == ip {
				return n, nil
			}
		}
	}
	return "", fmt.Errorf("%s: leader %s is not a server of this lab", dc, addr)
}
