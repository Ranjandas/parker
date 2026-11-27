// Package graph works out a lab's architecture from its expanded topology,
// for the web UI's topology view: which datacenter and partition each node
// sits in, what it runs, which services call which (through which mesh or
// terminating gateways), where ingress and API gateways send traffic, and
// how datacenters are federated or peered.
package graph

import (
	"encoding/json"
	"maps"
	"net/url"
	"path"
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
	// Product the node runs: consul, vault; "" for a low-level node.
	Product string
	// Role: server, client, gateway, dataplane or none.
	Role string
	// Services registered for the node (not sidecars).
	Services []string
	// Gateways it runs: mesh, ingress, terminating, api.
	Gateways []string
	IPs      []string
	// Links are the web UIs the node serves, for the UI's Open links.
	Links []Link
	// Namespaces: the Consul namespace of each service, and of each
	// gateway (keyed "<kind>-gateway"). Set only when the node's partition
	// has namespaces other than default, so CE labs carry none.
	Namespaces map[string]string
}

// Link is a web UI on a node.
type Link struct {
	Name   string // "Consul UI", or the service's name
	Port   int
	Scheme string // http or https
	Path   string
	// Token signs in to it (the Consul UI's initial management token).
	Token string
}

// The node that stands for clients outside the lab (browsers, curl),
// calling the ingress and API gateways. It isn't a network namespace.
const (
	OutsideNode = "internet"
	OutsideRole = "outside"
)

// Edge kinds.
const (
	Upstream = "upstream" // service -> service
	Route    = "route"    // ingress or API gateway -> service
	WAN      = "wan"      // WAN federation
	Peering  = "peering"  // cluster peering, dialer -> acceptor
	// Failover: where a service-resolver sends a caller's upstream when
	// its own instances are down (another dc, partition or peer).
	Failover = "failover"
	// Integration: one product using another, from the user to the one
	// it uses: a Consul dc's Connect CA in Vault, Vault's storage or
	// service registration in Consul, a Vault cluster's transit seal in
	// another. The label says which.
	Integration = "integration"
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
	Namespace string `json:"destination_namespace"`
}

type proxy struct {
	Destination string     `json:"destination_service_name"`
	Upstreams   []upstream `json:"upstreams"`
}

type service struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Port      int    `json:"port"`
	Kind      string `json:"kind"`
	Proxy     *proxy `json:"proxy"`
	Connect   *struct {
		Sidecar *struct {
			Proxy *proxy `json:"proxy"`
		} `json:"sidecar_service"`
	} `json:"connect"`
}

type agentConfig struct {
	Services   []service `json:"services"`
	ClientAddr string    `json:"client_addr"`
	Ports      struct {
		HTTP  *int `json:"http"`
		HTTPS int  `json:"https"`
	} `json:"ports"`
	UI struct {
		Enabled bool `json:"enabled"`
	} `json:"ui_config"`
	ACL struct {
		Tokens struct {
			InitialManagement string `json:"initial_management"`
		} `json:"tokens"`
	} `json:"acl"`
	RetryJoinWAN    []string `json:"retry_join_wan"`
	PrimaryGateways []string `json:"primary_gateways"`
	MeshGWFed       bool     `json:"enable_mesh_gateway_wan_federation"`
}

type dataplaneEntry struct {
	Node       string  `json:"node"`
	Datacenter string  `json:"datacenter"`
	Partition  string  `json:"partition"`
	Service    service `json:"service"`
}

// failoverTarget is one service-resolver failover target.
type failoverTarget struct {
	Service, Peer, Datacenter, Partition, Namespace string
}

// entry is the part of a config entry the graph reads.
type entry struct {
	Kind      string
	Name      string
	Listeners []struct {
		Name     string
		Port     int
		Protocol string
		Services []struct{ Name string }
	}
	Services []struct{ Name string } // terminating-gateway, tcp-route
	Parents  []struct{ Kind, Name string }
	Rules    []struct {
		Services []struct{ Name string }
	}
	// mesh: the peering control plane goes through mesh gateways.
	Peering struct{ PeerThroughMeshGateways bool }
	// service-resolver
	Failover map[string]struct {
		Service     string
		Datacenters []string
		Targets     []failoverTarget
	}
}

type dcEntry struct {
	dc, partition string
	e             entry
}

// place is a datacenter and partition ("" for the default one).
type place struct{ dc, partition string }

type caller struct {
	node string
	ns   string // the calling service's namespace ("" for default)
	ups  []upstream
}

// Build reads the graph out of an expanded lab.
func Build(f *spec.File) *Graph {
	g := &Graph{}
	byName := map[string]*Node{}
	var callers []caller
	peerings := map[string][2]place{} // name -> dialer, acceptor side
	gateways := map[string]string{}   // <dc>/<partition>/<service> -> gateway node
	var entries []dcEntry
	// externals: where each external service's node is registered (it
	// sits on several dcs' LANs, so it has no DC of its own).
	externals := map[string][]place{}
	var dpNS [][3]string // dataplane node, service, namespace

	for _, name := range slices.Sorted(maps.Keys(f.Nodes)) {
		rn := f.Nodes[name]
		n := Node{Name: name, Role: "none"}
		if l := rn.Labels; l != nil {
			n.DC, n.Partition = l.DC, l.Partition
			if l.Role != "" {
				n.Role, n.Product = l.Role, l.Product
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
						gateways[n.DC+"/"+partName(n.Partition)+"/"+pr.Run[i+1]] = name
					}
					setNS(&n, p, pr.Env["CONSUL_NAMESPACE"])
				}
			}
			if pn, ok := strings.CutPrefix(p, "peering-"); ok {
				// The process runs in its side's partition.
				side := place{n.DC, ""}
				if pr := rn.Processes[p]; pr != nil {
					side.partition = pr.Env["CONSUL_PARTITION"]
				}
				if name, ok := strings.CutSuffix(pn, "-token"); ok {
					pe := peerings[name]
					pe[1] = side
					peerings[name] = pe
				} else {
					pe := peerings[pn]
					pe[0] = side
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
				for _, l := range links(cfg) {
					// A fake service's own page draws its chain and faults.
					if p := rn.Processes[l.Name]; p != nil && p.Env["PARKER_FAKE"] != "" && !strings.Contains(p.Env["PARKER_FAKE"], `"protocol":"tcp"`) {
						l.Name, l.Path = l.Name+" (fake service)", "/ui"
					}
					n.Links = append(n.Links, l)
				}
				for _, s := range cfg.Services {
					if s.Kind == "" {
						setNS(&n, s.Name, s.Namespace)
					}
					if s.Connect != nil && s.Connect.Sidecar != nil && s.Connect.Sidecar.Proxy != nil && len(s.Connect.Sidecar.Proxy.Upstreams) > 0 {
						callers = append(callers, caller{node: name, ns: s.Namespace, ups: s.Connect.Sidecar.Proxy.Upstreams})
					}
				}
			}
		}
		if l, ok := vaultLink(f, rn); ok {
			n.Links = append(n.Links, l)
		}
		// The manifest says which partition each entry is written into.
		entryPart := map[string]string{}
		var refs []expand.EntryRef
		if json.Unmarshal([]byte(rn.Files[expand.ConfigEntriesManifest]), &refs) == nil {
			for _, r := range refs {
				entryPart[path.Base(r.File)] = r.Partition
			}
		}
		for _, fn := range slices.Sorted(maps.Keys(rn.Files)) {
			if (strings.HasPrefix(fn, "config-entry-") || strings.HasPrefix(fn, "gateway-entry-")) && strings.HasSuffix(fn, ".json") {
				var e entry
				if json.Unmarshal([]byte(rn.Files[fn]), &e) == nil {
					entries = append(entries, dcEntry{n.DC, entryPart[fn], e})
				}
			}
		}
		if raw, ok := rn.Files[expand.DataplaneManifest]; ok {
			var es []dataplaneEntry
			if json.Unmarshal([]byte(raw), &es) == nil {
				for _, e := range es {
					if e.Service.Kind == "" {
						dpNS = append(dpNS, [3]string{e.Node, e.Service.Name, e.Service.Namespace})
					}
					if rn := f.Nodes[e.Node]; rn != nil && rn.Labels != nil && rn.Labels.Role == expand.ExternalRole {
						externals[e.Node] = append(externals[e.Node], place{e.Datacenter, e.Partition})
					}
					if e.Service.Kind == "connect-proxy" && e.Service.Proxy != nil && len(e.Service.Proxy.Upstreams) > 0 {
						// Dataplane nodes are keyed <dc>-<name>; the manifest
						// names them by their Consul node name.
						callers = append(callers, caller{node: e.Node, ns: e.Service.Namespace, ups: e.Service.Proxy.Upstreams})
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
	for _, x := range dpNS {
		if n := resolve(x[0]); n != nil {
			setNS(n, x[1], x[2])
		}
	}

	// at: the node sits in (or, for an external service, is registered
	// in) dc's partition.
	at := func(n *Node, p place) bool {
		if n.DC == p.dc && partName(n.Partition) == partName(p.partition) {
			return true
		}
		for _, x := range externals[n.Name] {
			if x.dc == p.dc && partName(x.partition) == partName(p.partition) {
				return true
			}
		}
		return false
	}
	// Namespaces show only where a partition has more than default.
	var spaced []place
	for i := range g.Nodes {
		n := &g.Nodes[i]
		for _, ns := range n.Namespaces {
			if ns != spec.DefaultNamespace {
				spaced = append(spaced, place{n.DC, n.Partition})
				for _, x := range externals[n.Name] {
					spaced = append(spaced, x)
				}
				break
			}
		}
	}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if !slices.ContainsFunc(spaced, func(p place) bool { return at(n, p) }) {
			n.Namespaces = nil
			continue
		}
		for _, s := range n.Services {
			setNS(n, s, spec.DefaultNamespace)
		}
		for _, gw := range n.Gateways {
			setNS(n, gw+"-gateway", spec.DefaultNamespace)
		}
	}
	meshGW := func(p place) string {
		for i := range g.Nodes {
			if n := &g.Nodes[i]; at(n, p) && slices.Contains(n.Gateways, "mesh") {
				return n.Name
			}
		}
		return ""
	}
	// running lists the nodes in a dc and partition with a service.
	running := func(p place, svc string) []string {
		var out []string
		for i := range g.Nodes {
			if n := &g.Nodes[i]; at(n, p) && slices.Contains(n.Services, svc) {
				out = append(out, n.Name)
			}
		}
		return out
	}
	key := func(p place, svc string) string { return p.dc + "/" + partName(p.partition) + "/" + svc }
	terminated := map[string]string{} // <dc>/<partition>/<service> -> terminating gateway node
	route := func(p place, gwService, svc, label string) {
		gw, ok := gateways[key(p, gwService)]
		if !ok {
			return
		}
		for _, to := range running(p, svc) {
			g.Edges = append(g.Edges, Edge{Kind: Route, From: gw, To: to, Label: label})
		}
	}
	// Ingress and API gateways take traffic from outside the mesh: one
	// "internet" node feeds their listeners.
	var outside []Edge
	listen := func(here place, gwService string, e entry) {
		gw, ok := gateways[key(here, gwService)]
		if !ok {
			return
		}
		for _, l := range e.Listeners {
			label := ":" + itoa(l.Port)
			if l.Protocol != "" {
				label += " " + l.Protocol
			}
			outside = append(outside, Edge{Kind: Route, To: gw, Label: label})
		}
	}
	resolvers := map[string]entry{} // <dc>/<partition>/<service> -> service-resolver
	for _, de := range entries {
		e := de.e
		here := place{de.dc, de.partition}
		switch e.Kind {
		case "api-gateway":
			listen(here, e.Name, e)
		case "ingress-gateway":
			listen(here, e.Name, e)
			for _, l := range e.Listeners {
				for _, s := range l.Services {
					route(here, e.Name, s.Name, s.Name+" :"+itoa(l.Port))
				}
			}
		case "terminating-gateway":
			if gw, ok := gateways[key(here, e.Name)]; ok {
				for _, s := range e.Services {
					terminated[key(here, s.Name)] = gw
					// The gateway forwards to the service outside the mesh.
					route(here, e.Name, s.Name, s.Name)
				}
			}
		case "service-resolver":
			if len(e.Failover) > 0 {
				resolvers[key(here, e.Name)] = e
			}
		case "http-route", "tcp-route", "grpc-route":
			svcs := e.Services
			for _, r := range e.Rules {
				svcs = append(svcs, r.Services...)
			}
			for _, p := range e.Parents {
				if p.Kind == "api-gateway" || p.Kind == "" {
					for _, s := range svcs {
						route(here, p.Name, s.Name, s.Name)
					}
				}
			}
		}
	}

	// peerSide is the other side of a peering, seen from p.
	peerSide := func(name string, p place) (place, bool) {
		pe, ok := peerings[name]
		if !ok {
			return place{}, false
		}
		switch {
		case pe[0].dc == p.dc && partName(pe[0].partition) == partName(p.partition):
			return pe[1], true
		case pe[1].dc == p.dc && partName(pe[1].partition) == partName(p.partition):
			return pe[0], true
		case pe[0].dc == p.dc:
			return pe[1], true
		case pe[1].dc == p.dc:
			return pe[0], true
		}
		return place{}, false
	}
	// edges from a caller to svc's instances at p: through the mesh
	// gateways when p is elsewhere, and p's terminating gateway when one
	// links the service.
	edges := func(kind string, from *Node, p place, svc, label string) {
		for _, to := range running(p, svc) {
			if to == from.Name {
				continue
			}
			e := Edge{Kind: kind, From: from.Name, To: to, Label: label}
			if src := (place{from.DC, from.Partition}); src.dc != p.dc || partName(src.partition) != partName(p.partition) {
				if a, b := meshGW(src), meshGW(p); a != "" && b != "" {
					e.Via = []string{a, b}
				}
			}
			if gw, ok := terminated[key(p, svc)]; ok && gw != from.Name {
				e.Via = append(e.Via, gw)
			}
			g.Edges = append(g.Edges, e)
		}
	}

	for _, c := range callers {
		from := resolve(c.node)
		if from == nil {
			continue
		}
		for _, u := range c.ups {
			p := place{from.DC, from.Partition}
			if u.DC != "" {
				p.dc = u.DC
			}
			if u.Partition != "" {
				p.partition = u.Partition
			}
			if u.Peer != "" {
				if o, ok := peerSide(u.Peer, place{from.DC, from.Partition}); ok {
					p = o
				}
			}
			label := u.Name
			if u.Port > 0 {
				label += " :" + itoa(u.Port)
			}
			if u.Namespace != "" && nsName(u.Namespace) != nsName(c.ns) {
				label += " (ns " + u.Namespace + ")"
			}
			edges(Upstream, from, p, u.Name, label)

			// Failover: the resolver in the upstream's own dc/partition.
			r, ok := resolvers[key(p, u.Name)]
			if !ok || u.Peer != "" {
				continue
			}
			for _, subset := range slices.Sorted(maps.Keys(r.Failover)) {
				fo := r.Failover[subset]
				targets := fo.Targets
				for _, dc := range fo.Datacenters {
					targets = append(targets, failoverTarget{Service: fo.Service, Datacenter: dc})
				}
				for _, t := range targets {
					tp, svc := p, u.Name
					if t.Service != "" {
						svc = t.Service
					} else if fo.Service != "" {
						svc = fo.Service
					}
					where := ""
					switch {
					case t.Peer != "":
						o, ok := peerSide(t.Peer, p)
						if !ok {
							continue
						}
						tp, where = o, "peer "+t.Peer
					case t.Datacenter != "" || t.Partition != "":
						if t.Datacenter != "" {
							tp.dc = t.Datacenter
						}
						if t.Partition != "" {
							tp.partition = t.Partition
						}
						where = tp.dc
						if t.Partition != "" {
							where += "/" + t.Partition
						}
					default:
						where = "same dc"
					}
					if t.Namespace != "" && nsName(t.Namespace) != nsName(c.ns) {
						where += ", ns " + t.Namespace
					}
					edges(Failover, from, tp, svc, svc+" failover ("+where+")")
				}
			}
		}
	}

	// A gateway's route to a service that a caller's path already runs
	// through is that path's last hop: one line is enough, drawn as the
	// path that uses it (an upstream, or a failover when only a failover
	// goes that way). A route no path uses stays, as the gateway's own.
	used := map[[2]string]bool{}
	for _, e := range g.Edges {
		if (e.Kind == Upstream || e.Kind == Failover) && len(e.Via) > 0 {
			used[[2]string{e.Via[len(e.Via)-1], e.To}] = true
		}
	}
	g.Edges = slices.DeleteFunc(g.Edges, func(e Edge) bool { return e.Kind == Route && used[[2]string{e.From, e.To}] })

	if len(outside) > 0 {
		name := OutsideNode
		for byName[name] != nil {
			name += "-"
		}
		g.Nodes = append(g.Nodes, Node{Name: name, Role: OutsideRole})
		for _, e := range outside {
			e.From = name
			g.Edges = append(g.Edges, e)
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
				if gw := meshGW(place{dc, ""}); gw != "" {
					return gw
				}
				return firstServer(dc)
			}
			for _, dc := range order {
				if s, gw := firstServer(dc), meshGW(place{dc, ""}); s != "" && gw != "" {
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
	// A dc whose default partition's mesh entry sets PeerThroughMeshGateways
	// sends its peerings' control plane through its default-partition mesh
	// gateway (Consul allows it nowhere else).
	viaGW := map[string]bool{}
	for _, de := range entries {
		if de.e.Kind == "mesh" && de.e.Peering.PeerThroughMeshGateways && partName(de.partition) == spec.DefaultPartition {
			viaGW[de.dc] = true
		}
	}
	names := slices.Sorted(maps.Keys(peerings))
	for _, name := range names {
		pe := peerings[name]
		if a, b := firstServer(pe[0].dc), firstServer(pe[1].dc); a != "" && b != "" {
			e := Edge{Kind: Peering, From: a, To: b, Label: name}
			for _, side := range pe {
				if gw := meshGW(place{side.dc, ""}); viaGW[side.dc] && gw != "" {
					e.Via = append(e.Via, gw)
				}
			}
			g.Edges = append(g.Edges, e)
		}
	}
	g.Edges = append(g.Edges, integrations(f, g, firstServer)...)
	sort.SliceStable(g.Edges, func(i, j int) bool { return g.Edges[i].Kind < g.Edges[j].Kind })
	return g
}

// integrations are the links between products, read from the agents'
// configs: a Consul dc whose servers use a Vault cluster as their Connect
// CA, a Vault cluster that keeps its data in a Consul dc or registers in
// one, and a Vault cluster that auto-unseals with another's Transit
// engine. One edge
// per pair, between their first servers.
func integrations(f *spec.File, g *Graph, firstServer func(string) string) []Edge {
	byIP := map[string]string{}
	for _, n := range g.Nodes {
		for _, ip := range n.IPs {
			byIP[ip] = n.Name
		}
	}
	var out []Edge
	seen := map[string]bool{}
	add := func(from, to, label string) {
		if from == "" || to == "" || seen[from+"|"+to+"|"+label] {
			return
		}
		seen[from+"|"+to+"|"+label] = true
		out = append(out, Edge{Kind: Integration, From: from, To: to, Label: label})
	}
	for _, name := range slices.Sorted(maps.Keys(f.Nodes)) {
		rn := f.Nodes[name]
		l := rn.Labels
		if l == nil {
			continue
		}
		if l.Product == expand.ProductConsul && l.Role == "server" {
			var cfg struct {
				Connect struct {
					Provider string `json:"ca_provider"`
					Config   struct {
						Address string `json:"address"`
					} `json:"ca_config"`
				} `json:"connect"`
			}
			if json.Unmarshal([]byte(rn.Files["consul.json"]), &cfg) == nil && cfg.Connect.Provider == "vault" {
				if u, err := url.Parse(cfg.Connect.Config.Address); err == nil {
					if v := byIP[u.Hostname()]; v != "" {
						add(firstServer(l.DC), firstServer(f.Nodes[v].Labels.DC), "Connect CA")
					}
				}
			}
		}
		if l.Product == expand.ProductVault {
			var vc struct {
				Storage struct {
					Consul *struct{} `json:"consul"`
				} `json:"storage"`
				Registration struct {
					Consul *struct{} `json:"consul"`
				} `json:"service_registration"`
				Seal struct {
					Transit *struct {
						Address string `json:"address"`
					} `json:"transit"`
				} `json:"seal"`
			}
			var cc struct {
				Datacenter string `json:"datacenter"`
			}
			if json.Unmarshal([]byte(rn.Files[expand.VaultConfigFile]), &vc) == nil && vc.Seal.Transit != nil {
				if u, err := url.Parse(vc.Seal.Transit.Address); err == nil {
					if t := byIP[u.Hostname()]; t != "" {
						add(firstServer(l.DC), firstServer(f.Nodes[t].Labels.DC), "auto-unseal")
					}
				}
			}
			if json.Unmarshal([]byte(rn.Files[expand.VaultConfigFile]), &vc) == nil &&
				json.Unmarshal([]byte(rn.Files["consul.json"]), &cc) == nil && cc.Datacenter != "" {
				if vc.Storage.Consul != nil {
					add(firstServer(l.DC), firstServer(cc.Datacenter), "Consul storage")
				}
				if vc.Registration.Consul != nil {
					add(firstServer(l.DC), firstServer(cc.Datacenter), "registered")
				}
			}
		}
	}
	return out
}

// links are the web UIs an agent's config says the node serves: the Consul
// UI (over HTTPS when the agent has an https port) and each service's port.
// Only addresses reachable from outside the node count.
func links(cfg agentConfig) []Link {
	var out []Link
	if cfg.UI.Enabled && cfg.ClientAddr != "" && !strings.HasPrefix(cfg.ClientAddr, "127.") {
		switch {
		case cfg.Ports.HTTPS > 0:
			out = append(out, Link{Name: "Consul UI", Port: cfg.Ports.HTTPS, Scheme: "https", Path: "/ui/", Token: cfg.ACL.Tokens.InitialManagement})
		case cfg.Ports.HTTP == nil || *cfg.Ports.HTTP > 0:
			port := 8500
			if cfg.Ports.HTTP != nil {
				port = *cfg.Ports.HTTP
			}
			out = append(out, Link{Name: "Consul UI", Port: port, Scheme: "http", Path: "/ui/", Token: cfg.ACL.Tokens.InitialManagement})
		}
	}
	for _, s := range cfg.Services {
		if s.Port > 0 && s.Kind == "" {
			out = append(out, Link{Name: s.Name, Port: s.Port, Scheme: "http", Path: "/"})
		}
	}
	return out
}

// vaultLink is the Vault UI a Vault server serves, signed in with the
// cluster's root token.
func vaultLink(f *spec.File, rn *spec.RawNode) (Link, bool) {
	if rn.Labels == nil || rn.Labels.Product != expand.ProductVault {
		return Link{}, false
	}
	l := Link{Name: "Vault UI", Port: expand.VaultPort, Scheme: "http", Path: "/ui/", Token: expand.VaultRootToken(f, rn.Labels.DC)}
	raw, ok := rn.Files[expand.VaultConfigFile]
	if !ok {
		return l, true // dev mode: the UI is on, over HTTP
	}
	var cfg struct {
		UI       bool `json:"ui"`
		Listener struct {
			TCP struct {
				TLSDisable bool `json:"tls_disable"`
			} `json:"tcp"`
		} `json:"listener"`
	}
	if json.Unmarshal([]byte(raw), &cfg) != nil || !cfg.UI {
		return Link{}, false
	}
	if !cfg.Listener.TCP.TLSDisable {
		l.Scheme = "https"
	}
	return l, true
}

// setNS records the namespace of a node's service or gateway, keeping the
// first one seen.
func setNS(n *Node, key, ns string) {
	if ns == "" {
		return
	}
	if n.Namespaces == nil {
		n.Namespaces = map[string]string{}
	}
	if _, ok := n.Namespaces[key]; !ok {
		n.Namespaces[key] = ns
	}
}

func nsName(ns string) string {
	if ns == "" {
		return spec.DefaultNamespace
	}
	return ns
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// partName is a partition's name, "default" for "".
func partName(p string) string {
	if p == "" {
		return "default"
	}
	return p
}
