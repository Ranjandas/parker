package supervisor

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"

	"github.com/vishvananda/netns"
)

// inNetns runs fn on a locked OS thread switched into the namespace at path.
// Anything fn creates there (a forked child, a socket) stays in that
// namespace after the thread switches back.
func inNetns(path string, fn func() error) error {
	runtime.LockOSThread()
	orig, err := netns.Get()
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer orig.Close()
	target, err := netns.GetFromPath(path)
	if err != nil {
		runtime.UnlockOSThread()
		return fmt.Errorf("open netns %s: %w", path, err)
	}
	defer target.Close()
	if err := netns.Set(target); err != nil {
		runtime.UnlockOSThread()
		return err
	}
	fnErr := fn()
	if err := netns.Set(orig); err != nil {
		// Leave the thread locked: the runtime will discard it when this
		// goroutine exits rather than reuse a thread in the wrong netns.
		return fmt.Errorf("restore netns: %w", err)
	}
	runtime.UnlockOSThread()
	return fnErr
}

// startIn forks cmd inside the namespace at path.
func startIn(path string, cmd *exec.Cmd) error {
	return inNetns(path, cmd.Start)
}

// dialerIn returns a DialContext that opens sockets inside the namespace.
func dialerIn(path string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		var c net.Conn
		err := inNetns(path, func() error {
			var d net.Dialer
			var err error
			c, err = d.DialContext(ctx, network, addr)
			return err
		})
		return c, err
	}
}

// InNetns runs fn on an OS thread switched into the namespace at path, so a
// process fn forks (cmd.Start, pty.Start) starts inside it.
func InNetns(path string, fn func() error) error { return inNetns(path, fn) }

// DialerIn returns a DialContext that opens sockets inside the namespace at
// path.
func DialerIn(path string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialerIn(path)
}
