package lab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// ScenarioLab is a running lab as the scenario engine sees it.
type ScenarioLab struct {
	Low    *spec.File
	Handle *supervisor.Handle
	// ExecTimeout bounds each exec step; default 2 minutes.
	ExecTimeout time.Duration
}

func (l *ScenarioLab) Resolve(sel string) ([]string, error) { return ResolveNodes(l.Low, sel) }

// Ready is nil once every process on the nodes is running and ready.
func (l *ScenarioLab) Ready(nodes []string) error {
	st, err := GetStatus(l.Low.Name, l.Handle)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, n := range nodes {
		want[n] = true
	}
	var notReady []string
	for _, ns := range st.Nodes {
		if !want[ns.Name] {
			continue
		}
		for _, p := range ns.Processes {
			if !p.Alive || (p.Ready != nil && !*p.Ready) {
				notReady = append(notReady, ns.Name+"/"+p.Name)
			}
		}
	}
	if len(notReady) > 0 {
		return fmt.Errorf("not ready: %s", strings.Join(notReady, ", "))
	}
	return nil
}

// Exec runs argv in node's netns with the same environment as `parker
// exec`, and returns combined stdout and stderr.
func (l *ScenarioLab) Exec(ctx context.Context, node string, argv []string) (string, int, error) {
	timeout := l.ExecTimeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	home := supervisor.NodeDir(RunDir(l.Low.Name), node)
	path, env := ExecEnv(l.Low, node, []string{"HOME=" + home}, io.Discard)
	bin, err := LookPath(argv[0], path)
	if err != nil {
		return "", 0, err
	}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Args[0] = argv[0]
	cmd.Env, cmd.Dir = env, home
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := supervisor.InNetns(netsetup.NetnsPath(l.Low.Name, node), cmd.Start); err != nil {
		return "", 0, PrivHint(err)
	}
	err = cmd.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), -1, fmt.Errorf("timed out after %s", timeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), ee.ExitCode(), nil
	}
	return out.String(), 0, err
}

// Consul checks the assertion against the dc's servers.
func (l *ScenarioLab) Consul(ctx context.Context, a *spec.ConsulAssert) error {
	dc, err := l.dc(a.DC)
	if err != nil {
		return err
	}
	if a.Leader {
		if _, err := RaftLeader(l.Low, dc); err != nil {
			return err
		}
	}
	if a.Members == "" && a.Peers == "" && a.ServiceHealthy == "" {
		return nil
	}
	c, err := l.answering(dc)
	if err != nil {
		return err
	}
	if a.Members != "" {
		want, err := spec.ParseMembers(a.Members)
		if err != nil {
			return fmt.Errorf("members: %w", err)
		}
		ms, err := c.Agent().Members(false)
		if err != nil {
			return err
		}
		alive := 0
		for _, m := range ms {
			if m.Status == 1 {
				alive++
			}
		}
		if alive < want {
			return fmt.Errorf("%s: %d alive members, want %s", dc, alive, a.Members)
		}
	}
	if a.Peers != "" {
		want, err := spec.ParseMembers(a.Peers)
		if err != nil {
			return fmt.Errorf("peers: %w", err)
		}
		cfg, err := c.Operator().RaftGetConfiguration(nil)
		if err != nil {
			return err
		}
		if len(cfg.Servers) < want {
			return fmt.Errorf("%s: %d raft peers, want %s", dc, len(cfg.Servers), a.Peers)
		}
	}
	if svc := a.ServiceHealthy; svc != "" {
		entries, _, err := c.Health().Service(svc, "", true, &api.QueryOptions{Datacenter: dc})
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return fmt.Errorf("%s: no passing instance of %s", dc, svc)
		}
	}
	return nil
}

// dc is the named datacenter, or the only one.
func (l *ScenarioLab) dc(name string) (string, error) {
	dcs := map[string]bool{}
	for _, n := range l.Low.Nodes {
		if n.Labels != nil && n.Labels.DC != "" {
			dcs[n.Labels.DC] = true
		}
	}
	if name != "" {
		if !dcs[name] {
			return "", fmt.Errorf("no datacenter %q", name)
		}
		return name, nil
	}
	if len(dcs) != 1 {
		return "", fmt.Errorf("say which dc: (have %s)", strings.Join(slices.Sorted(maps.Keys(dcs)), ", "))
	}
	for d := range dcs {
		return d, nil
	}
	return "", nil
}

// answering is the first server of dc that has a leader to answer with.
func (l *ScenarioLab) answering(dc string) (*api.Client, error) {
	clients, err := ServerClients(l.Low, dc)
	if err != nil {
		return nil, err
	}
	var last error
	for _, n := range slices.Sorted(maps.Keys(clients)) {
		if leader, err := clients[n].Status().Leader(); err == nil && leader != "" {
			return clients[n], nil
		} else if err != nil {
			last = fmt.Errorf("%s: %w", n, err)
		}
	}
	if last == nil {
		last = fmt.Errorf("%s has no raft leader", dc)
	}
	return nil, last
}

// Snapshot copies every node's logs to <run dir>/snapshots/<time>/.
func (l *ScenarioLab) Snapshot(what string) (string, error) {
	if what != "logs" {
		return "", fmt.Errorf("snapshot %q: only logs", what)
	}
	run := RunDir(l.Low.Name)
	dst := filepath.Join(run, "snapshots", time.Now().UTC().Format("20060102T150405.000Z"))
	for _, n := range slices.Sorted(maps.Keys(l.Low.Nodes)) {
		src := supervisor.NodeDir(run, n)
		logs, _ := filepath.Glob(filepath.Join(src, "*.log"))
		if len(logs) == 0 {
			continue
		}
		if err := os.MkdirAll(filepath.Join(dst, n), 0o755); err != nil {
			return "", err
		}
		for _, f := range logs {
			b, err := os.ReadFile(f)
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(dst, n, filepath.Base(f)), b, 0o644); err != nil {
				return "", err
			}
		}
	}
	return dst, nil
}
