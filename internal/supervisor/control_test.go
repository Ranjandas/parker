package supervisor

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Ranjandas/parker/internal/spec"
)

// sleeper supervises `sleep 60` as node n's process name, the way runOne
// does but without a network namespace.
func sleeper(t *testing.T, h *Handle, name, policy string, deps ...string) *proc {
	t.Helper()
	p := &proc{id: "n/" + name, node: "n", name: name, spec: &spec.Process{Restart: policy},
		deps: map[string]string{}, kick: make(chan struct{}, 1), exited: make(chan struct{}), logPath: "/dev/null"}
	for _, d := range deps {
		p.deps[d] = "ready"
	}
	start := func() (<-chan error, error) {
		cmd := exec.Command("sleep", "60")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		runDone := make(chan struct{})
		p.mu.Lock()
		p.pid, p.alive, p.runDone = cmd.Process.Pid, true, runDone
		p.mu.Unlock()
		done := make(chan error, 1)
		go func() {
			err := cmd.Wait()
			p.mu.Lock()
			p.alive, p.isReady, p.paused = false, false, false
			p.mu.Unlock()
			close(runDone)
			done <- err
		}()
		return done, nil
	}
	done, err := start()
	if err != nil {
		t.Fatal(err)
	}
	h.procs[p.id] = p
	go func() { h.supervise(p, <-done, start, func(string, ...any) {}) }()
	t.Cleanup(func() {
		if pid, alive, _ := p.state(); alive {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	return p
}

func testHandle(t *testing.T) *Handle {
	life, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	up := make(chan struct{})
	close(up)
	return &Handle{lab: "x", runDir: t.TempDir(), procs: map[string]*proc{}, life: life, up: up, stop: cancel}
}

func procState(t *testing.T, pid int) string {
	t.Helper()
	b, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "gone"
	}
	return strings.TrimSpace(string(b))
}

func TestStopHoldsAndStartRestores(t *testing.T) {
	h := testHandle(t)
	agent := sleeper(t, h, "agent", "always")
	app := sleeper(t, h, "app", "always", "n/agent")
	ctx := context.Background()

	if err := h.StopNode(ctx, "n"); err != nil {
		t.Fatal(err)
	}
	// "always" would restart within a second; held processes stay down.
	time.Sleep(1500 * time.Millisecond)
	for _, p := range []*proc{agent, app} {
		if _, alive, _ := p.state(); alive {
			t.Fatalf("%s restarted while stopped", p.id)
		}
	}
	if s := h.Snapshot()["n/app"]; !s.Held {
		t.Errorf("snapshot: %+v, want Held", s)
	}

	if err := h.StartNode(ctx, "n"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*proc{agent, app} {
		if _, alive, _ := p.state(); !alive || p.held {
			t.Errorf("%s: alive=%v held=%v after start", p.id, alive, p.held)
		}
	}
	// Start on running processes is a no-op.
	pid, _, _ := agent.state()
	if err := h.StartNode(ctx, "n"); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := agent.state(); again != pid {
		t.Errorf("start restarted a running process")
	}
}

func TestKillAppliesRestartPolicy(t *testing.T) {
	h := testHandle(t)
	never := sleeper(t, h, "never", "no")
	ctx := context.Background()
	if err := h.KillNode(ctx, "n"); err != nil {
		t.Fatal(err)
	}
	if _, alive, _ := never.state(); alive {
		t.Fatal("still alive after kill")
	}
	time.Sleep(200 * time.Millisecond)
	if _, alive, _ := never.state(); alive {
		t.Fatal(`restart: no restarted it`)
	}
	// start brings it back.
	if err := h.StartNode(ctx, "n"); err != nil {
		t.Fatal(err)
	}
	if _, alive, _ := never.state(); !alive {
		t.Fatal("not started")
	}

	h2 := testHandle(t)
	always := sleeper(t, h2, "always", "always")
	old, _, _ := always.state()
	if err := h2.KillNode(ctx, "n"); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.waitRunning(ctx, always, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if pid, _, _ := always.state(); pid == old {
		t.Error("same pid after restart")
	}
}

func TestPauseResume(t *testing.T) {
	h := testHandle(t)
	p := sleeper(t, h, "a", "no")
	pid, _, _ := p.state()
	if err := h.PauseNode("n"); err != nil {
		t.Fatal(err)
	}
	if st := procState(t, pid); !strings.HasPrefix(st, "T") {
		t.Errorf("state after pause = %q, want T", st)
	}
	if !h.Snapshot()["n/a"].Paused {
		t.Error("snapshot not paused")
	}
	if err := h.ResumeNode("n"); err != nil {
		t.Fatal(err)
	}
	if st := procState(t, pid); strings.HasPrefix(st, "T") {
		t.Errorf("state after resume = %q", st)
	}
	// A paused process still stops.
	if err := h.PauseNode("n"); err != nil {
		t.Fatal(err)
	}
	if err := h.StopNode(context.Background(), "n"); err != nil {
		t.Fatal(err)
	}
	if _, alive, _ := p.state(); alive {
		t.Error("paused process survived stop")
	}
}

func TestNodeProcsOrderAndMissing(t *testing.T) {
	h := &Handle{procs: map[string]*proc{
		"n/a": {id: "n/a", node: "n", deps: map[string]string{"n/b": "ready"}},
		"n/b": {id: "n/b", node: "n", deps: map[string]string{"n/c": "ready"}},
		"n/c": {id: "n/c", node: "n"},
		"m/x": {id: "m/x", node: "m"},
	}}
	ps, err := h.nodeProcs("n")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range ps {
		ids = append(ids, p.id)
	}
	if got := strings.Join(ids, " "); got != "n/c n/b n/a" {
		t.Errorf("order = %s", got)
	}
	if _, err := h.nodeProcs("zz"); err == nil {
		t.Error("missing node: no error")
	}
}
