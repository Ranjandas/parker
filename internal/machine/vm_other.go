//go:build !darwin || !cgo

package machine

import (
	"context"
	"errors"
	"runtime"
)

// Supported reports whether this build can run a machine.
const Supported = false

// Run needs Apple's Virtualization framework: macOS only.
func (m Machine) Run(ctx context.Context, log func(string, ...any)) error { return Unsupported() }

// Unsupported says why this build can't run a machine.
func Unsupported() error {
	if runtime.GOOS == "darwin" {
		return errors.New("this parker build has no VM support: build it with CGO_ENABLED=1 and sign it (make machine)")
	}
	return errors.New("parker machine runs on macOS; on Linux, parker runs labs directly")
}
