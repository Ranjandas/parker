// Package scenario plays a scenario's steps against a running lab.
package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Ranjandas/parker/internal/fake"
	"github.com/Ranjandas/parker/internal/spec"
)

// Lab is what steps act on. The daemon implements it over a running lab;
// tests use a fake.
type Lab interface {
	// Resolve a selector to node names.
	Resolve(sel string) ([]string, error)
	// Ready reports nil once every process on the nodes is ready.
	Ready(nodes []string) error
	// Exec runs argv on node and returns its combined output and exit code.
	Exec(ctx context.Context, node string, argv []string) (out string, code int, err error)
	// Consul checks an assertion once.
	Consul(ctx context.Context, a *spec.ConsulAssert) error
	// Snapshot saves what (logs) and returns where it went.
	Snapshot(what string) (string, error)
	// Partition cuts the network between groups of nodes; one group is cut
	// off from the rest.
	Partition(groups [][]string) error
	// Heal removes the cuts on and to nodes; nil heals everything.
	Heal(nodes []string) error
	// Proc runs a process action (kill, stop, start, restart, pause,
	// resume) on one node's processes.
	Proc(ctx context.Context, action, node string) error
	// UpgradeOrder sorts nodes into the order to upgrade them in.
	UpgradeOrder(nodes []string) ([]string, error)
	// Upgrade moves one node's product processes to version and waits for
	// the node, and its cluster, to be healthy again.
	Upgrade(ctx context.Context, node, product, version string) error
	// Fake changes (or, for an empty step, reads) the faults of the fake
	// services on node and returns a line per service.
	Fake(ctx context.Context, node string, st *spec.FakeStep) ([]string, error)
	// Call sends one request to a fake service on node (svc "" for its
	// only one) and returns the status and body.
	Call(ctx context.Context, node, svc, path string, hdr map[string]string) (int, []byte, error)
	// LogLevel changes (or, for an empty step, reads) the log levels of
	// the processes on node and returns a line per process.
	LogLevel(ctx context.Context, node string, st *spec.LogLevelStep) ([]string, error)
}

// Upgrades lists a scenario's upgrade steps, so their binaries can be
// fetched up front.
func Upgrades(sc *spec.Scenario) []*spec.UpgradeStep {
	var out []*spec.UpgradeStep
	if sc == nil {
		return nil
	}
	for _, st := range sc.Steps {
		if st != nil && st.Upgrade != nil {
			out = append(out, st.Upgrade)
		}
	}
	return out
}

// ProcActions are the step actions that act on nodes' processes.
var ProcActions = []string{"kill", "stop", "start", "restart", "pause", "resume"}

// procTarget is the step's process action and its selector, if it has one.
func procTarget(st *spec.Step) (action, sel string) {
	for _, a := range []struct{ name, sel string }{
		{"kill", st.Kill}, {"stop", st.Stop}, {"start", st.Start},
		{"restart", st.Restart}, {"pause", st.Pause}, {"resume", st.Resume},
	} {
		if a.sel != "" {
			return a.name, a.sel
		}
	}
	return "", ""
}

// Clock lets tests run scenarios without waiting.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RealClock is the wall clock.
var RealClock Clock = realClock{}

// DefaultWait is how long `wait:` waits for readiness.
const DefaultWait = 2 * time.Minute

// StepError is a failed step.
type StepError struct {
	Index int // from 1
	Label string
	Err   error
}

func (e *StepError) Error() string { return fmt.Sprintf("step %d (%s): %v", e.Index, e.Label, e.Err) }
func (e *StepError) Unwrap() error { return e.Err }

// Validate checks a scenario's shape without a lab: one action per step,
// durations and at: that parse.
func Validate(sc *spec.Scenario) error {
	if sc == nil || len(sc.Steps) == 0 {
		return errors.New("no steps")
	}
	var errs []error
	for i, st := range sc.Steps {
		if st == nil {
			errs = append(errs, fmt.Errorf("step %d: empty", i+1))
			continue
		}
		if err := validateStep(st); err != nil {
			errs = append(errs, fmt.Errorf("step %d: %w", i+1, err))
		}
	}
	switch sc.OnFailure {
	case "", "keep", "teardown":
	default:
		errs = append(errs, fmt.Errorf("on_failure: want keep or teardown, got %q", sc.OnFailure))
	}
	return errors.Join(errs...)
}

func validateStep(st *spec.Step) error {
	if n := len(actions(st)); n != 1 {
		if n == 0 {
			return errors.New("no action (want one of wait, sleep, exec, assert, snapshot, partition, heal, " + strings.Join(ProcActions, ", ") + ", upgrade, fake)")
		}
		return fmt.Errorf("one action per step, got %s", strings.Join(actions(st), ", "))
	}
	if st.At != "" {
		if _, _, err := parseAt(st.At); err != nil {
			return err
		}
	}
	if st.Sleep != "" {
		if _, err := time.ParseDuration(st.Sleep); err != nil {
			return fmt.Errorf("sleep: %w", err)
		}
	}
	if e := st.Exec; e != nil && (e.On == "" || len(e.Run) == 0) {
		return errors.New("exec: needs on: and run:")
	}
	if a := st.Assert; a != nil {
		n := 0
		for _, set := range []bool{a.Consul != nil, a.Exec != nil, a.Call != nil} {
			if set {
				n++
			}
		}
		if n != 1 {
			return errors.New("assert: needs exactly one of consul:, exec: or call:")
		}
		if a.Call != nil && a.Call.On == "" {
			return errors.New("assert.call: needs on:")
		}
		if a.Exec != nil && (a.Exec.On == "" || len(a.Exec.Run) == 0) {
			return errors.New("assert.exec: needs on: and run:")
		}
		if c := a.Consul; c != nil && !c.Leader && c.Members == "" && c.Peers == "" && c.ServiceHealthy == "" {
			return errors.New("assert.consul: nothing to check (leader, members, peers, service_healthy)")
		}
		if a.Within != "" {
			if _, err := time.ParseDuration(a.Within); err != nil {
				return fmt.Errorf("assert.within: %w", err)
			}
		}
	}
	if st.Snapshot != "" && st.Snapshot != "logs" {
		return fmt.Errorf("snapshot: want logs, got %q", st.Snapshot)
	}
	if u := st.Upgrade; u != nil && (u.On == "" || u.To == "") {
		return errors.New("upgrade: needs on: and to:")
	}
	if f := st.Fake; f != nil {
		if f.On == "" {
			return errors.New("fake: needs on:")
		}
		if f.Reset && !(&spec.FakeStep{Health: f.Health, Latency: f.Latency, Errors: f.Errors, RateLimit: f.RateLimit, Limits: f.Limits}).Empty() {
			return errors.New("fake: reset: true takes no other changes")
		}
		if err := fake.Validate(f.Change()); err != nil {
			return fmt.Errorf("fake: %w", err)
		}
		if l := f.Limits; l != nil && (l.IdleTimeout != "" || l.HeaderTimeout != "" || l.H2MaxStreams != nil) {
			return errors.New("fake: limits.idle_timeout, header_timeout and h2_max_streams are set at start only: put them in the service's fake: block")
		}
	}
	if ll := st.LogLevel; ll != nil {
		if err := ValidateLogLevel(ll); err != nil {
			return err
		}
	}
	for i, g := range st.Partition {
		if len(g) == 0 || slices.Contains(g, "") {
			return fmt.Errorf("partition: group %d is empty", i+1)
		}
	}
	return nil
}

func actions(st *spec.Step) []string {
	var a []string
	if st.Wait != "" {
		a = append(a, "wait")
	}
	if st.Sleep != "" {
		a = append(a, "sleep")
	}
	if st.Exec != nil {
		a = append(a, "exec")
	}
	if st.Assert != nil {
		a = append(a, "assert")
	}
	if st.Snapshot != "" {
		a = append(a, "snapshot")
	}
	if len(st.Partition) > 0 {
		a = append(a, "partition")
	}
	if st.Heal != "" {
		a = append(a, "heal")
	}
	if st.Upgrade != nil {
		a = append(a, "upgrade")
	}
	if st.Fake != nil {
		a = append(a, "fake")
	}
	if st.LogLevel != nil {
		a = append(a, "log_level")
	}
	for _, x := range []struct{ name, sel string }{
		{"kill", st.Kill}, {"stop", st.Stop}, {"start", st.Start},
		{"restart", st.Restart}, {"pause", st.Pause}, {"resume", st.Resume},
	} {
		if x.sel != "" {
			a = append(a, x.name)
		}
	}
	return a
}

// Selectors lists the selectors a scenario uses, for checking them against
// the topology before anything runs.
func Selectors(sc *spec.Scenario) []string {
	var out []string
	for _, st := range sc.Steps {
		if st == nil {
			continue
		}
		if st.Wait != "" {
			out = append(out, waitSelector(st.Wait))
		}
		if st.Exec != nil {
			out = append(out, st.Exec.On)
		}
		if st.Assert != nil && st.Assert.Exec != nil {
			out = append(out, st.Assert.Exec.On)
		}
		if st.Assert != nil && st.Assert.Call != nil {
			out = append(out, st.Assert.Call.On)
		}
		for _, g := range st.Partition {
			out = append(out, g...)
		}
		if st.Heal != "" && st.Heal != "all" {
			out = append(out, st.Heal)
		}
		if _, sel := procTarget(st); sel != "" {
			out = append(out, sel)
		}
		if st.Upgrade != nil {
			out = append(out, st.Upgrade.On)
		}
		if st.Fake != nil {
			out = append(out, st.Fake.On)
		}
		if st.LogLevel != nil {
			out = append(out, st.LogLevel.On)
		}
	}
	return out
}

// parseAt reads "+30s" (after the previous step) or "T+30s" (after the start).
func parseAt(s string) (d time.Duration, absolute bool, err error) {
	rest := s
	if strings.HasPrefix(rest, "T") {
		absolute, rest = true, rest[1:]
	}
	if !strings.HasPrefix(rest, "+") {
		return 0, false, fmt.Errorf("at: want +DURATION or T+DURATION, got %q", s)
	}
	d, err = time.ParseDuration(rest[1:])
	if err != nil {
		return 0, false, fmt.Errorf("at: %w", err)
	}
	return d, absolute, nil
}

// "dc1 ready" and "dc1" both wait for dc1.
func waitSelector(w string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(w), " ready"))
}

// Label names a step in the output: its name, else its action.
func Label(st *spec.Step) string {
	if st.Name != "" {
		return st.Name
	}
	switch {
	case st.Wait != "":
		return "wait " + waitSelector(st.Wait)
	case st.Sleep != "":
		return "sleep " + st.Sleep
	case st.Exec != nil:
		return "exec on " + st.Exec.On
	case st.Assert != nil && st.Assert.Consul != nil:
		return "assert consul"
	case st.Assert != nil && st.Assert.Call != nil:
		return "assert call " + st.Assert.Call.On
	case st.Assert != nil:
		return "assert exec on " + st.Assert.Exec.On
	case st.Snapshot != "":
		return "snapshot " + st.Snapshot
	case len(st.Partition) > 0:
		var gs []string
		for _, g := range st.Partition {
			gs = append(gs, strings.Join(g, ","))
		}
		if len(gs) == 1 {
			return "partition " + gs[0] + " from the rest"
		}
		return "partition " + strings.Join(gs, " | ")
	case st.Heal != "":
		return "heal " + st.Heal
	case st.Upgrade != nil:
		return fmt.Sprintf("upgrade %s %s to %s", st.Upgrade.On, st.Upgrade.Product(), st.Upgrade.To)
	case st.Fake != nil:
		return fakeLabel(st.Fake)
	case st.LogLevel != nil:
		return logLevelLabel(st.LogLevel)
	}
	if a, sel := procTarget(st); a != "" {
		return a + " " + sel
	}
	return "step"
}

// Run plays the steps in order and stops at the first failure.
func Run(ctx context.Context, sc *spec.Scenario, l Lab, clk Clock, log io.Writer) error {
	if err := Validate(sc); err != nil {
		return err
	}
	if clk == nil {
		clk = RealClock
	}
	start := clk.Now()
	prevEnd := start
	say := func(format string, a ...any) {
		fmt.Fprintf(log, "[%6s] ", clk.Now().Sub(start).Round(100*time.Millisecond))
		fmt.Fprintf(log, format+"\n", a...)
	}
	for i, st := range sc.Steps {
		label := Label(st)
		if st.At != "" {
			d, abs, _ := parseAt(st.At)
			due := prevEnd.Add(d)
			if abs {
				due = start.Add(d)
			}
			if late := clk.Now().Sub(due); late > time.Second {
				say("step %d (%s) is %s late for at: %s", i+1, label, late.Round(100*time.Millisecond), st.At)
			}
			if err := clk.Sleep(ctx, due.Sub(clk.Now())); err != nil {
				return err
			}
		}
		say("step %d: %s", i+1, label)
		if err := runStep(ctx, st, l, clk, say); err != nil {
			say("FAIL step %d (%s): %v", i+1, label, err)
			return &StepError{Index: i + 1, Label: label, Err: err}
		}
		prevEnd = clk.Now()
	}
	say("PASS: %d steps", len(sc.Steps))
	return nil
}

func runStep(ctx context.Context, st *spec.Step, l Lab, clk Clock, say func(string, ...any)) error {
	switch {
	case st.Wait != "":
		nodes, err := l.Resolve(waitSelector(st.Wait))
		if err != nil {
			return err
		}
		return retry(ctx, clk, DefaultWait, func() error { return l.Ready(nodes) })

	case st.Sleep != "":
		d, _ := time.ParseDuration(st.Sleep)
		return clk.Sleep(ctx, d)

	case st.Exec != nil:
		nodes, err := l.Resolve(st.Exec.On)
		if err != nil {
			return err
		}
		var failed []string
		for _, n := range nodes {
			out, code, err := l.Exec(ctx, n, st.Exec.Run)
			if err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
			for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
				if line != "" {
					say("  %s | %s", n, line)
				}
			}
			if code != 0 {
				failed = append(failed, fmt.Sprintf("%s exited %d", n, code))
			}
		}
		if len(failed) > 0 && !st.Exec.AllowFailure {
			return errors.New(strings.Join(failed, ", "))
		}
		return nil

	case st.Assert != nil:
		within := time.Duration(0)
		if st.Assert.Within != "" {
			within, _ = time.ParseDuration(st.Assert.Within)
		}
		check := func() error { return l.Consul(ctx, st.Assert.Consul) }
		if e := st.Assert.Exec; e != nil {
			check = func() error { return assertExec(ctx, l, e) }
		}
		if c := st.Assert.Call; c != nil {
			check = func() error { return assertCall(ctx, l, c, say) }
		}
		return retry(ctx, clk, within, check)

	case st.Snapshot != "":
		where, err := l.Snapshot(st.Snapshot)
		if err != nil {
			return err
		}
		say("  %s saved to %s", st.Snapshot, where)
		return nil

	case len(st.Partition) > 0:
		var groups [][]string
		for _, g := range st.Partition {
			var nodes []string
			for _, sel := range g {
				ns, err := l.Resolve(sel)
				if err != nil {
					return err
				}
				nodes = append(nodes, ns...)
			}
			slices.Sort(nodes)
			nodes = slices.Compact(nodes)
			say("  side %d: %s", len(groups)+1, strings.Join(nodes, ", "))
			groups = append(groups, nodes)
		}
		return l.Partition(groups)

	case st.Heal != "":
		if st.Heal == "all" {
			return l.Heal(nil)
		}
		nodes, err := l.Resolve(st.Heal)
		if err != nil {
			return err
		}
		return l.Heal(nodes)

	case st.Upgrade != nil:
		u := st.Upgrade
		nodes, err := l.Resolve(u.On)
		if err != nil {
			return err
		}
		if nodes, err = l.UpgradeOrder(nodes); err != nil {
			return err
		}
		for _, n := range nodes {
			say("  upgrading %s to %s %s", n, u.Product(), u.To)
			if err := l.Upgrade(ctx, n, u.Product(), u.To); err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
		}
		return nil
	}
	if f := st.Fake; f != nil {
		nodes, err := l.Resolve(f.On)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			lines, err := l.Fake(ctx, n, f)
			if err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
			for _, line := range lines {
				say("  %s", line)
			}
		}
		return nil
	}
	if ll := st.LogLevel; ll != nil {
		nodes, err := l.Resolve(ll.On)
		if err != nil {
			return err
		}
		var errs []error
		for _, n := range nodes {
			lines, err := l.LogLevel(ctx, n, ll)
			for _, line := range lines {
				say("  %s", line)
			}
			if err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	if action, sel := procTarget(st); action != "" {
		nodes, err := l.Resolve(sel)
		if err != nil {
			return err
		}
		say("  %s %s", action, strings.Join(nodes, ", "))
		// Nodes go together: servers started one by one could each wait
		// for a quorum that needs the others.
		errs := make([]error, len(nodes))
		var wg sync.WaitGroup
		for i, n := range nodes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := l.Proc(ctx, action, n); err != nil {
					errs[i] = fmt.Errorf("%s: %w", n, err)
				}
			}()
		}
		wg.Wait()
		return errors.Join(errs...)
	}
	return errors.New("no action")
}

// assertCall sends the request and checks the code, a hop and the body.
// The last answer's chain is shown when it passes.
func assertCall(ctx context.Context, l Lab, c *spec.CallAssert, say func(string, ...any)) error {
	nodes, err := l.Resolve(c.On)
	if err != nil {
		return err
	}
	if len(nodes) != 1 {
		return fmt.Errorf("call: on: %s matches %d nodes; name one", c.On, len(nodes))
	}
	status, body, err := l.Call(ctx, nodes[0], c.Service, c.Path, c.Header)
	if err != nil {
		return err
	}
	want := c.Code
	if want == 0 {
		want = 200
	}
	var r fake.Response
	if json.Unmarshal(body, &r) != nil || r.Name == "" {
		return fmt.Errorf("%s answered %d, not with a fake service's JSON: %s", nodes[0], status, lastLine(string(body)))
	}
	if status != want {
		return fmt.Errorf("%s answered %d, want %d: %s", nodes[0], status, want, fake.Oneline(&r))
	}
	if c.Reaches != "" && !reaches(&r, c.Reaches) {
		return fmt.Errorf("the chain doesn't reach %s: %s", c.Reaches, fake.Oneline(&r))
	}
	if c.Body != "" && !strings.Contains(string(body), c.Body) {
		return fmt.Errorf("the answer has no %q: %s", c.Body, fake.Oneline(&r))
	}
	say("  %s", fake.Oneline(&r))
	return nil
}

// reaches tells whether some hop is want: a service name, dc/name or a
// node name.
func reaches(r *fake.Response, want string) bool {
	dc, name, scoped := strings.Cut(want, "/")
	for _, h := range fake.Hops(r) {
		switch {
		case scoped && h.DC == dc && h.Name == name:
			return true
		case !scoped && (h.Name == want || h.Node == want):
			return true
		}
	}
	return false
}

func assertExec(ctx context.Context, l Lab, e *spec.ExecAssert) error {
	nodes, err := l.Resolve(e.On)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		out, code, err := l.Exec(ctx, n, e.Run)
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		if code != 0 {
			return fmt.Errorf("%s: exited %d: %s", n, code, lastLine(out))
		}
		if e.Output != "" && !strings.Contains(out, e.Output) {
			return fmt.Errorf("%s: output has no %q: %s", n, e.Output, lastLine(out))
		}
	}
	return nil
}

// retry calls f until it passes or within runs out (at least once).
func retry(ctx context.Context, clk Clock, within time.Duration, f func() error) error {
	deadline := clk.Now().Add(within)
	for {
		err := f()
		if err == nil {
			return nil
		}
		if !clk.Now().Before(deadline) {
			if within > 0 {
				return fmt.Errorf("not true within %s: %w", within, err)
			}
			return err
		}
		if err := clk.Sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func fakeLabel(f *spec.FakeStep) string {
	on := f.On
	if f.Service != "" {
		on += " (" + f.Service + ")"
	}
	switch {
	case f.Reset:
		return "fake " + on + ": reset"
	case f.Empty():
		return "fake " + on
	}
	var parts []string
	if f.Health != nil {
		parts = append(parts, fmt.Sprintf("health %d", f.Health.Code))
	}
	if e := f.Errors; e != nil && e.Rate != nil {
		parts = append(parts, fmt.Sprintf("errors %g%%", *e.Rate*100))
	} else if e != nil {
		parts = append(parts, "errors")
	}
	if f.Latency != nil {
		parts = append(parts, "latency")
	}
	if r := f.RateLimit; r != nil && r.RPS != nil {
		parts = append(parts, fmt.Sprintf("rate limit %g/s", *r.RPS))
	} else if r != nil {
		parts = append(parts, "rate limit")
	}
	if f.Limits != nil {
		parts = append(parts, "limits")
	}
	return "fake " + on + ": " + strings.Join(parts, ", ")
}

// logLevelKinds are what log_level: only: takes.
var logLevelKinds = []string{"consul", "envoy", "fake"}

// logLevels are the levels log_level: takes; critical and off are Envoy's.
var logLevels = []string{"trace", "debug", "info", "warn", "error", "critical", "off"}

// ValidateLogLevel checks a log_level step without a lab.
func ValidateLogLevel(ll *spec.LogLevelStep) error {
	if ll.On == "" {
		return errors.New("log_level: needs on:")
	}
	if ll.Only != "" && !slices.Contains(logLevelKinds, ll.Only) {
		return fmt.Errorf("log_level: only: want consul, envoy or fake, got %q", ll.Only)
	}
	if ll.Reset && (ll.Level != "" || len(ll.Loggers) > 0) {
		return errors.New("log_level: reset: true takes no level or loggers")
	}
	check := func(what, l string) error {
		if !slices.Contains(logLevels, l) {
			return fmt.Errorf("log_level: %s %q: want %s", what, l, strings.Join(logLevels, ", "))
		}
		return nil
	}
	if ll.Level != "" {
		if err := check("level", ll.Level); err != nil {
			return err
		}
		if (ll.Level == "critical" || ll.Level == "off") && ll.Only != "envoy" && ll.Process == "" {
			return fmt.Errorf("log_level: level %s is Envoy's only: add only: envoy", ll.Level)
		}
	}
	if len(ll.Loggers) > 0 && ll.Only != "" && ll.Only != "envoy" {
		return errors.New("log_level: loggers are Envoy's (only: envoy)")
	}
	for _, k := range slices.Sorted(maps.Keys(ll.Loggers)) {
		if k == "" || k == "level" || k == "paths" {
			return fmt.Errorf("log_level: logger %q: name an Envoy logger (upstream, http, router, ...)", k)
		}
		if err := check("logger "+k, ll.Loggers[k]); err != nil {
			return err
		}
	}
	return nil
}

// logLevelLabel is "log level debug on dc1/web (envoy)".
func logLevelLabel(ll *spec.LogLevelStep) string {
	var what string
	switch {
	case ll.Reset:
		what = "reset log levels"
	case ll.Empty():
		what = "show log levels"
	default:
		var parts []string
		if ll.Level != "" {
			parts = append(parts, ll.Level)
		}
		for _, k := range slices.Sorted(maps.Keys(ll.Loggers)) {
			parts = append(parts, k+"="+ll.Loggers[k])
		}
		what = "log level " + strings.Join(parts, " ")
	}
	what += " on " + ll.On
	switch {
	case ll.Process != "":
		what += "/" + ll.Process
	case ll.Only != "":
		what += " (" + ll.Only + ")"
	}
	return what
}
