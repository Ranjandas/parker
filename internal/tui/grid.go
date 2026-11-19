package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// column of a grid. Columns are as wide as their content; when the
// terminal is too narrow, flex columns give up width first (truncated
// with …) down to minFlex.
type column struct {
	title string
	flex  bool
}

// cell is a grid cell's text and the style it renders in when its row
// isn't selected.
type cell struct {
	text  string
	style lipgloss.Style
	// section marks a heading row (a datacenter): it spans the grid and
	// the cursor skips it.
	section bool
}

// sectionRow is a heading row spanning the grid.
func sectionRow(title string) []cell { return []cell{{text: title, section: true}} }

func isSection(r []cell) bool { return len(r) > 0 && r[0].section }

const (
	colGap  = 2
	minFlex = 8
)

// grid is a scrolling table with one selected row: the nodes and labs
// views. Unlike bubbles/table it sizes columns to their content and colors
// cells, so states and roles stand out.
type grid struct {
	cols           []column
	rows           [][]cell
	cursor, offset int
	width, height  int // height counts body rows (not the header)
}

func newGrid(cols ...column) grid { return grid{cols: cols, width: 80, height: 10} }

func (g *grid) SetRows(rows [][]cell) {
	g.rows = rows
	g.SetCursor(g.cursor)
}

func (g *grid) SetSize(width, height int) {
	g.width, g.height = max(width, 10), max(height, 1)
	g.SetCursor(g.cursor)
}

func (g grid) Cursor() int { return g.cursor }

// SetCursor moves the selection to c, clamped to the rows and off
// section headings (to the next row, else the one before), and scrolls
// to it with its heading in view.
func (g *grid) SetCursor(c int) {
	c = max(min(c, len(g.rows)-1), 0)
	for i := c; i < len(g.rows); i++ {
		if !isSection(g.rows[i]) {
			c = i
			goto found
		}
	}
	for i := c; i >= 0; i-- {
		if i < len(g.rows) && !isSection(g.rows[i]) {
			c = i
			break
		}
	}
found:
	g.cursor = c
	if g.cursor < g.offset {
		g.offset = g.cursor
	}
	if g.cursor > 0 && g.offset == g.cursor && isSection(g.rows[g.cursor-1]) {
		g.offset-- // keep the heading above the first row of its group
	}
	if g.cursor >= g.offset+g.height {
		g.offset = g.cursor - g.height + 1
	}
	g.offset = max(min(g.offset, len(g.rows)-g.height), 0)
}

// move steps the cursor by delta rows, past section headings.
func (g *grid) move(delta int) {
	c := g.cursor + delta
	step := 1
	if delta < 0 {
		step = -1
	}
	for c >= 0 && c < len(g.rows) && isSection(g.rows[c]) {
		c += step
	}
	if c < 0 || c >= len(g.rows) {
		return // nothing selectable that way
	}
	g.SetCursor(c)
}

// Update moves the selection: arrows or j/k, pgup/pgdown, g/G or home/end.
func (g grid) Update(k tea.KeyMsg) (grid, tea.Cmd) {
	switch k.String() {
	case "up", "k":
		g.move(-1)
	case "down", "j":
		g.move(1)
	case "pgup", "ctrl+u":
		g.SetCursor(max(g.cursor-g.height, 0))
	case "pgdown", "ctrl+d":
		g.SetCursor(g.cursor + g.height)
	case "home", "g":
		g.SetCursor(0)
	case "end", "G":
		g.SetCursor(len(g.rows) - 1)
	}
	return g, nil
}

// widths sizes each column to its content, then takes width from flex
// columns (widest first) until the row fits.
func (g grid) widths() []int {
	w := make([]int, len(g.cols))
	for i, c := range g.cols {
		w[i] = ansi.StringWidth(c.title)
	}
	for _, r := range g.rows {
		if isSection(r) {
			continue
		}
		for i := range min(len(r), len(w)) {
			w[i] = max(w[i], ansi.StringWidth(r[i].text))
		}
	}
	total := 1 + colGap*(len(w)-1)
	for _, x := range w {
		total += x
	}
	for total > g.width {
		best := -1
		for i, c := range g.cols {
			if c.flex && w[i] > minFlex && (best < 0 || w[i] > w[best]) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		cut := min(total-g.width, w[best]-minFlex)
		w[best] -= cut
		total -= cut
	}
	return w
}

func (g grid) View() string {
	w := g.widths()
	line := func(cells []cell, style func(cell) lipgloss.Style) string {
		var b strings.Builder
		b.WriteByte(' ')
		for i := range w {
			c := cell{}
			if i < len(cells) {
				c = cells[i]
			}
			t := ansi.Truncate(c.text, w[i], "…")
			t += strings.Repeat(" ", w[i]-ansi.StringWidth(t))
			if i < len(w)-1 {
				t += strings.Repeat(" ", colGap)
			}
			b.WriteString(style(c).Render(t))
		}
		return ansi.Truncate(b.String(), g.width, "")
	}
	var b strings.Builder
	head := make([]cell, len(g.cols))
	for i, c := range g.cols {
		head[i] = cell{text: c.title}
	}
	b.WriteString(line(head, func(cell) lipgloss.Style { return headStyle }))
	b.WriteByte('\n')
	b.WriteString(ruleStyle.Render(strings.Repeat("─", g.width)))
	for i := g.offset; i < g.offset+g.height; i++ {
		b.WriteByte('\n')
		if i >= len(g.rows) {
			continue
		}
		if isSection(g.rows[i]) {
			b.WriteString(sectionLine(g.rows[i][0].text, g.width))
			continue
		}
		if i == g.cursor {
			l := line(g.rows[i], func(cell) lipgloss.Style { return plain })
			l += strings.Repeat(" ", max(g.width-ansi.StringWidth(l), 0))
			b.WriteString(selStyle.Render(l))
			continue
		}
		b.WriteString(line(g.rows[i], func(c cell) lipgloss.Style { return c.style }))
	}
	return b.String()
}

// sectionLine is a heading: "── dc1 ───── 3 servers · 1 client ──".
func sectionLine(title string, width int) string {
	name, rest, _ := strings.Cut(title, "\t")
	l := ruleStyle.Render("─ ") + sectionStyle.Render(name) + " "
	if rest != "" {
		l += mutedStyle.Render(rest) + " "
	}
	return l + ruleStyle.Render(strings.Repeat("─", max(width-ansi.StringWidth(l), 0)))
}
