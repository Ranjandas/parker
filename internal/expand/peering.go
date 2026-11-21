package expand

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Ranjandas/parker/internal/compat"
	"github.com/Ranjandas/parker/internal/spec"
)

// Cluster peering: the acceptor's server-1 generates a token into the run
// dir, the dialer's server-1 establishes the peering with it. Both run as
// one-shot processes the lab waits for.

const (
	PeeringTokenReady = "parker: peering token ready"
	peeringMinConsul  = "1.14"
	// stayUp keeps a one-shot setup process alive once it is done, like
	// acl-setup: the supervisor counts an exit as a failure.
	stayUp = "exec sleep 2147483647"
)

// PeeringActiveLine is what the dialer's process logs once the peering is
// active.
func PeeringActiveLine(name string) string { return "parker: peering " + name + " active" }

// side is one end of a peering: a datacenter and the admin partition in
// it ("" for the default partition).
type side struct{ dc, part string }

func (s side) String() string {
	if s.part == "" {
		return s.dc
	}
	return s.dc + "/" + s.part
}

type peering struct {
	name             string
	acceptor, dialer side
	via              bool
}

func (p peering) tokenProcess() string     { return "peering-" + p.name + "-token" }
func (p peering) establishProcess() string { return "peering-" + p.name }
func (p peering) tokenFile() string        { return VarRunDir + "/peering-" + p.name + ".token" }

// parseSide reads "dc" or "dc/partition" and checks both exist.
func parseSide(c *spec.Consul, v string) (side, error) {
	dc, part, _ := strings.Cut(v, "/")
	d := c.Datacenters[dc]
	if d == nil {
		return side{}, fmt.Errorf("no datacenter %q", dc)
	}
	if part == "" && strings.Contains(v, "/") {
		return side{}, fmt.Errorf("%q: name the partition after the slash, or drop it for the default partition", v)
	}
	part = partitionName(part)
	if part != "" && d.Partitions[part] == nil {
		return side{}, fmt.Errorf("no partition %q in %s (add it under consul.datacenters.%s.partitions)", part, dc, dc)
	}
	return side{dc, part}, nil
}

// peerings checks consul.peering and returns the peerings (by name) and
// the config entries they need per datacenter: the mesh entry for
// PeerThroughMeshGateways and an exported-services entry per exporting
// partition.
func peerings(c *spec.Consul, consulVersion string) ([]peering, map[string][]gatewayEntry, error) {
	if len(c.Peering) == 0 {
		return nil, nil, nil
	}
	var errs []error
	if yes, ok := compat.AtLeast(consulVersion, peeringMinConsul); ok && !yes {
		errs = append(errs, fmt.Errorf("consul.peering: needs Consul %s or later (versions.consul is %s)", peeringMinConsul, consulVersion))
	}
	if c.FederationMode() != "" {
		errs = append(errs, errors.New("consul.peering: peered datacenters can't also be federated; drop consul.federation or the peering"))
	}
	var out []peering
	type svcKey struct{ ns, name string }
	exports := map[side]map[svcKey][]string{} // side -> service -> peers
	through := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(c.Peering)) {
		p := c.Peering[name]
		where := "consul.peering." + name
		if p == nil {
			errs = append(errs, fmt.Errorf("%s: needs acceptor and dialer", where))
			continue
		}
		var sides [2]side
		bad := false
		for i, f := range [][2]string{{"acceptor", p.Acceptor}, {"dialer", p.Dialer}} {
			if f[1] == "" {
				errs = append(errs, fmt.Errorf("%s.%s: required", where, f[0]))
				bad = true
				continue
			}
			s, err := parseSide(c, f[1])
			if err != nil {
				errs = append(errs, fmt.Errorf("%s.%s: %w", where, f[0], err))
				bad = true
			}
			sides[i] = s
		}
		if bad {
			continue
		}
		acc, dial := sides[0], sides[1]
		if acc == dial {
			errs = append(errs, fmt.Errorf("%s: acceptor and dialer are both %s", where, acc))
			continue
		}
		if acc.dc == dial.dc {
			other := dial.part
			if other == "" {
				other = spec.DefaultPartition
			}
			errs = append(errs, fmt.Errorf("%s: Consul can't peer two partitions of the same datacenter (%s, %s); export the services to the other partition instead, with an exported-services entry whose Consumers are [{ Partition: %s }]", where, acc, dial, other))
			continue
		}
		for _, s := range sides {
			if !hasMeshGateway(c, s.dc, s.part) {
				at := "consul.datacenters." + s.dc
				if s.part != "" {
					at += ".partitions." + s.part
				}
				errs = append(errs, fmt.Errorf("%s: %s needs a mesh gateway for peered traffic; add gateways: { mesh: true } to %s", where, s, at))
			}
			if p.ViaMeshGateways {
				// Control plane traffic goes through the default
				// partition's gateway, where the servers are.
				if s.part != "" && !hasMeshGateway(c, s.dc, "") {
					errs = append(errs, fmt.Errorf("%s: via_mesh_gateways sends the control plane through %s's default partition, which needs a mesh gateway too; add gateways: { mesh: true } to consul.datacenters.%s", where, s.dc, s.dc))
				}
				through[s.dc] = true
			}
		}
		for _, k := range slices.Sorted(maps.Keys(p.Exports)) {
			s, err := parseSide(c, k)
			if err != nil || (s != acc && s != dial) {
				errs = append(errs, fmt.Errorf("%s.exports.%s: not a side of this peering (%s, %s)", where, k, acc, dial))
				continue
			}
			if exports[s] == nil {
				exports[s] = map[svcKey][]string{}
			}
			for _, v := range p.Exports[k] {
				ns, svc, ok := strings.Cut(v, "/")
				if !ok {
					svc, ns = v, ""
					nss := serviceNamespaces(c, s, svc)
					switch {
					case len(nss) > 1:
						errs = append(errs, fmt.Errorf("%s.exports.%s: %s runs in namespaces %s; write it as <namespace>/%s", where, k, svc, strings.Join(nss, ", "), svc))
						continue
					case len(nss) == 1:
						ns = nss[0]
					}
				}
				if ns == spec.DefaultNamespace {
					ns = ""
				}
				key := svcKey{ns, svc}
				exports[s][key] = append(exports[s][key], name)
			}
		}
		out = append(out, peering{name: name, acceptor: acc, dialer: dial, via: p.ViaMeshGateways})
	}
	errs = append(errs, peerUpstreams(c, out)...)
	entries := map[string][]gatewayEntry{}
	for _, dc := range slices.Sorted(maps.Keys(through)) {
		// PeerThroughMeshGateways is only allowed on the default
		// partition's mesh entry.
		entries[dc] = append(entries[dc], gatewayEntry{label: "consul.peering (mesh entry for via_mesh_gateways)",
			entry: spec.ConfigEntry{"Kind": "mesh", "Peering": map[string]any{"PeerThroughMeshGateways": true}}})
	}
	sideKeys := slices.SortedFunc(maps.Keys(exports), func(a, b side) int { return strings.Compare(a.String(), b.String()) })
	for _, s := range sideKeys {
		var svcs []any
		keys := slices.SortedFunc(maps.Keys(exports[s]), func(a, b svcKey) int {
			return strings.Compare(a.name+"\x00"+a.ns, b.name+"\x00"+b.ns)
		})
		for _, k := range keys {
			var consumers []any
			for _, peer := range exports[s][k] {
				consumers = append(consumers, map[string]any{"Peer": peer})
			}
			svc := map[string]any{"Name": k.name, "Consumers": consumers}
			if k.ns != "" {
				svc["Namespace"] = k.ns
			}
			svcs = append(svcs, svc)
		}
		name := s.part
		if name == "" {
			name = spec.DefaultPartition
		}
		entries[s.dc] = append(entries[s.dc], gatewayEntry{label: "consul.peering exports (exported-services entry)",
			entry: spec.ConfigEntry{"Kind": "exported-services", "Name": name, "Services": svcs}, partition: s.part, merge: true})
	}
	return out, entries, errors.Join(errs...)
}

// peerUpstreams checks that every upstream with peer: names a peering
// whose side is the caller's partition: peers are local to a partition.
func peerUpstreams(c *spec.Consul, ps []peering) []error {
	byName := map[string]peering{}
	for _, p := range ps {
		byName[p.name] = p
	}
	var errs []error
	for _, dcName := range slices.Sorted(maps.Keys(c.Datacenters)) {
		dc := c.Datacenters[dcName]
		if dc == nil {
			continue
		}
		for _, n := range dc.AllNodes() {
			if n.Node == nil {
				continue
			}
			here := side{dcName, partitionName(n.Partition)}
			svcs := maps.Clone(n.Node.Services)
			if n.Node.Service != nil {
				if svcs == nil {
					svcs = map[string]*spec.Service{}
				}
				svcs[n.Name] = n.Node.Service
			}
			for _, sn := range slices.Sorted(maps.Keys(svcs)) {
				if svcs[sn] == nil {
					continue
				}
				for _, un := range slices.Sorted(maps.Keys(svcs[sn].Upstreams)) {
					u := svcs[sn].Upstreams[un]
					if u == nil || u.Peer == "" {
						continue
					}
					where := fmt.Sprintf("%s: service %s, upstream %s", here, sn, un)
					p, ok := byName[u.Peer]
					if _, declared := c.Peering[u.Peer]; !declared {
						errs = append(errs, fmt.Errorf("%s: peer %s is not under consul.peering", where, u.Peer))
						continue
					}
					if ok && p.acceptor != here && p.dialer != here {
						errs = append(errs, fmt.Errorf("%s: peering %s connects %s and %s, not %s; peers belong to a partition, so call it from one of those", where, u.Peer, p.acceptor, p.dialer, here))
					}
				}
			}
		}
	}
	return errs
}

// serviceNamespaces lists the namespaces a service runs in on a side.
func serviceNamespaces(c *spec.Consul, s side, svc string) []string {
	dc := c.Datacenters[s.dc]
	seen := map[string]bool{}
	for _, n := range dc.AllNodes() {
		if partitionName(n.Partition) != s.part || n.Node == nil {
			continue
		}
		svcs := maps.Clone(n.Node.Services)
		if n.Node.Service != nil {
			if svcs == nil {
				svcs = map[string]*spec.Service{}
			}
			svcs[n.Name] = n.Node.Service
		}
		if def, ok := svcs[svc]; ok {
			ns := spec.ServiceNamespace(c, dc, n.Part, def)
			if ns == "" {
				ns = spec.DefaultNamespace
			}
			seen[ns] = true
		}
	}
	for _, x := range c.ExternalsIn(dc) {
		if x.Name == svc && x.Partition == s.part {
			ns := x.Namespace
			if ns == "" {
				ns = spec.DefaultNamespace
			}
			seen[ns] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// hasMeshGateway reports whether a datacenter's partition ("" for the
// default) has a mesh gateway. In the default partition it must be on the
// wan network.
func hasMeshGateway(c *spec.Consul, dc, part string) bool {
	for _, pg := range c.Datacenters[dc].AllGateways() {
		if partitionName(pg.Partition) != part {
			continue
		}
		g := pg.Gateway
		if g == nil {
			g = &spec.Gateway{}
		}
		kind := g.Kind
		if kind == "" {
			kind = pg.Name
		}
		if kind == "mesh" && g.OnWAN(c) {
			return true
		}
	}
	return false
}

// peered reports, for a datacenter, whether it takes part in a peering and
// whether its servers must be on the wan (a peering not through gateways).
func peered(ps []peering, dc string) (in, serversOnWAN bool) {
	for _, p := range ps {
		if p.acceptor.dc == dc || p.dialer.dc == dc {
			in = true
			if !p.via {
				serversOnWAN = true
			}
		}
	}
	return in, serversOnWAN
}

// tokenProc generates the peering token on the acceptor's server-1.
// external are the acceptor servers' wan gRPC addresses, for a peering not
// through gateways.
func (p peering) tokenProc(deps map[string]*spec.Dep, env map[string]string, external []string) *spec.Process {
	gen := "consul peering generate-token -name " + p.name
	if len(external) > 0 {
		gen += " -server-external-addresses " + strings.Join(external, ",")
	}
	f := p.tokenFile()
	script := fmt.Sprintf(`if ! { [ -s %[1]s ] && consul peering read -name %[2]s >/dev/null 2>&1; }; then
  %[4]s > %[1]s.tmp && mv %[1]s.tmp %[1]s || exit 1
fi
echo %[3]q
%[5]s`, f, p.name, PeeringTokenReady, gen, stayUp)
	return &spec.Process{
		Run:       spec.Command{"sh", "-c", script},
		Uses:      []string{"consul"},
		Env:       env,
		DependsOn: deps,
		ReadyWhen: &spec.Ready{Log: PeeringTokenReady, Timeout: "90s"},
		Restart:   "on-failure",
	}
}

// establishProc establishes the peering on the dialer's server-1, retrying
// while the acceptor side isn't reachable yet, and waits until it is active.
func (p peering) establishProc(deps map[string]*spec.Dep, env map[string]string) *spec.Process {
	active := PeeringActiveLine(p.name)
	script := fmt.Sprintf(`if ! consul peering read -name %[1]s 2>/dev/null | grep -q ACTIVE; then
  # The acceptor's gateway can lag its servers: until it routes to them the
  # exchange fails (tls: unrecognized name), so retry until ready_when's
  # timeout.
  until consul peering establish -name %[1]s -peering-token "$(cat %[2]s)"; do sleep 2; done
fi
until consul peering read -name %[1]s | grep -q ACTIVE; do sleep 1; done
echo %[3]q
%[4]s`, p.name, p.tokenFile(), active, stayUp)
	return &spec.Process{
		Run:       spec.Command{"sh", "-c", script},
		Uses:      []string{"consul"},
		Env:       env,
		DependsOn: deps,
		ReadyWhen: &spec.Ready{Log: active, Timeout: "120s"},
		Restart:   "on-failure",
	}
}

// dialsViaGateways reports whether a datacenter (any of its partitions)
// dials a peering through mesh gateways.
func dialsViaGateways(ps []peering, dc string) bool {
	for _, p := range ps {
		if p.via && p.dialer.dc == dc {
			return true
		}
	}
	return false
}
