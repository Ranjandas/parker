package lab

import (
	"errors"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestSpaceWarning(t *testing.T) {
	defer func(f func(string) (uint64, error)) { freeBytes = f }(freeBytes)
	low := &spec.File{Nodes: map[string]*spec.RawNode{
		"dc1-server-1": {Labels: &spec.NodeLabels{Role: "server"}},
		"dc1-server-2": {Labels: &spec.NodeLabels{Role: "server"}},
		"dc1-web":      {Labels: &spec.NodeLabels{Role: "client"}},
		"sleeper":      {},
	}}
	if got, want := needBytes(low), uint64(2*serverBytes+2*nodeBytes); got != want {
		t.Errorf("needBytes = %d, want %d", got, want)
	}
	freeBytes = func(string) (uint64, error) { return 100 << 20, nil }
	w := spaceWarning("/run/user/501/parker/run/x", low)
	if !strings.Contains(w, "100 MB free") || !strings.Contains(w, "about 416 MB") || !strings.Contains(w, "XDG_STATE_HOME") {
		t.Errorf("warning: %q", w)
	}
	freeBytes = func(string) (uint64, error) { return 10 << 30, nil }
	if w := spaceWarning("/x", low); w != "" {
		t.Errorf("plenty of room: %q", w)
	}
	freeBytes = func(string) (uint64, error) { return 0, errors.New("no statfs") }
	if w := spaceWarning("/x", low); w != "" {
		t.Errorf("statfs failure warns: %q", w)
	}
}

func TestSpaceHint(t *testing.T) {
	defer func(f func(string) (uint64, error)) { freeBytes = f }(freeBytes)
	freeBytes = func(string) (uint64, error) { return 3 << 20, nil }
	err := spaceHint("/run/x", errors.New("Failed to start Raft: fail to open write-ahead-log: no space left on device"))
	if !strings.Contains(err.Error(), "hint: the lab's run dir /run/x is out of space (3 MB free)") {
		t.Errorf("hint: %v", err)
	}
	plain := errors.New("process exited before becoming ready")
	if spaceHint("/run/x", plain) != plain {
		t.Error("other errors pass through")
	}
}
