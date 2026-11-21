package fake

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]Level{"trace": LevelDebug, "DEBUG": LevelDebug, "info": LevelInfo, "": LevelInfo, "warning": LevelWarn, "error": LevelError} {
		if got, err := ParseLevel(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("want an error")
	}
}

func TestLoadLogLevel(t *testing.T) {
	c, err := Load("api", "", 1, env(map[string]string{"LOG_LEVEL": "warn"}))
	if err != nil || c.LogLevel != LevelWarn {
		t.Fatalf("%v %v", c.LogLevel, err)
	}
	// The fake: block wins.
	c, err = Load("api", "", 1, env(map[string]string{"LOG_LEVEL": "warn", EnvConfig: `{"log_level":"debug"}`}))
	if err != nil || c.LogLevel != LevelDebug {
		t.Fatalf("%v %v", c.LogLevel, err)
	}
	if _, err := Load("api", "", 1, env(map[string]string{"LOG_LEVEL": "loud"})); err == nil {
		t.Error("want an error")
	}
}

func TestLogLevels(t *testing.T) {
	ts, s := start(t, &Config{Name: "api", Faults: DefaultFaults(), RequestFaults: true})
	var buf bytes.Buffer
	s.log = log.New(&buf, "", 0)

	get(t, ts.URL+"/x", map[string]string{"X-Probe": "1"})
	if out := buf.String(); !strings.Contains(out, "[INFO] request id=") || strings.Contains(out, "X-Probe") {
		t.Errorf("info:\n%s", out)
	}

	buf.Reset()
	s.SetLevel(LevelDebug)
	get(t, ts.URL+"/x", map[string]string{"X-Probe": "1"})
	if out := buf.String(); !strings.Contains(out, "log level: debug") || !strings.Contains(out, "header X-Probe: 1") {
		t.Errorf("debug:\n%s", out)
	}

	buf.Reset()
	s.SetLevel(LevelWarn)
	get(t, ts.URL+"/x", nil)
	if out := buf.String(); strings.Contains(out, "request id=") {
		t.Errorf("warn logged a good request:\n%s", out)
	}
	buf.Reset()
	get(t, ts.URL+"/x?error-rate=100", nil)
	if out := buf.String(); !strings.Contains(out, "request id=") {
		t.Errorf("warn dropped a failed request:\n%s", out)
	}
}

func TestControlLogLevel(t *testing.T) {
	c, err := Load("api", "", 1, env(map[string]string{"LOG_LEVEL": "warn"}))
	if err != nil {
		t.Fatal(err)
	}
	s := New(c)
	s.log = log.New(&bytes.Buffer{}, "", 0)
	sock := filepath.Join(t.TempDir(), SocketName("api"))
	if err := s.ListenControl(sock); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if l, err := ControlLogLevel(ctx, sock, "", false); err != nil || l != "warn" {
		t.Fatalf("get: %q %v", l, err)
	}
	if l, err := ControlLogLevel(ctx, sock, "trace", false); err != nil || l != "debug" || s.Level() != LevelDebug {
		t.Fatalf("set: %q %v", l, err)
	}
	if _, err := ControlLogLevel(ctx, sock, "loud", false); err == nil || !strings.Contains(err.Error(), "loud") {
		t.Fatalf("bad level: %v", err)
	}
	if l, err := ControlLogLevel(ctx, sock, "", true); err != nil || l != "warn" {
		t.Fatalf("reset: %q %v", l, err)
	}
	if _, err := ControlLogLevel(ctx, filepath.Join(t.TempDir(), "none.sock"), "", false); err == nil || !strings.Contains(err.Error(), "no control socket") {
		t.Fatalf("no socket: %v", err)
	}
}
