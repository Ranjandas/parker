// Package scenario plays a scenario's steps against a running lab.
package scenario

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

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
			return errors.New("no action (want one of wait, sleep, exec, assert, snapshot)")
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
		if (a.Consul == nil) == (a.Exec == nil) {
			return errors.New("assert: needs exactly one of consul: or exec:")
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
	case st.Assert != nil:
		return "assert exec on " + st.Assert.Exec.On
	case st.Snapshot != "":
		return "snapshot " + st.Snapshot
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
		return retry(ctx, clk, within, check)

	case st.Snapshot != "":
		where, err := l.Snapshot(st.Snapshot)
		if err != nil {
			return err
		}
		say("  %s saved to %s", st.Snapshot, where)
		return nil
	}
	return errors.New("no action")
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
