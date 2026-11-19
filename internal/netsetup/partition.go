package netsetup

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/spec"
)

// A partition is a set of blackhole routes inside node namespaces: each
// node of one group routes the other groups' addresses nowhere, so packets
// between them are silently lost both ways, as on a real cut link. The
// routes carry their own protocol number, so heal finds them with no state,
// and `parker down` takes them with the namespaces.

// CutPlan returns, for each node, the addresses it must stop reaching so
// the groups can't talk to each other. With one group, the rest of the lab
// is the other side. A node may be in only one group.
func CutPlan(f *spec.File, groups [][]string) (map[string][]string, error) {
	if len(groups) == 0 {
		return nil, errors.New("partition: no groups")
	}
	owner := map[string]int{}
	for i, g := range groups {
		if len(g) == 0 {
			return nil, fmt.Errorf("partition: group %d is empty", i+1)
		}
		for _, n := range g {
			if f.Nodes[n] == nil {
				return nil, fmt.Errorf("partition: no node %s", n)
			}
			if j, ok := owner[n]; ok && j != i {
				return nil, fmt.Errorf("partition: %s is in groups %d and %d", n, j+1, i+1)
			}
			owner[n] = i
		}
	}
	if len(groups) == 1 {
		var rest []string
		for _, n := range slices.Sorted(maps.Keys(f.Nodes)) {
			if _, ok := owner[n]; !ok {
				rest = append(rest, n)
				owner[n] = 1
			}
		}
		if len(rest) == 0 {
			return nil, errors.New("partition: the group is the whole lab; nothing to cut it off from")
		}
		groups = append(groups, rest)
	}
	plan := map[string][]string{}
	for n, gi := range owner {
		var ips []string
		for m, gj := range owner {
			if gj != gi {
				ips = append(ips, nodeIPs(f.Nodes[m])...)
			}
		}
		slices.Sort(ips)
		plan[n] = slices.Compact(ips)
	}
	return plan, nil
}

// HealPlan says which cut routes to drop: on the given nodes all of them,
// and elsewhere those to the given nodes' addresses. nil nodes means every
// node, everything.
func HealPlan(f *spec.File, nodes []string) (all map[string]bool, to map[string]bool) {
	all, to = map[string]bool{}, map[string]bool{}
	if nodes == nil {
		for n := range f.Nodes {
			all[n] = true
		}
		return all, to
	}
	for _, n := range nodes {
		all[n] = true
		if rn := f.Nodes[n]; rn != nil {
			for _, ip := range nodeIPs(rn) {
				to[ip] = true
			}
		}
	}
	return all, to
}

func nodeIPs(n *spec.RawNode) []string {
	var ips []string
	for _, k := range slices.Sorted(maps.Keys(n.Networks)) {
		if ip := strings.TrimSpace(n.Networks[k].IP); ip != "" {
			ips = append(ips, ip)
		}
	}
	return ips
}
