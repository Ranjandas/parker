package tui

import (
	"strings"
	"testing"

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
	want := []string{"dc1\t1 server|/", "|dc1-server-1/consul", "other|/", "|dc2-web-1/consul", "|dc2-web-1/web"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows %q", got)
	}
	c := mm.rows[4].cells(true)
	if c[0].text != "" || c[1].text != "" || c[2].text != "web" {
		t.Errorf("continuation row: %+v", c)
	}
	v := mm.View()
	if strings.Count(v, "dc2-web-1") != 1 || !strings.Contains(v, "─ dc1 1 server ─") {
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
	if roleCell("leader").text != "★ leader" || roleCell("").text != "-" || roleCell("follower").text != "follower" {
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
