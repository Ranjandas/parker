package lab

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/fake"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// FakeServices are the fake services (run: fake-service) on a node.
func FakeServices(low *spec.File, node string) []string {
	n := low.Nodes[node]
	if n == nil {
		return nil
	}
	var out []string
	for name, p := range n.Processes {
		if _, ok := p.Env[expand.FakeEnv]; ok {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// Fake changes or reads the faults of the fake services on node through
// their control sockets.
func (l *ScenarioLab) Fake(ctx context.Context, node string, st *spec.FakeStep) ([]string, error) {
	svcs := FakeServices(l.Low, node)
	if st.Service != "" {
		if !slices.Contains(svcs, st.Service) {
			return nil, fmt.Errorf("no fake service %q here (fake services: %s)", st.Service, list(svcs))
		}
		svcs = []string{st.Service}
	}
	if len(svcs) == 0 {
		return nil, fmt.Errorf("no fake services (run: fake-service) on this node; it runs %s", list(slices.Sorted(maps.Keys(l.Low.Nodes[node].Processes))))
	}
	var change *spec.Fake
	if !st.Empty() && !st.Reset {
		change = st.Change()
	}
	dir := supervisor.NodeDir(RunDir(l.Low.Name), node)
	var lines []string
	for _, svc := range svcs {
		f, err := fake.Control(ctx, filepath.Join(dir, fake.SocketName(svc)), change, st.Reset)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", svc, err)
		}
		lines = append(lines, fmt.Sprintf("%s/%s: %s", node, svc, f.Summary()))
	}
	return lines, nil
}

func list(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}
