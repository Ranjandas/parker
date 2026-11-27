package lab

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"

	"github.com/Ranjandas/parker/internal/expand"
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
	Paused   bool // by a pause fault
	Stopped  bool // by a stop fault, until start
}

type NodeStatus struct {
	Name      string
	IPs       []string
	Namespace bool
	Processes []ProcStatus
	Role      string // leader | follower | no-leader | client | ""
	// DC and Partition are where the node sits (its labels).
	DC, Partition string
	// MembersAlive of MembersTotal LAN members its agent sees.
	MembersAlive, MembersTotal int
	// Health of its service instances in the catalog, by service name.
	Health map[string]string
	// Product, State and Detail are the product-neutral summary: which
	// product the node runs and what it says about the node (see
	// parker.proto's NodeStatus).
	Product, State, Detail string
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

	nodes := slices.Sorted(maps.Keys(low.Nodes))
	agents := make([]agentInfo, len(nodes))
	vaults := make([][2]string, len(nodes))
	var roleWG sync.WaitGroup
	var health map[string]map[string]string
	roleWG.Add(1)
	go func() {
		defer roleWG.Done()
		health = ServiceHealth(low)
	}()
	for i, node := range nodes {
		n := low.Nodes[node]
		nsIno, nsOK := inode(netsetup.NetnsPath(name, node))
		ns := NodeStatus{Name: node, IPs: NodeIPs(n), Namespace: nsOK}
		if l := n.Labels; l != nil {
			ns.DC, ns.Partition, ns.Product = l.DC, l.Partition, l.Product
		}
		hasAgent := false
		for _, pn := range slices.Sorted(maps.Keys(n.Processes)) {
			id := node + "/" + pn
			ps := ProcStatus{Name: pn, LogPath: filepath.Join(supervisor.NodeDir(dir, node), pn+".log")}
			if info, ok := snap[id]; ok {
				ready := info.Ready
				ps.Pid, ps.Alive, ps.Ready, ps.Restarts = info.Pid, info.Alive, &ready, info.Restarts
				ps.Paused, ps.Stopped = info.Paused, info.Held && !info.Alive
			} else if pid := saved.Pids[id]; pid > 0 {
				ps.Pid = pid
				ps.Alive = nsOK && pidInNetns(pid, nsIno)
			}
			ns.Processes = append(ns.Processes, ps)
			if pn == "consul" && ps.Alive {
				hasAgent = true
			}
			if pn == "vault" && ps.Alive && ns.Product == expand.ProductVault {
				roleWG.Add(1)
				go func() {
					defer roleWG.Done()
					vaults[i] = vaultSummary(low, node)
				}()
			}
		}
		if hasAgent {
			roleWG.Add(1)
			go func() {
				defer roleWG.Done()
				agents[i] = consulAgent(name, node, ns.IPs, AgentCheck(low, node))
			}()
		}
		st.Nodes = append(st.Nodes, ns)
	}
	roleWG.Wait()
	for i := range st.Nodes {
		a := agents[i]
		st.Nodes[i].Role, st.Nodes[i].MembersAlive, st.Nodes[i].MembersTotal = a.role, a.alive, a.total
		st.Nodes[i].Health = health[st.Nodes[i].Name]
		if a.role != "" {
			st.Nodes[i].State = a.role
			if a.total > 0 {
				st.Nodes[i].Detail = fmt.Sprintf("%d/%d members", a.alive, a.total)
			}
		}
		if v := vaults[i]; v[0] != "" {
			st.Nodes[i].State, st.Nodes[i].Detail = v[0], v[1]
		}
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
