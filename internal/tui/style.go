package tui

import "github.com/charmbracelet/lipgloss"

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
	plain      = lipgloss.NewStyle()
	brandStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	titleStyle = lipgloss.NewStyle().Bold(true)
	mutedStyle = lipgloss.NewStyle().Foreground(colMuted)
	errStyle   = lipgloss.NewStyle().Foreground(colRed)
	keyStyle   = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	okStyle    = lipgloss.NewStyle().Foreground(colGreen)
	warnStyle  = lipgloss.NewStyle().Foreground(colGold)
	headStyle  = lipgloss.NewStyle().Bold(true).Foreground(colMuted)
	ruleStyle  = lipgloss.NewStyle().Foreground(colRule)
	selStyle   = lipgloss.NewStyle().Bold(true).Foreground(colNavy).Background(lipgloss.Color("#FF7A1A"))
	leadStyle  = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
)

// stateCell is a process state with its mark and color.
func stateCell(s string) cell {
	switch s {
	case "ready":
		return cell{"● ready", okStyle}
	case "running":
		return cell{"● running", okStyle}
	case "starting":
		return cell{"◐ starting", warnStyle}
	case "dead":
		return cell{"✕ dead", errStyle}
	case "stopped":
		return cell{"■ stopped", mutedStyle}
	case "paused":
		return cell{"‖ paused", mutedStyle}
	}
	return cell{s, plain}
}

// roleCell is a Consul role: the leader stands out, clients recede.
func roleCell(r string) cell {
	switch r {
	case "leader":
		return cell{"★ leader", leadStyle}
	case "no-leader":
		return cell{"no leader", errStyle}
	case "client", "-", "":
		if r == "" {
			r = "-"
		}
		return cell{r, mutedStyle}
	}
	return cell{r, plain}
}
