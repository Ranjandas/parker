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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/expand"
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
	// Static turns the logo animation off (NO_COLOR, slow links).
	Static bool
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

// row is one line of the nodes table: a process on a node, a folded
// node's summary, or a datacenter heading.
type row struct {
	node, ip, proc, pid, state, restarts, role string
	section                                    string // a datacenter heading, not a process
	spacer                                     bool   // a blank line between datacenters
	// A node with several processes has a fold marker; folded, its one
	// row sums them up: procText and stateText replace proc and state
	// (proc stays its first process, for logs).
	marker, procText, stateText string
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
	node, ip := cell{text: r.marker + r.node, style: titleStyle}, cell{text: r.ip, style: plain}
	if cont {
		node, ip = cell{}, cell{}
	}
	pid := cell{text: r.pid, style: mutedStyle}
	restarts := cell{text: r.restarts, style: mutedStyle}
	if r.restarts != "0" {
		restarts = cell{text: r.restarts, style: warnStyle}
	}
	proc, state := cell{text: r.proc, style: plain}, stateCell(r.state)
	if r.procText != "" {
		proc = cell{text: r.procText, style: mutedStyle}
	}
	if r.stateText != "" {
		state.text = r.stateText
	}
	return []cell{node, ip, proc, pid, state, restarts, roleCell(r.role)}
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
	// fold is each node folded or unfolded by hand; other nodes fold when
	// the lab overflows the screen (autoFold), unless something's wrong
	// on them.
	fold     map[string]bool
	autoFold bool

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
	frame   int // logo animation
}

// Messages.
type (
	tickMsg   struct{}
	animMsg   struct{}
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
		fold:      map[string]bool{},
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
	if m.o.Static {
		return tea.Batch(m.refresh(), tick(m.o.Refresh))
	}
	return tea.Batch(m.refresh(), tick(m.o.Refresh), animate())
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
	case animMsg:
		m.frame++
		return m, animate()
	case labsMsg:
		m.err = nil
		m.labs = msg.labs
		m.updated = time.Now()
		rows := make([][]cell, len(msg.labs))
		for i, l := range msg.labs {
			sup := cell{text: "no", style: mutedStyle}
			if l.Supervised {
				sup = cell{text: "yes", style: okStyle}
			}
			rows[i] = []cell{{text: l.Name, style: titleStyle}, {text: strconv.Itoa(int(l.Namespaces)), style: plain}, sup}
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
	h := m.bodyHeight()
	m.labTable.SetSize(m.width, h-2) // less the table's header and rule
	m.nodeTable.SetSize(m.width, h-2)
	m.vp.Width, m.vp.Height = m.width, h
	if m.view == viewLogs || m.view == viewRun {
		m.renderLog()
	}
	m.applyRows() // auto-folding depends on the height
}

func (m Model) openLab(name string) (tea.Model, tea.Cmd) {
	m.lab, m.view, m.status, m.filter, m.dc = name, viewNodes, nil, "", ""
	m.fold = map[string]bool{}
	m.nodeTable.SetRows(nil)
	return m, m.refresh()
}

// applyRows flattens the status into table rows and applies the filters.
// Nodes are grouped by datacenter (a heading row each), servers first,
// then clients, then nodes without an agent. The selection stays on the
// same process (or its node) when rows come and go.
func (m *Model) applyRows() {
	sel, hadSel := m.selected()
	m.rows = m.rows[:0]
	if m.status == nil {
		m.nodeTable.SetRows(nil)
		return
	}
	var groups []nodeGroup
	lines := 0
	for _, g := range groupNodes(m.status.Nodes) {
		if m.dc != "" && g.dc != m.dc {
			continue
		}
		var nodes []*parkerv1.NodeStatus
		for _, n := range g.nodes {
			if k := len(m.procRows(n)); k > 0 {
				nodes = append(nodes, n)
				lines += k
			}
		}
		if len(nodes) > 0 {
			g.nodes = nodes
			groups = append(groups, g)
		}
	}
	headings := m.dc == "" && len(groups) > 0 && (groups[0].dc != "" || len(groups) > 1)
	if headings {
		lines += 2*len(groups) - 1
	}
	m.autoFold = lines > m.nodeTable.height
	for i, g := range groups {
		if headings {
			if i > 0 {
				m.rows = append(m.rows, row{spacer: true})
			}
			m.rows = append(m.rows, row{section: g.title()})
		}
		for _, n := range g.nodes {
			m.rows = append(m.rows, m.nodeRows(n)...)
		}
	}
	if !slices.ContainsFunc(m.rows, func(r row) bool { return r.marker == "▾ " || r.marker == "▸ " }) {
		for i := range m.rows {
			m.rows[i].marker = "" // nothing folds: no gutter
		}
	}
	cells := make([][]cell, len(m.rows))
	for i, r := range m.rows {
		switch {
		case r.spacer:
			cells[i] = sectionRow("")
		case r.section != "":
			cells[i] = sectionRow(r.section)
		default:
			cells[i] = r.cells(i > 0 && m.rows[i-1].node == r.node)
		}
	}
	m.nodeTable.SetRows(cells)
	if hadSel {
		m.selectRow(sel.node, sel.proc)
	}
}

// selectRow moves the cursor to node's process proc, else to the node.
func (m *Model) selectRow(node, proc string) {
	at := -1
	for i, r := range m.rows {
		if r.node != node || r.section != "" || r.spacer {
			continue
		}
		if r.proc == proc {
			at = i
			break
		}
		if at < 0 {
			at = i
		}
	}
	if at >= 0 {
		m.nodeTable.SetCursor(at)
	}
}

// procRows is a node's processes as rows, in display order (the agent,
// gateways, each service then its sidecar, setup jobs), after the filter.
func (m *Model) procRows(n *parkerv1.NodeStatus) []row {
	ip := "-"
	if len(n.Ips) > 0 {
		ip = n.Ips[0]
	}
	if len(n.Ips) > 1 {
		ip += fmt.Sprintf(" +%d", len(n.Ips)-1) // the rest are in describe
	}
	if !n.Namespace {
		ip += " (no netns)"
	}
	names := map[string]bool{}
	for _, p := range n.Processes {
		names[p.Name] = true
	}
	procs := slices.Clone(n.Processes)
	slices.SortStableFunc(procs, func(a, b *parkerv1.ProcessStatus) int {
		ka, kb := procOrder(a.Name), procOrder(b.Name)
		if ka.rank != kb.rank {
			return ka.rank - kb.rank
		}
		if c := strings.Compare(ka.base, kb.base); c != 0 {
			return c
		}
		return ka.sub - kb.sub
	})
	var out []row
	for _, p := range procs {
		r := row{node: n.Name, ip: ip, proc: p.Name, pid: "-", state: ProcState(p), restarts: strconv.Itoa(int(p.Restarts)), role: procRole(n, p.Name, names)}
		if p.Pid > 0 {
			r.pid = strconv.Itoa(int(p.Pid))
		}
		if m.filter == "" || r.matches(m.filter) {
			out = append(out, r)
		}
	}
	return out
}

// nodeRows is a node's rows: its processes, or one summary row when it's
// folded.
func (m *Model) nodeRows(n *parkerv1.NodeStatus) []row {
	rows := m.procRows(n)
	if len(rows) < 2 {
		for i := range rows {
			rows[i].marker = "  " // line up with the fold markers
		}
		return rows
	}
	marker := "▾ "
	if !m.folded(n.Name, rows) {
		rows[0].marker = marker
		return rows
	}
	worst, ok, restarts := "ready", 0, 0
	for _, r := range rows {
		if stateRank[r.state] > stateRank[worst] {
			worst = r.state
		}
		if r.state == "ready" || r.state == "running" {
			ok++
		}
		k, _ := strconv.Atoi(r.restarts)
		restarts += k
	}
	count := ok
	if worst != "ready" && worst != "running" {
		count = 0
		for _, r := range rows {
			if r.state == worst {
				count++
			}
		}
	}
	mark, _, _ := strings.Cut(stateCell(worst).text, " ")
	return []row{{
		node: n.Name, ip: rows[0].ip, proc: rows[0].proc, pid: "", state: worst, restarts: strconv.Itoa(restarts),
		role: nodeRole(n), marker: "▸ ", procText: plural(len(rows), "process"),
		stateText: fmt.Sprintf("%s %d/%d %s", mark, count, len(rows), worst),
	}}
}

// stateRank orders states from fine to worst, for a folded node's state.
var stateRank = map[string]int{"ready": 0, "running": 1, "starting": 2, "paused": 3, "stopped": 4, "dead": 5}

// folded says whether a node shows as one row: as folded by hand, else
// when the lab overflows the screen and all is well on the node. Filtering
// unfolds everything, so matches show.
func (m *Model) folded(node string, rows []row) bool {
	if m.filter != "" {
		return false
	}
	if f, ok := m.fold[node]; ok {
		return f
	}
	if !m.autoFold {
		return false
	}
	for _, r := range rows {
		if r.state != "ready" && r.state != "running" {
			return false
		}
	}
	return true
}

// setFold folds or unfolds the selected node (toggles when fold is nil)
// and keeps it selected.
func (m *Model) setFold(fold *bool) {
	r, ok := m.selected()
	if !ok || !m.isFoldable(r.node) {
		return
	}
	want := r.marker != "▸ " // expanded: fold it
	if fold != nil {
		want = *fold
	}
	m.fold[r.node] = want
	m.applyRows()
	m.selectRow(r.node, "") // the node's first row
}

// isFoldable: the node has more than one process showing.
func (m *Model) isFoldable(node string) bool {
	for _, n := range m.status.GetNodes() {
		if n.Name == node {
			return len(m.procRows(n)) > 1
		}
	}
	return false
}

// toggleAll folds every node, or unfolds them all when none is unfolded.
func (m *Model) toggleAll() {
	anyOpen := false
	for _, r := range m.rows {
		if r.marker == "▾ " {
			anyOpen = true
		}
	}
	for _, n := range m.status.GetNodes() {
		m.fold[n.Name] = anyOpen
	}
	sel, _ := m.selected()
	m.applyRows()
	m.selectRow(sel.node, "")
}

// Roles a consul process shows for its server.
const (
	roleLeader   = "server ★"
	roleNoLeader = "server (no leader)"
)

// nodeRole is what a node's agent is: server (and whether it leads),
// client, or "-" with no agent.
func nodeRole(n *parkerv1.NodeStatus) string {
	switch n.Role {
	case "leader":
		return roleLeader
	case "no-leader":
		return roleNoLeader
	case "follower":
		return "server"
	case "client":
		return "client"
	}
	if kind(n) == 0 {
		return "server" // agent down
	}
	return "-"
}

var gatewayKinds = []string{"mesh", "ingress", "terminating", "api"}

// procRole is what a process is on its node: the agent's role, a gateway
// of some kind, a service, its sidecar, or a one-shot setup job.
func procRole(n *parkerv1.NodeStatus, name string, names map[string]bool) string {
	switch {
	case name == "consul":
		return nodeRole(n)
	case name == expand.ConfigEntries || name == expand.ACLSetup || strings.HasPrefix(name, "peering-"):
		return "setup"
	case strings.HasSuffix(name, expand.SidecarSuffix):
		return "sidecar → " + strings.TrimSuffix(name, expand.SidecarSuffix)
	case names[name+expand.SidecarSuffix]:
		return "service"
	}
	if k, ok := strings.CutSuffix(name, "-gateway"); ok && slices.Contains(gatewayKinds, k) {
		return k + " gateway"
	}
	return "-"
}

type procKey struct {
	rank, sub int
	base      string
}

// procOrder sorts a node's processes: the agent, gateways, services each
// followed by its sidecar, then setup jobs.
func procOrder(name string) procKey {
	switch {
	case name == "consul":
		return procKey{rank: 0}
	case strings.HasSuffix(name, "-gateway"):
		return procKey{rank: 1, base: name}
	case name == expand.ConfigEntries || name == expand.ACLSetup || strings.HasPrefix(name, "peering-"):
		return procKey{rank: 3, base: name}
	case strings.HasSuffix(name, expand.SidecarSuffix):
		return procKey{rank: 2, base: strings.TrimSuffix(name, expand.SidecarSuffix), sub: 1}
	}
	return procKey{rank: 2, base: name}
}

// nodeGroup is a datacenter's nodes in display order.
type nodeGroup struct {
	dc               string // "" for nodes outside any datacenter
	nodes            []*parkerv1.NodeStatus
	servers, clients int
	rows             []row
}

func (g nodeGroup) title() string {
	if g.dc == "" {
		return "other"
	}
	parts := []string{plural(g.servers, "server")}
	if g.clients > 0 {
		parts = append(parts, plural(g.clients, "client"))
	}
	if other := len(g.nodes) - g.servers - g.clients; other > 0 {
		parts = append(parts, plural(other, "other node"))
	}
	return g.dc + "\t" + strings.Join(parts, " · ")
}

func plural(n int, s string) string {
	if n == 1 {
		return "1 " + s
	}
	if strings.HasSuffix(s, "s") {
		return strconv.Itoa(n) + " " + s + "es"
	}
	return strconv.Itoa(n) + " " + s + "s"
}

func isServer(role string) bool {
	return role == "leader" || role == "follower" || role == "no-leader"
}

// serverName matches Parker's server node names, <dc>-server-<n>.
var serverName = regexp.MustCompile(`^([^-]+)-server-\d+$`)

// kind ranks a node: 0 server, 1 client agent, 2 no agent. A server whose
// agent is down still ranks (and groups) as a server by its name.
func kind(n *parkerv1.NodeStatus) int {
	switch {
	case isServer(n.Role) || serverName.MatchString(n.Name):
		return 0
	case n.Role == "client" || slices.ContainsFunc(n.Processes, func(p *parkerv1.ProcessStatus) bool { return p.Name == "consul" }):
		return 1
	}
	return 2
}

// groupNodes sorts nodes into datacenters: a node's dc is its name's
// prefix up to "-" when some server shares that prefix (Parker names
// nodes <dc>-<name>, servers <dc>-server-<n>). Within a dc servers come first, then clients, then
// nodes without an agent, each by name; dcs go by name, "other" last.
func groupNodes(nodes []*parkerv1.NodeStatus) []nodeGroup {
	dcs := map[string]bool{}
	for _, n := range nodes {
		if dc, _, ok := strings.Cut(n.Name, "-"); ok && kind(n) == 0 {
			dcs[dc] = true
		}
	}
	byDC := map[string]*nodeGroup{}
	var order []string
	for _, n := range nodes {
		dc, _, _ := strings.Cut(n.Name, "-")
		if !dcs[dc] {
			dc = ""
		}
		g := byDC[dc]
		if g == nil {
			g = &nodeGroup{dc: dc}
			byDC[dc] = g
			order = append(order, dc)
		}
		g.nodes = append(g.nodes, n)
		switch kind(n) {
		case 0:
			g.servers++
		case 1:
			g.clients++
		}
	}
	slices.SortFunc(order, func(a, b string) int {
		if (a == "") != (b == "") {
			if a == "" {
				return 1
			}
			return -1
		}
		return strings.Compare(a, b)
	})
	out := make([]nodeGroup, len(order))
	for i, dc := range order {
		g := byDC[dc]
		slices.SortStableFunc(g.nodes, func(a, b *parkerv1.NodeStatus) int {
			if ra, rb := kind(a), kind(b); ra != rb {
				return ra - rb
			}
			return strings.Compare(a.Name, b.Name)
		})
		out[i] = *g
	}
	return out
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
	if c < 0 || c >= len(m.rows) || m.rows[c].section != "" || m.rows[c].spacer {
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
		case "right":
			f := false
			m.setFold(&f)
			return m, nil
		case "left":
			f := true
			m.setFold(&f)
			return m, nil
		case " ", "space":
			m.setFold(nil)
			return m, nil
		case "z":
			m.toggleAll()
			return m, nil
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
		l = colorLog(l)
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
	var body string
	switch m.view {
	case viewLabs:
		if len(m.labs) == 0 && m.err == nil {
			body = mutedStyle.Render(" no labs yet: parker up FILE")
		} else {
			body = m.labTable.View()
		}
	case viewNodes:
		body = m.nodeTable.View()
	case viewLogs, viewDescribe, viewRun:
		body = m.vp.View()
	}
	foot, border := m.footer()
	// A blank line either side of the body, which is padded (or cut) to
	// its height so the footer always sits on the bottom line.
	return box(m.logo(), m.header(), m.width, ruleStyle) + "\n\n" +
		fitLines(body, m.bodyHeight()) + "\n\n" +
		box("", foot, m.width, border)
}

// bodyHeight is the lines between the header and footer boxes (3 lines
// each) and the blank line inside each.
func (m Model) bodyHeight() int { return max(m.height-8, 3) }

// fitLines pads s with empty lines, or cuts it, to exactly n lines.
func fitLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
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
	if len(crumbs) == 0 {
		crumbs = []string{"labs"}
	}
	var hs []string
	for i, c := range crumbs {
		st := mutedStyle
		if i == len(crumbs)-1 {
			st = titleStyle
		}
		hs = append(hs, st.Render(c))
	}
	h := strings.Join(hs, mutedStyle.Render(" › "))
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
		if gap := innerWidth(m.width) - ansi.StringWidth(h) - ansi.StringWidth(where); gap >= 3 {
			h += strings.Repeat(" ", gap) + mutedStyle.Render(where)
		}
	}
	return h
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

// footer is the key hints, or the input, error, question or progress in
// their place, with the border color that goes with it.
func (m Model) footer() (string, lipgloss.Style) {
	switch m.inputMode {
	case inputFilter:
		return keyStyle.Render("/") + m.input.View(), keyBorder
	case inputCommand:
		return keyStyle.Render(":") + m.input.View(), keyBorder
	}
	if m.err != nil {
		return errStyle.Render("error: " + m.err.Error()), errBorder
	}
	if p := m.confirm; p != nil {
		return errStyle.Render(p.label+"?") + " " + keyStyle.Render("y") + mutedStyle.Render(" yes  any other key: no"), errBorder
	}
	if m.flash != "" {
		return warnStyle.Render(m.flash), warnBorder
	}
	if m.busy != "" {
		return mutedStyle.Render("running " + m.busy + "…"), warnBorder
	}
	var keys [][2]string
	switch m.view {
	case viewLabs:
		keys = [][2]string{{"enter", "open"}, {":", "command"}, {"q", "quit"}}
	case viewNodes:
		keys = [][2]string{{"s", "shell"}, {"l", "logs"}, {"d", "describe"}, {"←/→", "fold"}, {"K", "kill"}, {"R", "restart"}, {"S", "stop/start"},
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
	return strings.Join(parts, "  "), ruleStyle
}

// logStamp and logLevel find a log line's leading timestamp and its
// [LEVEL] tag (Consul's format, Envoy's lower case too).
var (
	logStamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ][0-9:.]+(Z|[+-]\d{2}:?\d{2})?`)
	logLevel = regexp.MustCompile(`(?i)\[(error|err|warn|warning|info|debug|trace)\]`)
)

// colorLog mutes a log line's timestamp and colors its level: errors red,
// warnings gold, debug and trace muted.
func colorLog(l string) string {
	if strings.Contains(l, "\x1b") {
		return l // already colored
	}
	ts := logStamp.FindString(l)
	rest := l[len(ts):]
	if loc := logLevel.FindStringIndex(rest); loc != nil {
		tag := rest[loc[0]:loc[1]]
		st := plain
		switch strings.ToLower(tag[1:2]) {
		case "e":
			st = errStyle
		case "w":
			st = warnStyle
		case "d", "t":
			st = mutedStyle
		}
		rest = rest[:loc[0]] + st.Render(tag) + rest[loc[1]:]
	}
	if ts != "" {
		ts = mutedStyle.Render(ts)
	}
	return ts + rest
}
