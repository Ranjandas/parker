package lab

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/spec"
)

func expandExample(t *testing.T, path string) *spec.File {
	t.Helper()
	f, err := spec.Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	low, err := expand.Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	return low
}

// targets is "process kind admin start" per target on every node.
func targets(low *spec.File) map[string][]string {
	out := map[string][]string{}
	for n := range low.Nodes {
		for _, t := range LevelTargets(low, n) {
			out[n] = append(out[n], fmt.Sprintf("%s %s %s %s", t.Process, t.Kind, t.Admin, t.Start))
		}
	}
	return out
}

func TestLevelTargets(t *testing.T) {
	low := expandExample(t, "../../examples/mesh.yaml")
	got := targets(low)
	if len(got) == 0 {
		t.Fatal("no targets")
	}
	var envoys, consuls, fakes int
	for n, ts := range got {
		for _, s := range ts {
			switch f := strings.Fields(s); f[1] {
			case LevelEnvoy:
				envoys++
				if !strings.HasPrefix(f[2], "127.0.0.1:") || f[3] != "info" {
					t.Errorf("%s: %s", n, s)
				}
			case LevelConsul:
				consuls++
			case LevelFake:
				fakes++
			}
		}
	}
	if envoys == 0 || consuls == 0 {
		t.Errorf("envoys %d consuls %d: %v", envoys, consuls, got)
	}

	// A dataplane's Envoy is a target; consul-dataplane itself is not a
	// separate one (its admin port is its Envoy's).
	dp := targets(expandExample(t, "../../examples/dataplane.yaml"))
	found := false
	for _, ts := range dp {
		for _, s := range ts {
			if strings.Fields(s)[1] == LevelEnvoy {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("no dataplane Envoy target: %v", dp)
	}
}

func TestLevelTargetsStart(t *testing.T) {
	low := &spec.File{Name: "x", Nodes: map[string]*spec.RawNode{"n": {
		Files: map[string]string{"consul.json": `{"log_level": "DEBUG", "server": true}`},
		Processes: map[string]*spec.Process{
			"consul": {Run: spec.Command{"consul", "agent", "-config-file=x"}},
			"web-sidecar": {Run: spec.Command{"consul", "connect", "envoy", "-sidecar-for", "web",
				"-admin-bind", "127.0.0.1:19001", "--", "--log-level", "warn"}},
			"dp":  {Run: spec.Command{"consul-dataplane", "-envoy-admin-bind-port", "19002", "-log-level", "trace"}},
			"web": {Run: spec.Command{"parker", "fake-service", "--port", "9090"}, Env: map[string]string{expand.FakeEnv: `{"log_level":"trace"}`}},
			"top": {Run: spec.Command{"top"}},
		},
	}}}
	got := strings.Join(targets(low)["n"], "; ")
	want := "consul consul  debug; dp envoy 127.0.0.1:19002 trace; web fake  debug; web-sidecar envoy 127.0.0.1:19001 warn"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSummarizeEnvoyLoggers(t *testing.T) {
	body := "active loggers:\n  admin: info\n  http: debug\n  router: info\n  upstream: trace\n  config: warning\n"
	if got, want := SummarizeEnvoyLoggers(body), "info (config: warn, http: debug, upstream: trace)"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got, want := SummarizeEnvoyLoggers("active loggers:\n  a: debug\n  b: debug\n"), "debug (2 loggers)"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestWithLogLevel(t *testing.T) {
	out, err := withLogLevel([]byte(`{"log_level":"info","ports":{"http":8500}}`), "debug")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["log_level"] != "debug" || m["ports"].(map[string]any)["http"].(float64) != 8500 {
		t.Errorf("%s", out)
	}
	if _, err := withLogLevel([]byte("not json"), "debug"); err == nil {
		t.Error("want an error")
	}
}

func TestAgentLevel(t *testing.T) {
	self := map[string]map[string]any{"DebugConfig": {"Logging": map[string]any{"LogLevel": "DEBUG"}}}
	if got := agentLevel(self); got != "debug" {
		t.Errorf("got %q", got)
	}
	if got := agentLevel(map[string]map[string]any{}); got != "" {
		t.Errorf("got %q", got)
	}
}
