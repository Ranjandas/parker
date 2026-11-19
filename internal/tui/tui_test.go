package tui

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	tea "github.com/charmbracelet/bubbletea"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/scenario"
)

type fakeClient struct {
	labs   []*parkerv1.LabSummary
	status map[string]*parkerv1.StatusResponse
}

func (f *fakeClient) ListLabs(context.Context, *connect.Request[parkerv1.ListLabsRequest]) (*connect.Response[parkerv1.ListLabsResponse], error) {
	return connect.NewResponse(&parkerv1.ListLabsResponse{Labs: f.labs}), nil
}

func (f *fakeClient) Status(_ context.Context, r *connect.Request[parkerv1.StatusRequest]) (*connect.Response[parkerv1.StatusResponse], error) {
	st, ok := f.status[r.Msg.Lab]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoLab)
	}
	return connect.NewResponse(st), nil
}

func (f *fakeClient) Describe(_ context.Context, r *connect.Request[parkerv1.DescribeRequest]) (*connect.Response[parkerv1.DescribeResponse], error) {
	return connect.NewResponse(&parkerv1.DescribeResponse{Node: r.Msg.Node, Yaml: r.Msg.Node + ":\n  networks: {}\n"}), nil
}

func (f *fakeClient) Logs(context.Context, *connect.Request[parkerv1.LogsRequest]) (*connect.ServerStreamForClient[parkerv1.LogChunk], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errNoLab)
}

var errNoLab = connectErr("no such lab")

type connectErr string

func (e connectErr) Error() string { return string(e) }

func sample() *fakeClient {
	yes := true
	return &fakeClient{
		labs: []*parkerv1.LabSummary{{Name: "a", Namespaces: 2, Supervised: true}, {Name: "b", Namespaces: 1}},
		status: map[string]*parkerv1.StatusResponse{"a": {Lab: "a", Supervised: true, Nodes: []*parkerv1.NodeStatus{
			{Name: "dc1-server-1", Ips: []string{"10.1.0.10"}, Namespace: true, Role: "leader",
				Processes: []*parkerv1.ProcessStatus{{Name: "consul", Pid: 10, Alive: true, Ready: &yes}}},
			{Name: "dc2-web-1", Ips: []string{"10.2.0.101"}, Namespace: true, Role: "client",
				Processes: []*parkerv1.ProcessStatus{{Name: "consul", Pid: 11, Alive: true, Ready: &yes}, {Name: "web", Pid: 12, Alive: true}}},
		}}},
	}
}

// drive feeds msg to m and runs the resulting commands (except ticks and
// blocking ones) until there are none left.
func drive(t *testing.T, m tea.Model, msgs ...tea.Msg) tea.Model {
	t.Helper()
	queue := msgs
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		queue = append(queue, run(cmd)...)
	}
	return m
}

func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	// Commands that wait (cursor blink, ticks) are dropped.
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(50 * time.Millisecond):
		return nil
	}
	switch msg := msg.(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, run(c)...)
		}
		return out
	case tickMsg, nil:
		return nil
	}
	return []tea.Msg{msg}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typed(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		out = append(out, key(string(r)))
	}
	return out
}

func TestLabsThenNodes(t *testing.T) {
	m := tea.Model(New(Options{Client: sample(), Where: "test.sock"}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30}, labsMsgFrom(t, m))
	v := m.View()
	if !strings.Contains(v, "a") || !strings.Contains(v, "enter open") {
		t.Fatalf("labs view:\n%s", v)
	}
	m = drive(t, m, key("enter"))
	v = m.View()
	for _, want := range []string{"━● parker", "│ a   2 nodes", "dc1-server-1", "leader", "dc2-web-1", "running", "3 nodes", "leader dc1-server-1"} {
		if want == "3 nodes" {
			want = "2 nodes"
		}
		if !strings.Contains(v, want) {
			t.Errorf("nodes view missing %q:\n%s", want, v)
		}
	}
}

func labsMsgFrom(t *testing.T, m tea.Model) tea.Msg {
	return m.(Model).refresh()()
}

func TestFilterAndDC(t *testing.T) {
	m := tea.Model(New(Options{Client: sample(), Lab: "a"}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30}, m.(Model).refresh()())
	m = drive(t, m, append([]tea.Msg{key("/")}, append(typed("web"), key("enter"))...)...)
	mm := m.(Model)
	if len(mm.rows) != 2 || mm.rows[0].node != "dc2-web-1" {
		t.Fatalf("filter web: %+v", mm.rows)
	}
	m = drive(t, m, key("esc"))           // clears the filter
	if n := len(m.(Model).rows); n != 5 { // 3 processes under 2 headings
		t.Fatalf("after esc: %d rows", n)
	}
	m = drive(t, m, append([]tea.Msg{key(":")}, append(typed("dc dc1"), key("enter"))...)...)
	mm = m.(Model)
	if len(mm.rows) != 1 || mm.rows[0].node != "dc1-server-1" || !strings.Contains(mm.View(), "a › dc1") {
		t.Fatalf(":dc dc1: %+v\n%s", mm.rows, mm.View())
	}
	m = drive(t, m, append([]tea.Msg{key(":")}, append(typed("bogus"), key("enter"))...)...)
	if !strings.Contains(m.View(), "unknown command") {
		t.Errorf("no error for bogus command:\n%s", m.View())
	}
}

func TestDescribeAndBack(t *testing.T) {
	m := tea.Model(New(Options{Client: sample(), Lab: "a"}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 20}, m.(Model).refresh()(), key("down"), key("d"))
	mm := m.(Model)
	if mm.view != viewDescribe || !strings.Contains(mm.View(), "dc2-web-1:") {
		t.Fatalf("describe:\n%s", mm.View())
	}
	m = drive(t, m, key("esc"))
	if m.(Model).view != viewNodes {
		t.Fatal("esc from describe")
	}
}

func TestShellUsesSelectedNode(t *testing.T) {
	var gotLab, gotNode string
	shell := func(lab, node string) *exec.Cmd {
		gotLab, gotNode = lab, node
		return exec.Command("true")
	}
	m := tea.Model(New(Options{Client: sample(), Lab: "a", Shell: shell}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 20}, m.(Model).refresh()(), key("down"), key("down"))
	_, cmd := m.Update(key("s"))
	if cmd == nil || gotLab != "a" || gotNode != "dc2-web-1" {
		t.Errorf("shell for %q/%q (cmd %v)", gotLab, gotNode, cmd != nil)
	}
}

func TestLogBufferAndFilter(t *testing.T) {
	m := New(Options{Client: sample(), Lab: "a"})
	m.view, m.width = viewLogs, 80
	m.appendLog("one\ntw")
	m.appendLog("o\nthree agent\n")
	if got := strings.Join(m.logLines, ""); got != "one\ntwo\nthree agent\n" {
		t.Fatalf("lines %q", m.logLines)
	}
	m.logFilter = "agent"
	m.renderLog()
	if v := m.vp.View(); !strings.Contains(v, "three agent") || strings.Contains(v, "one") {
		t.Errorf("filtered view:\n%s", v)
	}
	for range maxLogLines + 10 {
		m.appendLog("x\n")
	}
	if len(m.logLines) != maxLogLines {
		t.Errorf("buffer not capped: %d", len(m.logLines))
	}
}

func TestStatusErrorShown(t *testing.T) {
	m := tea.Model(New(Options{Client: sample(), Lab: "nope"}))
	m = drive(t, m, m.(Model).refresh()())
	if !strings.Contains(m.View(), "error: no such lab") {
		t.Errorf("error not shown:\n%s", m.View())
	}
}

func TestOneLabOpensOnceThenEscStaysOnLabs(t *testing.T) {
	c := sample()
	c.labs = c.labs[:1]
	m := tea.Model(New(Options{Client: c}))
	m = drive(t, m, m.(Model).refresh()())
	if m.(Model).view != viewNodes {
		t.Fatal("single lab not opened")
	}
	m = drive(t, m, key("esc"))
	if m.(Model).view != viewLabs {
		t.Fatalf("esc reopened the lab: view %d", m.(Model).view)
	}
}

func TestProcState(t *testing.T) {
	yes, no := true, false
	for want, p := range map[string]*parkerv1.ProcessStatus{
		"stopped":  {Stopped: true},
		"dead":     {},
		"paused":   {Alive: true, Paused: true, Ready: &yes},
		"running":  {Alive: true},
		"ready":    {Alive: true, Ready: &yes},
		"starting": {Alive: true, Ready: &no},
	} {
		if got := ProcState(p); got != want {
			t.Errorf("%+v: %s, want %s", p, got, want)
		}
	}
}

type runCall struct{ lab, scenario, inline string }

// fakeRun records Run calls and answers with lines and err.
func fakeRun(calls *[]runCall, lines []string, err error) func(context.Context, string, string, string, func(string)) error {
	return func(_ context.Context, lab, sc, inline string, line func(string)) error {
		*calls = append(*calls, runCall{lab, sc, inline})
		for _, l := range lines {
			line(l)
		}
		return err
	}
}

func cmdline(s string) []tea.Msg {
	return append([]tea.Msg{key(":")}, append(typed(s), key("enter"))...)
}

func TestKillAsksFirst(t *testing.T) {
	var calls []runCall
	m := tea.Model(New(Options{Client: sample(), Lab: "a", Run: fakeRun(&calls, []string{"[ 0s] step 1: kill dc1-server-1"}, nil)}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 20}, m.(Model).refresh()(), key("K"))
	if v := m.View(); !strings.Contains(v, "kill dc1-server-1?") {
		t.Fatalf("no confirmation:\n%s", v)
	}
	m = drive(t, m, key("n"))
	if len(calls) != 0 || !strings.Contains(m.View(), "cancelled") {
		t.Fatalf("n should cancel: %v\n%s", calls, m.View())
	}
	m = drive(t, m, key("K"), key("y"))
	if len(calls) != 1 || calls[0] != (runCall{"a", "", "steps:\n- kill: dc1-server-1\n"}) {
		t.Fatalf("calls = %+v", calls)
	}
	if v := m.View(); !strings.Contains(v, "kill dc1-server-1: done") {
		t.Errorf("no result:\n%s", v)
	}
}

func TestToggleKeysFollowState(t *testing.T) {
	c := sample()
	p := c.status["a"].Nodes[0].Processes[0]
	p.Alive, p.Stopped = false, true
	var calls []runCall
	m := tea.Model(New(Options{Client: c, Lab: "a", Run: fakeRun(&calls, nil, nil)}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 20}, m.(Model).refresh()(), key("S"))
	// Starting is safe: no confirmation.
	if len(calls) != 1 || !strings.Contains(calls[0].inline, "start: dc1-server-1") {
		t.Fatalf("S on a stopped node: %+v", calls)
	}
	p.Alive, p.Stopped, p.Paused = true, false, true
	m = drive(t, m, m.(Model).refresh()(), key("P"))
	if len(calls) != 2 || !strings.Contains(calls[1].inline, "resume: dc1-server-1") {
		t.Fatalf("P on a paused node: %+v", calls)
	}
	p.Paused = false
	m = drive(t, m, m.(Model).refresh()(), key("P"), key("y"))
	m = drive(t, m, key("S"), key("y"))
	if len(calls) != 4 || !strings.Contains(calls[2].inline, "pause: dc1-server-1") || !strings.Contains(calls[3].inline, "stop: dc1-server-1") {
		t.Fatalf("P, S on a running node: %+v", calls)
	}
}

func TestPartitionHealAndErrors(t *testing.T) {
	var calls []runCall
	run := fakeRun(&calls, nil, nil)
	m := tea.Model(New(Options{Client: sample(), Lab: "a", Run: run}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 20}, m.(Model).refresh()(), key("x"))
	if v := m.View(); !strings.Contains(v, "partition dc1-server-1 from the rest?") {
		t.Fatalf("x:\n%s", v)
	}
	m = drive(t, m, key("y"))
	m = drive(t, m, cmdline("partition dc1-server-1 dc2")...)
	m = drive(t, m, key("y"))
	m = drive(t, m, key("H"))
	want := []string{
		"steps:\n- partition:\n  - dc1-server-1\n",
		"steps:\n- partition:\n  - dc1-server-1\n  - dc2\n",
		"steps:\n- heal: all\n",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v", calls)
	}
	for i, w := range want {
		if calls[i].inline != w {
			t.Errorf("call %d inline %q, want %q", i, calls[i].inline, w)
		}
	}

	// A failed action says why.
	c := sample()
	m = tea.Model(New(Options{Client: c, Lab: "a", Run: fakeRun(&calls, nil, connectErr("needs the lab supervised"))}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 20}, m.(Model).refresh()(), key("R"), key("y"))
	if v := m.View(); !strings.Contains(v, "restart dc1-server-1: needs the lab supervised") {
		t.Errorf("error not shown:\n%s", v)
	}
	m = drive(t, m, cmdline("kill a b")...)
	if v := m.View(); !strings.Contains(v, "kill takes one selector") {
		t.Errorf("bad args:\n%s", v)
	}

	// Without the Run API, actions explain themselves.
	m = tea.Model(New(Options{Client: c, Lab: "a"}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 20}, m.(Model).refresh()(), key("H"))
	if v := m.View(); !strings.Contains(v, "actions need the daemon") {
		t.Errorf("no Run:\n%s", v)
	}
}

func TestRunScenarioView(t *testing.T) {
	var calls []runCall
	lines := []string{"[    0s] step 1: wait dc1", "[  1.0s] PASS: 1 steps"}
	m := tea.Model(New(Options{Client: sample(), Lab: "a", Run: fakeRun(&calls, lines, nil)}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 20}, m.(Model).refresh()())
	m = drive(t, m, cmdline("run leader-loss")...)
	mm := m.(Model)
	if mm.view != viewRun || len(calls) != 1 || calls[0].scenario != "leader-loss" || calls[0].inline != "" {
		t.Fatalf("view %d calls %+v", mm.view, calls)
	}
	v := mm.View()
	for _, w := range []string{"a › scenario leader-loss", "step 1: wait dc1", "PASS: 1 steps", "scenario passed"} {
		if !strings.Contains(v, w) {
			t.Errorf("run view missing %q:\n%s", w, v)
		}
	}
	m = drive(t, m, key("esc"))
	if m.(Model).view != viewNodes {
		t.Error("esc from the run view")
	}

	m = tea.Model(New(Options{Client: sample(), Lab: "a", Run: fakeRun(&calls, nil, connectErr("step 2 (assert consul): no leader"))}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 20}, m.(Model).refresh()())
	m = drive(t, m, cmdline("run")...)
	if v := m.View(); !strings.Contains(v, "scenario failed: step 2") || !strings.Contains(v, "a › scenario") {
		t.Errorf("failed run:\n%s", v)
	}
}

func TestOneActionAtATime(t *testing.T) {
	m := New(Options{Client: sample(), Lab: "a", Run: func(context.Context, string, string, string, func(string)) error { return nil }})
	m.busy = "kill dc1-server-1"
	st, _ := scenario.FaultStep("heal", nil)
	mm, cmd := m.act("heal all", st)
	if cmd != nil || !strings.Contains(mm.View(), "still running: kill dc1-server-1") {
		t.Errorf("second action started:\n%s", mm.View())
	}
}

func TestPortForward(t *testing.T) {
	type fw struct{ lab, node, local, remote string }
	var got []fw
	var ctxs []context.Context
	forward := func(ctx context.Context, lab, node, local, remote string) (string, error) {
		got, ctxs = append(got, fw{lab, node, local, remote}), append(ctxs, ctx)
		if remote == "9999" {
			return "", connectErr("address in use")
		}
		return "127.0.0.1:" + local, nil
	}
	m := tea.Model(New(Options{Client: sample(), Lab: "a", Forward: forward}))
	m = drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 20}, m.(Model).refresh()())
	m = drive(t, m, cmdline("pf 18500:8500")...)
	if len(got) != 1 || got[0] != (fw{"a", "dc1-server-1", "18500", "8500"}) {
		t.Fatalf("forward calls %+v", got)
	}
	if v := m.View(); !strings.Contains(v, "forwarding 127.0.0.1:18500 → dc1-server-1:8500") || !strings.Contains(v, "1 port-forwards") {
		t.Errorf("view:\n%s", v)
	}
	m = drive(t, m, key("down"))
	m = drive(t, m, cmdline("pf 8080")...)
	if got[1] != (fw{"a", "dc2-web-1", "8080", "8080"}) {
		t.Errorf("second forward %+v", got[1])
	}
	m = drive(t, m, cmdline("pf 9999")...)
	if v := m.View(); !strings.Contains(v, "port-forward: address in use") {
		t.Errorf("error:\n%s", v)
	}
	m = drive(t, m, cmdline("pf x")...)
	if v := m.View(); !strings.Contains(v, "takes port numbers") {
		t.Errorf("bad port:\n%s", v)
	}
	m = drive(t, m, cmdline("pf")...)
	if v := m.View(); !strings.Contains(v, "→ dc2-web-1:8080") {
		t.Errorf("list:\n%s", v)
	}
	m = drive(t, m, cmdline("pf stop")...)
	if v := m.View(); !strings.Contains(v, "stopped 2 port-forwards") || ctxs[0].Err() == nil || ctxs[1].Err() == nil || ctxs[2].Err() == nil {
		t.Errorf("stop:\n%s", v)
	}
}
