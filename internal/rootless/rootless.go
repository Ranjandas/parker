// Package rootless runs the Parker daemon without root: inside a private
// user + network + mount namespace where the invoking user is root and holds
// CAP_NET_ADMIN over a throwaway network stack. Labs live entirely in that
// namespace, so the host is untouched; the CLI reaches them through the
// daemon's socket (exec, logs, proxy, port-forward).
package rootless

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// childEnv marks the re-executed daemon inside the namespaces.
const childEnv = "PARKER_ROOTLESS_CHILD"

// Dir is where a user's rootless daemon keeps its socket and run dirs:
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

// RunBase of the rootless daemon's labs.
func RunBase() string { return filepath.Join(Dir(), "run") }

// InChild reports whether this process is the daemon inside the namespaces.
func InChild() bool { return os.Getenv(childEnv) == "1" }

// Command returns `self args...` set up to start in new user, network and
// mount namespaces with the current user mapped to root.
func Command(self string, args ...string) *exec.Cmd {
	cmd := exec.Command(self, args...)
	cmd.Env = append(os.Environ(), childEnv+"=1", "PARKER_RUN_DIR="+RunBase())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
		Setsid:                     true,
	}
	return cmd
}

// Setup runs inside the child before serving: private mounts, a private
// /run/netns for named namespaces, and loopback up. The rootless dir stays
// visible so host-side clients can reach the socket.
func Setup() error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	if st, err := os.Stat("/run/netns"); err == nil && st.IsDir() {
		if err := unix.Mount("tmpfs", "/run/netns", "tmpfs", 0, "mode=755"); err != nil {
			return fmt.Errorf("mount /run/netns: %w", err)
		}
	} else {
		// No /run/netns on the host and we can't create it there: put a
		// tmpfs over /run and bring the rootless dir back on top.
		keep, err := os.Open(Dir())
		if err != nil {
			return err
		}
		defer keep.Close()
		if err := unix.Mount("tmpfs", "/run", "tmpfs", 0, "mode=755"); err != nil {
			return fmt.Errorf("mount /run: %w", err)
		}
		if err := os.MkdirAll("/run/netns", 0o755); err != nil {
			return err
		}
		if strings.HasPrefix(Dir(), "/run/") {
			if err := os.MkdirAll(Dir(), 0o700); err != nil {
				return err
			}
			src := "/proc/self/fd/" + strconv.Itoa(int(keep.Fd()))
			if err := unix.Mount(src, Dir(), "", unix.MS_BIND, ""); err != nil {
				return fmt.Errorf("restore %s: %w", Dir(), err)
			}
		}
	}
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return err
	}
	return netlink.LinkSetUp(lo)
}

// Explain turns a failure to create the namespaces into advice.
func Explain(err error) error {
	if b, rerr := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); rerr == nil && len(b) > 0 && b[0] == '1' {
		return fmt.Errorf("%w\nAppArmor blocks unprivileged user namespaces here (Ubuntu 23.10+). Either:\n"+
			"  sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0\n"+
			"or run parker with sudo", err)
	}
	if b, rerr := os.ReadFile("/proc/sys/user/max_user_namespaces"); rerr == nil && len(b) > 0 && b[0] == '0' {
		return fmt.Errorf("%w\nuser namespaces are disabled (user.max_user_namespaces=0); enable them or run parker with sudo", err)
	}
	return fmt.Errorf("%w\n(rootless mode needs unprivileged user namespaces; or run parker with sudo)", err)
}
