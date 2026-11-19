package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Palette: the Parker logo colors (orange #FF7A1A, red #FF4D2E, gold
// #FFC94A, navy #0F1B2D, cream #F4F1EA) on dark terminals, and deeper
// shades of the same hues on light ones so text keeps its contrast.
var (
	colAccent = lipgloss.AdaptiveColor{Dark: "#FF7A1A", Light: "#C2410C"}
	colRed    = lipgloss.AdaptiveColor{Dark: "#FF4D2E", Light: "#C81E1E"}
	colGold   = lipgloss.AdaptiveColor{Dark: "#FFC94A", Light: "#A16207"}
	colGreen  = lipgloss.AdaptiveColor{Dark: "#5BD68A", Light: "#15803D"}
	colMuted  = lipgloss.AdaptiveColor{Dark: "#9AA4B2", Light: "#5B6472"}
	colRule   = lipgloss.AdaptiveColor{Dark: "#3B4252", Light: "#C9CED6"}
	colNavy   = lipgloss.Color("#0F1B2D")
)

var (
	plain        = lipgloss.NewStyle()
	brandStyle   = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	titleStyle   = lipgloss.NewStyle().Bold(true)
	mutedStyle   = lipgloss.NewStyle().Foreground(colMuted)
	errStyle     = lipgloss.NewStyle().Foreground(colRed)
	keyStyle     = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	okStyle      = lipgloss.NewStyle().Foreground(colGreen)
	warnStyle    = lipgloss.NewStyle().Foreground(colGold)
	headStyle    = lipgloss.NewStyle().Bold(true).Foreground(colMuted)
	ruleStyle    = lipgloss.NewStyle().Foreground(colRule)
	selStyle     = lipgloss.NewStyle().Bold(true).Foreground(colNavy).Background(lipgloss.Color("#FF7A1A"))
	leadStyle    = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(colGold)
)

// stateCell is a process state with its mark and color.
func stateCell(s string) cell {
	switch s {
	case "ready":
		return cell{text: "● ready", style: okStyle}
	case "running":
		return cell{text: "● running", style: okStyle}
	case "starting":
		return cell{text: "◐ starting", style: warnStyle}
	case "dead":
		return cell{text: "✕ dead", style: errStyle}
	case "stopped":
		return cell{text: "■ stopped", style: mutedStyle}
	case "paused":
		return cell{text: "‖ paused", style: mutedStyle}
	}
	return cell{text: s, style: plain}
}

// roleCell is a Consul role: the leader stands out, clients recede.
func roleCell(r string) cell {
	switch r {
	case "leader":
		return cell{text: "★ leader", style: leadStyle}
	case "no-leader":
		return cell{text: "no leader", style: errStyle}
	case "client", "-", "":
		if r == "" {
			r = "-"
		}
		return cell{text: r, style: mutedStyle}
	}
	return cell{text: r, style: plain}
}

var (
	keyBorder  = lipgloss.NewStyle().Foreground(colAccent)
	warnBorder = lipgloss.NewStyle().Foreground(colGold)
	errBorder  = lipgloss.NewStyle().Foreground(colRed)
)

// innerWidth is the content width of a box as wide as the terminal.
func innerWidth(width int) int { return max(width, 20) - 4 }

// box draws content (one line) in a rounded border the width of the
// terminal, with title set into the top edge:
//
//	╭─ title ─────────╮
//	│ content         │
//	╰─────────────────╯
func box(title, content string, width int, border lipgloss.Style) string {
	width = max(width, 20)
	inner := innerWidth(width)
	content = ansi.Truncate(content, inner, "…")
	content += strings.Repeat(" ", max(inner-ansi.StringWidth(content), 0))
	top := border.Render("╭─")
	used := 2
	if title != "" {
		top += " " + title + " "
		used += ansi.StringWidth(title) + 2
	}
	top += border.Render(strings.Repeat("─", max(width-used-1, 0)) + "╮")
	mid := border.Render("│") + " " + content + " " + border.Render("│")
	bot := border.Render("╰" + strings.Repeat("─", width-2) + "╯")
	return top + "\n" + mid + "\n" + bot
}

// The logo, after the monogram: speed lines trailing a sun. The lines
// shimmer from gold through orange to red as frames advance, so Parker
// looks like it's moving.
var trail = []lipgloss.TerminalColor{colGold, colAccent, colRed, colRule}

const trailLen = 6

func (m Model) logo() string {
	var b strings.Builder
	for i := range trailLen {
		c := trail[(i-m.frame%len(trail)+len(trail))%len(trail)]
		if m.o.Static {
			c = trail[min(trailLen-1-i, len(trail)-1)] // a still gradient, brightest at the sun
		}
		b.WriteString(lipgloss.NewStyle().Foreground(c).Render("━"))
	}
	return b.String() + brandStyle.Render("● parker")
}

func animate() tea.Cmd {
	return tea.Tick(140*time.Millisecond, func(time.Time) tea.Msg { return animMsg{} })
}
