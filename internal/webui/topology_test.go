package webui_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/Ranjandas/parker/internal/daemon"
	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/graph"
	"github.com/Ranjandas/parker/internal/spec"
)

// layoutOf lays out an example's topology with static/topolayout.js.
func layoutOf(t *testing.T, node, example string) (pos map[string]struct{ X, Y float64 }, crossings, overlaps, around []string) {
	t.Helper()
	f, err := spec.Load(filepath.Join("..", "..", "examples", example), nil)
	if err != nil {
		t.Fatal(err)
	}
	low, err := expand.Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	// What the UI gets from the Graph RPC.
	b, err := protojson.Marshal(daemon.GraphToProto("lab", graph.Build(low)))
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(in, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, filepath.Join("testdata", "topology", "check.js"), in).Output()
	if err != nil {
		t.Fatalf("check.js: %v", err)
	}
	var r struct {
		Pos       map[string]struct{ X, Y float64 }
		Crossings []string
		Overlaps  []string
		Around    []string
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	return r.Pos, r.Crossings, r.Overlaps, r.Around
}

// TestTopologyLayout checks the topology view's layout of the examples:
// no two cards overlap, in the multi-dc labs no line has to run under a
// card it doesn't connect, and a line doesn't cross a datacenter it
// doesn't go through.
func TestTopologyLayout(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	clean := []string{"peering-partitions.yaml", "peering.yaml", "mesh-federation.yaml", "federation.yaml", "mesh.yaml"}
	examples, _ := filepath.Glob(filepath.Join("..", "..", "examples", "*.yaml"))
	for _, ex := range examples {
		ex = filepath.Base(ex)
		t.Run(ex, func(t *testing.T) {
			pos, crossings, overlaps, _ := layoutOf(t, node, ex)
			if len(pos) == 0 {
				t.Skip("no nodes")
			}
			if len(overlaps) > 0 {
				t.Errorf("cards overlap: %q", overlaps)
			}
			if slices.Contains(clean, ex) && len(crossings) > 0 {
				t.Errorf("lines run under cards: %q", crossings)
			}
		})
	}

	// api, outside Consul, sits past dc2: dc1's terminating gateway reaches
	// it in a corridor below dc2, not through dc2's box.
	for _, ex := range []string{"terminating-failover.yaml", "terminating-failover-partitions.yaml"} {
		_, crossings, _, around := layoutOf(t, node, ex)
		if len(around) != 1 || !strings.HasSuffix(around[0], "terminating > api around dc2") {
			t.Errorf("%s: lines round other datacenters: %q", ex, around)
		}
		for _, c := range crossings {
			if strings.Contains(c, "detour") {
				t.Errorf("%s: %s", ex, c)
			}
		}
	}

	// Peering between partitions: each dc's gateways face the other dc,
	// the default partition's (control plane) on the servers' row.
	pos, _, _, _ := layoutOf(t, node, "peering-partitions.yaml")
	if !(pos["dc1-mesh"].X > pos["dc1-server-1"].X && pos["dc2-mesh"].X < pos["dc2-server-1"].X) {
		t.Errorf("default-partition gateways don't face each other: %v", pos)
	}
	if pos["dc1-mesh"].Y != pos["dc1-server-1"].Y || pos["dc2-mesh"].Y != pos["dc2-server-1"].Y {
		t.Errorf("default-partition gateways aren't beside the servers: %v", pos)
	}
	if pos["dc1-team-a-mesh"].Y != pos["dc2-team-b-mesh"].Y || pos["dc1-team-a-mesh"].Y <= pos["dc1-mesh"].Y {
		t.Errorf("partition gateways aren't on a row of their own: %v", pos)
	}
}
