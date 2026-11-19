package fetch

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// chownToSudoUser keeps the user's cache owned by them when run under sudo.
func chownToSudoUser(dir, file string) {
	name := os.Getenv("SUDO_USER")
	if name == "" || os.Geteuid() != 0 {
		return
	}
	u, err := user.Lookup(name)
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	for d := dir; d != CacheDir() && d != "/" && d != "."; d = filepath.Dir(d) {
		_ = os.Chown(d, uid, gid)
	}
	_ = os.Chown(CacheDir(), uid, gid)
	_ = os.Chown(file, uid, gid)
}
