package lab

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func shellTopo() *spec.File {
	return &spec.File{Name: "demo", Nodes: map[string]*spec.RawNode{
		"dc1-server-1": {Labels: &spec.NodeLabels{DC: "dc1", Name: "server-1"}},
		"dc2-web-1":    {Labels: &spec.NodeLabels{DC: "dc2", Name: "web-1", Partition: "ap1"}},
		"dc2-db-1":     {Labels: &spec.NodeLabels{DC: "dc2", Name: "db-1", Partition: "default"}},
		"bastion":      {},
	}}
}

func TestShellLabel(t *testing.T) {
	low := shellTopo()
	for node, want := range map[string]string{
		"dc1-server-1": "dc1/server-1",
		"dc2-web-1":    "dc2/web-1 · ap1",
		"dc2-db-1":     "dc2/db-1",
		"bastion":      "bastion",
	} {
		if got := ShellLabel(low, node); got != want {
			t.Errorf("%s: %q, want %q", node, got, want)
		}
	}
}

func envVal(env []string, k string) string {
	for _, kv := range slices.Backward(env) {
		if v, ok := strings.CutPrefix(kv, k+"="); ok {
			return v
		}
	}
	return ""
}

func TestNodeShellLeavesCommandsAlone(t *testing.T) {
	t.Setenv("PARKER_RUN_DIR", t.TempDir())
	for _, argv := range [][]string{{"consul", "members"}, {"bash", "-c", "true"}, {"python3"}} {
		got, env := NodeShell(shellTopo(), "dc1-server-1", argv, nil, "")
		if !slices.Equal(got, argv) || len(env) != 0 {
			t.Errorf("%v: became %v %v", argv, got, env)
		}
	}
}

func TestNodeShellPlainSh(t *testing.T) {
	t.Setenv("PARKER_RUN_DIR", t.TempDir())
	argv, env := NodeShell(shellTopo(), "dc2-web-1", []string{"/bin/sh"}, nil, "")
	if !slices.Equal(argv, []string{"/bin/sh"}) {
		t.Errorf("argv %v", argv)
	}
	if h := envVal(env, "HISTFILE"); filepath.Base(h) != ".sh_history" || !strings.Contains(h, "dc2-web-1") {
		t.Errorf("HISTFILE %q", h)
	}
	if p := envVal(env, "PS1"); !strings.HasPrefix(p, "dc2/web-1 · ap1 ") {
		t.Errorf("PS1 %q", p)
	}
}

// TestNodeShellBash runs bash on the generated rcfile: the user's bashrc is
// still loaded, the prompt names the node, and history goes to the node's
// own file even when the user's bashrc set another.
func TestNodeShellBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	t.Setenv("PARKER_RUN_DIR", t.TempDir())
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("USER_RC=loaded\nHISTFILE=/nonexistent/h\nPS1='user> '\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	low := shellTopo()
	argv, env := NodeShell(low, "dc2-web-1", []string{bash}, os.Environ(), home)
	if len(argv) != 3 || argv[1] != "--rcfile" {
		t.Fatalf("argv %v", argv)
	}
	// -i reads the rcfile; print what the prompt expands to.
	cmd := exec.Command(argv[0], append(argv[1:], "-i", "-c", `echo "rc=$USER_RC hist=$HISTFILE"; echo "ps1=${PS1@P}"`)...)
	cmd.Env = env
	cmd.Dir = home
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	s := string(out)
	hist := filepath.Join(RunDir("demo"), "nodes", "dc2-web-1", ".bash_history")
	for _, want := range []string{"rc=loaded", "hist=" + hist, "dc2/web-1 · ap1", "(demo)"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "user> ") {
		t.Errorf("user's prompt kept:\n%s", s)
	}
}
