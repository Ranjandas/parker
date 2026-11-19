package supervisor

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func procs(deps map[string][]string) map[string]*proc {
	m := map[string]*proc{}
	for id, ds := range deps {
		p := &proc{id: id, deps: map[string]string{}}
		for _, d := range ds {
			p.deps[d] = "ready"
		}
		m[id] = p
	}
	return m
}

func TestCheckCycles(t *testing.T) {
	ok := procs(map[string][]string{
		"s1/consul": nil, "s2/consul": {"s1/consul"}, "c/consul": {"s1/consul", "s2/consul"}, "c/web": {"c/consul"},
	})
	if err := checkCycles(ok); err != nil {
		t.Fatalf("DAG: %v", err)
	}
	cyc := procs(map[string][]string{"a/x": {"b/x"}, "b/x": {"c/x"}, "c/x": {"a/x"}})
	err := checkCycles(cyc)
	if err == nil || !strings.Contains(err.Error(), "dependency cycle: a/x -> b/x -> c/x -> a/x") {
		t.Fatalf("cycle: %v", err)
	}
	self := procs(map[string][]string{"a/x": {"a/x"}})
	if err := checkCycles(self); err == nil {
		t.Fatal("self-dependency not caught")
	}
	missing := procs(map[string][]string{"a/x": {"nope/x"}})
	if err := checkCycles(missing); err == nil || !strings.Contains(err.Error(), "unknown process nope/x") {
		t.Fatalf("missing: %v", err)
	}
}

func tcpDial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func TestCheckOncePortAndHTTP(t *testing.T) {
	ctx := context.Background()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := checkOnce(ctx, &spec.Ready{Port: port}, tcpDial, http.DefaultClient, ""); err != nil {
		t.Errorf("open port: %v", err)
	}
	ln.Close()
	if err := checkOnce(ctx, &spec.Ready{Port: port}, tcpDial, http.DefaultClient, ""); err == nil {
		t.Error("closed port counted as ready")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/teapot" {
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()
	for _, c := range []struct {
		r    spec.HTTPCheck
		pass bool
	}{
		{spec.HTTPCheck{URL: srv.URL + "/"}, true},
		{spec.HTTPCheck{URL: srv.URL + "/teapot"}, false},
		{spec.HTTPCheck{URL: srv.URL + "/teapot", Status: 418}, true},
	} {
		err := checkOnce(ctx, &spec.Ready{HTTP: &c.r}, tcpDial, srv.Client(), "")
		if (err == nil) != c.pass {
			t.Errorf("%+v: err=%v, want pass=%v", c.r, err, c.pass)
		}
	}
}

func TestCheckOnceLog(t *testing.T) {
	log := filepath.Join(t.TempDir(), "p.log")
	r := &spec.Ready{Log: "agent: Synced node info"}
	if err := checkOnce(context.Background(), r, tcpDial, nil, log); err == nil {
		t.Error("missing log counted as ready")
	}
	os.WriteFile(log, []byte("starting\n[INFO] agent: Synced node info\n"), 0o644)
	if err := checkOnce(context.Background(), r, tcpDial, nil, log); err != nil {
		t.Errorf("log line present: %v", err)
	}
}

// fakeConsul answers the agent API calls the readiness checks make.
func fakeConsul(t *testing.T, leader string, alive int, healthyOn []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	js := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("/v1/status/leader", func(w http.ResponseWriter, r *http.Request) { js(w, leader) })
	mux.HandleFunc("/v1/agent/members", func(w http.ResponseWriter, r *http.Request) {
		var ms []map[string]any
		n := alive
		if r.URL.Query().Get("wan") == "1" {
			n = alive * 2 // pretend another dc has as many servers
		}
		for range n {
			ms = append(ms, map[string]any{"Name": "n", "Status": 1})
		}
		ms = append(ms, map[string]any{"Name": "gone", "Status": 4})
		js(w, ms)
	})
	mux.HandleFunc("/v1/agent/self", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"Config": map[string]any{"NodeName": "web-1", "Server": false}})
	})
	mux.HandleFunc("/v1/health/service/", func(w http.ResponseWriter, r *http.Request) {
		var es []map[string]any
		for _, n := range healthyOn {
			es = append(es, map[string]any{"Node": map[string]any{"Node": n}, "Service": map[string]any{"Service": "web"}})
		}
		js(w, es)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckOnceConsul(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		leader  string
		alive   int
		healthy []string
		check   spec.ConsulCheck
		pass    bool
	}{
		{"leader known", "10.0.0.10:8300", 3, nil, spec.ConsulCheck{Leader: true}, true},
		{"no leader", "", 3, nil, spec.ConsulCheck{Leader: true}, false},
		{"enough members", "", 4, nil, spec.ConsulCheck{Members: ">=4"}, true},
		{"too few members", "", 3, nil, spec.ConsulCheck{Members: ">=4"}, false},
		{"enough wan members", "", 3, nil, spec.ConsulCheck{WANMembers: ">=6"}, true},
		{"too few wan members", "", 2, nil, spec.ConsulCheck{WANMembers: ">=6"}, false},
		{"service passing here", "", 1, []string{"web-1"}, spec.ConsulCheck{ServiceHealthy: "web"}, true},
		{"service passing elsewhere", "", 1, []string{"web-2"}, spec.ConsulCheck{ServiceHealthy: "web"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := fakeConsul(t, c.leader, c.alive, c.healthy)
			ck := c.check
			ck.Addr = strings.TrimPrefix(srv.URL, "http://")
			err := checkOnce(ctx, &spec.Ready{Consul: &ck}, tcpDial, srv.Client(), "")
			if (err == nil) != c.pass {
				t.Errorf("err=%v, want pass=%v", err, c.pass)
			}
		})
	}
}

func TestBinaryKey(t *testing.T) {
	f := &spec.File{Versions: map[string]string{"consul": "1.22.7"}}
	for _, c := range []struct {
		p    spec.Process
		want string
		ok   bool
	}{
		{spec.Process{Run: spec.Command{"consul", "agent"}}, "consul@1.22.7", true},
		{spec.Process{Run: spec.Command{"consul", "agent"}, Version: "1.21.0"}, "consul@1.21.0", true},
		{spec.Process{Run: spec.Command{"sleep", "1"}}, "", false},
		{spec.Process{}, "", false},
	} {
		got, ok := binaryKey(f, &c.p)
		if got != c.want || ok != c.ok {
			t.Errorf("binaryKey(%v) = %q, %v", c.p.Run, got, ok)
		}
	}
}

func TestRuntimeVarsAndPath(t *testing.T) {
	n := &spec.RawNode{Networks: map[string]*spec.Attachment{"b": {IP: "10.0.1.5"}, "a": {IP: "10.0.0.5"}}}
	r := runtimeVars("/run/parker/x", "/run/parker/x/nodes/n", "/usr/bin/parker", n)
	got := r.Replace("${run_dir} ${node_dir} ${parker} ${node.ip} ${other}")
	want := "/run/parker/x /run/parker/x/nodes/n /usr/bin/parker 10.0.0.5 ${other}"
	if got != want {
		t.Errorf("runtimeVars: %q, want %q", got, want)
	}
	p := pathWith(map[string]string{"envoy": "/c/envoy/1.30/envoy", "consul": "/c/consul/1.22/consul"})
	if !strings.HasPrefix(p, "/c/consul/1.22:/c/envoy/1.30:/usr/local/bin:") {
		t.Errorf("pathWith: %q", p)
	}
}

func TestTail(t *testing.T) {
	f := filepath.Join(t.TempDir(), "log")
	os.WriteFile(f, []byte("1\n2\n3\n4\n5\n"), 0o644)
	if got := tail(f, 2); got != "4\n5" {
		t.Errorf("tail = %q", got)
	}
	if got := tail(f+"x", 2); got != "" {
		t.Errorf("missing file: %q", got)
	}
}

func TestSnapshotAndState(t *testing.T) {
	dir := t.TempDir()
	h := &Handle{lab: "x", runDir: dir, procs: map[string]*proc{
		"n/a": {node: "n", name: "a", pid: 42, alive: true, isReady: true, restarts: 2},
		"n/b": {node: "n", name: "b"},
	}}
	s := h.Snapshot()
	if s["n/a"].Pid != 42 || !s["n/a"].Ready || s["n/a"].Restarts != 2 || s["n/b"].Alive {
		t.Errorf("snapshot: %+v", s)
	}
	h.writeState()
	var st State
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	if st.Lab != "x" || st.Pids["n/a"] != 42 || len(st.Pids) != 1 {
		t.Errorf("state: %+v", st)
	}
}

func TestSuperviseRestarts(t *testing.T) {
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := make(chan struct{})
	close(up)
	p := &proc{id: "n/a", spec: &spec.Process{Restart: "on-failure"}}
	h := &Handle{lab: "x", runDir: t.TempDir(), procs: map[string]*proc{"n/a": p}, life: life, up: up}
	starts := 0
	start := func() (<-chan error, error) {
		starts++
		done := make(chan error, 1)
		done <- nil // the restarted run exits cleanly: on-failure stops here
		return done, nil
	}
	finished := make(chan struct{})
	go func() {
		h.supervise(p, errFake, start, func(string, ...any) {})
		close(finished)
	}()
	<-finished
	if starts != 1 || p.restarts != 1 {
		t.Errorf("starts=%d restarts=%d, want 1 and 1", starts, p.restarts)
	}

	// Policy "no" never restarts; a cancelled life stops supervision.
	p2 := &proc{id: "n/b", spec: &spec.Process{Restart: "no"}}
	h.supervise(p2, errFake, func() (<-chan error, error) { t.Error("restarted"); return nil, nil }, nil)
	cancel()
	p3 := &proc{id: "n/c", spec: &spec.Process{Restart: "always"}}
	h.supervise(p3, nil, func() (<-chan error, error) { t.Error("restarted after stop"); return nil, nil }, nil)
}

var errFake = &os.PathError{Op: "wait", Path: "x", Err: os.ErrClosed}

func TestConsulCheckOverHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Consul-Token") != "mgmt" {
			http.Error(w, "ACL not found", http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode("10.0.0.10:8300")
	}))
	defer srv.Close()
	dir := t.TempDir()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	os.WriteFile(filepath.Join(dir, "ca.pem"), caPEM, 0o644)
	addr := strings.TrimPrefix(srv.URL, "https://")
	logPath := filepath.Join(dir, "consul.log")
	ctx := context.Background()
	hc := &http.Client{Transport: &http.Transport{}}

	ok := &spec.Ready{Consul: &spec.ConsulCheck{Addr: addr, Leader: true, Token: "mgmt", CAFile: "ca.pem"}}
	if err := checkOnce(ctx, ok, tcpDial, hc, logPath); err != nil {
		t.Errorf("https check: %v", err)
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "ca.pem"), []byte("not a cert"), 0o644)
	bad := &spec.Ready{Consul: &spec.ConsulCheck{Addr: addr, Leader: true, Token: "mgmt", CAFile: filepath.Join(other, "ca.pem")}}
	if err := checkOnce(ctx, bad, tcpDial, hc, logPath); err == nil {
		t.Error("check passed with a bad CA file")
	}
	plain := &spec.Ready{Consul: &spec.ConsulCheck{Addr: addr, Leader: true, Token: "mgmt"}}
	if err := checkOnce(ctx, plain, tcpDial, hc, logPath); err == nil {
		t.Error("plain HTTP check passed against an HTTPS server")
	}
}

func TestConsulConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ConsulConfig(&spec.ConsulCheck{}, http.DefaultClient, dir)
	if err != nil || cfg.Scheme != "http" || cfg.Address != "127.0.0.1:8500" {
		t.Errorf("plain: %+v %v", cfg, err)
	}
	if _, err := ConsulConfig(&spec.ConsulCheck{CAFile: "missing.pem"}, http.DefaultClient, dir); err == nil {
		t.Error("missing CA file accepted")
	}
}
