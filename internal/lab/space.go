package lab

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Ranjandas/parker/internal/spec"
)

// Rough disk a lab node needs in its run dir. A Consul server's Raft WAL
// preallocates 64 MB segments and snapshots beside them; agents and Envoy
// only write logs and small state.
const (
	serverBytes = 192 << 20
	nodeBytes   = 16 << 20
)

// freeBytes is the space available to the caller on path's filesystem.
var freeBytes = func(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// needBytes estimates the run-dir space low needs.
func needBytes(low *spec.File) uint64 {
	var n uint64
	for _, node := range low.Nodes {
		if node.Labels != nil && node.Labels.Role == "server" {
			n += serverBytes
		} else {
			n += nodeBytes
		}
	}
	return n
}

// spaceWarning is a warning when dir's filesystem has less room than low
// is likely to need, else "".
func spaceWarning(dir string, low *spec.File) string {
	free, err := freeBytes(dir)
	if err != nil {
		return ""
	}
	if need := needBytes(low); free < need {
		return fmt.Sprintf("warning: %s has %s free; this lab may need about %s (%s)", dir, mb(free), mb(need), moveHint)
	}
	return ""
}

// spaceHint adds where to find more room to an error that ran out of it.
func spaceHint(dir string, err error) error {
	if err == nil || !strings.Contains(err.Error(), "no space left on device") {
		return err
	}
	free := "?"
	if f, ferr := freeBytes(dir); ferr == nil {
		free = mb(f)
	}
	return fmt.Errorf("%w\nhint: the lab's run dir %s is out of space (%s free); %s", err, dir, free, moveHint)
}

const moveHint = "set PARKER_RUN_DIR (root) or XDG_STATE_HOME (rootless) to a bigger filesystem and restart the daemon"

func mb(b uint64) string { return fmt.Sprintf("%d MB", b>>20) }
