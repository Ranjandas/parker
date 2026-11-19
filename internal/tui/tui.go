// Package tui is `parker tui`: a k9s-style terminal UI over the daemon API.
// Browse labs and their nodes with live status, open a shell in a node,
// follow a process log, or describe a node, without typing long commands.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
)

// Client is the part of the daemon API the TUI uses.
type Client interface {
	ListLabs(context.Context, *connect.Request[parkerv1.ListLabsRequest]) (*connect.Response[parkerv1.ListLabsResponse], error)
	Status(context.Context, *connect.Request[parkerv1.StatusRequest]) (*connect.Response[parkerv1.StatusResponse], error)
	Describe(context.Context, *connect.Request[parkerv1.DescribeRequest]) (*connect.Response[parkerv1.DescribeResponse], error)
	Logs(context.Context, *connect.Request[parkerv1.LogsRequest]) (*connect.ServerStreamForClient[parkerv1.LogChunk], error)
}

// Options configure the TUI.
type Options struct {
	Client Client
	// Where is shown in the header: the daemon socket or URL.
	Where string
	// Lab opens straight into this lab's nodes.
	Lab string
	// Shell returns the command that opens a shell in a node (run with the
	// terminal handed over, e.g. `parker exec LAB NODE`).
	Shell func(lab, node string) *exec.Cmd
	// Refresh is the status poll interval; default 2s.
	Refresh time.Duration
}

// Run starts the TUI and returns when the user quits.
func Run(o Options) error {
	_, err := tea.NewProgram(New(o), tea.WithAltScreen()).Run()
	return err
}

type view int

const (
	viewLabs view = iota
	viewNodes
	viewLogs
	viewDescribe
)

type inputMode int

const (
	inputNone inputMode = iota
	inputFilter
	inputCommand
)

// row is one line of the nodes table: a process on a node.
type row struct {
	node, ip, proc, pid, state, restarts, role string
}

func (r row) cells() table.Row {
	return table.Row{r.node, r.ip, r.proc, r.pid, r.state, r.restarts, r.role}
}

func (r row) matches(f string) bool {
	return strings.Contains(strings.ToLower(strings.Join(r.cells(), " ")), strings.ToLower(f))
}

// Model is the Bubble Tea model.
type Model struct {
	o             Options
	view          view
	width, height int

	labs     []*parkerv1.LabSummary
	labTable table.Model

	lab       string
	status    *parkerv1.StatusResponse
	rows      []row // after filter
	nodeTable table.Model
	filter    string // nodes view filter
	dc        string // :dc prefix filter

	input     textinput.Model
	inputMode inputMode

	logTarget string // node/process
	logLines  []string
	logFilter string
	follow    bool
	wrap      bool
	logCh     chan tea.Msg
	logCancel context.CancelFunc
	vp        viewport.Model

	describeTitle string
	autoOpened    bool

	err     error
	flash   string
	updated time.Time
}

// Messages.
type (
	tickMsg   struct{}
	labsMsg   struct{ labs []*parkerv1.LabSummary }
	statusMsg struct {
		lab string
		st  *parkerv1.StatusResponse
	}
	describeMsg struct{ node, yaml string }
	logChunkMsg struct{ data string }
	logEndMsg   struct{ err error }
	errMsg      struct{ err error }
	shellDone   struct{ err error }
)

const maxLogLines = 5000

// New builds the model; exported for tests.
func New(o Options) Model {
	if o.Refresh == 0 {
		o.Refresh = 2 * time.Second
	}
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 200
	m := Model{
		o:         o,
		labTable:  newTable([]table.Column{{Title: "LAB", Width: 24}, {Title: "NAMESPACES", Width: 10}, {Title: "SUPERVISED", Width: 10}}),
		nodeTable: newTable(nodeColumns(80)),
		input:     ti,
		follow:    true,
		vp:        viewport.New(80, 20),
		width:     80,
		height:    24,
	}
	if o.Lab != "" {
		m.lab, m.view = o.Lab, viewNodes
	}
	return m
}

func newTable(cols []table.Column) table.Model {
	t := table.New(table.WithColumns(cols), table.WithFocused(true), table.WithHeight(10))
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).BorderStyle(lipgloss.NormalBorder()).BorderBottom(true).BorderForeground(lipgloss.Color("8"))
	s.Selected = s.Selected.Foreground(lipgloss.Color("0")).Background(lipgloss.Color("12")).Bold(false)
	t.SetStyles(s)
	return t
}

// nodeColumns share the width: NODE and IP grow, the rest are fixed.
func nodeColumns(width int) []table.Column {
	fixed := 10 + 8 + 9 + 8 + 10 // PROCESS PID STATE RESTARTS ROLE
	rest := max(width-fixed-14, 30)
	return []table.Column{
		{Title: "NODE", Width: rest * 3 / 5}, {Title: "IP", Width: rest * 2 / 5},
		{Title: "PROCESS", Width: 10}, {Title: "PID", Width: 8}, {Title: "STATE", Width: 9},
		{Title: "RESTARTS", Width: 8}, {Title: "ROLE", Width: 10},
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), tick(m.o.Refresh))
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) refresh() tea.Cmd {
	c := m.o.Client
	switch m.view {
	case viewLabs:
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, err := c.ListLabs(ctx, connect.NewRequest(&parkerv1.ListLabsRequest{}))
			if err != nil {
				return errMsg{rpcErr(err)}
			}
			return labsMsg{r.Msg.Labs}
		}
	case viewNodes:
		lab := m.lab
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, err := c.Status(ctx, connect.NewRequest(&parkerv1.StatusRequest{Lab: lab}))
			if err != nil {
				return errMsg{rpcErr(err)}
			}
			return statusMsg{lab, r.Msg}
		}
	}
	return nil
}

func rpcErr(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return errors.New(ce.Message())
	}
	return err
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.refresh(), tick(m.o.Refresh))
	case labsMsg:
		m.err = nil
		m.labs = msg.labs
		m.updated = time.Now()
		rows := make([]table.Row, len(msg.labs))
		for i, l := range msg.labs {
			rows[i] = table.Row{l.Name, strconv.Itoa(int(l.Namespaces)), yesNo(l.Supervised)}
		}
		m.labTable.SetRows(rows)
		// One lab and nothing chosen yet: open it.
		if len(msg.labs) == 1 && m.view == viewLabs && m.lab == "" && !m.autoOpened {
			m.autoOpened = true
			return m.openLab(msg.labs[0].Name)
		}
		return m, nil
	case statusMsg:
		if msg.lab != m.lab {
			return m, nil
		}
		m.err = nil
		m.status = msg.st
		m.updated = time.Now()
		m.applyRows()
		return m, nil
	case describeMsg:
		m.describeTitle = msg.node
		m.view = viewDescribe
		m.vp.SetContent(msg.yaml)
		m.vp.GotoTop()
		return m, nil
	case logChunkMsg:
		m.appendLog(msg.data)
		return m, waitLog(m.logCh)
	case logEndMsg:
		if msg.err != nil && m.view == viewLogs {
			m.flash = "log stream ended: " + msg.err.Error()
		}
		return m, nil
	case errMsg:
		m.err = msg.err
		return m, nil
	case shellDone:
		if msg.err != nil {
			m.flash = "shell: " + msg.err.Error()
		}
		return m, m.refresh()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (m *Model) resize() {
	h := max(m.height-4, 3) // header, info line, footer
	m.labTable.SetHeight(h)
	m.labTable.SetWidth(m.width)
	m.nodeTable.SetColumns(nodeColumns(m.width))
	m.nodeTable.SetHeight(h)
	m.nodeTable.SetWidth(m.width)
	m.vp.Width, m.vp.Height = m.width, h
	if m.view == viewLogs {
		m.renderLog()
	}
}

func (m Model) openLab(name string) (tea.Model, tea.Cmd) {
	m.lab, m.view, m.status, m.filter, m.dc = name, viewNodes, nil, "", ""
	m.nodeTable.SetRows(nil)
	return m, m.refresh()
}

// applyRows flattens the status into table rows and applies the filters.
func (m *Model) applyRows() {
	m.rows = m.rows[:0]
	if m.status == nil {
		m.nodeTable.SetRows(nil)
		return
	}
	for _, n := range m.status.Nodes {
		if m.dc != "" && !strings.HasPrefix(n.Name, m.dc+"-") {
			continue
		}
		ip := strings.Join(n.Ips, ",")
		if !n.Namespace {
			ip += " (no netns)"
		}
		for _, p := range n.Processes {
			r := row{node: n.Name, ip: ip, proc: p.Name, pid: "-", state: ProcState(p), restarts: strconv.Itoa(int(p.Restarts)), role: "-"}
			if p.Pid > 0 {
				r.pid = strconv.Itoa(int(p.Pid))
			}
			if p.Name == "consul" && n.Role != "" {
				r.role = n.Role
			}
			if m.filter == "" || r.matches(m.filter) {
				m.rows = append(m.rows, r)
			}
		}
	}
	trows := make([]table.Row, len(m.rows))
	for i, r := range m.rows {
		trows[i] = r.cells()
	}
	m.nodeTable.SetRows(trows)
	if c := m.nodeTable.Cursor(); c >= len(trows) && len(trows) > 0 {
		m.nodeTable.SetCursor(len(trows) - 1)
	}
}

// ProcState is a process's STATE column: stopped, dead, paused, running, ready
// or starting.
func ProcState(p *parkerv1.ProcessStatus) string {
	switch {
	case p.Stopped:
		return "stopped"
	case !p.Alive:
		return "dead"
	case p.Paused:
		return "paused"
	}
	if p.Ready == nil {
		return "running"
	}
	if *p.Ready {
		return "ready"
	}
	return "starting"
}

func (m Model) selected() (row, bool) {
	c := m.nodeTable.Cursor()
	if c < 0 || c >= len(m.rows) {
		return row{}, false
	}
	return m.rows[c], true
}

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		m.stopLogs()
		return m, tea.Quit
	}
	if m.inputMode != inputNone {
		return m.inputKey(k)
	}
	m.flash = ""
	switch k.String() {
	case "/":
		return m.startInput(inputFilter)
	case ":":
		return m.startInput(inputCommand)
	case "q":
		if m.view == viewLabs || (m.view == viewNodes && m.o.Lab != "") {
			return m, tea.Quit
		}
		return m.back()
	case "esc":
		return m.back()
	}
	switch m.view {
	case viewLabs:
		switch k.String() {
		case "enter":
			if c := m.labTable.Cursor(); c >= 0 && c < len(m.labs) {
				return m.openLab(m.labs[c].Name)
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.labTable, cmd = m.labTable.Update(k)
		return m, cmd
	case viewNodes:
		switch k.String() {
		case "s", "enter":
			r, ok := m.selected()
			if !ok || m.o.Shell == nil {
				return m, nil
			}
			return m, tea.ExecProcess(m.o.Shell(m.lab, r.node), func(err error) tea.Msg { return shellDone{err} })
		case "l":
			r, ok := m.selected()
			if !ok {
				return m, nil
			}
			return m.openLogs(r.node, r.proc)
		case "d":
			r, ok := m.selected()
			if !ok {
				return m, nil
			}
			c, lab := m.o.Client, m.lab
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				resp, err := c.Describe(ctx, connect.NewRequest(&parkerv1.DescribeRequest{Lab: lab, Node: r.node}))
				if err != nil {
					return errMsg{rpcErr(err)}
				}
				return describeMsg{resp.Msg.Node, resp.Msg.Yaml}
			}
		case "r":
			return m, m.refresh()
		}
		var cmd tea.Cmd
		m.nodeTable, cmd = m.nodeTable.Update(k)
		return m, cmd
	case viewLogs:
		switch k.String() {
		case "f":
			m.follow = !m.follow
			if m.follow {
				m.vp.GotoBottom()
			}
			return m, nil
		case "w":
			m.wrap = !m.wrap
			m.renderLog()
			return m, nil
		case "G", "end":
			m.follow = true
			m.vp.GotoBottom()
			return m, nil
		case "g", "home":
			m.follow = false
			m.vp.GotoTop()
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		if !m.vp.AtBottom() {
			m.follow = false
		}
		return m, cmd
	case viewDescribe:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		return m, cmd
	}
	return m, nil
}

func (m Model) back() (tea.Model, tea.Cmd) {
	switch m.view {
	case viewLogs:
		m.stopLogs()
		m.view = viewNodes
		return m, m.refresh()
	case viewDescribe:
		m.view = viewNodes
		return m, nil
	case viewNodes:
		if m.filter != "" || m.dc != "" {
			m.filter, m.dc = "", ""
			m.applyRows()
			return m, nil
		}
		m.view, m.lab, m.status = viewLabs, "", nil
		return m, m.refresh()
	}
	return m, nil
}

func (m Model) startInput(mode inputMode) (tea.Model, tea.Cmd) {
	m.inputMode = mode
	m.input.SetValue("")
	if mode == inputFilter {
		switch m.view {
		case viewNodes:
			m.input.SetValue(m.filter)
		case viewLogs:
			m.input.SetValue(m.logFilter)
		}
		m.input.CursorEnd()
	}
	return m, m.input.Focus()
}

func (m Model) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.inputMode = inputNone
		m.input.Blur()
		return m, nil
	case "enter":
		mode, val := m.inputMode, strings.TrimSpace(m.input.Value())
		m.inputMode = inputNone
		m.input.Blur()
		if mode == inputCommand {
			return m.command(val)
		}
		return m.setFilter(val), nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	if m.inputMode == inputFilter {
		m = m.setFilter(m.input.Value()) // filter as you type
	}
	return m, cmd
}

func (m Model) setFilter(f string) Model {
	switch m.view {
	case viewNodes:
		m.filter = f
		m.applyRows()
	case viewLogs:
		m.logFilter = f
		m.renderLog()
	}
	return m
}

// command runs a `:` command.
func (m Model) command(c string) (tea.Model, tea.Cmd) {
	verb, arg, _ := strings.Cut(c, " ")
	arg = strings.TrimSpace(arg)
	switch verb {
	case "q", "quit":
		m.stopLogs()
		return m, tea.Quit
	case "labs", "lab":
		if arg == "" {
			m.stopLogs()
			m.view, m.lab, m.status = viewLabs, "", nil
			return m, m.refresh()
		}
		m.stopLogs()
		return m.openLab(arg)
	case "dc":
		if m.lab == "" {
			m.flash = ":dc needs a lab open"
			return m, nil
		}
		m.stopLogs()
		m.view, m.dc = viewNodes, arg
		m.applyRows()
		return m, nil
	case "":
		return m, nil
	}
	m.flash = fmt.Sprintf("unknown command %q (try :labs, :lab NAME, :dc NAME, :q)", verb)
	return m, nil
}

// ---------------------------------------------------------------- logs

func (m Model) openLogs(node, proc string) (tea.Model, tea.Cmd) {
	m.stopLogs()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg, 64)
	m.view, m.logTarget, m.logLines, m.logFilter, m.follow = viewLogs, node+"/"+proc, nil, "", true
	m.logCh, m.logCancel = ch, cancel
	m.renderLog()
	c, lab := m.o.Client, m.lab
	go func() {
		defer close(ch)
		s, err := c.Logs(ctx, connect.NewRequest(&parkerv1.LogsRequest{Lab: lab, Node: node, Process: proc, TailLines: 500, Follow: true}))
		if err != nil {
			ch <- logEndMsg{rpcErr(err)}
			return
		}
		defer s.Close()
		for s.Receive() {
			select {
			case ch <- logChunkMsg{string(s.Msg().Data)}:
			case <-ctx.Done():
				return
			}
		}
		if err := s.Err(); err != nil && ctx.Err() == nil {
			ch <- logEndMsg{rpcErr(err)}
		}
	}()
	return m, waitLog(ch)
}

func waitLog(ch chan tea.Msg) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return logEndMsg{}
		}
		return msg
	}
}

func (m *Model) stopLogs() {
	if m.logCancel != nil {
		m.logCancel()
		m.logCancel = nil
	}
}

func (m *Model) appendLog(data string) {
	parts := strings.Split(data, "\n")
	if n := len(m.logLines); n > 0 && !strings.HasSuffix(m.logLines[n-1], "\n") {
		// Continue a partial last line.
		m.logLines[n-1] += parts[0]
		parts = parts[1:]
		if len(parts) > 0 {
			m.logLines[n-1] += "\n"
		}
	}
	for i, p := range parts {
		if i == len(parts)-1 {
			if p != "" {
				m.logLines = append(m.logLines, p) // partial
			}
			break
		}
		m.logLines = append(m.logLines, p+"\n")
	}
	if over := len(m.logLines) - maxLogLines; over > 0 {
		m.logLines = slices.Delete(m.logLines, 0, over)
	}
	m.renderLog()
}

func (m *Model) renderLog() {
	var b strings.Builder
	f := strings.ToLower(m.logFilter)
	for _, l := range m.logLines {
		l = strings.TrimSuffix(l, "\n")
		if f != "" && !strings.Contains(strings.ToLower(l), f) {
			continue
		}
		if m.wrap {
			l = ansi.Hardwrap(l, max(m.width, 10), true)
		} else {
			l = ansi.Truncate(l, max(m.width, 10), "…")
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	m.vp.SetContent(strings.TrimSuffix(b.String(), "\n"))
	if m.follow {
		m.vp.GotoBottom()
	}
}

// ---------------------------------------------------------------- view

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	keyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

func (m Model) View() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteByte('\n')
	switch m.view {
	case viewLabs:
		if len(m.labs) == 0 && m.err == nil {
			b.WriteString(dimStyle.Render("no labs yet: parker up FILE"))
		} else {
			b.WriteString(m.labTable.View())
		}
	case viewNodes:
		b.WriteString(m.nodeTable.View())
	case viewLogs, viewDescribe:
		b.WriteString(m.vp.View())
	}
	b.WriteByte('\n')
	b.WriteString(m.footer())
	return b.String()
}

func (m Model) header() string {
	crumbs := []string{"parker"}
	switch m.view {
	case viewNodes:
		crumbs = append(crumbs, m.lab)
		if m.dc != "" {
			crumbs = append(crumbs, m.dc)
		}
	case viewLogs:
		crumbs = append(crumbs, m.lab, m.logTarget, "logs")
	case viewDescribe:
		crumbs = append(crumbs, m.lab, m.describeTitle)
	}
	h := titleStyle.Render(strings.Join(crumbs, " › "))
	info := m.o.Where
	switch m.view {
	case viewNodes:
		if m.status != nil {
			sup := "not supervised"
			if m.status.Supervised {
				sup = "supervised"
			}
			info += " · " + summary(m.status) + " · " + sup
		}
	case viewLogs:
		mode := "follow"
		if !m.follow {
			mode = "paused"
		}
		info += fmt.Sprintf(" · %d lines · %s", len(m.logLines), mode)
		if m.wrap {
			info += " · wrap"
		}
	}
	if m.filter != "" && m.view == viewNodes {
		info += " · filter " + strconv.Quote(m.filter)
	}
	if m.logFilter != "" && m.view == viewLogs {
		info += " · filter " + strconv.Quote(m.logFilter)
	}
	return h + "  " + dimStyle.Render(info)
}

// summary counts ready processes and names the leader.
func summary(st *parkerv1.StatusResponse) string {
	var total, ready int
	leader := ""
	for _, n := range st.Nodes {
		for _, p := range n.Processes {
			total++
			if s := ProcState(p); s == "ready" || s == "running" {
				ready++
			}
		}
		if n.Role == "leader" {
			leader = n.Name
		}
	}
	s := fmt.Sprintf("%d nodes · %d/%d up", len(st.Nodes), ready, total)
	if leader != "" {
		s += " · leader " + leader
	}
	return s
}

func (m Model) footer() string {
	switch m.inputMode {
	case inputFilter:
		return keyStyle.Render("/") + m.input.View()
	case inputCommand:
		return keyStyle.Render(":") + m.input.View()
	}
	if m.err != nil {
		return errStyle.Render("error: " + m.err.Error())
	}
	if m.flash != "" {
		return errStyle.Render(m.flash)
	}
	var keys [][2]string
	switch m.view {
	case viewLabs:
		keys = [][2]string{{"enter", "open"}, {":", "command"}, {"q", "quit"}}
	case viewNodes:
		keys = [][2]string{{"s", "shell"}, {"l", "logs"}, {"d", "describe"}, {"/", "filter"}, {":", "command"}, {"esc", "back"}}
	case viewLogs:
		keys = [][2]string{{"f", "follow"}, {"w", "wrap"}, {"/", "filter"}, {"g/G", "top/end"}, {"esc", "back"}}
	case viewDescribe:
		keys = [][2]string{{"↑/↓", "scroll"}, {"esc", "back"}}
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = keyStyle.Render(k[0]) + " " + dimStyle.Render(k[1])
	}
	return strings.Join(parts, "  ")
}
