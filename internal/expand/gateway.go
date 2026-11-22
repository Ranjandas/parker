package expand

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/Ranjandas/parker/internal/spec"
)

// MeshGatewayPort is where mesh gateways listen, on the LAN and the WAN
// (and the registered port of the other gateway kinds).
const MeshGatewayPort = 8443

// gatewayNode is one gateway node: a client agent plus the gateway's
// Envoy, registered by `consul connect envoy -register`.
type gatewayNode struct {
	group     string // key under gateways:
	partition string // as under partitions: ("" outside them)
	name      string // node name (group, or group-N with count)
	kind      string
	count     int
	wan       bool
	namespace string // "" for the partition's default namespace
}

func (g gatewayNode) process() string { return g.kind + "-gateway" }

// service is the gateway's Consul service (and config entry) name: the
// group, or <kind>-gateway when the group is just the kind.
func (g gatewayNode) service() string {
	if g.group == g.kind {
		return g.kind + "-gateway"
	}
	return g.group
}

// gatewayEntry is a config entry Parker writes for a gateway.
type gatewayEntry struct {
	label     string
	entry     spec.ConfigEntry
	partition string // written into this partition ("" for the default)
	// merge folds Services into an exported-services entry the user
	// wrote in the same partition (Consul keeps one per partition).
	merge bool
}

// gatewayNodes resolves each datacenter's gateways (sorted by node name)
// and checks them, and what mesh-gateway federation needs.
func gatewayNodes(c *spec.Consul, dcNames []string, primary string) (map[string][]gatewayNode, map[string][]gatewayEntry, error) {
	defaults := c.Defaults
	if defaults == nil {
		defaults = &spec.Inheritable{}
	}
	meshFed := c.FederationMode() == spec.FederationMeshGateways
	out := map[string][]gatewayNode{}
	entries := map[string][]gatewayEntry{}
	var errs []error
	for _, dcName := range dcNames {
		dc := c.Datacenters[dcName]
		wanMesh := false
		nodeNames := map[string]bool{} // by partition/name
		for _, n := range dc.AllNodes() {
			nodeNames[n.Partition+"/"+n.Name] = true
		}
		for _, placed := range dc.AllGateways() {
			group, g := placed.Name, placed.Gateway
			where := fmt.Sprintf("consul.datacenters.%s.gateways.%s", dcName, group)
			if placed.Partition != "" {
				where = fmt.Sprintf("consul.datacenters.%s.partitions.%s.gateways.%s", dcName, placed.Partition, group)
			}
			if g == nil {
				g = &spec.Gateway{}
			}
			kind := g.Kind
			if kind == "" {
				kind = group
			}
			entryKind, ok := spec.GatewayKinds[kind]
			if !ok {
				errs = append(errs, fmt.Errorf("%s: kind: want mesh, ingress, terminating or api, got %q (set kind: when the name isn't one)", where, kind))
				continue
			}
			if kind != "mesh" && g.WAN != nil && *g.WAN {
				errs = append(errs, fmt.Errorf("%s: wan: only mesh gateways join the wan network", where))
				continue
			}
			if kind == "mesh" && g.Entry != nil {
				errs = append(errs, fmt.Errorf("%s: entry: mesh gateways have no config entry of their own; set MeshGateway in proxy-defaults under config_entries", where))
				continue
			}
			if kind == "mesh" && g.Namespace != "" && g.Namespace != spec.DefaultNamespace {
				errs = append(errs, fmt.Errorf("%s: namespace: mesh gateways sit in their partition's default namespace", where))
				continue
			}
			if nodeNames[placed.Partition+"/"+group] {
				errs = append(errs, fmt.Errorf("%s: a node is already named %s", where, group))
				continue
			}
			if g.Count < 0 {
				errs = append(errs, fmt.Errorf("%s: count: want 1 or more", where))
				continue
			}
			wan := kind == "mesh" && g.OnWAN(c)
			if wan {
				wanMesh = true
			}
			count := max(g.Count, 1)
			var gn gatewayNode
			for k := range count {
				name := group
				if g.Count > 1 {
					name = group + "-" + strconv.Itoa(k+1)
				}
				gn = gatewayNode{group: group, partition: placed.Partition, name: name, kind: kind, count: g.Count, wan: wan}
				if g.Namespace != spec.DefaultNamespace {
					gn.namespace = g.Namespace
				}
				out[dcName] = append(out[dcName], gn)
			}
			if entryKind != "" {
				e := spec.ConfigEntry{}
				for k, v := range g.Entry {
					e[k] = v
				}
				e["Kind"], e["Name"] = entryKind, gn.service()
				if gn.namespace != "" {
					e["Namespace"] = gn.namespace
				}
				entries[dcName] = append(entries[dcName], gatewayEntry{label: where + ".entry", entry: e, partition: partitionName(placed.Partition)})
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
	return out, entries, errors.Join(errs...)
}

// envoyProcess runs the gateway's Envoy on node key, registered at lan
// (and wan). A mesh gateway binds 8443 on every address and, for
// mesh-gateway federation, exposes the servers. Only mesh gateways take
// -expose-servers: Envoy for other kinds refuses to start with it.
func (g gatewayNode) envoyProcess(key, lan, wan string, admin int, exposeServers bool, env map[string]string, tls bool) *spec.Process {
	env["CONSUL_GRPC_ADDR"] = fmt.Sprintf("127.0.0.1:%d", GRPCPort)
	if tls {
		env["CONSUL_GRPC_ADDR"] = fmt.Sprintf("https://127.0.0.1:%d", GRPCTLSPort)
	}
	port := strconv.Itoa(MeshGatewayPort)
	run := spec.Command{"consul", "connect", "envoy", "-gateway=" + g.kind, "-register",
		"-service", g.service(), "-address", lan + ":" + port}
	if g.kind == "mesh" {
		// Reachable on the LAN and wan addresses alike. Other kinds bind
		// their listeners (from the config entry) on the LAN address.
		run = append(run, "-bind-address", "default=0.0.0.0:"+port)
	}
	if wan != "" {
		run = append(run, "-wan-address", wan+":"+port)
	}
	if exposeServers && g.kind == "mesh" {
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
