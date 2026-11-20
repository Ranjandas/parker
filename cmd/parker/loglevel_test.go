package main

import (
	"strings"
	"testing"
)

func TestLogLevelStep(t *testing.T) {
	if st, err := logLevelStep("dc1-web-1", "", "", "", nil); st != nil || err != nil {
		t.Fatalf("no flags: %v %v", st, err)
	}
	st, err := logLevelStep("dc1-web-1", "web-sidecar", "debug", "", []string{"upstream=trace,http=debug", "router=info"})
	if err != nil {
		t.Fatal(err)
	}
	ll := st.LogLevel
	if ll.On != "dc1-web-1" || ll.Process != "web-sidecar" || ll.Level != "debug" || len(ll.Loggers) != 3 || ll.Loggers["upstream"] != "trace" {
		t.Errorf("%+v", ll)
	}
	if st, _ := logLevelStep("dc1", "", "reset", "", nil); !st.LogLevel.Reset || st.LogLevel.Level != "" {
		t.Errorf("reset: %+v", st.LogLevel)
	}
	if st, _ := logLevelStep("dc1", "", "show", "consul", nil); !st.LogLevel.Empty() || st.LogLevel.Only != "consul" {
		t.Errorf("show: %+v", st.LogLevel)
	}
	for _, c := range []struct {
		level, only string
		loggers     []string
		err         string
	}{
		{"loud", "", nil, `"loud"`},
		{"", "", []string{"upstream"}, "NAME=LEVEL"},
		{"debug", "nomad", nil, "only:"},
		{"off", "", nil, "Envoy's only"},
	} {
		if _, err := logLevelStep("dc1", "", c.level, c.only, c.loggers); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

func TestLevelLine(t *testing.T) {
	for in, want := range map[string]string{
		"[  0.1s]   dc1-web-1/web-sidecar (envoy): debug (60 loggers)": "dc1-web-1/web-sidecar (envoy): debug (60 loggers)",
		"[    0s] step 1: log level debug on dc1-web-1":                "",
		"[  0.2s] PASS: 1 steps":                                       "",
	} {
		got, keep := levelLine(in)
		if (want == "") == keep || (keep && got != want) {
			t.Errorf("%q: %q %v", in, got, keep)
		}
	}
}
