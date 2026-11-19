package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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
	var cont []bool
	for i, r := range mm.rows {
		cont = append(cont, i > 0 && mm.rows[i-1].node == r.node)
	}
	if len(mm.rows) != 3 || cont[1] || !cont[2] {
		t.Fatalf("rows %+v", mm.rows)
	}
	c := mm.rows[2].cells(true)
	if c[0].text != "" || c[1].text != "" || c[2].text != "web" {
		t.Errorf("continuation row: %+v", c)
	}
	v := mm.View()
	if strings.Count(v, "dc2-web-1") != 1 {
		t.Errorf("dc2-web-1 listed once for its two processes:\n%s", v)
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
