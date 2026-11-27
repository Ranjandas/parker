package tui

import (
	"fmt"
	"slices"
	"strings"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/expand"
)

// ---------------------------------------------------------------- health

// healthRank orders catalog statuses from fine to worst.
var healthRank = map[string]int{"passing": 0, "warning": 1, "critical": 2, "maintenance": 2}

func worseHealth(a, b string) string {
	if a == "" || healthRank[b] > healthRank[a] {
		return b
	}
	return a
}

// healthCell is a catalog status with its mark and color.
func healthCell(h string) cell {
	switch h {
	case "passing":
		return cell{text: "● passing", style: okStyle}
	case "warning":
		return cell{text: "▲ warning", style: warnStyle}
	case "critical", "maintenance":
		return cell{text: "✕ " + h, style: errStyle}
	case "":
		return cell{text: "-", style: mutedStyle}
	}
	return cell{text: h, style: plain}
}

// membersText is an agent's view of its LAN: "4/4 members".
func membersText(n *parkerv1.NodeStatus) string {
	if n.MembersTotal == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d members", n.MembersAlive, n.MembersTotal)
}

// serviceOf is the catalog service a process runs: web for web, its
// sidecar proxy for web-sidecar, and for a gateway the one service on the
// node no other process claims (a gateway's service is its group's name).
func serviceOf(n *parkerv1.NodeStatus, proc string) string {
	if base, ok := strings.CutSuffix(proc, expand.SidecarSuffix); ok {
		return base + "-sidecar-proxy"
	}
	if _, ok := n.Health[proc]; ok {
		return proc
	}
	if k, ok := strings.CutSuffix(proc, "-gateway"); ok && slices.Contains(gatewayKinds, k) {
		var free []string
		for s := range n.Health {
			if !slices.ContainsFunc(n.Processes, func(p *parkerv1.ProcessStatus) bool {
				return p.Name == s || p.Name+"-proxy" == s // web, web-sidecar-proxy
			}) {
				free = append(free, s)
			}
		}
		if len(free) == 1 {
			return free[0]
		}
	}
	return ""
}

// consulText is a process's HEALTH column: the agent's members, a Vault
// server's raft voters, else the health of the service it runs (""
// when nothing reports on it).
func consulText(n *parkerv1.NodeStatus, proc string) string {
	if proc == "consul" {
		return membersText(n)
	}
	if proc == "vault" && n.Product == expand.ProductVault {
		return n.Detail
	}
	if s := serviceOf(n, proc); s != "" {
		return n.Health[s]
	}
	return ""
}

// consulCell styles a CONSUL value.
func consulCell(v string) cell {
	if a, t, ok := parseMembers(v); ok {
		if a < t {
			return cell{text: v, style: warnStyle}
		}
		return cell{text: v, style: mutedStyle}
	}
	return healthCell(v)
}

func parseMembers(v string) (alive, total int, ok bool) {
	n, err := fmt.Sscanf(v, "%d/%d members", &alive, &total)
	return alive, total, err == nil && n == 2
}

// ---------------------------------------------------------------- datacenters

// place is a datacenter, or an admin partition in one.
type place struct{ dc, part string }

func (p place) String() string {
	if p.part == "" {
		return p.dc
	}
	return p.dc + "/" + p.part
}

func (p place) has(n *parkerv1.NodeStatus) bool {
	if p.dc == "" {
		return true
	}
	return nodeDC(n) == p.dc && (p.part == "" || nodePart(n) == p.part)
}

// nodeDC is a node's datacenter: its label, else its name's prefix.
func nodeDC(n *parkerv1.NodeStatus) string {
	if n.Dc != "" {
		return n.Dc
	}
	dc, _, _ := strings.Cut(n.Name, "-")
	return dc
}

// nodePart is a node's admin partition, "default" when it names none.
func nodePart(n *parkerv1.NodeStatus) string {
	if n.Partition == "" {
		return "default"
	}
	return n.Partition
}

// places are the lab's datacenters, each followed by its partitions when
// it has some besides the default.
func places(st *parkerv1.StatusResponse) []place {
	parts := map[string]map[string]bool{}
	for _, g := range groupNodes(st.GetNodes()) {
		if g.dc == "" || parts[g.dc] != nil {
			continue
		}
		parts[g.dc] = map[string]bool{}
	}
	for _, n := range st.GetNodes() {
		if ps := parts[nodeDC(n)]; ps != nil && n.Partition != "" {
			ps[n.Partition] = true
		}
	}
	var out []place
	for _, dc := range sortedKeys(parts) {
		out = append(out, place{dc: dc})
		if len(parts[dc]) > 0 {
			out = append(out, place{dc: dc, part: "default"})
			for _, p := range sortedKeys(parts[dc]) {
				if p != "default" {
					out = append(out, place{dc: dc, part: p})
				}
			}
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// hasDCLevel: the lab has more than one datacenter or uses partitions, so
// opening it shows the datacenters first.
func hasDCLevel(st *parkerv1.StatusResponse) bool {
	ps := places(st)
	return len(ps) > 1
}

var dcColumns = []column{{title: "DC / CLUSTER", flex: true}, {title: "NODES"}, {title: "UP"}, {title: "LEADER", flex: true},
	{title: "MEMBERS"}, {title: "SERVICES"}}

// applyDCRows fills the datacenters table.
func (m *Model) applyDCRows() {
	m.places = places(m.status)
	cells := make([][]cell, len(m.places))
	for i, p := range m.places {
		var nodes, up, total, alive, members int
		leader, health, raft := "-", map[string]int{}, ""
		for _, n := range m.status.GetNodes() {
			if !p.has(n) {
				continue
			}
			nodes++
			for _, pr := range n.Processes {
				total++
				if s := ProcState(pr); s == "ready" || s == "running" {
					up++
				}
			}
			if (n.Role == "leader" || n.State == "active") && p.part == "" {
				leader = n.Name
			}
			if n.State == "active" && n.Detail != "" {
				raft = n.Detail
			}
			if n.Role == "leader" || n.Role == "follower" || n.Role == "no-leader" {
				alive, members = max(alive, int(n.MembersAlive)), max(members, int(n.MembersTotal))
			}
			for s, h := range n.Health {
				if !strings.HasSuffix(s, "-sidecar-proxy") {
					health[h]++
				}
			}
		}
		name := cell{text: p.dc, style: titleStyle}
		if p.part != "" {
			name = cell{text: "  └ " + p.part, style: plain}
		}
		upCell := cell{text: fmt.Sprintf("%d/%d", up, total), style: okStyle}
		if up < total {
			upCell.style = warnStyle
		}
		lead := cell{text: leader, style: leadStyle}
		if leader == "-" {
			lead.style = mutedStyle
			if p.part == "" {
				lead = cell{text: "no leader", style: errStyle}
			}
		}
		mem := cell{text: "-", style: mutedStyle}
		if members > 0 && p.part == "" {
			mem = consulCell(fmt.Sprintf("%d/%d members", alive, members))
		} else if raft != "" {
			mem = cell{text: raft, style: mutedStyle}
		}
		cells[i] = []cell{name, {text: fmt.Sprint(nodes), style: plain}, upCell, lead, mem, healthSummary(health)}
	}
	m.dcTable.SetRows(cells)
}

// healthSummary is "● 5/5 passing", or the count of the worst status.
func healthSummary(h map[string]int) cell {
	total := 0
	worst := ""
	for s, k := range h {
		total += k
		worst = worseHealth(worst, s)
	}
	if total == 0 {
		return cell{text: "-", style: mutedStyle}
	}
	c := healthCell(worst)
	mark, word, _ := strings.Cut(c.text, " ")
	c.text = fmt.Sprintf("%s %d/%d %s", mark, h[worst], total, word)
	return c
}

// ---------------------------------------------------------------- services

// svcRow is a line of the services view: a service heading, or one of its
// instances.
type svcRow struct {
	section         string
	node, proc      string
	service, health string
	sidecar, where  string
}

var svcColumns = []column{{title: "SERVICE", flex: true}, {title: "NODE", flex: true}, {title: "WHERE"}, {title: "HEALTH"}, {title: "SIDECAR"}}

// applySvcRows lists the catalog's services and their instances, in the
// datacenter or partition picked (all of them when none is).
func (m *Model) applySvcRows() {
	type inst struct {
		n    *parkerv1.NodeStatus
		h    string
		side string
	}
	by := map[string][]inst{}
	at := place{dc: m.dc, part: m.part}
	for _, g := range groupNodes(m.status.GetNodes()) {
		for _, n := range g.nodes {
			if !at.has(n) {
				continue
			}
			for s, h := range n.Health {
				if strings.HasSuffix(s, "-sidecar-proxy") {
					continue
				}
				by[s] = append(by[s], inst{n, h, n.Health[s+"-sidecar-proxy"]})
			}
		}
	}
	m.svcRows = m.svcRows[:0]
	var cells [][]cell
	for i, s := range sortedKeys(by) {
		counts := map[string]int{}
		for _, in := range by[s] {
			counts[in.h]++
		}
		if i > 0 {
			m.svcRows = append(m.svcRows, svcRow{section: " "})
			cells = append(cells, sectionRow(""))
		}
		sum := healthSummary(counts)
		title := s + "\t" + plural(len(by[s]), "instance") + " · " + sum.text
		m.svcRows = append(m.svcRows, svcRow{section: title})
		cells = append(cells, sectionRow(title))
		for _, in := range by[s] {
			proc := s
			if !slices.ContainsFunc(in.n.Processes, func(p *parkerv1.ProcessStatus) bool { return p.Name == s }) {
				proc = "" // a gateway runs under its kind's name
				for _, p := range in.n.Processes {
					if serviceOf(in.n, p.Name) == s {
						proc = p.Name
					}
				}
			}
			where := place{dc: nodeDC(in.n)}
			if in.n.Partition != "" {
				where.part = in.n.Partition
			}
			r := svcRow{node: in.n.Name, proc: proc, service: s, health: in.h, sidecar: in.side, where: where.String()}
			m.svcRows = append(m.svcRows, r)
			cells = append(cells, []cell{{text: "  " + s, style: plain}, {text: in.n.Name, style: titleStyle},
				{text: r.where, style: mutedStyle}, healthCell(in.h), healthCell(in.side)})
		}
	}
	m.svcTable.SetRows(cells)
}
