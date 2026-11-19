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
	// Life bounds supervision after Run returns: restart policies apply
	// until it is done or Handle.Stop is called. Nil means Background.
	Life context.Context
}

type proc struct {
	id     string // node/process
	node   string
	name   string
	spec   *spec.Process
	deps   map[string]string // id -> condition
	start  chan struct{}     // closed when started
	ready  chan struct{}     // closed when ready
	exited chan struct{}     // closed when the first run exits
	err    error

	mu       sync.Mutex
	pid      int
	alive    bool
	isReady  bool
	restarts int
	logPath  string
}

// ProcInfo is a snapshot of one supervised process.
type ProcInfo struct {
	Node, Name string
	Pid        int
	Alive      bool
	Ready      bool
	Restarts   int
	LogPath    string
}

// Handle supervises a lab's processes after Run returns.
type Handle struct {
	lab, runDir string
	procs       map[string]*proc
	stop        context.CancelFunc
	life        context.Context
	up          chan struct{} // closed when Run succeeded
}

// Stop ends supervision: no more restarts. Processes keep running; use
// netsetup.Down to remove them.
func (h *Handle) Stop() { h.stop() }

// Snapshot returns every process's state, keyed by node/process.
func (h *Handle) Snapshot() map[string]ProcInfo {
	out := map[string]ProcInfo{}
	for id, p := range h.procs {
		p.mu.Lock()
		out[id] = ProcInfo{Node: p.node, Name: p.name, Pid: p.pid, Alive: p.alive, Ready: p.isReady, Restarts: p.restarts, LogPath: p.logPath}
		p.mu.Unlock()
	}
	return out
}

func (h *Handle) writeState() {
	st := &State{Lab: h.lab, StartedAt: time.Now().UTC(), Pids: map[string]int{}}
	for id, p := range h.Snapshot() {
		if p.Pid > 0 {
			st.Pids[id] = p.Pid
		}
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = os.WriteFile(filepath.Join(h.runDir, "state.json"), b, 0o644)
}

// ShouldRestart applies a restart policy to an exit.
func ShouldRestart(policy string, exitErr error) bool {
	switch policy {
	case "always":
		return true
	case "on-failure":
		return exitErr != nil
	}
	return false
}

// State is written to <run_dir>/state.json for status and humans.
type State struct {
	Lab       string         `json:"lab"`
	StartedAt time.Time      `json:"started_at"`
	Pids      map[string]int `json:"pids"`
}

func NodeDir(runDir, node string) string { return filepath.Join(runDir, "nodes", node) }

// Run starts everything and returns once all processes are ready, or the
// first error. On error, supervision is already stopped.
func Run(ctx context.Context, f *spec.File, o Options) (*Handle, error) {
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
	if o.Life == nil {
		o.Life = context.Background()
	}
	life, stop := context.WithCancel(o.Life)
	h := &Handle{lab: o.Lab, runDir: o.RunDir, procs: procs, stop: stop, life: life, up: make(chan struct{})}

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
			if err := runOne(ctx, h, f, o, p, procs, bins, self, logf); err != nil {
				p.err = err
				cancel(fmt.Errorf("%s: %w", p.id, err))
			}
		}()
	}
	wg.Wait()
	h.writeState()

	if cause := context.Cause(ctx); cause != nil {
		stop()
		if errors.Is(cause, context.Canceled) {
			return h, ctx.Err()
		}
		return h, cause
	}
	close(h.up)
	return h, nil
}

func runOne(ctx context.Context, h *Handle, f *spec.File, o Options, p *proc, procs map[string]*proc,
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
	env := []string{"PATH=" + pathWith(bins), "HOME=" + dir, "PARKER_LAB=" + o.Lab, "PARKER_NODE=" + p.node}
	for k, v := range p.spec.Env {
		env = append(env, k+"="+vars.Replace(v))
	}
	logPath := filepath.Join(dir, p.name+".log")
	nsPath := netsetup.NetnsPath(o.Lab, p.node)
	p.mu.Lock()
	p.logPath = logPath
	p.mu.Unlock()

	// start forks one run of the process; the returned channel yields its
	// exit error.
	start := func() (<-chan error, error) {
		lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = lf, lf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		cmd.Env = env
		if err := startIn(nsPath, cmd); err != nil {
			lf.Close()
			return nil, fmt.Errorf("start: %w", err)
		}
		p.mu.Lock()
		p.pid, p.alive = cmd.Process.Pid, true
		p.mu.Unlock()
		done := make(chan error, 1)
		go func() {
			err := cmd.Wait()
			lf.Close()
			p.mu.Lock()
			p.alive, p.isReady = false, false
			p.mu.Unlock()
			done <- err
		}()
		return done, nil
	}

	done, err := start()
	if err != nil {
		return err
	}
	close(p.start)
	logf("  started %-28s pid %d", p.id, p.pid)
	go func() {
		exitErr := <-done
		close(p.exited)
		h.supervise(p, exitErr, start, logf)
	}()

	t0 := time.Now()
	if err := waitReady(ctx, p, nsPath, n, logPath); err != nil {
		return fmt.Errorf("%w\n--- last lines of %s ---\n%s", err, logPath, tail(logPath, 15))
	}
	p.mu.Lock()
	p.isReady = true
	p.mu.Unlock()
	close(p.ready)
	logf("  ready   %-28s %s", p.id, time.Since(t0).Round(100*time.Millisecond))
	return nil
}

// supervise applies the restart policy after each exit, once the lab is up
// and until supervision stops. Restarts back off from 1s to 30s; a run that
// lasted a minute resets the backoff. Restarted processes count as ready
// once running (readiness checks run only during up).
func (h *Handle) supervise(p *proc, exitErr error, start func() (<-chan error, error), logf func(string, ...any)) {
	select {
	case <-h.up:
	case <-h.life.Done():
		return
	}
	backoff := time.Second
	for {
		if !ShouldRestart(p.spec.Restart, exitErr) {
			return
		}
		select {
		case <-h.life.Done():
			return
		case <-time.After(backoff):
		}
		t0 := time.Now()
		done, err := start()
		if err != nil {
			exitErr = err
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		p.mu.Lock()
		p.restarts++
		p.isReady = true
		p.mu.Unlock()
		h.writeState()
		select {
		case exitErr = <-done:
		case <-h.life.Done():
			return
		}
		if time.Since(t0) > time.Minute {
			backoff = time.Second
		} else {
			backoff = min(backoff*2, 30*time.Second)
		}
	}
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
