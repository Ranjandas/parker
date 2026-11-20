package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"connectrpc.com/connect"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/scenario"
	"github.com/Ranjandas/parker/internal/spec"
)

// logLevelStep is the log_level: step for parker logs --level/--logger:
// level is a level, reset, or show ("" with no loggers is no step).
func logLevelStep(sel, proc, level, only string, loggers []string) (*spec.Step, error) {
	if level == "" && len(loggers) == 0 {
		return nil, nil
	}
	st := &spec.LogLevelStep{On: sel, Process: proc, Only: only}
	switch level {
	case "reset":
		st.Reset = true
	case "show", "":
	default:
		st.Level = level
	}
	for _, kv := range loggers {
		for _, kv := range strings.Split(kv, ",") {
			if kv = strings.TrimSpace(kv); kv == "" {
				continue
			}
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return nil, fmt.Errorf("--logger %q: want NAME=LEVEL (upstream=trace)", kv)
			}
			if st.Loggers == nil {
				st.Loggers = map[string]string{}
			}
			st.Loggers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	step := &spec.Step{LogLevel: st}
	if err := scenario.Validate(&spec.Scenario{Steps: []*spec.Step{step}}); err != nil {
		return nil, err
	}
	return step, nil
}

// applyLogLevel plays the step on the lab and prints a line per process.
func applyLogLevel(ctx context.Context, lab string, st *spec.Step, w io.Writer) error {
	y, err := scenario.Inline(st)
	if err != nil {
		return err
	}
	cl, err := ensureDaemon(ctx)
	if err != nil {
		return err
	}
	stream, err := cl.Run(ctx, connect.NewRequest(&parkerv1.RunRequest{Lab: lab, Inline: y}))
	if err != nil {
		return rpcErr(err)
	}
	var failed string
	for stream.Receive() {
		line, keep := levelLine(stream.Msg().Line)
		if strings.HasPrefix(line, "FAIL ") {
			failed = line
			continue
		}
		if keep {
			fmt.Fprintln(w, line)
		}
	}
	if err := stream.Err(); err != nil {
		if failed != "" {
			return fmt.Errorf("%s", strings.TrimPrefix(failed, "FAIL "))
		}
		return rpcErr(err)
	}
	if failed != "" {
		return fmt.Errorf("%s", strings.TrimPrefix(failed, "FAIL "))
	}
	return nil
}

var runStamp = regexp.MustCompile(`^\[\s*[^\]]*\]\s?`)

// levelLine drops a scenario line's clock and its step and PASS lines,
// leaving "dc1-web-1/web-sidecar (envoy): debug (60 loggers)".
func levelLine(s string) (string, bool) {
	s = runStamp.ReplaceAllString(s, "")
	if strings.HasPrefix(s, "step ") || strings.HasPrefix(s, "PASS") {
		return s, false
	}
	return strings.TrimSpace(s), true
}
