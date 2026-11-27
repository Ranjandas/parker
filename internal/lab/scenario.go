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

	"github.com/Ranjandas/parker/internal/fetch"
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
		// Count voters only. Autopilot drops a server that was failed for
		// long and adds it back as a non-voter until it has been healthy for
		// a while; counting it would let the next fault start while the dc
		// can't lose another server (killing the leader then leaves no
		// quorum).
		voters := 0
		for _, s := range cfg.Servers {
			if s.Voter {
				voters++
			}
		}
		if voters < want {
			return fmt.Errorf("%s: %d voting raft peers of %d, want %s", dc, voters, len(cfg.Servers), a.Peers)
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

// Partition cuts the network between groups of nodes.
func (l *ScenarioLab) Partition(groups [][]string) error {
	return PrivHint(netsetup.Partition(l.Low.Name, l.Low, groups))
}

// Heal removes partitions on and to nodes; nil heals all.
func (l *ScenarioLab) Heal(nodes []string) error {
	_, err := netsetup.Heal(l.Low.Name, l.Low, nodes)
	return PrivHint(err)
}

// Proc acts on a node's processes through the supervisor.
func (l *ScenarioLab) Proc(ctx context.Context, action, node string) error {
	h := l.Handle
	if h == nil {
		return fmt.Errorf("%s needs the lab supervised by this daemon (bring it up with parker up or run)", action)
	}
	switch action {
	case "kill":
		return h.KillNode(ctx, node)
	case "stop":
		return h.StopNode(ctx, node)
	case "start":
		return h.StartNode(ctx, node)
	case "restart":
		return h.RestartNode(ctx, node)
	case "pause":
		return h.PauseNode(node)
	case "resume":
		return h.ResumeNode(node)
	}
	return fmt.Errorf("unknown action %q", action)
}

// UpgradeWait bounds how long an upgrade waits for a dc's servers to be
// healthy again after each server.
var UpgradeWait = 2 * time.Minute

// UpgradeOrder puts servers first, dc by dc with the raft leader last, then
// every other node.
func (l *ScenarioLab) UpgradeOrder(nodes []string) ([]string, error) {
	return upgradeOrder(l.Low, nodes, func(dc string) (string, error) { return RaftLeader(l.Low, dc) })
}

func upgradeOrder(low *spec.File, nodes []string, leader func(dc string) (string, error)) ([]string, error) {
	label := func(n string) *spec.NodeLabels {
		if rn := low.Nodes[n]; rn != nil && rn.Labels != nil {
			return rn.Labels
		}
		return &spec.NodeLabels{}
	}
	servers := map[string][]string{}
	var rest []string
	for _, n := range slices.Sorted(slices.Values(nodes)) {
		if l := label(n); l.Role == "server" {
			servers[l.DC] = append(servers[l.DC], n)
		} else {
			rest = append(rest, n)
		}
	}
	var out []string
	for _, dc := range slices.Sorted(maps.Keys(servers)) {
		ss := servers[dc]
		if len(ss) > 1 {
			ld, err := leader(dc)
			if err != nil {
				return nil, fmt.Errorf("%s: %w (the leader goes last)", dc, err)
			}
			if i := slices.Index(ss, ld); i >= 0 {
				ss = append(slices.Delete(slices.Clone(ss), i, i+1), ld)
			}
		}
		out = append(out, ss...)
	}
	return append(out, rest...), nil
}

// Upgrade swaps node's product binary for version and waits for it; after
// a server, also for every server of its dc to be healthy (autopilot).
func (l *ScenarioLab) Upgrade(ctx context.Context, node, product, version string) error {
	if l.Handle == nil {
		return errors.New("upgrade needs the lab supervised by this daemon (bring it up with parker up or run)")
	}
	bin, err := fetch.Binary(product, version, io.Discard)
	if err != nil {
		return err
	}
	if err := l.Handle.UpgradeNode(ctx, node, product, version, bin); err != nil {
		return err
	}
	lb := l.Low.Nodes[node].Labels
	if product != "consul" || lb == nil || lb.Role != "server" {
		return nil
	}
	deadline := time.Now().Add(UpgradeWait)
	for {
		err := l.serversHealthy(lb.DC, lb.Name, version)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s servers not healthy %s after the upgrade: %w", lb.DC, UpgradeWait, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// serversHealthy is nil once autopilot calls every server of dc healthy
// and sees server (its Consul node name) on version.
func (l *ScenarioLab) serversHealthy(dc, server, version string) error {
	c, err := l.answering(dc)
	if err != nil {
		return err
	}
	h, err := c.Operator().AutopilotServerHealth(nil)
	if err != nil {
		return err
	}
	return healthyOn(h, server, version)
}

func healthyOn(h *api.OperatorHealthReply, server, version string) error {
	var bad []string
	seen := false
	for _, s := range h.Servers {
		if !s.Healthy {
			bad = append(bad, s.Name)
		}
		if s.Name == server {
			seen = true
			if s.Version != version {
				return fmt.Errorf("%s still reports %s", server, s.Version)
			}
		}
	}
	if !seen {
		return fmt.Errorf("autopilot doesn't list %s yet", server)
	}
	if len(bad) > 0 || !h.Healthy {
		return fmt.Errorf("unhealthy: %s", strings.Join(bad, ", "))
	}
	return nil
}

// Vault runs a vault: step's action on one Vault server.
func (l *ScenarioLab) Vault(ctx context.Context, action, node string) error {
	switch action {
	case "seal":
		return VaultSeal(ctx, l.Low, node, func() error { return l.Proc(ctx, "restart", node) })
	case "unseal":
		return VaultUnseal(ctx, l.Low, node)
	case "step_down":
		return VaultStepDown(ctx, l.Low, node)
	}
	return fmt.Errorf("unknown vault action %q", action)
}

// VaultCheck checks one Vault server against an assert.vault.
func (l *ScenarioLab) VaultCheck(ctx context.Context, node string, a *spec.VaultAssert) error {
	s, err := VaultStatus(ctx, l.Low, node)
	if err != nil {
		return err
	}
	if a.Sealed != nil && s.Sealed != *a.Sealed {
		return fmt.Errorf("%s: sealed is %t, want %t", node, s.Sealed, *a.Sealed)
	}
	if a.Active != nil && s.Active != *a.Active {
		return fmt.Errorf("%s: active is %t, want %t", node, s.Active, *a.Active)
	}
	if a.RaftPeers != "" {
		want, err := spec.ParseMembers(a.RaftPeers)
		if err != nil {
			return fmt.Errorf("raft_peers: %w", err)
		}
		n, err := VaultRaftPeers(ctx, l.Low, node)
		if err != nil {
			return err
		}
		if n < want {
			return fmt.Errorf("%s: %d voting raft peers, want %s", node, n, a.RaftPeers)
		}
	}
	return nil
}
