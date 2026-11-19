// Package supervisor starts a lab's processes in dependency order, each in
// its node's network namespace, and waits for their readiness checks.
//
// Processes are started in their own session so they outlive `parker up`;
// `parker down` finds them again by network namespace.
package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/Ranjandas/parker/internal/fetch"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
)

type Options struct {
	Lab    string
	RunDir string // the lab's run directory
	Log    io.Writer
}

type proc struct {
	id     string // node/process
	node   string
	name   string
	spec   *spec.Process
	deps   map[string]string // id -> condition
	start  chan struct{}     // closed when started
	ready  chan struct{}     // closed when ready
	exited chan struct{}
	pid    int
	err    error
}

// State is written to <run_dir>/state.json for status and humans.
type State struct {
	Lab       string         `json:"lab"`
	StartedAt time.Time      `json:"started_at"`
	Pids      map[string]int `json:"pids"`
}

func NodeDir(runDir, node string) string { return filepath.Join(runDir, "nodes", node) }

// Run starts everything and returns once all processes are ready, or the
// first error.
func Run(ctx context.Context, f *spec.File, o Options) (*State, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}

	// Resolve binaries up front so downloads don't count against timeouts.
	bins := map[string]string{}
	for _, n := range f.Nodes {
		for _, p := range n.Processes {
			if key, ok := binaryKey(f, p); ok && bins[key] == "" {
				product, version, _ := strings.Cut(key, "@")
				path, err := fetch.Binary(product, version, o.Log)
				if err != nil {
					return nil, err
				}
				bins[key] = path
			}
		}
	}

	procs := map[string]*proc{}
	for _, nodeName := range slices.Sorted(maps.Keys(f.Nodes)) {
		n := f.Nodes[nodeName]
		dir := NodeDir(o.RunDir, nodeName)
		vars := runtimeVars(o.RunDir, dir, self, n)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		for name, content := range n.Files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(vars.Replace(content)), 0o644); err != nil {
				return nil, err
			}
		}
		for pn, p := range n.Processes {
			pr := &proc{
				id: nodeName + "/" + pn, node: nodeName, name: pn, spec: p,
				deps:  map[string]string{},
				start: make(chan struct{}), ready: make(chan struct{}), exited: make(chan struct{}),
			}
			for d, dep := range p.DependsOn {
				if !strings.Contains(d, "/") {
					d = nodeName + "/" + d
				}
				cond := "ready"
				if dep != nil && dep.Condition != "" {
					cond = dep.Condition
				}
				pr.deps[d] = cond
			}
			procs[pr.id] = pr
		}
	}
	if err := checkCycles(procs); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var mu sync.Mutex
	logf := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(o.Log, format+"\n", a...)
	}

	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runOne(ctx, f, o, p, procs, bins, self, logf); err != nil {
				p.err = err
				cancel(fmt.Errorf("%s: %w", p.id, err))
			}
		}()
	}
	wg.Wait()

	st := &State{Lab: o.Lab, StartedAt: time.Now().UTC(), Pids: map[string]int{}}
	for id, p := range procs {
		if p.pid > 0 {
			st.Pids[id] = p.pid
		}
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = os.WriteFile(filepath.Join(o.RunDir, "state.json"), b, 0o644)

	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return st, cause
	}
	return st, ctx.Err()
}

func runOne(ctx context.Context, f *spec.File, o Options, p *proc, procs map[string]*proc,
	bins map[string]string, self string, logf func(string, ...any)) error {

	for _, d := range slices.Sorted(maps.Keys(p.deps)) {
		dp := procs[d]
		ch := dp.ready
		if p.deps[d] == "started" {
			ch = dp.start
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil // someone else failed; they report it
		}
	}

	n := f.Nodes[p.node]
	dir := NodeDir(o.RunDir, p.node)
	vars := runtimeVars(o.RunDir, dir, self, n)
	argv := make([]string, len(p.spec.Run))
	for i, a := range p.spec.Run {
		argv[i] = vars.Replace(a)
	}
	if key, ok := binaryKey(f, p.spec); ok {
		argv[0] = bins[key]
	}

	logPath := filepath.Join(dir, p.name+".log")
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	env := []string{"PATH=" + pathWith(bins), "HOME=" + dir, "PARKER_LAB=" + o.Lab, "PARKER_NODE=" + p.node}
	for k, v := range p.spec.Env {
		env = append(env, k+"="+vars.Replace(v))
	}
	cmd.Env = env

	nsPath := netsetup.NetnsPath(o.Lab, p.node)
	if err := startIn(nsPath, cmd); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	p.pid = cmd.Process.Pid
	close(p.start)
	logf("  started %-28s pid %d", p.id, p.pid)
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()

	t0 := time.Now()
	if err := waitReady(ctx, p, nsPath, n, logPath); err != nil {
		return fmt.Errorf("%w\n--- last lines of %s ---\n%s", err, logPath, tail(logPath, 15))
	}
	close(p.ready)
	logf("  ready   %-28s %s", p.id, time.Since(t0).Round(100*time.Millisecond))
	return nil
}

func waitReady(ctx context.Context, p *proc, nsPath string, n *spec.RawNode, logPath string) error {
	r := p.spec.ReadyWhen
	if r == nil {
		return nil
	}
	timeout := 30 * time.Second
	if r.Timeout != "" {
		d, err := time.ParseDuration(r.Timeout)
		if err != nil {
			return fmt.Errorf("ready_when.timeout: %w", err)
		}
		timeout = d
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dial := dialerIn(nsPath)
	httpc := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DialContext: dial, DisableKeepAlives: true}}

	var last error
	for {
		last = checkOnce(ctx, r, dial, httpc, logPath)
		if last == nil {
			return nil
		}
		select {
		case <-p.exited:
			return fmt.Errorf("process exited before becoming ready")
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("not ready after %s: %v", timeout, last)
			}
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func checkOnce(ctx context.Context, r *spec.Ready, dial func(context.Context, string, string) (net.Conn, error),
	httpc *http.Client, logPath string) error {
	if r.Port > 0 {
		c, err := dial(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", r.Port))
		if err != nil {
			return fmt.Errorf("port %d: %w", r.Port, err)
		}
		c.Close()
	}
	if r.HTTP != nil {
		resp, err := httpc.Get(r.HTTP.URL)
		if err != nil {
			return err
		}
		resp.Body.Close()
		want := r.HTTP.Status
		if want == 0 {
			want = 200
		}
		if resp.StatusCode != want {
			return fmt.Errorf("%s: status %d, want %d", r.HTTP.URL, resp.StatusCode, want)
		}
	}
	if r.Log != "" {
		b, _ := os.ReadFile(logPath)
		if !bytes.Contains(b, []byte(r.Log)) {
			return fmt.Errorf("log line %q not seen yet", r.Log)
		}
	}
	if r.Consul != nil {
		addr := r.Consul.Addr
		if addr == "" {
			addr = "127.0.0.1:8500"
		}
		cfg := api.DefaultConfig()
		cfg.Address = addr
		cfg.HttpClient = httpc
		c, err := api.NewClient(cfg)
		if err != nil {
			return err
		}
		if r.Consul.Leader {
			l, err := c.Status().Leader()
			if err != nil {
				return fmt.Errorf("consul leader: %w", err)
			}
			if l == "" {
				return fmt.Errorf("consul: no leader yet")
			}
		}
		if r.Consul.Members != "" {
			want, err := spec.ParseMembers(r.Consul.Members)
			if err != nil {
				return fmt.Errorf("consul.members: %w", err)
			}
			ms, err := c.Agent().Members(false)
			if err != nil {
				return fmt.Errorf("consul members: %w", err)
			}
			alive := 0
			for _, m := range ms {
				if m.Status == 1 {
					alive++
				}
			}
			if alive < want {
				return fmt.Errorf("consul: %d alive members, want >=%d", alive, want)
			}
		}
		if svc := r.Consul.ServiceHealthy; svc != "" {
			// Ready means the catalog (not just the local agent) shows this
			// node's instance as passing, so other nodes can discover it.
			self, err := c.Agent().NodeName()
			if err != nil {
				return fmt.Errorf("consul agent: %w", err)
			}
			entries, _, err := c.Health().Service(svc, "", true, nil)
			if err != nil {
				return fmt.Errorf("consul health of %s: %w", svc, err)
			}
			if !slices.ContainsFunc(entries, func(e *api.ServiceEntry) bool { return e.Node.Node == self }) {
				return fmt.Errorf("consul: %s on %s not passing in the catalog yet", svc, self)
			}
		}
	}
	return nil
}

// binaryKey returns "product@version" when the process's first word names
// a binary pinned in versions:.
func binaryKey(f *spec.File, p *spec.Process) (string, bool) {
	if len(p.Run) == 0 {
		return "", false
	}
	v, ok := f.Versions[p.Run[0]]
	if !ok {
		return "", false
	}
	if p.Version != "" {
		v = p.Version
	}
	return p.Run[0] + "@" + v, true
}

func runtimeVars(runDir, nodeDir, self string, n *spec.RawNode) *strings.Replacer {
	ip := ""
	for _, k := range slices.Sorted(maps.Keys(n.Networks)) {
		ip = n.Networks[k].IP
		break
	}
	return strings.NewReplacer("${run_dir}", runDir, "${node_dir}", nodeDir, "${parker}", self, "${node.ip}", ip)
}

func pathWith(bins map[string]string) string {
	var dirs []string
	for _, k := range slices.Sorted(maps.Keys(bins)) {
		dirs = append(dirs, filepath.Dir(bins[k]))
	}
	return strings.Join(append(dirs, "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"), ":")
}

func checkCycles(procs map[string]*proc) error {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var visit func(id string, path []string) error
	visit = func(id string, path []string) error {
		switch color[id] {
		case grey:
			return fmt.Errorf("dependency cycle: %s -> %s", strings.Join(path, " -> "), id)
		case black:
			return nil
		}
		color[id] = grey
		for d := range procs[id].deps {
			if _, ok := procs[d]; !ok {
				return fmt.Errorf("%s depends on unknown process %s", id, d)
			}
			if err := visit(d, append(path, id)); err != nil {
				return err
			}
		}
		color[id] = black
		return nil
	}
	for _, id := range slices.Sorted(maps.Keys(procs)) {
		if err := visit(id, nil); err != nil {
			return err
		}
	}
	return nil
}

func tail(path string, n int) string {
	fh, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer fh.Close()
	var lines []string
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return strings.Join(lines, "\n")
}
