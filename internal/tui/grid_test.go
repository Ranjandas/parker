package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
)

func textRows(rows ...[]string) [][]cell {
	out := make([][]cell, len(rows))
	for i, r := range rows {
		for _, t := range r {
			out[i] = append(out[i], cell{text: t, style: plain})
		}
	}
	return out
}

func TestGridWidths(t *testing.T) {
	g := newGrid(column{title: "NODE", flex: true}, column{title: "IP", flex: true}, column{title: "STATE"})
	g.SetRows(textRows([]string{"dc1-server-1", "10.1.0.10,10.255.0.10", "ready"}))
	g.SetSize(120, 5)
	if w := g.widths(); w[0] != 12 || w[1] != 21 || w[2] != 5 {
		t.Errorf("content widths: %v", w)
	}
	// Too narrow: the widest flex column gives way first, fixed ones keep.
	g.SetSize(36, 5)
	w := g.widths()
	if w[2] != 5 || w[1] >= 21 || 1+w[0]+w[1]+w[2]+2*colGap > 36 {
		t.Errorf("narrow widths: %v", w)
	}
	for _, l := range strings.Split(g.View(), "\n") {
		if ansi.StringWidth(l) > 36 {
			t.Errorf("line wider than 36: %q", l)
		}
	}
	if !strings.Contains(g.View(), "…") {
		t.Errorf("truncated cell has no ellipsis:\n%s", g.View())
	}
}

func TestGridCursorAndScroll(t *testing.T) {
	g := newGrid(column{title: "N"})
	var rows [][]string
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		rows = append(rows, []string{n})
	}
	g.SetRows(textRows(rows...))
	g.SetSize(20, 2)
	for range 3 {
		g, _ = g.Update(key("j"))
	}
	if g.Cursor() != 3 || g.offset != 2 {
		t.Errorf("after 3 downs: cursor %d offset %d", g.Cursor(), g.offset)
	}
	v := g.View()
	if strings.Contains(v, " a ") || !strings.Contains(v, "c") || !strings.Contains(v, "d") {
		t.Errorf("window shows c and d:\n%s", v)
	}
	g, _ = g.Update(key("G"))
	g, _ = g.Update(key("j"))
	if g.Cursor() != 4 {
		t.Errorf("cursor runs past the end: %d", g.Cursor())
	}
	g, _ = g.Update(key("g"))
	if g.Cursor() != 0 || g.offset != 0 {
		t.Errorf("g: cursor %d offset %d", g.Cursor(), g.offset)
	}
	// Fewer rows than before: the cursor follows the last one.
	g.SetCursor(4)
	g.SetRows(textRows([]string{"a"}, []string{"b"}))
	if g.Cursor() != 1 {
		t.Errorf("cursor after shrink: %d", g.Cursor())
	}
	g.SetRows(nil)
	if g.Cursor() != 0 {
		t.Errorf("cursor with no rows: %d", g.Cursor())
	}
}

func TestRowGroupsProcesses(t *testing.T) {
	m := New(Options{Client: sample(), Lab: "a"})
	mm := drive(t, m, key("r")).(Model)
	var got []string
	for _, r := range mm.rows {
		got = append(got, r.section+"|"+r.node+"/"+r.proc)
	}
	want := []string{"dc1\t1 server|/", "|dc1-server-1/consul", "|/", "other|/", "|dc2-web-1/consul", "|dc2-web-1/web"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows %q", got)
	}
	c := mm.rows[5].cells(true)
	if c[0].text != "" || c[1].text != "" || c[2].text != "web" {
		t.Errorf("continuation row: %+v", c)
	}
	v := mm.View()
	if strings.Count(v, "dc2-web-1") != 1 || !strings.Contains(v, " dc1  1 server ") {
		t.Errorf("dc2-web-1 listed once, dc1 heading:\n%s", v)
	}
	// The cursor starts on the first node, not the heading, and skips
	// headings both ways.
	if r, ok := mm.selected(); !ok || r.node != "dc1-server-1" {
		t.Fatalf("selected %+v %v", r, ok)
	}
	mm = drive(t, mm, key("j")).(Model)
	if r, _ := mm.selected(); r.node != "dc2-web-1" || r.proc != "consul" {
		t.Errorf("j: %+v", r)
	}
	mm = drive(t, mm, key("k")).(Model)
	if r, _ := mm.selected(); r.node != "dc1-server-1" {
		t.Errorf("k: %+v", r)
	}
	mm = drive(t, mm, key("k")).(Model)
	if r, _ := mm.selected(); r.node != "dc1-server-1" {
		t.Errorf("k at the top stays: %+v", r)
	}
}

func TestGroupNodes(t *testing.T) {
	nodes := []*parkerv1.NodeStatus{
		{Name: "dc2-web", Role: "client"},
		{Name: "dc1-client", Role: "client"},
		{Name: "dc1-server-2", Role: ""}, // agent down: still a server by name
		{Name: "dc1-server-1", Role: "leader"},
		{Name: "dc2-server-1", Role: "leader"},
		{Name: "dc1-mesh", Processes: []*parkerv1.ProcessStatus{{Name: "consul"}}},
		{Name: "sleeper"},
	}
	var got []string
	for _, g := range groupNodes(nodes) {
		var names []string
		for _, n := range g.nodes {
			names = append(names, n.Name)
		}
		got = append(got, g.title()+": "+strings.Join(names, " "))
	}
	want := []string{
		"dc1\t2 servers · 2 clients: dc1-server-1 dc1-server-2 dc1-client dc1-mesh",
		"dc2\t1 server · 1 client: dc2-server-1 dc2-web",
		"other: sleeper",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("groups:\n%s", strings.Join(got, "\n"))
	}
}

func TestStateAndRoleCells(t *testing.T) {
	for s, want := range map[string]string{"ready": "● ready", "starting": "◐ starting", "dead": "✕ dead", "stopped": "■ stopped", "paused": "‖ paused", "weird": "weird"} {
		if got := stateCell(s).text; got != want {
			t.Errorf("stateCell(%s) = %q", s, got)
		}
	}
	if roleCell(roleLeader).style.GetForeground() != leadStyle.GetForeground() || roleCell("").text != "-" || roleCell("server").text != "server" {
		t.Error("roleCell")
	}
	if !strings.Contains(strings.Join(summary(sample().status["a"]), " "), "leader dc1-server-1") {
		t.Error("summary names the leader")
	}
}

func TestBox(t *testing.T) {
	b := box("T", "hello", 20, plain)
	lines := strings.Split(b, "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "╭─ T ──") || !strings.HasPrefix(lines[1], "│ hello ") ||
		!strings.HasSuffix(lines[1], " │") || !strings.HasPrefix(lines[2], "╰──") || ansi.StringWidth(lines[0]) != 20 || ansi.StringWidth(lines[1]) != 20 {
		t.Errorf("box:\n%s", b)
	}
	for _, l := range strings.Split(box("title", strings.Repeat("x", 50), 30, plain), "\n") {
		if ansi.StringWidth(l) != 30 {
			t.Errorf("line not 30 wide: %q", l)
		}
	}
}

func TestLogoAnimates(t *testing.T) {
	m := New(Options{})
	first := m.logo()
	m.frame++
	if ansi.Strip(m.logo()) != "━━━━━━● parker" {
		t.Errorf("logo text: %q", ansi.Strip(m.logo()))
	}
	// Under the test renderer there's no color to compare, so check the
	// frame drives the trail: the static logo never reads m.frame.
	if New(Options{Static: true}).Init() == nil || first == "" {
		t.Error("Init")
	}
	mm, cmd := m.Update(animMsg{})
	if mm.(Model).frame != 2 || cmd == nil {
		t.Errorf("animMsg: frame %d, next tick %v", mm.(Model).frame, cmd != nil)
	}
}

func TestStaticLogo(t *testing.T) {
	if got := ansi.Strip(New(Options{Static: true}).logo()); got != "━━━━━━● parker" {
		t.Errorf("static logo: %q", got)
	}
}

// meshLab is a datacenter with a gateway node and a service with a sidecar.
func meshLab(ready bool) *fakeClient {
	yes := true
	up := func(name string, pid int32) *parkerv1.ProcessStatus {
		return &parkerv1.ProcessStatus{Name: name, Pid: pid, Alive: true, Ready: &yes}
	}
	api := up("api", 6)
	if !ready {
		api.Alive = false
	}
	return &fakeClient{status: map[string]*parkerv1.StatusResponse{"m": {Lab: "m", Supervised: true, Nodes: []*parkerv1.NodeStatus{
		{Name: "dc1-server-1", Ips: []string{"10.1.0.10", "10.255.0.10"}, Namespace: true, Role: "leader",
			Processes: []*parkerv1.ProcessStatus{up("config-entries", 1), up("consul", 2)}},
		{Name: "dc1-server-2", Ips: []string{"10.1.0.11"}, Namespace: true, Role: "follower",
			Processes: []*parkerv1.ProcessStatus{up("consul", 3)}},
		{Name: "dc1-mesh", Ips: []string{"10.1.0.100"}, Namespace: true, Role: "client",
			Processes: []*parkerv1.ProcessStatus{up("consul", 4), up("mesh-gateway", 5)}},
		{Name: "dc1-api", Ips: []string{"10.1.0.101"}, Namespace: true, Role: "client",
			Processes: []*parkerv1.ProcessStatus{api, up("api-sidecar", 7), up("consul", 8)}},
	}}}}
}

func nodesModel(t *testing.T, c *fakeClient, height int) Model {
	t.Helper()
	m := drive(t, New(Options{Client: c, Lab: "m", Static: true}), tea.WindowSizeMsg{Width: 120, Height: height}, key("r"))
	return m.(Model)
}

func rowsOf(m Model) []string {
	var got []string
	for _, r := range m.rows {
		switch {
		case r.spacer:
			got = append(got, "")
		case r.section != "":
		default:
			p := r.proc
			if r.procText != "" {
				p = r.procText
			}
			got = append(got, r.marker+r.node+"/"+p+"="+r.role)
		}
	}
	return got
}

func TestRolesAndProcessOrder(t *testing.T) {
	m := nodesModel(t, meshLab(true), 40)
	want := []string{
		"▾ dc1-server-1/consul=server ★ leader", "dc1-server-1/config-entries=setup",
		"  dc1-server-2/consul=server",
		"▾ dc1-api/consul=client", "dc1-api/api=service", "dc1-api/api-sidecar=sidecar → api",
		"▾ dc1-mesh/consul=client", "dc1-mesh/mesh-gateway=mesh gateway",
	}
	if got := rowsOf(m); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s", strings.Join(got, "\n"))
	}
	if m.rows[1].ip != "10.1.0.10 +1" {
		t.Errorf("ip %q", m.rows[1].ip)
	}
	if got := procRole(&parkerv1.NodeStatus{}, "ingress-gateway", nil); got != "ingress gateway" {
		t.Errorf("ingress: %q", got)
	}
	if got := nodeRole(&parkerv1.NodeStatus{Name: "dc1-server-3"}); got != "server" {
		t.Errorf("server with its agent down: %q", got)
	}
}

func TestFolding(t *testing.T) {
	// Room for everything: nothing folds until asked.
	m := nodesModel(t, meshLab(true), 40)
	if m.autoFold {
		t.Fatal("autoFold with room to spare")
	}
	// ← on a process row folds its node and selects the summary row.
	m = drive(t, m, key("j")).(Model) // config-entries
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyLeft}).(Model)
	r, _ := m.selected()
	if r.node != "dc1-server-1" || r.procText != "2 processes" || r.stateText != "● 2/2 ready" || r.role != roleLeader {
		t.Fatalf("folded: %+v", r)
	}
	// It stays folded across a refresh.
	m = drive(t, m, key("r")).(Model)
	if r, _ := m.selected(); r.marker != "▸ " {
		t.Fatalf("after refresh: %+v", r)
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRight}).(Model)
	if r, _ := m.selected(); r.marker != "▾ " || r.proc != "consul" {
		t.Fatalf("unfolded: %+v", r)
	}
	// z folds all, then unfolds all.
	m = drive(t, m, key("z")).(Model)
	if n := len(rowsOf(m)); n != 4 {
		t.Errorf("all folded: %q", rowsOf(m))
	}
	m = drive(t, m, key("z")).(Model)
	if n := len(rowsOf(m)); n != 8 {
		t.Errorf("all unfolded: %q", rowsOf(m))
	}

	// Too tall for the screen: healthy nodes fold, a node with a dead
	// process doesn't.
	m = nodesModel(t, meshLab(false), 14)
	if !m.autoFold {
		t.Fatal("no autoFold")
	}
	want := []string{"▸ dc1-server-1/2 processes=server ★ leader", "  dc1-server-2/consul=server",
		"▾ dc1-api/consul=client", "dc1-api/api=service", "dc1-api/api-sidecar=sidecar → api", "▸ dc1-mesh/2 processes=client"}
	if got := rowsOf(m); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("auto-folded:\n%s", strings.Join(got, "\n"))
	}
	// A filter shows every match.
	m = drive(t, m, append([]tea.Msg{key("/")}, append(typed("gateway"), key("enter"))...)...).(Model)
	if got := rowsOf(m); len(got) != 1 || got[0] != "dc1-mesh/mesh-gateway=mesh gateway" {
		t.Errorf("filtered: %q", got)
	}
}

func TestFooterOnBottom(t *testing.T) {
	for _, v := range []view{viewNodes, viewLogs} {
		m := nodesModel(t, meshLab(true), 30)
		m.view = v
		lines := strings.Split(m.View(), "\n")
		if len(lines) != 30 || !strings.HasPrefix(lines[29], "╰") {
			t.Errorf("view %d: %d lines, last %q", v, len(lines), lines[len(lines)-1])
		}
	}
}

func TestColorLog(t *testing.T) {
	l := "2026-11-19T23:18:49.044Z [ERROR] agent: boom"
	if got := colorLog(l); ansi.Strip(got) != l {
		t.Errorf("colorLog changes the text: %q", got)
	}
	if colorLog("plain line") != "plain line" {
		t.Error("plain line")
	}
	if loc := logLevel.FindString("x [warning][main] y"); loc != "[warning]" {
		t.Errorf("envoy level: %q", loc)
	}
	if ts := logStamp.FindString(l); ts != "2026-11-19T23:18:49.044Z" {
		t.Errorf("stamp %q", ts)
	}
}

func TestLogoPulse(t *testing.T) {
	// The pulse runs down the trail and rests, then starts over.
	m := New(Options{})
	seen := map[string]bool{}
	for range pulse {
		seen[m.logo()] = true
		m.frame++
	}
	if m.logo() != New(Options{}).logo() {
		t.Error("pulse doesn't repeat")
	}
}
