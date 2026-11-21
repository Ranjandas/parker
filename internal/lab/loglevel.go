package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Ranjandas/parker/internal/fake"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// Log level targets: the processes whose level can change while they run.
const (
	LevelConsul = "consul"
	LevelEnvoy  = "envoy"
	LevelFake   = "fake"
)

// LevelTarget is one process whose log level can change at runtime.
type LevelTarget struct {
	Node, Process string
	// Kind is consul, envoy or fake.
	Kind string
	// Admin is an Envoy's admin address (host:port).
	Admin string
	// Start is the level the lab started it at.
	Start string
}

// LevelTargets are the node's processes whose log level can change: its
// Consul agent, its Envoys (sidecars, gateways, a dataplane's Envoy) and
// its fake services. consul-dataplane's own level is fixed at start.
func LevelTargets(low *spec.File, node string) []LevelTarget {
	n := low.Nodes[node]
	if n == nil {
		return nil
	}
	var out []LevelTarget
	fakes := map[string]bool{}
	for _, f := range FakeInfos(low, node) {
		fakes[f.Service] = true
	}
	for _, name := range slices.Sorted(maps.Keys(n.Processes)) {
		p := n.Processes[name]
		t := LevelTarget{Node: node, Process: name, Start: "info"}
		switch {
		case fakes[name]:
			t.Kind = LevelFake
			if raw, ok := p.Env["LOG_LEVEL"]; ok && raw != "" {
				t.Start = raw
			}
			var f spec.Fake
			if json.Unmarshal([]byte(p.Env[fake.EnvConfig]), &f) == nil && f.LogLevel != "" {
				t.Start = f.LogLevel
			}
			if l, err := fake.ParseLevel(t.Start); err == nil {
				t.Start = l.String()
			}
		case len(p.Run) > 1 && filepath.Base(p.Run[0]) == "consul" && p.Run[1] == "agent" && n.Files["consul.json"] != "":
			t.Kind = LevelConsul
			var cfg map[string]any
			if json.Unmarshal([]byte(n.Files["consul.json"]), &cfg) == nil {
				if l, ok := cfg["log_level"].(string); ok && l != "" {
					t.Start = strings.ToLower(l)
				}
			}
		default:
			admin := envoyAdmin(p.Run)
			if admin == "" {
				continue
			}
			t.Kind, t.Admin = LevelEnvoy, admin
			t.Start = flagValue(p.Run, "--log-level", "-log-level", "info")
		}
		out = append(out, t)
	}
	return out
}

// envoyAdmin finds an Envoy's admin address in a command line: consul
// connect envoy's -admin-bind, or consul-dataplane's
// -envoy-admin-bind-address and -envoy-admin-bind-port.
func envoyAdmin(run spec.Command) string {
	if a := flagValue(run, "-admin-bind", "--admin-bind", ""); a != "" {
		return a
	}
	if port := flagValue(run, "-envoy-admin-bind-port", "--envoy-admin-bind-port", ""); port != "" {
		return hostPort(flagValue(run, "-envoy-admin-bind-address", "--envoy-admin-bind-address", "127.0.0.1"), port)
	}
	return ""
}

func hostPort(host, port string) string { return host + ":" + port }

// flagValue is the value after the last of the flags in run, else def.
func flagValue(run spec.Command, a, b, def string) string {
	v := def
	for i, x := range run {
		if (x == a || x == b) && i+1 < len(run) {
			v = run[i+1]
		}
		if k, val, ok := strings.Cut(x, "="); ok && (k == a || k == b) {
			v = val
		}
	}
	return v
}

// LogLevel changes (or, for an empty step, reads) the log levels of the
// processes on node and returns a line per process.
func (l *ScenarioLab) LogLevel(ctx context.Context, node string, st *spec.LogLevelStep) ([]string, error) {
	return SetLogLevels(ctx, l.Low, node, st)
}

// SetLogLevels applies a log_level step to one node.
func SetLogLevels(ctx context.Context, low *spec.File, node string, st *spec.LogLevelStep) ([]string, error) {
	all := LevelTargets(low, node)
	var ts []LevelTarget
	for _, t := range all {
		if st.Only != "" && t.Kind != st.Only {
			continue
		}
		if st.Process != "" && t.Process != st.Process {
			continue
		}
		// Loggers alone are Envoy's; leave the rest be.
		if st.Level == "" && !st.Reset && len(st.Loggers) > 0 && t.Kind != LevelEnvoy {
			continue
		}
		ts = append(ts, t)
	}
	if len(ts) == 0 {
		var have []string
		for _, t := range all {
			have = append(have, t.Process+" ("+t.Kind+")")
		}
		what := "processes with a log level to change"
		if st.Process != "" {
			what = "process " + st.Process + " with a log level to change"
		}
		return nil, fmt.Errorf("%s: no %s (it has %s)", node, what, list(have))
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var lines []string
	var errs []error
	for _, t := range ts {
		got, err := setLevel(ctx, low, t, st)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", node, t.Process, err))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s/%s (%s): %s", node, t.Process, t.Kind, got))
	}
	return lines, errors.Join(errs...)
}

func setLevel(ctx context.Context, low *spec.File, t LevelTarget, st *spec.LogLevelStep) (string, error) {
	level := st.Level
	if st.Reset {
		level = t.Start
	}
	switch t.Kind {
	case LevelFake:
		if level == "critical" || level == "off" {
			return "", fmt.Errorf("a fake service has no %s level", level)
		}
		sock := filepath.Join(supervisor.NodeDir(RunDir(low.Name), t.Node), fake.SocketName(t.Process))
		return fake.ControlLogLevel(ctx, sock, level, false)
	case LevelConsul:
		return consulLevel(ctx, low, t, level)
	case LevelEnvoy:
		loggers := st.Loggers
		if st.Reset {
			loggers = nil
		}
		return envoyLevel(ctx, low, t, level, loggers)
	}
	return "", fmt.Errorf("unknown kind %q", t.Kind)
}

// consulLevel sets log_level in the node's consul.json and reloads the
// agent (log_level is reloadable), then reads the level back. The new
// level outlives an agent restart, until a reset or the next up.
func consulLevel(ctx context.Context, low *spec.File, t LevelTarget, level string) (string, error) {
	if level == "critical" || level == "off" {
		return "", fmt.Errorf("consul has no %s level", level)
	}
	c, err := AgentClient(low, t.Node, 10*time.Second)
	if err != nil {
		return "", err
	}
	if level != "" {
		path := filepath.Join(supervisor.NodeDir(RunDir(low.Name), t.Node), "consul.json")
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		out, err := withLogLevel(b, level)
		if err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return "", err
		}
		if err := c.Agent().Reload(); err != nil {
			return "", fmt.Errorf("reload: %w", PrivHint(err))
		}
	}
	self, err := c.Agent().Self()
	if err != nil {
		return "", PrivHint(err)
	}
	if got := agentLevel(self); got != "" {
		return got, nil
	}
	if level != "" {
		return level + " (reloaded)", nil
	}
	return "unknown", nil
}

// withLogLevel sets log_level in a consul.json.
func withLogLevel(b []byte, level string) ([]byte, error) {
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	cfg["log_level"] = level
	out, err := json.MarshalIndent(cfg, "", "  ")
	return append(out, '\n'), err
}

// agentLevel is the level in /v1/agent/self's DebugConfig.Logging.
func agentLevel(self map[string]map[string]any) string {
	lg, _ := self["DebugConfig"]["Logging"].(map[string]any)
	l, _ := lg["LogLevel"].(string)
	return strings.ToLower(l)
}

// envoyLevel posts to the admin /logging endpoint: level for every
// logger, then each named logger, and summarises the levels after.
func envoyLevel(ctx context.Context, low *spec.File, t LevelTarget, level string, loggers map[string]string) (string, error) {
	cl := &http.Client{Transport: &http.Transport{DialContext: supervisor.DialerIn(netsetup.NetnsPath(low.Name, t.Node)), DisableKeepAlives: true}}
	post := func(q url.Values) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+t.Admin+"/logging?"+q.Encode(), nil)
		if err != nil {
			return "", err
		}
		res, err := cl.Do(req)
		if err != nil {
			return "", PrivHint(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if res.StatusCode != http.StatusOK {
			return "", fmt.Errorf("admin /logging: %s: %s", res.Status, strings.TrimSpace(string(b)))
		}
		return string(b), nil
	}
	if level != "" {
		if _, err := post(url.Values{"level": {envoyWord(level)}}); err != nil {
			return "", err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(loggers)) {
		if _, err := post(url.Values{name: {envoyWord(loggers[name])}}); err != nil {
			return "", fmt.Errorf("logger %s: %w", name, err)
		}
	}
	out, err := post(nil)
	if err != nil {
		return "", err
	}
	return SummarizeEnvoyLoggers(out), nil
}

// envoyWord is Envoy's name for a level: warning, not warn.
func envoyWord(l string) string {
	if l == "warn" {
		return "warning"
	}
	return l
}

// SummarizeEnvoyLoggers turns /logging's "active loggers:" list into the
// level most loggers have plus the exceptions: "info (upstream: trace)".
func SummarizeEnvoyLoggers(body string) string {
	levels := map[string]string{}
	count := map[string]int{}
	for _, line := range strings.Split(body, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if !ok || k == "" || strings.Contains(k, " ") {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "warning" {
			v = "warn"
		}
		levels[k] = v
		count[v]++
	}
	if len(levels) == 0 {
		return strings.TrimSpace(body)
	}
	most, n := "", -1
	for _, l := range slices.Sorted(maps.Keys(count)) {
		if count[l] > n {
			most, n = l, count[l]
		}
	}
	var odd []string
	for _, k := range slices.Sorted(maps.Keys(levels)) {
		if levels[k] != most {
			odd = append(odd, k+": "+levels[k])
		}
	}
	s := most + " (" + strconv.Itoa(len(levels)) + " loggers)"
	if len(odd) > 0 {
		if len(odd) > 8 {
			odd = append(odd[:8], fmt.Sprintf("+%d more", len(odd)-8))
		}
		s = most + " (" + strings.Join(odd, ", ") + ")"
	}
	return s
}
