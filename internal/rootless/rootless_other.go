//go:build !linux

package rootless

import (
	"errors"
	"os/exec"
	"runtime"
	"syscall"
)

var errUnsupported = errors.New("rootless mode needs Linux user namespaces, not " + runtime.GOOS)

// Command returns a command that fails to start: no namespaces here.
func Command(self string, args ...string) *exec.Cmd {
	cmd := exec.Command(self, args...)
	cmd.Err = errUnsupported
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	return cmd
}

func Setup() error            { return errUnsupported }
func Explain(err error) error { return err }
