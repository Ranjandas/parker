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

type peering struct {
	name             string
	acceptor, dialer string
	via              bool
}

func (p peering) tokenProcess() string     { return "peering-" + p.name + "-token" }
func (p peering) establishProcess() string { return "peering-" + p.name }
func (p peering) tokenFile() string        { return VarRunDir + "/peering-" + p.name + ".token" }

// peerings checks consul.peering and returns the peerings (by name) and
// the config entries they need per datacenter: the mesh entry for
// PeerThroughMeshGateways and an exported-services entry for exports.
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
	exports := map[string]map[string][]string{} // dc -> service -> peers
	through := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(c.Peering)) {
		p := c.Peering[name]
		where := "consul.peering." + name
		if p == nil {
			errs = append(errs, fmt.Errorf("%s: needs acceptor and dialer", where))
			continue
		}
		bad := false
		for _, side := range [][2]string{{"acceptor", p.Acceptor}, {"dialer", p.Dialer}} {
			switch {
			case side[1] == "":
				errs = append(errs, fmt.Errorf("%s.%s: required", where, side[0]))
				bad = true
			case strings.Contains(side[1], "/"):
				errs = append(errs, fmt.Errorf("%s.%s: partitions (%s) are not supported yet", where, side[0], side[1]))
				bad = true
			case c.Datacenters[side[1]] == nil:
				errs = append(errs, fmt.Errorf("%s.%s: no datacenter %q", where, side[0], side[1]))
				bad = true
			}
		}
		if bad {
			continue
		}
		if p.Acceptor == p.Dialer {
			errs = append(errs, fmt.Errorf("%s: acceptor and dialer are both %s", where, p.Acceptor))
			continue
		}
		for _, dc := range []string{p.Acceptor, p.Dialer} {
			if !hasWANMeshGateway(c, dc) {
				errs = append(errs, fmt.Errorf("%s: %s needs a mesh gateway for peered traffic; add gateways: { mesh: true } to it", where, dc))
			}
			if p.ViaMeshGateways {
				through[dc] = true
			}
		}
		for _, dc := range slices.Sorted(maps.Keys(p.Exports)) {
			if dc != p.Acceptor && dc != p.Dialer {
				errs = append(errs, fmt.Errorf("%s.exports.%s: not a side of this peering (%s, %s)", where, dc, p.Acceptor, p.Dialer))
				continue
			}
			if exports[dc] == nil {
				exports[dc] = map[string][]string{}
			}
			for _, svc := range p.Exports[dc] {
				exports[dc][svc] = append(exports[dc][svc], name)
			}
		}
		out = append(out, peering{name: name, acceptor: p.Acceptor, dialer: p.Dialer, via: p.ViaMeshGateways})
	}
	entries := map[string][]gatewayEntry{}
	for _, dc := range slices.Sorted(maps.Keys(through)) {
		entries[dc] = append(entries[dc], gatewayEntry{label: "consul.peering (mesh entry for via_mesh_gateways)",
			entry: spec.ConfigEntry{"Kind": "mesh", "Peering": map[string]any{"PeerThroughMeshGateways": true}}})
	}
	for _, dc := range slices.Sorted(maps.Keys(exports)) {
		var svcs []any
		for _, svc := range slices.Sorted(maps.Keys(exports[dc])) {
			var consumers []any
			for _, peer := range exports[dc][svc] {
				consumers = append(consumers, map[string]any{"Peer": peer})
			}
			svcs = append(svcs, map[string]any{"Name": svc, "Consumers": consumers})
		}
		entries[dc] = append(entries[dc], gatewayEntry{label: "consul.peering exports (exported-services entry)",
			entry: spec.ConfigEntry{"Kind": "exported-services", "Name": "default", "Services": svcs}})
	}
	return out, entries, errors.Join(errs...)
}

func hasWANMeshGateway(c *spec.Consul, dc string) bool {
	for group, g := range c.Datacenters[dc].Gateways {
		if g == nil {
			g = &spec.Gateway{}
		}
		kind := g.Kind
		if kind == "" {
			kind = group
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
		if p.acceptor == dc || p.dialer == dc {
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

// establishProc establishes the peering on the dialer's server-1 and waits
// until it is active.
func (p peering) establishProc(deps map[string]*spec.Dep, env map[string]string) *spec.Process {
	active := PeeringActiveLine(p.name)
	script := fmt.Sprintf(`if ! consul peering read -name %[1]s 2>/dev/null | grep -q ACTIVE; then
  consul peering establish -name %[1]s -peering-token "$(cat %[2]s)" || exit 1
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
