package rootless

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestPaths(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	t.Setenv("XDG_STATE_HOME", "/home/u/.state")
	if Dir() != "/run/user/1000/parker" || SocketPath() != "/run/user/1000/parker/parker.sock" || RunBase() != "/home/u/.state/parker/run" {
		t.Errorf("XDG paths: %s %s %s", Dir(), SocketPath(), RunBase())
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	if RunBase() != "/home/u/.local/state/parker/run" {
		t.Errorf("labs go on disk, not the runtime tmpfs: %s", RunBase())
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if want := "parker-" + strconv.Itoa(os.Getuid()); !strings.HasSuffix(Dir(), want) {
		t.Errorf("fallback dir %q, want suffix %q", Dir(), want)
	}
}

func TestInChild(t *testing.T) {
	t.Setenv(childEnv, "")
	if InChild() {
		t.Error("not the child")
	}
	t.Setenv(childEnv, "1")
	if !InChild() {
		t.Error("child not detected")
	}
}

func TestCommandSetsUpChild(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	t.Setenv("XDG_STATE_HOME", "/home/u/.state")
	cmd := Command("/bin/parker", "daemon")
	env := strings.Join(cmd.Env, "\n")
	if !strings.Contains(env, childEnv+"=1") || !strings.Contains(env, "PARKER_RUN_DIR=/home/u/.state/parker/run") {
		t.Errorf("child env missing markers")
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("no SysProcAttr")
	}
}

func TestExplainKeepsError(t *testing.T) {
	base := errors.New("fork/exec: operation not permitted")
	if err := Explain(base); !errors.Is(err, base) {
		t.Errorf("Explain lost the cause: %v", err)
	}
}
