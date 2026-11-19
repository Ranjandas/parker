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
	for _, want := range []string{"parker › a", "dc1-server-1", "leader", "dc2-web-1", "running", "3 nodes", "leader dc1-server-1"} {
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
	m = drive(t, m, key("esc")) // clears the filter
	if n := len(m.(Model).rows); n != 3 {
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
