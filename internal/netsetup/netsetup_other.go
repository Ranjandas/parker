//go:build !linux

package netsetup

import (
	"errors"
	"runtime"

	"github.com/Ranjandas/parker/internal/spec"
)

// ErrUnsupported: labs need Linux network namespaces. On other systems
// parker is a client for a daemon on a Linux host (PARKER_HOST).
var ErrUnsupported = errors.New("labs need Linux; on " + runtime.GOOS + " set PARKER_HOST to a parker daemon on a Linux host")

func Up(lab string, f *spec.File) error               { return ErrUnsupported }
func Namespaces(lab string) ([]string, error)         { return nil, ErrUnsupported }
func Down(lab string, log func(string, ...any)) error { return ErrUnsupported }
func HostNets() ([]HostNet, error)                    { return nil, nil }

func Partition(lab string, f *spec.File, groups [][]string) error { return ErrUnsupported }
func Heal(lab string, f *spec.File, nodes []string) (int, error)  { return 0, ErrUnsupported }
func CutsOn(lab, node string) ([]string, error)                   { return nil, ErrUnsupported }
