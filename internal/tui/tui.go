// Package tui is `parker tui`: a k9s-style terminal UI over the daemon API.
// Browse labs and their nodes with live status, open a shell in a node,
// follow a process log, describe a node, break it on purpose (kill, stop,
// pause, partition) and play scenarios, without typing long commands.
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
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/scenario"
	"github.com/Ranjandas/parker/internal/spec"
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
	// Run plays a scenario on the daemon (a named one, or inline YAML),
	// calling line for each line of progress. Fault actions and :run need
	// it.
	Run func(ctx context.Context, lab, scenario, inline string, line func(string)) error
	// Forward listens on local (a port, or "" for any) and tunnels each
	// connection to node:remote through the daemon until ctx ends. It
	// returns the address it listens on.
	Forward func(ctx context.Context, lab, node, local, remote string) (string, error)
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
	viewRun // a scenario's progress, streamed like a log
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

func (r row) fields() []string {
	return []string{r.node, r.ip, r.proc, r.pid, r.state, r.restarts, r.role}
}

func (r row) matches(f string) bool {
	return strings.Contains(strings.ToLower(strings.Join(r.fields(), " ")), strings.ToLower(f))
}

// cells styles r for the grid. cont marks a further process of the node
// above, whose NODE and IP stay blank so the node reads as one group.
func (r row) cells(cont bool) []cell {
	node, ip := cell{r.node, titleStyle}, cell{r.ip, plain}
	if cont {
		node, ip = cell{}, cell{}
	}
	pid := cell{r.pid, mutedStyle}
	restarts := cell{r.restarts, mutedStyle}
	if r.restarts != "0" {
		restarts = cell{r.restarts, warnStyle}
	}
	return []cell{node, ip, {r.proc, plain}, pid, stateCell(r.state), restarts, roleCell(r.role)}
}

// Model is the Bubble Tea model.
type Model struct {
	o             Options
	view          view
	width, height int

	labs     []*parkerv1.LabSummary
	labTable grid

	lab       string
	status    *parkerv1.StatusResponse
	rows      []row // after filter
	nodeTable grid
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

	confirm *pending // an action waiting for y
	busy    string   // the action in flight

	forwards []forward // port-forwards running until quit or :pf stop

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
	actionDone  struct {
		label, last string
		err         error
	}
	runEndMsg struct{ err error }
)

type forward struct {
	desc   string
	cancel context.CancelFunc
}

// pending is a fault action shown for confirmation.
type pending struct {
	label string
	step  *spec.Step
}

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
		labTable:  newGrid(column{title: "LAB", flex: true}, column{title: "NAMESPACES"}, column{title: "SUPERVISED"}),
		nodeTable: newGrid(nodeColumns...),
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

// nodeColumns: NODE and IP give way on a narrow terminal.
var nodeColumns = []column{{title: "NODE", flex: true}, {title: "IP", flex: true}, {title: "PROCESS"},
	{title: "PID"}, {title: "STATE"}, {title: "RESTARTS"}, {title: "ROLE"}}

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
		rows := make([][]cell, len(msg.labs))
		for i, l := range msg.labs {
			sup := cell{"no", mutedStyle}
			if l.Supervised {
				sup = cell{"yes", okStyle}
			}
			rows[i] = []cell{{l.Name, titleStyle}, {strconv.Itoa(int(l.Namespaces)), plain}, sup}
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
	case runEndMsg:
		if m.view == viewRun {
			if msg.err != nil {
				m.flash = "scenario failed: " + msg.err.Error()
			} else {
				m.flash = "scenario passed"
			}
		}
		return m, waitLog(m.logCh)
	case actionDone:
		m.busy = ""
		if msg.err != nil {
			m.flash = msg.label + ": " + msg.err.Error()
		} else {
			m.flash = msg.label + ": done"
		}
		return m, m.refresh()
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

func (m *Model) resize() {
	h := max(m.height-4, 3) // header, info line, footer
	m.labTable.SetSize(m.width, h)
	m.nodeTable.SetSize(m.width, h)
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
	cells := make([][]cell, len(m.rows))
	for i, r := range m.rows {
		cells[i] = r.cells(i > 0 && m.rows[i-1].node == r.node)
	}
	m.nodeTable.SetRows(cells)
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
		m.stopForwards()
		return m, tea.Quit
	}
	if m.inputMode != inputNone {
		return m.inputKey(k)
	}
	m.flash = ""
	if p := m.confirm; p != nil {
		m.confirm = nil
		if k.String() == "y" || k.String() == "enter" {
			return m.act(p.label, p.step)
		}
		m.flash = "cancelled"
		return m, nil
	}
	switch k.String() {
	case "/":
		return m.startInput(inputFilter)
	case ":":
		return m.startInput(inputCommand)
	case "q":
		if m.view == viewLabs || (m.view == viewNodes && m.o.Lab != "") {
			m.stopForwards()
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
		case "K", "R", "S", "P", "x", "H":
			return m.hotkey(k.String())
		}
		var cmd tea.Cmd
		m.nodeTable, cmd = m.nodeTable.Update(k)
		return m, cmd
	case viewLogs, viewRun:
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
	case viewLogs, viewRun:
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
		case viewLogs, viewRun:
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
	case viewLogs, viewRun:
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
		m.stopForwards()
		return m, tea.Quit
	case "pf", "port-forward":
		return m.portForward(arg)
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
	case "run":
		if m.lab == "" {
			m.flash = ":run needs a lab open"
			return m, nil
		}
		return m.openRun(arg)
	case "partition", "heal", "kill", "stop", "start", "restart", "pause", "resume":
		if m.lab == "" {
			m.flash = ":" + verb + " needs a lab open"
			return m, nil
		}
		args := strings.Fields(arg)
		if len(args) == 0 && verb != "heal" {
			r, ok := m.selected()
			if !ok {
				m.flash = ":" + verb + " needs a selector or a selected node"
				return m, nil
			}
			args = []string{r.node}
		}
		st, err := scenario.FaultStep(verb, args)
		if err != nil {
			m.flash = err.Error()
			return m, nil
		}
		label := scenario.Label(st)
		if verb == "heal" || verb == "start" || verb == "resume" {
			return m.act(label, st)
		}
		m.confirm = &pending{label: label, step: st}
		return m, nil
	case "":
		return m, nil
	}
	m.flash = fmt.Sprintf("unknown command %q (try :labs, :lab NAME, :dc NAME, :kill, :partition, :heal, :run, :q)", verb)
	return m, nil
}

// ---------------------------------------------------------------- actions

// hotkey maps a nodes-view key to a fault on the selected node. Disruptive
// ones ask first.
func (m Model) hotkey(key string) (tea.Model, tea.Cmd) {
	if key == "H" {
		st, _ := scenario.FaultStep("heal", nil)
		return m.act(scenario.Label(st), st)
	}
	r, ok := m.selected()
	if !ok {
		return m, nil
	}
	action := map[string]string{"K": "kill", "R": "restart", "S": "stop", "P": "pause", "x": "partition"}[key]
	switch {
	case key == "S" && (r.state == "stopped" || r.state == "dead"):
		action = "start"
	case key == "P" && r.state == "paused":
		action = "resume"
	}
	return m.command(action + " " + r.node)
}

// portForward handles :pf [LOCAL:]REMOTE (from the selected node),
// :pf (list) and :pf stop.
func (m Model) portForward(arg string) (tea.Model, tea.Cmd) {
	switch arg {
	case "":
		if len(m.forwards) == 0 {
			m.flash = "no port-forwards; :pf [LOCAL:]REMOTE forwards from the selected node"
			return m, nil
		}
		var ds []string
		for _, f := range m.forwards {
			ds = append(ds, f.desc)
		}
		m.flash = strings.Join(ds, " · ")
		return m, nil
	case "stop":
		n := len(m.forwards)
		m.stopForwards()
		m.flash = fmt.Sprintf("stopped %d port-forwards", n)
		return m, nil
	}
	if m.o.Forward == nil {
		m.flash = "port-forward isn't available here"
		return m, nil
	}
	r, ok := m.selected()
	if !ok || m.view != viewNodes {
		m.flash = ":pf needs a node selected"
		return m, nil
	}
	local, remote, found := strings.Cut(arg, ":")
	if !found {
		local, remote = arg, arg
	}
	if _, err := strconv.Atoi(remote); err != nil {
		m.flash = ":pf [LOCAL:]REMOTE takes port numbers"
		return m, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	addr, err := m.o.Forward(ctx, m.lab, r.node, local, remote)
	if err != nil {
		cancel()
		m.flash = "port-forward: " + err.Error()
		return m, nil
	}
	desc := addr + " → " + r.node + ":" + remote
	m.forwards = append(m.forwards, forward{desc: desc, cancel: cancel})
	m.flash = "forwarding " + desc
	return m, nil
}

func (m *Model) stopForwards() {
	for _, f := range m.forwards {
		f.cancel()
	}
	m.forwards = nil
}

// act runs one fault step on the daemon.
func (m Model) act(label string, st *spec.Step) (tea.Model, tea.Cmd) {
	if m.o.Run == nil {
		m.flash = "actions need the daemon's Run API"
		return m, nil
	}
	if m.busy != "" {
		m.flash = "still running: " + m.busy
		return m, nil
	}
	y, err := scenario.Inline(st)
	if err != nil {
		m.flash = err.Error()
		return m, nil
	}
	m.busy = label
	run, lab := m.o.Run, m.lab
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		var last string
		err := run(ctx, lab, "", y, func(l string) {
			if strings.TrimSpace(l) != "" {
				last = l
			}
		})
		return actionDone{label: label, last: last, err: err}
	}
}

// openRun plays a scenario (name, or the lab's only one) and streams its
// progress; leaving the view stops it.
func (m Model) openRun(name string) (tea.Model, tea.Cmd) {
	if m.o.Run == nil {
		m.flash = ":run needs the daemon's Run API"
		return m, nil
	}
	m.stopLogs()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg, 64)
	target := "scenario"
	if name != "" {
		target = "scenario " + name
	}
	m.view, m.logTarget, m.logLines, m.logFilter, m.follow = viewRun, target, nil, "", true
	m.logCh, m.logCancel = ch, cancel
	m.renderLog()
	run, lab := m.o.Run, m.lab
	go func() {
		defer close(ch)
		err := run(ctx, lab, name, "", func(l string) {
			select {
			case ch <- logChunkMsg{l + "\n"}:
			case <-ctx.Done():
			}
		})
		if ctx.Err() == nil {
			ch <- runEndMsg{err}
		}
	}()
	return m, waitLog(ch)
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

func (m Model) View() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteByte('\n')
	switch m.view {
	case viewLabs:
		if len(m.labs) == 0 && m.err == nil {
			b.WriteString(mutedStyle.Render("no labs yet: parker up FILE"))
		} else {
			b.WriteString(m.labTable.View())
		}
	case viewNodes:
		b.WriteString(m.nodeTable.View())
	case viewLogs, viewDescribe, viewRun:
		b.WriteString(m.vp.View())
	}
	b.WriteByte('\n')
	b.WriteString(m.footer())
	return b.String()
}

// header is the breadcrumbs and what the view shows, with the daemon's
// address on the right when it fits.
func (m Model) header() string {
	crumbs := []string{}
	switch m.view {
	case viewNodes:
		crumbs = append(crumbs, m.lab)
		if m.dc != "" {
			crumbs = append(crumbs, m.dc)
		}
	case viewLogs:
		crumbs = append(crumbs, m.lab, m.logTarget, "logs")
	case viewRun:
		crumbs = append(crumbs, m.lab, m.logTarget)
	case viewDescribe:
		crumbs = append(crumbs, m.lab, m.describeTitle)
	}
	sep := mutedStyle.Render(" › ")
	h := " " + brandStyle.Render("▲ parker")
	for i, c := range crumbs {
		st := mutedStyle
		if i == len(crumbs)-1 {
			st = titleStyle
		}
		h += sep + st.Render(c)
	}
	var info []string
	switch m.view {
	case viewNodes:
		if m.status != nil {
			info = append(info, summary(m.status)...)
			if m.status.Supervised {
				info = append(info, mutedStyle.Render("supervised"))
			} else {
				info = append(info, warnStyle.Render("not supervised"))
			}
		}
		if n := len(m.forwards); n > 0 {
			info = append(info, plain.Render(fmt.Sprintf("%d port-forwards", n)))
		}
	case viewLogs:
		mode := "follow"
		if !m.follow {
			mode = "paused"
		}
		info = append(info, mutedStyle.Render(fmt.Sprintf("%d lines", len(m.logLines))), mutedStyle.Render(mode))
		if m.wrap {
			info = append(info, mutedStyle.Render("wrap"))
		}
	}
	if m.filter != "" && m.view == viewNodes {
		info = append(info, keyStyle.Render("filter ")+plain.Render(strconv.Quote(m.filter)))
	}
	if m.logFilter != "" && (m.view == viewLogs || m.view == viewRun) {
		info = append(info, keyStyle.Render("filter ")+plain.Render(strconv.Quote(m.logFilter)))
	}
	if len(info) > 0 {
		h += "   " + strings.Join(info, mutedStyle.Render(" · "))
	}
	if where := m.o.Where; where != "" {
		if gap := m.width - ansi.StringWidth(h) - ansi.StringWidth(where) - 1; gap >= 3 {
			h += strings.Repeat(" ", gap) + mutedStyle.Render(where)
		}
	}
	return ansi.Truncate(h, max(m.width, 10), "…")
}

// summary counts nodes and ready processes and names the leader, colored:
// green when every process is up, gold when some aren't, red when one died.
func summary(st *parkerv1.StatusResponse) []string {
	var total, ready, dead int
	var leaders []string
	for _, n := range st.Nodes {
		for _, p := range n.Processes {
			total++
			switch ProcState(p) {
			case "ready", "running":
				ready++
			case "dead":
				dead++
			}
		}
		if n.Role == "leader" {
			leaders = append(leaders, n.Name)
		}
	}
	up := okStyle
	switch {
	case dead > 0:
		up = errStyle
	case ready < total:
		up = warnStyle
	}
	out := []string{plain.Render(fmt.Sprintf("%d nodes", len(st.Nodes))), up.Render(fmt.Sprintf("%d/%d up", ready, total))}
	if len(leaders) > 0 {
		label := "leader "
		if len(leaders) > 1 {
			label = "leaders " // one per datacenter
		}
		out = append(out, mutedStyle.Render(label)+leadStyle.Render(strings.Join(leaders, ", ")))
	}
	return out
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
	if p := m.confirm; p != nil {
		return errStyle.Render(p.label+"?") + " " + keyStyle.Render("y") + mutedStyle.Render(" yes  any other key: no")
	}
	if m.flash != "" {
		return warnStyle.Render(m.flash)
	}
	if m.busy != "" {
		return mutedStyle.Render("running " + m.busy + "…")
	}
	var keys [][2]string
	switch m.view {
	case viewLabs:
		keys = [][2]string{{"enter", "open"}, {":", "command"}, {"q", "quit"}}
	case viewNodes:
		keys = [][2]string{{"s", "shell"}, {"l", "logs"}, {"d", "describe"}, {"K", "kill"}, {"R", "restart"}, {"S", "stop/start"},
			{"P", "pause"}, {"x", "partition"}, {"H", "heal"}, {"/", "filter"}, {":", "command"}, {"esc", "back"}}
	case viewLogs:
		keys = [][2]string{{"f", "follow"}, {"w", "wrap"}, {"/", "filter"}, {"g/G", "top/end"}, {"esc", "back"}}
	case viewDescribe:
		keys = [][2]string{{"↑/↓", "scroll"}, {"esc", "back"}}
	case viewRun:
		keys = [][2]string{{"f", "follow"}, {"w", "wrap"}, {"/", "filter"}, {"esc", "stop and back"}}
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = keyStyle.Render(k[0]) + " " + mutedStyle.Render(k[1])
	}
	return ansi.Truncate(" "+strings.Join(parts, "  "), max(m.width, 10), "…")
}
