package expand

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/Ranjandas/parker/internal/spec"
)

// MeshGatewayPort is where mesh gateways listen, on the LAN and the WAN.
const MeshGatewayPort = 8443

// gatewayNode is one gateway node: a client agent plus the gateway's
// Envoy, registered by `consul connect envoy -register`.
type gatewayNode struct {
	group string // key under gateways:
	name  string // node name (group, or group-N with count)
	kind  string
	count int
	wan   bool
}

func (g gatewayNode) process() string { return g.kind + "-gateway" }

// gatewayNodes resolves each datacenter's gateways (sorted by node name)
// and checks them, and what mesh-gateway federation needs.
func gatewayNodes(c *spec.Consul, dcNames []string, primary string) (map[string][]gatewayNode, error) {
	defaults := c.Defaults
	if defaults == nil {
		defaults = &spec.Inheritable{}
	}
	meshFed := c.FederationMode() == spec.FederationMeshGateways
	out := map[string][]gatewayNode{}
	var errs []error
	for _, dcName := range dcNames {
		dc := c.Datacenters[dcName]
		wanMesh := false
		for _, group := range slices.Sorted(maps.Keys(dc.Gateways)) {
			g := dc.Gateways[group]
			where := fmt.Sprintf("consul.datacenters.%s.gateways.%s", dcName, group)
			if g == nil {
				g = &spec.Gateway{}
			}
			kind := g.Kind
			if kind == "" {
				kind = group
			}
			if kind != "mesh" {
				errs = append(errs, fmt.Errorf("%s: kind: want mesh, got %q (set kind: when the name isn't one)", where, kind))
				continue
			}
			if _, ok := dc.Nodes[group]; ok {
				errs = append(errs, fmt.Errorf("%s: a node is already named %s", where, group))
				continue
			}
			if g.Count < 0 {
				errs = append(errs, fmt.Errorf("%s: count: want 1 or more", where))
				continue
			}
			wan := g.OnWAN(c)
			if wan && kind == "mesh" {
				wanMesh = true
			}
			count := max(g.Count, 1)
			for k := range count {
				name := group
				if g.Count > 1 {
					name = group + "-" + strconv.Itoa(k+1)
				}
				out[dcName] = append(out[dcName], gatewayNode{group: group, name: name, kind: kind, count: g.Count, wan: wan})
			}
		}
		if meshFed {
			if !tlsOn(defaults, dc) {
				errs = append(errs, fmt.Errorf("consul.federation: mesh-gateways needs tls: true (consul.datacenters.%s)", dcName))
			}
			if !wanMesh {
				errs = append(errs, fmt.Errorf("consul.federation: mesh-gateways needs a mesh gateway on the wan network in every datacenter; add gateways: { mesh: true } to %s", dcName))
			}
		}
	}
	return out, errors.Join(errs...)
}

// envoyProcess runs the gateway's Envoy on node key. It binds 8443 on
// every address, advertises lan (and wan) and, for mesh-gateway
// federation, exposes the servers.
func (g gatewayNode) envoyProcess(key, lan, wan string, admin int, exposeServers bool, env map[string]string, tls bool) *spec.Process {
	env["CONSUL_GRPC_ADDR"] = fmt.Sprintf("127.0.0.1:%d", GRPCPort)
	if tls {
		env["CONSUL_GRPC_ADDR"] = fmt.Sprintf("https://127.0.0.1:%d", GRPCTLSPort)
	}
	port := strconv.Itoa(MeshGatewayPort)
	run := spec.Command{"consul", "connect", "envoy", "-gateway=" + g.kind, "-register",
		"-service", g.kind + "-gateway", "-address", lan + ":" + port,
		"-bind-address", "default=0.0.0.0:" + port}
	if wan != "" {
		run = append(run, "-wan-address", wan+":"+port)
	}
	if exposeServers {
		run = append(run, "-expose-servers")
	}
	run = append(run, "-admin-bind", fmt.Sprintf("127.0.0.1:%d", admin), "--", "--log-level", "info")
	return &spec.Process{
		Run:       run,
		Uses:      []string{"envoy"},
		Env:       env,
		DependsOn: map[string]*spec.Dep{key + "/consul": {Condition: "ready"}},
		ReadyWhen: &spec.Ready{HTTP: &spec.HTTPCheck{URL: fmt.Sprintf("http://127.0.0.1:%d/ready", admin)}, Timeout: "90s"},
		Restart:   "on-failure",
	}
}

func totalServers(c *spec.Consul) int {
	n := 0
	for _, dc := range c.Datacenters {
		n += dc.Servers.Count
	}
	return n
}
