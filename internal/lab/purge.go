package lab

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Ranjandas/parker/internal/netsetup"
)

var labNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Purge deletes a lab's run directory: logs, saved topology, configs,
// certs and Consul data. It refuses a lab whose namespaces still exist
// (down it first), a name that isn't a lab name, and a run dir that is a
// symlink, so it never deletes outside the run base.
func Purge(name string, log io.Writer) error {
	if !labNameRe.MatchString(name) {
		return fmt.Errorf("purge: %q is not a lab name", name)
	}
	if ns, _ := netsetup.Namespaces(name); len(ns) > 0 {
		return fmt.Errorf("purge: lab %q is still running (%d namespaces); run parker down first", name, len(ns))
	}
	dir := RunDir(name)
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		fmt.Fprintf(log, "nothing to purge: no %s\n", dir)
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("purge: %s is not a directory (left alone)", dir)
	}
	size := dirSize(dir)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("purge: %w", err)
	}
	fmt.Fprintf(log, "removed %s (%s)\n", dir, humanBytes(size))
	return nil
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Running reports whether a lab's namespaces exist.
func Running(name string) bool {
	ns, _ := netsetup.Namespaces(name)
	return len(ns) > 0
}
