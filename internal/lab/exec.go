package lab

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/fetch"
	"github.com/Ranjandas/parker/internal/spec"
)

// ResolveNode maps dc1/server-1 or dc1-server-1 to the expanded node name.
func ResolveNode(low *spec.File, node string) (string, error) {
	n := strings.ReplaceAll(node, "/", "-")
	if _, ok := low.Nodes[n]; !ok {
		return "", fmt.Errorf("no node %q (have: %s)", node, strings.Join(slices.Sorted(maps.Keys(low.Nodes)), ", "))
	}
	return n, nil
}

// ExecEnv returns PATH (with the lab's pinned binaries first) and the
// environment for a command run inside a node.
func ExecEnv(low *spec.File, node string, base []string, log io.Writer) (path string, env []string) {
	path = os.Getenv("PATH")
	for k, v := range low.Versions {
		if Needs(low, k) {
			if b, err := fetch.Binary(k, v, log); err == nil {
				path = filepath.Dir(b) + ":" + path
			}
		}
	}
	env = append(slices.Clone(base), "PATH="+path, "PARKER_LAB="+low.Name, "PARKER_NODE="+node)
	return path, env
}

// LookPath finds name in a PATH string.
func LookPath(name, path string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, d := range filepath.SplitList(path) {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found in PATH", name)
}
