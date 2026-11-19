package supervisor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"syscall"
	"time"

	"github.com/Ranjandas/parker/internal/netsetup"
)

// Fault actions on a node's processes, for scenarios. Each process runs in
// its own session, so signals go to its whole process group.

// StopGrace is how long Stop waits after SIGTERM before SIGKILL.
var StopGrace = 10 * time.Second

// nodeProcs is the node's processes in dependency order (dependencies
// first), or an error for a node the lab doesn't have.
func (h *Handle) nodeProcs(node string) ([]*proc, error) {
	var ps []*proc
	for _, id := range slices.Sorted(maps.Keys(h.procs)) {
		if p := h.procs[id]; p.node == node {
			ps = append(ps, p)
		}
	}
	if len(ps) == 0 {
		return nil, fmt.Errorf("node %s has no supervised processes", node)
	}
	return depOrder(ps), nil
}

// depOrder sorts ps so a process comes after the ones it depends on
// (among ps); ties keep their order.
func depOrder(ps []*proc) []*proc {
	in := map[string]*proc{}
	for _, p := range ps {
		in[p.id] = p
	}
	seen := map[string]bool{}
	var out []*proc
	var visit func(p *proc)
	visit = func(p *proc) {
		if seen[p.id] {
			return
		}
		seen[p.id] = true
		for _, d := range slices.Sorted(maps.Keys(p.deps)) {
			if dp, ok := in[d]; ok {
				visit(dp)
			}
		}
		out = append(out, p)
	}
	for _, p := range ps {
		visit(p)
	}
	return out
}

func (p *proc) state() (pid int, alive bool, runDone chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pid, p.alive, p.runDone
}

func signalGroup(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// waitExit waits for the run to end, up to d.
func waitExit(ctx context.Context, done <-chan struct{}, d time.Duration) bool {
	if done == nil {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// KillNode SIGKILLs the node's processes and waits for them to die. The
// restart policy applies as to any crash.
func (h *Handle) KillNode(ctx context.Context, node string) error {
	ps, err := h.nodeProcs(node)
	if err != nil {
		return err
	}
	for _, p := range ps {
		pid, alive, done := p.state()
		if !alive {
			continue
		}
		if err := signalGroup(pid, syscall.SIGKILL); err != nil {
			return fmt.Errorf("%s: %w", p.id, err)
		}
		if !waitExit(ctx, done, 5*time.Second) {
			return fmt.Errorf("%s: still running after SIGKILL", p.id)
		}
	}
	return nil
}

// StopNode ends the node's processes (SIGTERM, then SIGKILL after StopGrace)
// in reverse dependency order and keeps them down until StartNode.
func (h *Handle) StopNode(ctx context.Context, node string) error {
	ps, err := h.nodeProcs(node)
	if err != nil {
		return err
	}
	slices.Reverse(ps)
	return stopProcs(ctx, ps)
}

func stopProcs(ctx context.Context, ps []*proc) error {
	for _, p := range ps {
		p.mu.Lock()
		p.held = true
		pid, alive, done, paused := p.pid, p.alive, p.runDone, p.paused
		p.mu.Unlock()
		select { // drop a pending start
		case <-p.kick:
		default:
		}
		if !alive {
			continue
		}
		if err := signalGroup(pid, syscall.SIGTERM); err != nil {
			return fmt.Errorf("%s: %w", p.id, err)
		}
		if paused {
			_ = signalGroup(pid, syscall.SIGCONT) // so it sees the TERM
		}
		if waitExit(ctx, done, StopGrace) {
			continue
		}
		if err := signalGroup(pid, syscall.SIGKILL); err != nil {
			return fmt.Errorf("%s: %w", p.id, err)
		}
		if !waitExit(ctx, done, 5*time.Second) {
			return fmt.Errorf("%s: still running after SIGKILL", p.id)
		}
	}
	return nil
}

// StartNode starts the node's processes that are not running, in dependency
// order, and waits for each one's readiness check.
func (h *Handle) StartNode(ctx context.Context, node string) error {
	ps, err := h.nodeProcs(node)
	if err != nil {
		return err
	}
	return h.startProcs(ctx, ps)
}

func (h *Handle) startProcs(ctx context.Context, ps []*proc) error {
	for _, p := range ps {
		p.mu.Lock()
		p.held = false
		alive := p.alive
		p.mu.Unlock()
		if alive {
			continue
		}
		if p.kick == nil {
			return fmt.Errorf("%s: not supervised", p.id)
		}
		select {
		case p.kick <- struct{}{}:
		default:
		}
		done, err := h.waitRunning(ctx, p, 10*time.Second)
		if err != nil {
			return err
		}
		nsPath := netsetup.NetnsPath(h.lab, p.node)
		if err := waitReady(ctx, p, done, nsPath, p.logPath); err != nil {
			return fmt.Errorf("%s: %w\n--- last lines of %s ---\n%s", p.id, err, p.logPath, tail(p.logPath, 15))
		}
	}
	return nil
}

// waitRunning waits for supervise to start p and returns that run's exit
// channel.
func (h *Handle) waitRunning(ctx context.Context, p *proc, d time.Duration) (<-chan struct{}, error) {
	deadline := time.Now().Add(d)
	for {
		if _, alive, done := p.state(); alive {
			return done, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s: did not start within %s (see %s)", p.id, d, p.logPath)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-h.life.Done():
			return nil, errors.New("supervision stopped")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// RestartNode is StopNode then StartNode.
func (h *Handle) RestartNode(ctx context.Context, node string) error {
	if err := h.StopNode(ctx, node); err != nil {
		return err
	}
	return h.StartNode(ctx, node)
}

// PauseNode SIGSTOPs the node's processes; ResumeNode SIGCONTs them.
func (h *Handle) PauseNode(node string) error { return h.pause(node, true) }

func (h *Handle) ResumeNode(node string) error { return h.pause(node, false) }

func (h *Handle) pause(node string, stop bool) error {
	ps, err := h.nodeProcs(node)
	if err != nil {
		return err
	}
	sig := syscall.SIGCONT
	if stop {
		sig = syscall.SIGSTOP
	}
	for _, p := range ps {
		p.mu.Lock()
		pid, alive := p.pid, p.alive
		if alive {
			p.paused = stop
		}
		p.mu.Unlock()
		if !alive {
			continue
		}
		if err := signalGroup(pid, sig); err != nil {
			return fmt.Errorf("%s: %w", p.id, err)
		}
	}
	return nil
}

// UpgradeNode swaps the binary of the node's processes that run product
// (consul) for bin, the given version of it: each is stopped, switched and
// started again, waiting for its readiness check. The processes that
// depend on it keep running.
func (h *Handle) UpgradeNode(ctx context.Context, node, product, version, bin string) error {
	ps, err := h.nodeProcs(node)
	if err != nil {
		return err
	}
	var hit []*proc
	for _, p := range ps {
		p.mu.Lock()
		ok := len(p.spec.Run) > 0 && p.spec.Run[0] == product && len(p.argv) > 0
		p.mu.Unlock()
		if ok {
			hit = append(hit, p)
		}
	}
	if len(hit) == 0 {
		return fmt.Errorf("node %s runs no %s", node, product)
	}
	if err := stopProcs(ctx, hit); err != nil {
		return err
	}
	for _, p := range hit {
		p.mu.Lock()
		p.argv = append([]string{bin}, p.argv[1:]...)
		p.version = version
		p.mu.Unlock()
	}
	return h.startProcs(ctx, hit)
}
