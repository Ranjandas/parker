package rootless

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

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
