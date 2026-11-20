package lab

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/Ranjandas/parker/internal/spec"
)

// healthRank orders check statuses from fine to worst.
var healthRank = map[string]int{api.HealthPassing: 0, api.HealthWarning: 1, api.HealthCritical: 2, api.HealthMaint: 2}

func worse(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	ra, ok := healthRank[a]
	if !ok {
		ra = 1
	}
	rb, ok := healthRank[b]
	if !ok {
		rb = 1
	}
	if rb > ra {
		return b
	}
	return a
}

// healthFn lists the checks of one partition of a datacenter (all
// namespaces on Enterprise).
type healthFn func(dc, partition string) (api.HealthChecks, error)

// ServiceHealth asks each datacenter's servers for its checks and returns,
// by node key, each service instance's status: the worst of its own
// checks and its node's (serfHealth and the like). A datacenter whose
// servers don't answer is left out.
func ServiceHealth(low *spec.File) map[string]map[string]string {
	ent := strings.Contains(low.Versions["consul"], "+ent")
	return serviceHealth(low, func(dc, part string) (api.HealthChecks, error) {
		clients, err := ServerClients(low, dc)
		if err != nil {
			return nil, err
		}
		var last error
		for _, n := range slices.Sorted(maps.Keys(clients)) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			q := (&api.QueryOptions{AllowStale: true}).WithContext(ctx)
			if ent {
				q.Partition, q.Namespace = part, "*"
			}
			checks, _, err := clients[n].Health().State(api.HealthAny, q)
			cancel()
			if err == nil {
				return checks, nil
			}
			last = err
		}
		return nil, last
	})
}

func serviceHealth(low *spec.File, list healthFn) map[string]map[string]string {
	type where struct{ dc, part, name string }
	keys := map[where]string{}
	parts := map[string]map[string]bool{} // dc -> partitions
	for key, n := range low.Nodes {
		l := n.Labels
		if l == nil || l.DC == "" {
			continue
		}
		p := l.Partition
		if p == "" {
			p = spec.DefaultPartition
		}
		keys[where{l.DC, p, l.Name}] = key
		if parts[l.DC] == nil {
			parts[l.DC] = map[string]bool{}
		}
		parts[l.DC][p] = true
	}
	out := map[string]map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for dc, ps := range parts {
		for p := range ps {
			wg.Add(1)
			go func() {
				defer wg.Done()
				checks, err := list(dc, p)
				if err != nil {
					return
				}
				nodeWorst := map[string]string{}
				svc := map[string]map[string]string{}
				for _, c := range checks {
					if c.ServiceName == "" {
						nodeWorst[c.Node] = worse(nodeWorst[c.Node], c.Status)
						continue
					}
					if svc[c.Node] == nil {
						svc[c.Node] = map[string]string{}
					}
					svc[c.Node][c.ServiceName] = worse(svc[c.Node][c.ServiceName], c.Status)
				}
				mu.Lock()
				defer mu.Unlock()
				for node, byName := range svc {
					key, ok := keys[where{dc, p, node}]
					if !ok {
						continue
					}
					if out[key] == nil {
						out[key] = map[string]string{}
					}
					for name, st := range byName {
						out[key][name] = worse(st, nodeWorst[node])
					}
				}
			}()
		}
	}
	wg.Wait()
	return out
}
