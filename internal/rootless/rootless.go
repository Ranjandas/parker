// Package rootless runs the Parker daemon without root: inside a private
// user + network + mount namespace where the invoking user is root and holds
// CAP_NET_ADMIN over a throwaway network stack. Labs live entirely in that
// namespace, so the host is untouched; the CLI reaches them through the
// daemon's socket (exec, logs, proxy, port-forward).
package rootless

import (
	"os"
	"path/filepath"
	"strconv"
)

// childEnv marks the re-executed daemon inside the namespaces.
const childEnv = "PARKER_ROOTLESS_CHILD"

// Dir is where a user's rootless daemon keeps its socket:
// $XDG_RUNTIME_DIR/parker, else /tmp/parker-<uid>. The same path is visible
// inside and outside the namespace.
func Dir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "parker")
	}
	return filepath.Join(os.TempDir(), "parker-"+strconv.Itoa(os.Getuid()))
}

// SocketPath of the rootless daemon.
func SocketPath() string { return filepath.Join(Dir(), "parker.sock") }

// RunBase of the rootless daemon's labs: $XDG_STATE_HOME/parker/run, else
// ~/.local/state/parker/run, else Dir()/run. Labs hold Consul data, which
// outgrows the runtime dir (a small tmpfs), so they live on disk.
func RunBase() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "parker", "run")
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".local", "state", "parker", "run")
	}
	return filepath.Join(Dir(), "run")
}

// InChild reports whether this process is the daemon inside the namespaces.
func InChild() bool { return os.Getenv(childEnv) == "1" }
