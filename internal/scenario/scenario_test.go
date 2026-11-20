package scenario

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/Ranjandas/parker/internal/spec"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	if d > 0 {
		c.t = c.t.Add(d)
	}
	return nil
}

type fakeLab struct {
	clk      *fakeClock
	log      []string
	readyAt  time.Time         // Ready fails before this
	leaderAt time.Time         // Consul leader fails before this
	exec     map[string]string // node -> output; "FAIL" exits 1
	mu       sync.Mutex
	procs    []string // "action node", in any order
	procFail string   // node whose Proc fails
	calls    []string // bodies Call answers in turn (status 200, or 503 when it has "503")
}

func (f *fakeLab) Resolve(sel string) ([]string, error) {
	switch sel {
	case "dc1":
		return []string{"dc1-server-1", "dc1-web-1"}, nil
	case "dc1/web":
		return []string{"dc1-web-1", "dc1-web-2"}, nil
	case "dc1/leader":
		return []string{"dc1-server-1"}, nil
	}
	return nil, fmt.Errorf("selector %q matches no nodes", sel)
}

func (f *fakeLab) Ready(nodes []string) error {
	f.log = append(f.log, "ready "+strings.Join(nodes, ","))
	if f.clk.Now().Before(f.readyAt) {
		return errors.New("not ready: dc1-web-1/web")
	}
	return nil
}

func (f *fakeLab) Exec(_ context.Context, node string, argv []string) (string, int, error) {
	f.log = append(f.log, "exec "+node+" "+strings.Join(argv, " "))
	out := f.exec[node]
	if out == "FAIL" {
		return "boom\n", 1, nil
	}
	return out, 0, nil
}

func (f *fakeLab) Consul(_ context.Context, a *spec.ConsulAssert) error {
	f.log = append(f.log, "consul")
	if a.Leader && f.clk.Now().Before(f.leaderAt) {
		return errors.New("dc1 has no raft leader")
	}
	return nil
}

func (f *fakeLab) Snapshot(what string) (string, error) {
	f.log = append(f.log, "snapshot "+what)
	return "/run/parker/x/snapshots/1", nil
}

func (f *fakeLab) Partition(groups [][]string) error {
	var gs []string
	for _, g := range groups {
		gs = append(gs, strings.Join(g, ","))
	}
	f.log = append(f.log, "partition "+strings.Join(gs, " | "))
	return nil
}

func (f *fakeLab) Heal(nodes []string) error {
	if nodes == nil {
		f.log = append(f.log, "heal all")
	} else {
		f.log = append(f.log, "heal "+strings.Join(nodes, ","))
	}
	return nil
}

func (f *fakeLab) Proc(_ context.Context, action, node string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs = append(f.procs, action+" "+node)
	if node == f.procFail {
		return errors.New("no such process")
	}
	return nil
}

func (f *fakeLab) UpgradeOrder(nodes []string) ([]string, error) {
	out := slices.Clone(nodes)
	slices.Reverse(out)
	return out, nil
}

func (f *fakeLab) Upgrade(_ context.Context, node, product, version string) error {
	f.log = append(f.log, "upgrade "+node+" "+product+"@"+version)
	if node == f.procFail {
		return errors.New("unhealthy")
	}
	return nil
}

func (f *fakeLab) Fake(_ context.Context, node string, st *spec.FakeStep) ([]string, error) {
	f.log = append(f.log, "fake "+node)
	if node == f.procFail {
		return nil, errors.New("no control socket")
	}
	return []string{node + "/api: health 503"}, nil
}

func (f *fakeLab) Call(_ context.Context, node, svc, path string, hdr map[string]string) (int, []byte, error) {
	f.log = append(f.log, "call "+node+path)
	if len(f.calls) == 0 {
		return 0, nil, errors.New("connection refused")
	}
	b := f.calls[0]
	if len(f.calls) > 1 {
		f.calls = f.calls[1:]
	}
	if strings.Contains(b, `"code":503`) {
		return 503, []byte(b), nil
	}
	return 200, []byte(b), nil
}

func parse(t *testing.T, y string) *spec.Scenario {
	t.Helper()
	var sc spec.Scenario
	if err := yaml.UnmarshalWithOptions([]byte(y), &sc, yaml.Strict()); err != nil {
		t.Fatal(err)
	}
	return &sc
}

func run(t *testing.T, sc *spec.Scenario, f *fakeLab) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Run(context.Background(), sc, f, f.clk, &out)
	return out.String(), err
}

func newLab() *fakeLab {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	return &fakeLab{clk: clk, exec: map[string]string{"dc1-server-1": "peers ok\n", "dc1-web-1": "hello web\n", "dc1-web-2": "hello web\n"}}
}

func TestRunPasses(t *testing.T) {
	f := newLab()
	f.readyAt = f.clk.t.Add(3 * time.Second)
	f.leaderAt = f.clk.t.Add(20 * time.Second)
	sc := parse(t, `
steps:
  - wait: dc1 ready
  - exec: { on: dc1/leader, run: "consul operator raft list-peers | head -1" }
  - at: +10s
    sleep: 1s
  - assert: { consul: { leader: true }, within: 30s }
  - assert: { exec: { on: dc1/web, run: [curl, -sf, "localhost:8080"], output: web } }
  - snapshot: logs
`)
	out, err := run(t, sc, f)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"step 1: wait dc1", "dc1-server-1 | peers ok", "logs saved to /run/parker/x/snapshots/1", "PASS: 6 steps"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if f.log[len(f.log)-1] != "snapshot logs" {
		t.Errorf("log %v", f.log)
	}
	if !strings.Contains(strings.Join(f.log, "\n"), "exec dc1-server-1 sh -c consul operator raft list-peers | head -1") {
		t.Errorf("string run: goes through sh -c: %v", f.log)
	}
	if !strings.Contains(strings.Join(f.log, "\n"), "exec dc1-web-2 curl -sf localhost:8080") {
		t.Errorf("list run: and every node of the selector: %v", f.log)
	}
	// wait polled until 3s, exec, at:+10s, sleep 1s, leader polled to 20s.
	if got := f.clk.t.Sub(time.Unix(1000, 0)); got < 20*time.Second || got > 21*time.Second {
		t.Errorf("elapsed %s", got)
	}
}

func TestRunAtAbsoluteAndLate(t *testing.T) {
	f := newLab()
	sc := parse(t, `
steps:
  - sleep: 5s
  - at: T+8s
    snapshot: logs
  - at: T+2s
    snapshot: logs
`)
	out, err := run(t, sc, f)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.clk.t.Sub(time.Unix(1000, 0)); got != 8*time.Second {
		t.Errorf("elapsed %s, want 8s", got)
	}
	if !strings.Contains(out, "step 3 (snapshot logs) is 6s late for at: T+2s") {
		t.Errorf("late warning missing:\n%s", out)
	}
}

func TestRunFails(t *testing.T) {
	for _, c := range []struct{ name, steps, want string }{
		{"assert within", `[{assert: {consul: {leader: true}, within: 5s}}]`, "step 1 (assert consul): not true within 5s: dc1 has no raft leader"},
		{"assert once", `[{assert: {consul: {leader: true}}}]`, "step 1 (assert consul): dc1 has no raft leader"},
		{"exec exit", `[{exec: {on: dc1/web, run: "false"}}]`, "step 1 (exec on dc1/web): dc1-web-2 exited 1"},
		{"exec output", `[{assert: {exec: {on: dc1/leader, run: x, output: nope}}}]`, `output has no "nope": peers ok`},
		{"bad selector", `[{name: poke, exec: {on: dc9, run: x}}]`, `step 1 (poke): selector "dc9" matches no nodes`},
		{"wait times out", `[{wait: dc1}]`, "not true within 2m0s: not ready"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newLab()
			f.leaderAt = f.clk.t.Add(time.Hour)
			f.readyAt = f.clk.t.Add(time.Hour)
			f.exec["dc1-web-2"] = "FAIL"
			_, err := run(t, parse(t, "steps: "+c.steps), f)
			var se *StepError
			if !errors.As(err, &se) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err %v, want %q", err, c.want)
			}
		})
	}
	// allow_failure keeps going.
	f := newLab()
	f.exec["dc1-web-2"] = "FAIL"
	if _, err := run(t, parse(t, `steps: [{exec: {on: dc1/web, run: x, allow_failure: true}}, {snapshot: logs}]`), f); err != nil {
		t.Errorf("allow_failure: %v", err)
	}
}

func TestValidate(t *testing.T) {
	for steps, want := range map[string]string{
		`[]`:                              "no steps",
		`[{name: x}]`:                     "no action",
		`[{sleep: 1s, snapshot: logs}]`:   "one action per step, got sleep, snapshot",
		`[{at: "30s", sleep: 1s}]`:        "want +DURATION or T+DURATION",
		`[{at: "+3x", sleep: 1s}]`:        "at:",
		`[{sleep: soon}]`:                 "sleep:",
		`[{exec: {on: dc1}}]`:             "exec: needs on: and run:",
		`[{assert: {within: 3s}}]`:        "exactly one of consul:, exec: or call:",
		`[{assert: {consul: {dc: dc1}}}]`: "nothing to check",
		`[{assert: {consul: {leader: true}, within: x}}]`: "assert.within",
		`[{snapshot: everything}]`:                        "want logs",
	} {
		err := Validate(parse(t, "steps: "+steps))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", steps, err, want)
		}
	}
	if err := Validate(parse(t, "steps: [{sleep: 1s}]\non_failure: explode")); err == nil {
		t.Error("bad on_failure accepted")
	}
	sc := parse(t, `steps: [{wait: dc1 ready}, {exec: {on: dc1/web, run: x}}, {assert: {exec: {on: svc:api, run: x}}}, {sleep: 1s}]`)
	if got := strings.Join(Selectors(sc), " "); got != "dc1 dc1/web svc:api" {
		t.Errorf("selectors %q", got)
	}
}

func TestFaultSteps(t *testing.T) {
	sc := parse(t, `
steps:
  - partition: [[dc1/leader, dc1/web], dc1]
  - heal: dc1/web
  - partition: [dc1/web]
  - heal: all
  - kill: dc1/leader
  - stop: dc1/web
  - start: dc1/web
  - restart: dc1/leader
  - pause: dc1/web
  - resume: dc1/web
`)
	f := &fakeLab{clk: &fakeClock{}}
	out, err := run(t, sc, f)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLog := []string{
		// Groups are de-duplicated and sorted; overlap is the lab's to refuse.
		"partition dc1-server-1,dc1-web-1,dc1-web-2 | dc1-server-1,dc1-web-1",
		"heal dc1-web-1,dc1-web-2",
		"partition dc1-web-1,dc1-web-2",
		"heal all",
	}
	if !slices.Equal(f.log, wantLog) {
		t.Errorf("log:\n%s\nwant:\n%s", strings.Join(f.log, "\n"), strings.Join(wantLog, "\n"))
	}
	slices.Sort(f.procs)
	wantProcs := []string{
		"kill dc1-server-1",
		"pause dc1-web-1", "pause dc1-web-2",
		"restart dc1-server-1",
		"resume dc1-web-1", "resume dc1-web-2",
		"start dc1-web-1", "start dc1-web-2",
		"stop dc1-web-1", "stop dc1-web-2",
	}
	if !slices.Equal(f.procs, wantProcs) {
		t.Errorf("procs: %v", f.procs)
	}
	for _, s := range []string{"step 1: partition dc1/leader,dc1/web | dc1", "side 1: dc1-server-1, dc1-web-1, dc1-web-2",
		"step 3: partition dc1/web from the rest", "step 4: heal all", "step 5: kill dc1/leader", "  stop dc1-web-1, dc1-web-2"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

func TestProcStepFailureNamesNode(t *testing.T) {
	f := &fakeLab{clk: &fakeClock{}, procFail: "dc1-web-2"}
	_, err := run(t, parse(t, "steps:\n  - stop: dc1/web\n"), f)
	var se *StepError
	if !errors.As(err, &se) || se.Index != 1 || !strings.Contains(err.Error(), "dc1-web-2: no such process") {
		t.Fatalf("err = %v", err)
	}
	if len(f.procs) != 2 {
		t.Errorf("both nodes should be tried: %v", f.procs)
	}
}

func TestFaultValidationAndSelectors(t *testing.T) {
	for y, want := range map[string]string{
		"steps:\n  - kill: dc1\n    stop: dc1\n": "one action per step, got kill, stop",
		"steps:\n  - partition: [dc1, []]\n":     "partition: group 2 is empty",
		"steps:\n  - {}\n":                       "partition, heal, kill",
	} {
		if err := Validate(parse(t, y)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", y, err, want)
		}
	}
	sc := parse(t, "steps:\n  - partition: [[a, b], c]\n  - heal: all\n  - heal: d\n  - pause: e\n")
	if got := strings.Join(Selectors(sc), " "); got != "a b c d e" {
		t.Errorf("Selectors = %q", got)
	}
}

func TestUpgradeStep(t *testing.T) {
	sc := parse(t, "steps:\n  - upgrade: { on: dc1/web, to: 1.22.8 }\n  - upgrade: { on: dc1/leader, to: 1.4.0, binary: nomad }\n")
	f := &fakeLab{clk: &fakeClock{}}
	out, err := run(t, sc, f)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// In the lab's order (the fake reverses), one node at a time.
	want := []string{"upgrade dc1-web-2 consul@1.22.8", "upgrade dc1-web-1 consul@1.22.8", "upgrade dc1-server-1 nomad@1.4.0"}
	if !slices.Equal(f.log, want) {
		t.Errorf("log = %v", f.log)
	}
	if !strings.Contains(out, "step 1: upgrade dc1/web consul to 1.22.8") || !strings.Contains(out, "upgrading dc1-web-2 to consul 1.22.8") {
		t.Errorf("output:\n%s", out)
	}

	// A failed node stops the rollout there.
	f = &fakeLab{clk: &fakeClock{}, procFail: "dc1-web-2"}
	if _, err := run(t, parse(t, "steps:\n  - upgrade: { on: dc1/web, to: 1.22.8 }\n"), f); err == nil || !strings.Contains(err.Error(), "dc1-web-2: unhealthy") {
		t.Errorf("err = %v", err)
	}
	if len(f.log) != 1 {
		t.Errorf("kept going after a failure: %v", f.log)
	}

	if err := Validate(parse(t, "steps:\n  - upgrade: { on: dc1 }\n")); err == nil || !strings.Contains(err.Error(), "needs on: and to:") {
		t.Errorf("validate: %v", err)
	}
	ups := Upgrades(parse(t, "steps:\n  - sleep: 1s\n  - upgrade: { on: dc1, to: 1.2.3 }\n"))
	if len(ups) != 1 || ups[0].To != "1.2.3" || ups[0].Product() != "consul" {
		t.Errorf("Upgrades = %+v", ups)
	}
	if got := Selectors(parse(t, "steps:\n  - upgrade: { on: dc1/servers, to: 1.2.3 }\n")); !slices.Equal(got, []string{"dc1/servers"}) {
		t.Errorf("Selectors = %v", got)
	}
}

func TestFakeStep(t *testing.T) {
	sc := parse(t, "steps:\n  - fake: { on: dc1/web, health: { code: 503 }, errors: { rate: 0.5 } }\n  - fake: { on: dc1/leader, reset: true }\n")
	f := &fakeLab{clk: &fakeClock{}}
	out, err := run(t, sc, f)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := []string{"fake dc1-web-1", "fake dc1-web-2", "fake dc1-server-1"}; !slices.Equal(f.log, want) {
		t.Errorf("log = %v", f.log)
	}
	for _, w := range []string{"step 1: fake dc1/web: health 503, errors 50%", "dc1-web-2/api: health 503", "step 2: fake dc1/leader: reset"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	f = &fakeLab{clk: &fakeClock{}, procFail: "dc1-web-1"}
	if _, err := run(t, parse(t, "steps:\n  - fake: { on: dc1/web }\n"), f); err == nil || !strings.Contains(err.Error(), "dc1-web-1: no control socket") {
		t.Errorf("err = %v", err)
	}
	for bad, want := range map[string]string{
		"fake: { health: { code: 503 } }":                       "needs on:",
		"fake: { on: dc1, reset: true, health: { code: 503 } }": "takes no other changes",
		"fake: { on: dc1, errors: { rate: 5 } }":                "errors.rate",
	} {
		if err := Validate(parse(t, "steps:\n  - "+bad+"\n")); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if got := Selectors(parse(t, "steps:\n  - fake: { on: dc1/api }\n")); !slices.Equal(got, []string{"dc1/api"}) {
		t.Errorf("Selectors = %v", got)
	}
}

func TestCallAssert(t *testing.T) {
	dc1 := `{"name":"web","dc":"dc1","code":200,"upstream_calls":{"x":{"name":"api","dc":"dc1","node":"dc1-api","code":200}}}`
	dc2 := `{"name":"web","dc":"dc1","code":200,"upstream_calls":{"x":{"name":"api","dc":"dc2","node":"dc2-api","code":200}}}`
	down := `{"name":"web","dc":"dc1","code":503,"upstream_calls":{"x":{"name":"api","dc":"dc1","code":503}}}`
	// Retried until api answers from dc2.
	f := &fakeLab{clk: &fakeClock{}, calls: []string{down, dc1, dc2}}
	out, err := run(t, parse(t, "steps:\n  - assert: { call: { on: dc1/leader, path: /x, reaches: dc2/api }, within: 10s }\n"), f)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(f.log) != 3 || f.log[0] != "call dc1-server-1/x" {
		t.Errorf("log = %v", f.log)
	}
	if !strings.Contains(out, "step 1: assert call dc1/leader") || !strings.Contains(out, "web@dc1 → api@dc2") {
		t.Errorf("output:\n%s", out)
	}
	for y, want := range map[string]string{
		"{ on: dc1/leader }":                              "answered 503, want 200",
		"{ on: dc1/leader, code: 503, reaches: dc2-api }": "doesn't reach dc2-api",
		"{ on: dc1/leader, code: 503, body: rows }":       `no "rows"`,
		"{ on: dc1/web }":                                 "matches 2 nodes",
	} {
		f := &fakeLab{clk: &fakeClock{}, calls: []string{down}}
		if _, err := run(t, parse(t, "steps:\n  - assert: { call: "+y+" }\n"), f); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", y, err, want)
		}
	}
	if _, err := run(t, parse(t, "steps:\n  - assert: { call: { on: dc1/leader, code: 503, reaches: api } }\n"), &fakeLab{clk: &fakeClock{}, calls: []string{down}}); err != nil {
		t.Errorf("reaches by name: %v", err)
	}
	for bad, want := range map[string]string{
		"assert: { call: { path: / } }":                                 "needs on:",
		"assert: { call: { on: dc1 }, exec: { on: dc1, run: [true] } }": "exactly one",
	} {
		if err := Validate(parse(t, "steps:\n  - "+bad+"\n")); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if got := Selectors(parse(t, "steps:\n  - assert: { call: { on: dc1/api } }\n")); !slices.Equal(got, []string{"dc1/api"}) {
		t.Errorf("Selectors = %v", got)
	}
}
