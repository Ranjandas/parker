package lab

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/Ranjandas/parker/internal/spec"
)

func topo() *spec.File {
	return &spec.File{
		Parker:   spec.SchemaVersion,
		Name:     "demo",
		Versions: map[string]string{"consul": "1.22.7", "envoy": "1.30.0"},
		Networks: map[string]*spec.Network{"lan": {Subnet: "10.77.0.0/24"}, "b": {Subnet: "10.78.0.0/24"}},
		Nodes: map[string]*spec.RawNode{
			"dc1-server-1": {
				Networks:  map[string]*spec.Attachment{"lan": {IP: "10.77.0.10"}},
				Processes: map[string]*spec.Process{"consul": {Run: spec.Command{"consul", "agent"}}},
			},
			"box": {
				Networks: map[string]*spec.Attachment{"lan": {IP: "10.77.0.20"}, "b": {IP: "10.78.0.20"}},
				Processes: map[string]*spec.Process{
					"sleeper": {Run: spec.Command{"sleep", "300"}},
					"web":     {Run: spec.Command{"python3", "-m", "http.server"}},
				},
			},
		},
	}
}

func TestResolveNode(t *testing.T) {
	low := topo()
	for in, want := range map[string]string{"dc1/server-1": "dc1-server-1", "dc1-server-1": "dc1-server-1", "box": "box"} {
		got, err := ResolveNode(low, in)
		if err != nil || got != want {
			t.Errorf("ResolveNode(%q) = %q, %v", in, got, err)
		}
	}
	_, err := ResolveNode(low, "dc2/server-1")
	if err == nil || !strings.Contains(err.Error(), "have: box, dc1-server-1") {
		t.Errorf("unknown node: %v", err)
	}
}

func TestNodeIPsAndNeeds(t *testing.T) {
	low := topo()
	if got := NodeIPs(low.Nodes["box"]); !slices.Equal(got, []string{"10.78.0.20", "10.77.0.20"}) {
		t.Errorf("NodeIPs sorted by network name: %v", got)
	}
	if !Needs(low, "consul") || Needs(low, "envoy") {
		t.Error("Needs: consul is run, envoy is not")
	}
}

func TestLookPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "data"), nil, 0o644)
	path := "/nonexistent:" + dir
	if got, err := LookPath("tool", path); err != nil || got != exe {
		t.Errorf("LookPath(tool) = %q, %v", got, err)
	}
	if _, err := LookPath("data", path); err == nil {
		t.Error("non-executable file found")
	}
	if got, _ := LookPath("./rel/x", path); got != "./rel/x" {
		t.Errorf("paths are returned as is: %q", got)
	}
}

func TestExecEnv(t *testing.T) {
	low := topo()
	low.Versions = nil // nothing to fetch
	t.Setenv("PATH", "/usr/bin")
	path, env := ExecEnv(low, "box", []string{"HOME=/root"}, nil)
	if path != "/usr/bin" {
		t.Errorf("path %q", path)
	}
	want := []string{"HOME=/root", "PATH=/usr/bin", "PARKER_LAB=demo", "PARKER_NODE=box"}
	if !slices.Equal(env, want) {
		t.Errorf("env %v, want %v", env, want)
	}
}

func writeLab(t *testing.T, base string, low *spec.File) {
	t.Helper()
	dir := filepath.Join(base, low.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := yaml.Marshal(low)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, TopologyFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLabsAndStatusFromRunDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("PARKER_RUN_DIR", base)
	if labs, err := Labs(); err != nil || len(labs) != 0 {
		t.Fatalf("empty: %v %v", labs, err)
	}
	writeLab(t, base, topo())
	os.MkdirAll(filepath.Join(base, "not-a-lab"), 0o755) // no topology.yaml
	os.WriteFile(filepath.Join(base, "daemon.log"), nil, 0o644)
	labs, err := Labs()
	if err != nil || !slices.Equal(labs, []string{"demo"}) {
		t.Fatalf("Labs = %v, %v", labs, err)
	}

	// A state file with a pid that isn't in the (missing) namespace.
	state, _ := json.Marshal(map[string]any{"lab": "demo", "pids": map[string]int{"box/sleeper": os.Getpid()}})
	os.WriteFile(filepath.Join(base, "demo", "state.json"), state, 0o644)

	st, err := GetStatus("demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Lab != "demo" || st.Supervised || st.RunDir != filepath.Join(base, "demo") {
		t.Errorf("status header: %+v", st)
	}
	if len(st.Nodes) != 2 || st.Nodes[0].Name != "box" || st.Nodes[1].Name != "dc1-server-1" {
		t.Fatalf("nodes: %+v", st.Nodes)
	}
	box := st.Nodes[0]
	if box.Namespace || len(box.Processes) != 2 || box.Processes[0].Name != "sleeper" {
		t.Fatalf("box: %+v", box)
	}
	sl := box.Processes[0]
	if sl.Pid != os.Getpid() || sl.Alive || sl.Ready != nil {
		t.Errorf("pid outside the lab's netns must not count as alive: %+v", sl)
	}
	if !strings.HasSuffix(sl.LogPath, "demo/nodes/box/sleeper.log") {
		t.Errorf("log path %q", sl.LogPath)
	}
	if st.Nodes[1].Role != "" {
		t.Errorf("no live agent, no role: %q", st.Nodes[1].Role)
	}

	if _, err := GetStatus("nope", nil); err == nil {
		t.Error("status of a missing lab")
	}
}

func TestRoleFrom(t *testing.T) {
	for _, c := range []struct {
		name   string
		server bool
		leader string
		ips    []string
		want   string
	}{
		{"client", false, "10.0.0.10:8300", []string{"10.0.0.100"}, "client"},
		{"leader", true, "10.0.0.10:8300", []string{"10.0.0.10"}, "leader"},
		{"follower", true, "10.0.0.10:8300", []string{"10.0.0.11"}, "follower"},
		{"no leader", true, "", []string{"10.0.0.11"}, "no-leader"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/agent/self":
					json.NewEncoder(w).Encode(map[string]any{"Config": map[string]any{"Server": c.server}})
				case "/v1/status/leader":
					json.NewEncoder(w).Encode(c.leader)
				}
			}))
			defer srv.Close()
			if got := roleFrom(srv.Client(), srv.URL, c.ips); got != c.want {
				t.Errorf("role %q, want %q", got, c.want)
			}
		})
	}
	if got := roleFrom(http.DefaultClient, "http://127.0.0.1:1", nil); got != "" {
		t.Errorf("unreachable agent: %q", got)
	}
}

func TestPrivHint(t *testing.T) {
	if PrivHint(nil) != nil {
		t.Error("nil stays nil")
	}
	err := PrivHint(errors.New("create bridge: operation not permitted"))
	if os.Geteuid() != 0 && !strings.Contains(err.Error(), "try sudo") {
		t.Errorf("no hint for non-root: %v", err)
	}
	if other := PrivHint(errors.New("boom")); other.Error() != "boom" {
		t.Errorf("unrelated error changed: %v", other)
	}
}
