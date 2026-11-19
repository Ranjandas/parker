package lab

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"

	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/supervisor"
)

type ProcStatus struct {
	Name     string
	Pid      int
	Alive    bool
	Ready    *bool // nil when unknown (lab not supervised by this process)
	Restarts int
	LogPath  string
}

type NodeStatus struct {
	Name      string
	IPs       []string
	Namespace bool
	Processes []ProcStatus
	Role      string // leader | follower | no-leader | client | ""
}

type Status struct {
	Lab        string
	RunDir     string
	Supervised bool
	Nodes      []NodeStatus
}

// GetStatus describes a lab from its saved topology, its namespaces and
// /proc. With a supervisor handle it also knows readiness and restarts.
func GetStatus(name string, h *supervisor.Handle) (*Status, error) {
	low, err := LoadTopology(name)
	if err != nil {
		return nil, err
	}
	dir := RunDir(name)
	st := &Status{Lab: name, RunDir: dir, Supervised: h != nil}

	var snap map[string]supervisor.ProcInfo
	if h != nil {
		snap = h.Snapshot()
	}
	var saved supervisor.State
	if b, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		_ = json.Unmarshal(b, &saved)
	}

	token := ManagementToken(low)
	nodes := slices.Sorted(maps.Keys(low.Nodes))
	roles := make([]string, len(nodes))
	var roleWG sync.WaitGroup
	for i, node := range nodes {
		n := low.Nodes[node]
		nsIno, nsOK := inode(netsetup.NetnsPath(name, node))
		ns := NodeStatus{Name: node, IPs: NodeIPs(n), Namespace: nsOK}
		hasAgent := false
		for _, pn := range slices.Sorted(maps.Keys(n.Processes)) {
			id := node + "/" + pn
			ps := ProcStatus{Name: pn, LogPath: filepath.Join(supervisor.NodeDir(dir, node), pn+".log")}
			if info, ok := snap[id]; ok {
				ready := info.Ready
				ps.Pid, ps.Alive, ps.Ready, ps.Restarts = info.Pid, info.Alive, &ready, info.Restarts
			} else if pid := saved.Pids[id]; pid > 0 {
				ps.Pid = pid
				ps.Alive = nsOK && pidInNetns(pid, nsIno)
			}
			ns.Processes = append(ns.Processes, ps)
			if pn == "consul" && ps.Alive {
				hasAgent = true
			}
		}
		if hasAgent {
			roleWG.Add(1)
			go func() {
				defer roleWG.Done()
				roles[i] = consulRole(name, node, ns.IPs, token)
			}()
		}
		st.Nodes = append(st.Nodes, ns)
	}
	roleWG.Wait()
	for i := range st.Nodes {
		st.Nodes[i].Role = roles[i]
	}
	return st, nil
}

func inode(path string) (uint64, bool) {
	var s syscall.Stat_t
	if err := syscall.Stat(path, &s); err != nil {
		return 0, false
	}
	return s.Ino, true
}

// pidInNetns reports whether pid is running inside the netns with inode ino
// (so a recycled pid elsewhere doesn't count).
func pidInNetns(pid int, ino uint64) bool {
	i, ok := inode(filepath.Join("/proc", strconv.Itoa(pid), "ns/net"))
	return ok && i == ino
}
