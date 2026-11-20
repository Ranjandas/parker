// Package graph works out a lab's architecture from its expanded topology,
// for the web UI's topology view: which datacenter and partition each node
// sits in, what it runs, which services call which (through which mesh or
// terminating gateways), where ingress and API gateways send traffic, and
// how datacenters are federated or peered.
package graph

import (
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/spec"
)

// Node is one network namespace.
type Node struct {
	Name      string
	DC        string
	Partition string // "" outside partitions:
	// Role: server, client, gateway, dataplane or none.
	Role string
	// Services registered for the node (not sidecars).
	Services []string
	// Gateways it runs: mesh, ingress, terminating, api.
	Gateways []string
	IPs      []string
}

// Edge kinds.
const (
	Upstream = "upstream" // service -> service
	Route    = "route"    // ingress or API gateway -> service
	WAN      = "wan"      // WAN federation
	Peering  = "peering"  // cluster peering, dialer -> acceptor
)

// Edge connects two nodes.
type Edge struct {
	Kind  string
	From  string
	To    string
	Label string
	// Via are the mesh gateways an upstream's traffic crosses, in order.
	Via []string
}

// Graph of one lab.
type Graph struct {
	Nodes []Node
	Edges []Edge
	// Federation: "", "wan" (servers join over the wan) or
	// "mesh-gateways".
	Federation string
}

type upstream struct {
	Name      string `json:"destination_name"`
	Port      int    `json:"local_bind_port"`
	DC        string `json:"datacenter"`
	Peer      string `json:"destination_peer"`
	Partition string `json:"destination_partition"`
}

type proxy struct {
	Destination string     `json:"destination_service_name"`
	Upstreams   []upstream `json:"upstreams"`
}

type service struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Proxy   *proxy `json:"proxy"`
	Connect *struct {
		Sidecar *struct {
			Proxy *proxy `json:"proxy"`
		} `json:"sidecar_service"`
	} `json:"connect"`
}

type agentConfig struct {
	Services        []service `json:"services"`
	RetryJoinWAN    []string  `json:"retry_join_wan"`
	PrimaryGateways []string  `json:"primary_gateways"`
	MeshGWFed       bool      `json:"enable_mesh_gateway_wan_federation"`
}

type dataplaneEntry struct {
	Node    string  `json:"node"`
	Service service `json:"service"`
}

// entry is the part of a config entry the graph reads.
type entry struct {
	Kind      string
	Name      string
	Listeners []struct {
		Port     int
		Services []struct{ Name string }
	}
	Services []struct{ Name string } // terminating-gateway, tcp-route
	Parents  []struct{ Kind, Name string }
	Rules    []struct {
		Services []struct{ Name string }
	}
}

type dcEntry struct {
	dc string
	e  entry
}

type caller struct {
	node string
	ups  []upstream
}

// Build reads the graph out of an expanded lab.
func Build(f *spec.File) *Graph {
	g := &Graph{}
	byName := map[string]*Node{}
	var callers []caller
	peerings := map[string][2]string{} // name -> dialer dc, acceptor dc
	gateways := map[string]string{}    // <dc>/<service> -> gateway node
	var entries []dcEntry

	for _, name := range slices.Sorted(maps.Keys(f.Nodes)) {
		rn := f.Nodes[name]
		n := Node{Name: name, Role: "none"}
		if l := rn.Labels; l != nil {
			n.DC, n.Partition = l.DC, l.Partition
			if l.Role != "" {
				n.Role = l.Role
			}
			n.Services = append(n.Services, l.Services...)
		}
		for _, net := range slices.Sorted(maps.Keys(rn.Networks)) {
			if a := rn.Networks[net]; a != nil && a.IP != "" {
				n.IPs = append(n.IPs, a.IP)
			}
		}
		for _, p := range slices.Sorted(maps.Keys(rn.Processes)) {
			if gw, ok := strings.CutSuffix(p, "-gateway"); ok {
				n.Gateways = append(n.Gateways, gw)
				if pr := rn.Processes[p]; pr != nil {
					if i := slices.Index(pr.Run, "-service"); i >= 0 && i+1 < len(pr.Run) {
						gateways[n.DC+"/"+pr.Run[i+1]] = name
					}
				}
			}
			if pn, ok := strings.CutPrefix(p, "peering-"); ok {
				if name, ok := strings.CutSuffix(pn, "-token"); ok {
					pe := peerings[name]
					pe[1] = n.DC
					peerings[name] = pe
				} else {
					pe := peerings[pn]
					pe[0] = n.DC
					peerings[pn] = pe
				}
			}
		}
		if raw, ok := rn.Files["consul.json"]; ok {
			var cfg agentConfig
			if json.Unmarshal([]byte(raw), &cfg) == nil {
				if n.Role == "server" {
					switch {
					case cfg.MeshGWFed || len(cfg.PrimaryGateways) > 0:
						g.Federation = "mesh-gateways"
					case len(cfg.RetryJoinWAN) > 0 && g.Federation == "":
						g.Federation = "wan"
					}
				}
				for _, s := range cfg.Services {
					if s.Connect != nil && s.Connect.Sidecar != nil && s.Connect.Sidecar.Proxy != nil && len(s.Connect.Sidecar.Proxy.Upstreams) > 0 {
						callers = append(callers, caller{node: name, ups: s.Connect.Sidecar.Proxy.Upstreams})
					}
				}
			}
		}
		for _, fn := range slices.Sorted(maps.Keys(rn.Files)) {
			if (strings.HasPrefix(fn, "config-entry-") || strings.HasPrefix(fn, "gateway-entry-")) && strings.HasSuffix(fn, ".json") {
				var e entry
				if json.Unmarshal([]byte(rn.Files[fn]), &e) == nil {
					entries = append(entries, dcEntry{n.DC, e})
				}
			}
		}
		if raw, ok := rn.Files[expand.DataplaneManifest]; ok {
			var es []dataplaneEntry
			if json.Unmarshal([]byte(raw), &es) == nil {
				for _, e := range es {
					if e.Service.Kind == "connect-proxy" && e.Service.Proxy != nil && len(e.Service.Proxy.Upstreams) > 0 {
						// Dataplane nodes are keyed <dc>-<name>; the manifest
						// names them by their Consul node name.
						callers = append(callers, caller{node: e.Node, ups: e.Service.Proxy.Upstreams})
					}
				}
			}
		}
		g.Nodes = append(g.Nodes, n)
	}
	for i := range g.Nodes {
		byName[g.Nodes[i].Name] = &g.Nodes[i]
	}
	// A dataplane caller is named by its Consul node name: map it back.
	resolve := func(name string) *Node {
		if n, ok := byName[name]; ok {
			return n
		}
		for i := range g.Nodes {
			if n := &g.Nodes[i]; n.Role == expand.DataplaneRole && strings.HasSuffix(n.Name, "-"+name) {
				return n
			}
		}
		return nil
	}

	part := func(p string) string {
		if p == "" {
			return "default"
		}
		return p
	}
	meshGW := func(dc, partition string) string {
		for _, n := range g.Nodes {
			if n.DC == dc && part(n.Partition) == part(partition) && slices.Contains(n.Gateways, "mesh") {
				return n.Name
			}
		}
		return ""
	}
	// running lists the nodes in a dc and partition with a service.
	running := func(dc, partition, svc string) []string {
		var out []string
		for _, n := range g.Nodes {
			if n.DC == dc && part(n.Partition) == part(partition) && slices.Contains(n.Services, svc) {
				out = append(out, n.Name)
			}
		}
		return out
	}
	terminated := map[string]string{} // <dc>/<service> -> terminating gateway node
	route := func(dc, gwService, svc, label string) {
		gw, ok := gateways[dc+"/"+gwService]
		if !ok {
			return
		}
		for _, to := range running(dc, byName[gw].Partition, svc) {
			g.Edges = append(g.Edges, Edge{Kind: Route, From: gw, To: to, Label: label})
		}
	}
	for _, de := range entries {
		e := de.e
		switch e.Kind {
		case "ingress-gateway":
			for _, l := range e.Listeners {
				for _, s := range l.Services {
					route(de.dc, e.Name, s.Name, s.Name+" :"+itoa(l.Port))
				}
			}
		case "terminating-gateway":
			if gw, ok := gateways[de.dc+"/"+e.Name]; ok {
				for _, s := range e.Services {
					terminated[de.dc+"/"+s.Name] = gw
				}
			}
		case "http-route", "tcp-route", "grpc-route":
			svcs := e.Services
			for _, r := range e.Rules {
				svcs = append(svcs, r.Services...)
			}
			for _, p := range e.Parents {
				if p.Kind == "api-gateway" || p.Kind == "" {
					for _, s := range svcs {
						route(de.dc, p.Name, s.Name, s.Name)
					}
				}
			}
		}
	}

	for _, c := range callers {
		from := resolve(c.node)
		if from == nil {
			continue
		}
		for _, u := range c.ups {
			dc, partition := from.DC, from.Partition
			if u.DC != "" {
				dc = u.DC
			}
			if u.Partition != "" {
				partition = u.Partition
			}
			if u.Peer != "" {
				pe := peerings[u.Peer]
				switch from.DC {
				case pe[0]:
					dc = pe[1]
				case pe[1]:
					dc = pe[0]
				}
			}
			label := u.Name
			if u.Port > 0 {
				label += " :" + itoa(u.Port)
			}
			for _, to := range g.Nodes {
				if to.DC != dc || part(to.Partition) != part(partition) || !slices.Contains(to.Services, u.Name) || to.Name == from.Name {
					continue
				}
				e := Edge{Kind: Upstream, From: from.Name, To: to.Name, Label: label}
				if to.DC != from.DC || part(to.Partition) != part(from.Partition) {
					a, b := meshGW(from.DC, from.Partition), meshGW(to.DC, to.Partition)
					if a != "" && b != "" {
						e.Via = []string{a, b}
					}
				}
				if gw, ok := terminated[to.DC+"/"+u.Name]; ok && gw != from.Name {
					e.Via = append(e.Via, gw)
				}
				g.Edges = append(g.Edges, e)
			}
		}
	}

	dcs := map[string]bool{}
	for _, n := range g.Nodes {
		if n.DC != "" {
			dcs[n.DC] = true
		}
	}
	order := slices.Sorted(maps.Keys(dcs))
	firstServer := func(dc string) string {
		for _, n := range g.Nodes {
			if n.DC == dc && n.Role == "server" {
				return n.Name
			}
		}
		return ""
	}
	if g.Federation != "" {
		rep := firstServer
		if g.Federation == "mesh-gateways" {
			rep = func(dc string) string {
				if gw := meshGW(dc, ""); gw != "" {
					return gw
				}
				return firstServer(dc)
			}
			for _, dc := range order {
				if s, gw := firstServer(dc), meshGW(dc, ""); s != "" && gw != "" {
					g.Edges = append(g.Edges, Edge{Kind: WAN, From: s, To: gw})
				}
			}
		}
		for i := 0; i+1 < len(order); i++ {
			if a, b := rep(order[i]), rep(order[i+1]); a != "" && b != "" {
				g.Edges = append(g.Edges, Edge{Kind: WAN, From: a, To: b, Label: "wan"})
			}
		}
	}
	names := slices.Sorted(maps.Keys(peerings))
	for _, name := range names {
		pe := peerings[name]
		if a, b := firstServer(pe[0]), firstServer(pe[1]); a != "" && b != "" {
			g.Edges = append(g.Edges, Edge{Kind: Peering, From: a, To: b, Label: name})
		}
	}
	sort.SliceStable(g.Edges, func(i, j int) bool { return g.Edges[i].Kind < g.Edges[j].Kind })
	return g
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
