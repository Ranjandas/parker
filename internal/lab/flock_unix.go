//go:build unix

package lab

import (
	"os"
	"syscall"
)

func flock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
