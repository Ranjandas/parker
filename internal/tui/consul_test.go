package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
)

// consulLab has two datacenters, an admin partition in dc1 and catalog
// health on its services.
func consulLab() *fakeClient {
	yes := true
	up := func(names ...string) []*parkerv1.ProcessStatus {
		var out []*parkerv1.ProcessStatus
		for _, n := range names {
			out = append(out, &parkerv1.ProcessStatus{Name: n, Pid: 1, Alive: true, Ready: &yes})
		}
		return out
	}
	return &fakeClient{
		labs: []*parkerv1.LabSummary{{Name: "p", Supervised: true}},
		status: map[string]*parkerv1.StatusResponse{"p": {Lab: "p", Supervised: true, Nodes: []*parkerv1.NodeStatus{
			{Name: "dc1-server-1", Dc: "dc1", Role: "leader", MembersAlive: 3, MembersTotal: 4, Processes: up("consul")},
			{Name: "dc1-web", Dc: "dc1", Role: "client", MembersAlive: 3, MembersTotal: 4, Processes: up("consul", "web", "web-sidecar"),
				Health: map[string]string{"web": "passing", "web-sidecar-proxy": "passing"}},
			{Name: "dc1-gw", Dc: "dc1", Role: "client", Processes: up("consul", "mesh-gateway"), Health: map[string]string{"gw": "passing"}},
			{Name: "dc1-team-a-billing", Dc: "dc1", Partition: "team-a", Role: "client", Processes: up("consul", "billing"),
				Health: map[string]string{"billing": "critical"}},
			{Name: "dc2-server-1", Dc: "dc2", Role: "leader", MembersAlive: 2, MembersTotal: 2, Processes: up("consul")},
			{Name: "dc2-web", Dc: "dc2", Role: "client", Processes: up("consul", "web"), Health: map[string]string{"web": "warning"}},
		}}},
	}
}

func cmd(s string) []tea.Msg { return append([]tea.Msg{key(":")}, append(typed(s), key("enter"))...) }

func TestDatacenterLevel(t *testing.T) {
	m := tea.Model(New(Options{Client: consulLab(), Lab: "p", Static: true}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 30}, m.(Model).refresh()())
	mm := m.(Model)
	if mm.view != viewDCs {
		t.Fatalf("opened on view %d, want the datacenters", mm.view)
	}
	var got []string
	for _, p := range mm.places {
		got = append(got, p.String())
	}
	if strings.Join(got, " ") != "dc1 dc1/default dc1/team-a dc2" {
		t.Fatalf("places %v", got)
	}
	v := mm.View()
	for _, want := range []string{"p › datacenters", "└ team-a", "3/4 members", "dc1-server-1", "✕ 1/3 critical", "▲ 1/1 warning"} {
		if !strings.Contains(v, want) {
			t.Errorf("datacenters view lacks %q:\n%s", want, v)
		}
	}
	// team-a: just its node, under its heading.
	m = drive(t, m, key("down"), key("down"), key("enter"))
	mm = m.(Model)
	var nodes []string
	for _, r := range mm.rows {
		if r.node != "" {
			nodes = append(nodes, r.node)
		}
	}
	if mm.view != viewNodes || strings.Join(nodes, " ") != "dc1-team-a-billing dc1-team-a-billing" || !strings.Contains(mm.View(), "p › dc1/team-a") {
		t.Fatalf("team-a nodes %v\n%s", nodes, mm.View())
	}
	m = drive(t, m, key("esc"))
	if m.(Model).view != viewDCs || m.(Model).dc != "" {
		t.Fatal("esc from a datacenter's nodes goes back to the datacenters")
	}
	// :dc dc1 shows dc1 with a heading per partition.
	m = drive(t, m, cmd("dc dc1")...)
	v = m.View()
	if !strings.Contains(v, "dc1 / team-a") || strings.Contains(v, "dc2-web") {
		t.Errorf(":dc dc1\n%s", v)
	}
	m = drive(t, m, cmd("dc")...)
	if m.(Model).view != viewDCs {
		t.Error(":dc with no name opens the datacenters")
	}
	m = drive(t, m, key("esc"))
	if m.(Model).view != viewLabs {
		t.Error("esc from the datacenters goes to the labs")
	}
}

func TestConsulColumn(t *testing.T) {
	m := tea.Model(New(Options{Client: consulLab(), Lab: "p", Static: true}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 40}, m.(Model).refresh()())
	m = drive(t, m, cmd("dc dc1")...)
	byProc := map[string]string{}
	for _, r := range m.(Model).rows {
		byProc[r.node+"/"+r.proc] = r.consul
	}
	for k, want := range map[string]string{
		"dc1-server-1/consul": "3/4 members", "dc1-web/web": "passing", "dc1-web/web-sidecar": "passing",
		"dc1-gw/mesh-gateway": "passing", "dc1-team-a-billing/billing": "critical", "dc1-gw/consul": "",
	} {
		if byProc[k] != want {
			t.Errorf("%s: CONSUL %q, want %q", k, byProc[k], want)
		}
	}
	if c := consulCell("3/4 members"); c.style.GetForeground() != warnStyle.GetForeground() {
		t.Error("missing members don't stand out")
	}
	// Folded, a node shows trouble first, else its members.
	if got := foldedConsul("3/4 members", "critical"); got != "critical" {
		t.Errorf("folded with a critical service: %q", got)
	}
	if got := foldedConsul("4/4 members", "passing"); got != "4/4 members" {
		t.Errorf("folded and healthy: %q", got)
	}
}

func TestServicesView(t *testing.T) {
	m := tea.Model(New(Options{Client: consulLab(), Lab: "p", Static: true}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40}, m.(Model).refresh()())
	m = drive(t, m, cmd("svc")...)
	mm := m.(Model)
	v := mm.View()
	for _, want := range []string{"p › services", "web", "2 instances · ▲ 1/2 warning", "billing", "dc1/team-a", "gw", "1 instance · ● 1/1 passing"} {
		if !strings.Contains(v, want) {
			t.Errorf("services view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "sidecar-proxy") {
		t.Errorf("sidecars are a column, not services:\n%s", v)
	}
	// The gateway's instance points at the process that runs it.
	for _, r := range mm.svcRows {
		if r.service == "gw" && r.proc != "mesh-gateway" {
			t.Errorf("gw runs in %q", r.proc)
		}
	}
	// enter jumps to the instance in the nodes view.
	for i, r := range mm.svcRows {
		if r.service == "web" && r.node == "dc2-web" {
			mm.svcTable.SetCursor(i)
		}
	}
	m = drive(t, mm, key("enter"))
	mm = m.(Model)
	if r, ok := mm.selected(); mm.view != viewNodes || !ok || r.node != "dc2-web" || r.proc != "web" {
		t.Fatalf("jumped to %+v (view %d)", r, mm.view)
	}
	// esc from services goes back where :svc came from.
	m = drive(t, m, cmd("svc")...)
	m = drive(t, m, key("esc"))
	if m.(Model).view != viewNodes {
		t.Error("esc from services")
	}
}

func TestOneDatacenterSkipsTheLevel(t *testing.T) {
	m := tea.Model(New(Options{Client: sample(), Lab: "a"}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30}, m.(Model).refresh()())
	if m.(Model).view != viewNodes {
		t.Error("a lab with one datacenter opens on its nodes")
	}
}
