package lab

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPurge(t *testing.T) {
	base := t.TempDir()
	t.Setenv("PARKER_RUN_DIR", base)
	dir := filepath.Join(base, "demo")
	if err := os.MkdirAll(filepath.Join(dir, "nodes", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nodes", "a", "consul.log"), bytes.Repeat([]byte("x"), 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if err := Purge("demo", &log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("run dir still there: %v", err)
	}
	if !strings.Contains(log.String(), "removed "+dir+" (2.0 KB)") {
		t.Errorf("log %q", log.String())
	}
	log.Reset()
	if err := Purge("demo", &log); err != nil || !strings.Contains(log.String(), "nothing to purge") {
		t.Errorf("twice: %v %q", err, log.String())
	}

	// Never outside the run base: not a lab name, or a symlink out of it.
	for _, bad := range []string{"", "..", "../etc", "a/b", "Demo"} {
		if err := Purge(bad, &log); err == nil || !strings.Contains(err.Error(), "not a lab name") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err := Purge("link", &log); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("symlink: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("symlink target removed: %v", err)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KB", 5 << 20: "5.0 MB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("%d: %s, want %s", n, got, want)
		}
	}
}
