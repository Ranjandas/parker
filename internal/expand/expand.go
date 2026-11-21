// Package expand turns the high-level `consul:` section into the low-level
// layer of networks, raw nodes and processes. The engine only runs the
// low-level form, so `parker expand` output is itself a valid input.
package expand

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/Ranjandas/parker/internal/fake"
	"github.com/Ranjandas/parker/internal/scenario"
	"github.com/Ranjandas/parker/internal/selector"
	"github.com/Ranjandas/parker/internal/spec"
)

// Runtime variables, resolved by the engine at `parker up`.
const (
	VarRunDir  = "${run_dir}"  // the lab's run directory
	VarNodeDir = "${node_dir}" // this node's directory under run_dir
	VarParker  = "${parker}"   // path to the parker binary (built-in processes)
)

// The generated WAN network for federated datacenters. Declare
// `networks: { wan: {...} }` to amend it (e.g. another subnet).
const (
	WANNetwork = "wan"
	WANSubnet  = "10.255.0.0/24"
	wanStride  = 20 // WAN addresses per datacenter
)

// Rand is where generated tokens and the gossip key come from; tests
// replace it to get stable output.
var Rand io.Reader = rand.Reader

// ACLSetup is the process that creates the agent token once the ACL
// primary's leader is up; it runs on <dc>-server-1.
const ACLSetup = "acl-setup"

// ACLReadyLine is what acl-setup logs when the agent token exists.
const ACLReadyLine = "parker: ACLs set up"

type secrets struct{ management, agent, gossip string }

// RetryInterval is how often agents retry joining, in place of Consul's
// 30s: a lab's agents often start before what they join is ready (a
// partition's servers, a WAN peer), and a lab should be up in seconds.
const RetryInterval = "1s"

func uuid() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(Rand, b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func newSecrets() (secrets, error) {
	var s secrets
	var err error
	if s.management, err = uuid(); err != nil {
		return s, err
	}
	if s.agent, err = uuid(); err != nil {
		return s, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(Rand, key); err != nil {
		return s, err
	}
	s.gossip = base64.StdEncoding.EncodeToString(key)
	return s, nil
}

func aclOn(d *spec.Inheritable, dc *spec.Datacenter) bool {
	on := false
	if d.ACL != nil {
		on = *d.ACL
	}
	if dc.ACL != nil {
		on = *dc.ACL
	}
	return on
}

func addrAt(base netip.Addr, off int) string {
	a := base
	for range off {
		a = a.Next()
	}
	return a.String()
}

// Expand returns a new File with consul: replaced by networks: and nodes:.
// User-declared low-level networks and nodes are kept and win on conflict.
func Expand(in *spec.File) (*spec.File, error) {
	out := &spec.File{
		Parker:   in.Parker,
		Name:     in.Name,
		Versions: maps.Clone(in.Versions),
		License:  in.License, // a path only; the license itself never lands here
		Networks: map[string]*spec.Network{},
		Nodes:    map[string]*spec.RawNode{},
	}
	if in.Consul != nil {
		wanSubnet := WANSubnet
		if w := in.Networks[WANNetwork]; w != nil && w.Subnet != "" {
			wanSubnet = w.Subnet
		}
		if err := expandConsul(in.Consul, wanSubnet, out); err != nil {
			return nil, err
		}
	}
	for k, v := range in.Networks {
		if cur, ok := out.Networks[k]; ok && v.Subnet == "" {
			v.Subnet = cur.Subnet
		}
		out.Networks[k] = v
	}
	maps.Copy(out.Nodes, in.Nodes)
	for k, n := range out.Networks {
		if n.Gateway == "" {
			p, err := netip.ParsePrefix(n.Subnet)
			if err != nil {
				return nil, fmt.Errorf("networks.%s.subnet: %w", k, err)
			}
			n.Gateway = p.Masked().Addr().Next().String()
		}
	}
	if err := checkOverlaps(out.Networks); err != nil {
		return nil, err
	}
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("expanded topology: %w", err)
	}
	out.Scenario, out.Scenarios = in.Scenario, in.Scenarios
	if err := checkScenarios(out); err != nil {
		return nil, err
	}
	return out, nil
}

// checkScenarios validates each scenario and its selectors against the
// expanded nodes (raft roles are only known when it runs).
func checkScenarios(low *spec.File) error {
	all := map[string]*spec.Scenario{}
	if low.Scenario != nil {
		all["scenario"] = low.Scenario
	}
	for n, sc := range low.Scenarios {
		all["scenarios."+n] = sc
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(all)) {
		sc := all[name]
		if err := scenario.Validate(sc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		for _, u := range scenario.Upgrades(sc) {
			if low.Versions[u.Product()] == "" {
				errs = append(errs, fmt.Errorf("%s: upgrade: %s is not pinned under versions:", name, u.Product()))
			}
		}
		for _, sel := range scenario.Selectors(sc) {
			if _, err := selector.Resolve(low, sel, nil); err != nil && !errors.Is(err, selector.ErrNoRaft) {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
		}
	}
	return errors.Join(errs...)
}

func expandConsul(c *spec.Consul, wanSubnet string, out *spec.File) error {
	dcNames := slices.Sorted(maps.Keys(c.Datacenters))
	primary := ""
	for _, n := range dcNames {
		if p := c.Datacenters[n].Primary; p != nil && *p {
			primary = n
		}
	}
	if primary == "" {
		primary = dcNames[0]
	}
	defaults := c.Defaults
	if defaults == nil {
		defaults = &spec.Inheritable{}
	}

	// WAN federation: every server also joins the generated `wan` network,
	// at .10+ for the first dc, .30+ for the second, and so on.
	wanIPs := map[string][]string{}
	var allWAN []string
	var wanBase netip.Addr
	wanHosts := 0
	if c.UsesWAN() {
		wp, err := netip.ParsePrefix(wanSubnet)
		if err != nil {
			return fmt.Errorf("networks.%s.subnet: %w", WANNetwork, err)
		}
		wanBase = wp.Masked().Addr()
		wanHosts = 1<<(32-wp.Bits()) - 2
		out.Networks[WANNetwork] = &spec.Network{Subnet: wanSubnet}
	}
	if c.FederationMode() != "" || len(c.Peering) > 0 {
		// Under mesh-gateways federation servers only need the wan to
		// reach the gateways' public addresses: they keep serf WAN on
		// their LAN address, which other datacenters can't route to.
		wp, _ := netip.ParsePrefix(wanSubnet)
		for i, dcName := range dcNames {
			n := c.Datacenters[dcName].Servers.Count
			if n > wanStride {
				return fmt.Errorf("consul.datacenters.%s.servers: at most %d servers per datacenter with WAN federation", dcName, wanStride)
			}
			for s := range n {
				ip := addrAt(wanBase, 10+i*wanStride+s)
				wanIPs[dcName] = append(wanIPs[dcName], ip)
				allWAN = append(allWAN, ip)
			}
		}
		if hosts := 1<<(32-wp.Bits()) - 2; 10+len(dcNames)*wanStride > hosts {
			return fmt.Errorf("consul.federation: %d datacenters don't fit the %s wan network", len(dcNames), wanSubnet)
		}
	}

	federated := c.FederationMode() != ""
	if federated {
		for _, dcName := range dcNames {
			if aclOn(defaults, c.Datacenters[dcName]) != aclOn(defaults, c.Datacenters[primary]) {
				return fmt.Errorf("consul.datacenters.%s.acl: federated datacenters must all have acl on or all off (set it under consul.defaults)", dcName)
			}
		}
	}
	if federated {
		for _, dcName := range dcNames {
			if tlsOn(defaults, c.Datacenters[dcName]) != tlsOn(defaults, c.Datacenters[primary]) {
				return fmt.Errorf("consul.datacenters.%s.tls: federated datacenters must all have tls on or all off (set it under consul.defaults)", dcName)
			}
		}
	}
	var labCA *ca
	for _, dcName := range dcNames {
		if tlsOn(defaults, c.Datacenters[dcName]) {
			var err error
			if labCA, err = newCA(out.Name); err != nil {
				return err
			}
			break
		}
	}
	mesh, err := meshServices(c)
	if err != nil {
		return err
	}
	exts, err := externalServices(c)
	if err != nil {
		return err
	}
	gws, gwEntries, err := gatewayNodes(c, dcNames, primary)
	if err != nil {
		return err
	}
	peers, peerEntries, err := peerings(c, out.Versions["consul"])
	if err != nil {
		return err
	}
	for dc, es := range peerEntries {
		gwEntries[dc] = append(gwEntries[dc], es...)
	}
	for dc, es := range fakeProtocolEntries(c) {
		// Before the gateways', whose listeners may need the protocol.
		gwEntries[dc] = append(es, gwEntries[dc]...)
	}
	if len(gws) > 0 || len(peers) > 0 {
		mesh = true
	}
	if mesh && out.Versions["envoy"] == "" {
		return errors.New("connect: sidecar needs versions.envoy (see `parker validate` for the Envoy versions your Consul supports)")
	}
	meshFed := c.FederationMode() == spec.FederationMeshGateways
	// primaryGWs are the primary's mesh gateways on the wan network
	// (wan address and their gateway process), for secondaries to dial.
	var primaryGWs, primaryGWRefs []string
	gwWAN := map[string]string{} // node key -> wan ip
	for i, dcName := range dcNames {
		k := 0
		for _, g := range gws[dcName] {
			if !g.wan {
				continue
			}
			if k >= 10 {
				return fmt.Errorf("consul.datacenters.%s.gateways: at most 10 gateways per datacenter on the wan network", dcName)
			}
			off := 200 + i*10 + k
			if off > wanHosts {
				return fmt.Errorf("consul.datacenters.%s.gateways: no room on the %s wan network", dcName, wanSubnet)
			}
			ip := addrAt(wanBase, off)
			// The node's key: a partition's gateway is dc-partition-name.
			gk := spec.NodeKey(dcName, g.partition, g.name)
			gwWAN[gk] = ip
			if dcName == primary && g.kind == "mesh" {
				primaryGWs = append(primaryGWs, fmt.Sprintf("%s:%d", ip, MeshGatewayPort))
				primaryGWRefs = append(primaryGWRefs, gk+"/"+g.process())
			}
			k++
		}
	}
	var sec secrets
	for _, dcName := range dcNames {
		if aclOn(defaults, c.Datacenters[dcName]) {
			var err error
			if sec, err = newSecrets(); err != nil {
				return err
			}
			break
		}
	}

	for i, dcName := range dcNames {
		dc := c.Datacenters[dcName]
		acl := aclOn(defaults, dc)
		// Without federation every dc is its own primary.
		dcPrimary := dcName
		if federated {
			dcPrimary = primary
		}
		token := ""
		if acl {
			token = sec.management
		}
		tls := tlsOn(defaults, dc)
		caFile := ""
		if tls {
			caFile = CAFile
		}
		// check is how readiness checks reach this dc's agents.
		check := spec.ConsulCheck{Token: token, CAFile: caFile}
		aclDeps := map[string]*spec.Dep{}
		if acl {
			aclDeps[dcPrimary+"-server-1/"+ACLSetup] = &spec.Dep{Condition: "ready"}
		}
		subnet := dc.Subnet
		if subnet == "" {
			subnet = fmt.Sprintf("10.%d.0.0/24", i+1)
		}
		pfx, err := netip.ParsePrefix(subnet)
		if err != nil {
			return fmt.Errorf("consul.datacenters.%s.subnet: %w", dcName, err)
		}
		base := pfx.Masked().Addr()
		ipAt := func(off int) string { return addrAt(base, off) }

		lan := dcName + "-lan"
		out.Networks[lan] = &spec.Network{Subnet: subnet}

		// Servers: .10 upwards. Client nodes: .100 upwards.
		n := dc.Servers.Count
		serverIPs := make([]string, n)
		for s := range n {
			serverIPs[s] = ipAt(10 + s)
		}
		ui := dc.UI == nil || *dc.UI
		var serverRefs []string
		// Dataplane nodes reach the servers over gRPC.
		dataplanes := slices.ContainsFunc(dc.AllNodes(), func(p spec.PlacedNode) bool { return p.Node != nil && p.Node.Agent == "dataplane" })
		var dpEntries []DataplaneEntry

		for s := range n {
			name := fmt.Sprintf("server-%d", s+1)
			ip := serverIPs[s]
			cfg := map[string]any{
				"node_name":            name,
				"datacenter":           dcName,
				"primary_datacenter":   dcPrimary,
				"data_dir":             VarNodeDir + "/data",
				"server":               true,
				"bootstrap_expect":     n,
				"bind_addr":            ip,
				"advertise_addr":       ip,
				"client_addr":          "0.0.0.0",
				"retry_join":           others(serverIPs, ip),
				"retry_interval":       RetryInterval,
				"log_level":            logLevel(defaults, dc, false),
				"disable_update_check": true,
				"ui_config":            map[string]any{"enabled": ui && s == 0},
			}
			if mesh {
				cfg["connect"] = map[string]any{"enabled": true}
			}
			sc := check
			sc.Leader = true
			ready := &spec.Ready{Consul: &sc, Timeout: "90s"}
			files := map[string]string{}
			if acl {
				tokens := map[string]any{"agent": sec.agent}
				aclCfg := map[string]any{"enabled": true, "default_policy": "deny", "enable_token_persistence": true, "tokens": tokens}
				if dcName == dcPrimary {
					tokens["initial_management"] = sec.management
				} else {
					aclCfg["enable_token_replication"] = true
					tokens["replication"] = sec.management
				}
				cfg["acl"] = aclCfg
				cfg["encrypt"] = sec.gossip
			}
			attach := map[string]*spec.Attachment{lan: {IP: ip}}
			if c.Federated() {
				// Serf LAN on the dc network, serf WAN on the wan network;
				// RPC (8300) listens on both.
				wan := wanIPs[dcName][s]
				cfg["bind_addr"] = "0.0.0.0"
				cfg["serf_lan"] = ip
				cfg["serf_wan"] = wan
				cfg["advertise_addr_wan"] = wan
				cfg["retry_join_wan"] = others(allWAN, wan)
				cfg["retry_interval_wan"] = RetryInterval
				attach[WANNetwork] = &spec.Attachment{IP: wan}
				ready.Consul.WANMembers = ">=" + strconv.Itoa(len(allWAN))
			}
			inPeering, peerOnWAN := peered(peers, dcName)
			// A dialer peering through mesh gateways tries the peer's
			// gateway directly too, between tries through its own; it
			// must reach it, or each try fails and Consul backs off for
			// up to a minute, while only the tries that land on the
			// acceptor's leader succeed (#122).
			if c.FederationMode() == spec.FederationMeshGateways || peerOnWAN || dialsViaGateways(peers, dcName) {
				// RPC listens everywhere so that dials to a gateway's
				// wan address leave from (and come back to) the wan
				// interface; serf stays on the LAN address.
				attach[WANNetwork] = &spec.Attachment{IP: wanIPs[dcName][s]}
				cfg["bind_addr"] = "0.0.0.0"
				cfg["serf_lan"] = ip
				cfg["serf_wan"] = ip
			}
			if tls {
				ips := []string{ip}
				if federated || peerOnWAN {
					ips = append(ips, wanIPs[dcName][s])
				}
				if err := labCA.serverTLS(cfg, files, name, dcName, ips); err != nil {
					return err
				}
			}
			if inPeering || dataplanes {
				// Peering and dataplanes use the servers' gRPC port.
				ports, _ := cfg["ports"].(map[string]any)
				if ports == nil {
					ports = map[string]any{}
				}
				if tls {
					ports["grpc_tls"] = GRPCTLSPort
				} else {
					ports["grpc"] = GRPCPort
				}
				cfg["ports"] = ports
				if tls && dataplanes {
					// Dataplanes have no client certificate; they verify
					// the servers and authenticate with ACL tokens.
					if t, ok := cfg["tls"].(map[string]any); ok {
						t["grpc"] = map[string]any{"verify_incoming": false}
					}
				}
			}
			var serverDeps map[string]*spec.Dep
			if meshFed {
				// Servers reach other datacenters through the mesh
				// gateways; secondaries find the primary through its own.
				conn, _ := cfg["connect"].(map[string]any)
				if conn == nil {
					conn = map[string]any{"enabled": true}
				}
				conn["enable_mesh_gateway_wan_federation"] = true
				cfg["connect"] = conn
				if dcName != primary {
					cfg["primary_gateways"] = primaryGWs
					cfg["primary_gateways_interval"] = "5s"
					ready.Consul.WANMembers = ">=" + strconv.Itoa(totalServers(c))
					serverDeps = map[string]*spec.Dep{}
					for _, r := range primaryGWRefs {
						serverDeps[r] = &spec.Dep{Condition: "ready"}
					}
				}
			}
			deepMerge(cfg, defaults.Agents.Cfg(), dc.Agents.Cfg(), defaults.Server.Cfg(), dc.Server.Cfg())
			ov := dc.Servers.Overrides[name]
			version := ""
			if ov != nil {
				deepMerge(cfg, ov.Config)
				version = ov.Version
			}
			key := dcName + "-" + name
			files["consul.json"] = mustJSON(cfg)
			out.Nodes[key] = &spec.RawNode{
				Networks: attach,
				Files:    files,
				Labels:   &spec.NodeLabels{DC: dcName, Name: name, Role: "server"},
				Processes: map[string]*spec.Process{
					"consul": {
						Run:       spec.Command{"consul", "agent", "-config-file=" + VarNodeDir + "/consul.json"},
						Version:   version,
						DependsOn: serverDeps,
						ReadyWhen: ready,
					},
				},
			}
			serverRefs = append(serverRefs, key+"/consul")
		}
		if acl && dcName == dcPrimary {
			deps := map[string]*spec.Dep{}
			for _, r := range serverRefs {
				deps[r] = &spec.Dep{Condition: "ready"}
			}
			out.Nodes[dcName+"-server-1"].Processes[ACLSetup] = &spec.Process{
				Run:       aclSetupCmd(sec.agent, c.UsesTenancy()),
				Env:       apiEnv(token, tls),
				DependsOn: deps,
				ReadyWhen: &spec.Ready{Log: ACLReadyLine, Timeout: "60s"},
			}
		}

		// Partitions and namespaces, and with ACLs a token for every
		// agent, service and gateway (identity.go). The manifest is
		// written once the nodes below have asked for their tokens.
		ten := tenancyFor(c, dc)
		ids := newIdentities(dcName, c.UsesTenancy())
		setupDeps := maps.Clone(aclDeps) // what clients and config entries wait for
		if acl || !ten.empty() {
			deps := map[string]*spec.Dep{}
			for _, r := range serverRefs {
				deps[r] = &spec.Dep{Condition: "ready"}
			}
			maps.Copy(deps, aclDeps)
			s1 := out.Nodes[dcName+"-server-1"]
			s1.Processes[Tenancy] = tenancyProcess(deps, apiEnv(token, tls))
			setupDeps[dcName+"-server-1/"+Tenancy] = &spec.Dep{Condition: "ready"}
		}

		if hasEntries(dc) || len(gwEntries[dcName]) > 0 {
			if err := expandConfigEntries(out.Nodes[dcName+"-server-1"], dcName, dc, gwEntries[dcName], serverRefs, setupDeps, apiEnv(token, tls)); err != nil {
				return err
			}
		}
		for _, p := range peers {
			if p.acceptor.dc != dcName && p.dialer.dc != dcName {
				continue
			}
			here := p.dialer
			if p.acceptor.dc == dcName {
				here = p.acceptor
			}
			s1 := out.Nodes[dcName+"-server-1"]
			deps := map[string]*spec.Dep{}
			for _, r := range serverRefs {
				deps[r] = &spec.Dep{Condition: "ready"}
			}
			maps.Copy(deps, setupDeps)
			if _, ok := s1.Processes[ConfigEntries]; ok {
				deps[dcName+"-server-1/"+ConfigEntries] = &spec.Dep{Condition: "ready"}
			}
			if p.via {
				// The token carries the acceptor's gateway addresses and
				// the dialer reaches them through its own gateway: the
				// default partition's, where the servers are.
				for _, g := range gws[dcName] {
					if g.kind == "mesh" && g.wan && partitionName(g.partition) == "" {
						deps[spec.NodeKey(dcName, g.partition, g.name)+"/"+g.process()] = &spec.Dep{Condition: "ready"}
						break
					}
				}
			}
			env := tenantEnv(apiEnv(token, tls), here.part, "")
			if p.acceptor.dc == dcName {
				var external []string
				if !p.via {
					port := GRPCPort
					if tls {
						port = GRPCTLSPort
					}
					for _, ip := range wanIPs[dcName] {
						external = append(external, fmt.Sprintf("%s:%d", ip, port))
					}
				}
				s1.Processes[p.tokenProcess()] = p.tokenProc(deps, env, external)
			} else {
				deps[p.acceptor.dc+"-server-1/"+p.tokenProcess()] = &spec.Dep{Condition: "ready"}
				s1.Processes[p.establishProcess()] = p.establishProc(deps, env)
			}
		}

		clientOff := 100
		nodes := dc.AllNodes()
		gwOf := map[string]gatewayNode{} // by partition/group
		for _, g := range gws[dcName] {
			if _, ok := gwOf[g.partition+"/"+g.group]; !ok {
				nodes = append(nodes, spec.PlacedNode{Name: g.group, Node: &spec.Node{Count: g.count}, Partition: g.partition, Part: dc.Partitions[g.partition]})
			}
			gwOf[g.partition+"/"+g.group] = g
		}
		slices.SortFunc(nodes, func(a, b spec.PlacedNode) int {
			return strings.Compare(a.Name+"\x00"+a.Partition, b.Name+"\x00"+b.Partition)
		})
		for _, placed := range nodes {
			nodeName, node, part := placed.Name, placed.Node, partitionName(placed.Partition)
			gw, isGW := gwOf[placed.Partition+"/"+nodeName]
			if node == nil {
				node = &spec.Node{}
			}
			var partSD *spec.Service
			var partClient *spec.AgentSettings
			if placed.Part != nil {
				partSD, partClient = placed.Part.ServiceDefaults, placed.Part.Client
			}
			count := max(node.Count, 1)
			for k := range count {
				name := nodeName
				if node.Count > 1 {
					name = nodeName + "-" + strconv.Itoa(k+1)
				}
				ip := ipAt(clientOff)
				clientOff++
				key := spec.NodeKey(dcName, placed.Partition, name)
				if _, clash := out.Nodes[key]; clash {
					return fmt.Errorf("consul.datacenters.%s: two nodes expand to %s (rename one, e.g. a default-partition node named like <partition>-<node>)", dcName, key)
				}
				rn := &spec.RawNode{
					Networks:  map[string]*spec.Attachment{lan: {IP: ip}},
					Files:     map[string]string{},
					Processes: map[string]*spec.Process{},
				}
				gwWANIP := ""
				if isGW {
					gwWANIP = gwWAN[key]
					if gwWANIP != "" {
						rn.Networks[WANNetwork] = &spec.Attachment{IP: gwWANIP}
					}
				}
				for pn, p := range node.Processes {
					rn.Processes[pn] = p
				}

				services := node.Services
				if node.Service != nil {
					services = map[string]*spec.Service{nodeName: node.Service}
				}
				var regs []any
				sidecars := 0
				isDP := node.Agent == "dataplane"
				transparentNode := false
				for _, s := range services {
					if mergeService(defaults.ServiceDefaults, dc.ServiceDefaults, partSD, s).Connect == "tproxy" {
						transparentNode = true
					}
				}
				for _, svcName := range slices.Sorted(maps.Keys(services)) {
					svc := mergeService(defaults.ServiceDefaults, dc.ServiceDefaults, partSD, services[svcName])
					svcCheck := check
					svcCheck.Partition = part
					if svc.Namespace != spec.DefaultNamespace {
						svcCheck.Namespace = svc.Namespace
					}
					reg, proc := expandService(svcName, dcName, svc, svcCheck, SidecarPort+sidecars)
					if isDP {
						// No agent to register with or to ask about health:
						// dataplane-health on server-1 does both.
						dpEntries = append(dpEntries, dataplaneEntries(name, ip, dcName, part, node.Meta, svcName, svc, SidecarPort+sidecars)...)
						if proc != nil && proc.ReadyWhen != nil {
							proc.ReadyWhen.Consul = nil
						}
						if proc != nil {
							rn.Processes[svcName] = proc
						}
						if inMesh(svc) {
							d := dataplane{svc: svcName, node: name, ns: svcCheck.Namespace, part: part, dc: dcName, servers: serverIPs,
								admin: SidecarAdminPort + sidecars, graceful: DataplaneGracefulPort + sidecars, tls: tls,
								deps: map[string]*spec.Dep{dcName + "-server-1/" + DataplaneHealth: {Condition: "ready"}}}
							if acl {
								if d.token, err = ids.service(part, svcCheck.Namespace, svcName); err != nil {
									return err
								}
							}
							rn.Processes[svcName+SidecarSuffix] = d.process()
							sidecars++
						}
						continue
					}
					svcToken := ""
					if acl {
						// Registers and syncs the service, and its sidecar.
						if svcToken, err = ids.service(part, svcCheck.Namespace, svcName); err != nil {
							return err
						}
						reg["token"] = svcToken
					}
					regs = append(regs, reg)
					if proc != nil {
						rn.Processes[svcName] = proc
					}
					if inMesh(svc) {
						sc := sidecar{svc: svcName, key: key, admin: SidecarAdminPort + sidecars, port: SidecarPort + sidecars,
							tls: tls, marked: transparentNode, transparent: svc.Connect == "tproxy"}
						rn.Processes[svcName+SidecarSuffix] = sc.process(tenantEnv(apiEnv(svcToken, tls), part, svcCheck.Namespace))
						sidecars++
					}
				}
				if isGW {
					g := gw
					g.name = name
					// Gateways register in their partition's default
					// namespace (Consul looks for mesh gateways there)
					// unless they set namespace:.
					gwNS := g.namespace
					if gwNS == "" && c.UsesTenancy() {
						gwNS = spec.DefaultNamespace
					}
					gwToken := ""
					if acl {
						var linked []linkedService
						for _, e := range gwEntries[dcName] {
							if e.partition == part && e.entry["Kind"] == "terminating-gateway" && e.entry["Name"] == g.service() {
								linked = linkedServices(e.entry)
							}
						}
						tokNS := gwNS
						if tokNS == spec.DefaultNamespace {
							tokNS = ""
						}
						if gwToken, err = ids.gateway(g, part, tokNS, linked); err != nil {
							return err
						}
					}
					gp := g.envoyProcess(key, ip, gwWANIP, SidecarAdminPort+sidecars, meshFed, tenantEnv(apiEnv(gwToken, tls), part, gwNS), tls)
					if meshFed && dcName != primary && gwWANIP != "" {
						// Ready once the primary reaches this datacenter,
						// which goes through this gateway: ask a primary
						// server on its wan address.
						reach := check
						reach.Addr = fmt.Sprintf("%s:%d", wanIPs[primary][0], HTTPSPort)
						reach.Reaches = dcName
						gp.ReadyWhen.Consul = &reach
					}
					rn.Processes[g.process()] = gp
					sidecars++ // the agent opens its gRPC port for it
				}

				if isDP && tls {
					rn.Files[CAFile] = labCA.certPEM
				}
				if node.Agent != "none" && !isDP {
					cfg := map[string]any{
						"node_name":            name,
						"datacenter":           dcName,
						"primary_datacenter":   dcPrimary,
						"data_dir":             VarNodeDir + "/data",
						"server":               false,
						"bind_addr":            ip,
						"advertise_addr":       ip,
						"client_addr":          "0.0.0.0",
						"retry_join":           serverIPs,
						"retry_interval":       RetryInterval,
						"log_level":            logLevel(defaults, dc, true),
						"disable_update_check": true,
					}
					if part != "" {
						cfg["partition"] = part
					}
					if partClient != nil && partClient.LogLevel != "" {
						cfg["log_level"] = partClient.LogLevel
					}
					if len(node.Meta) > 0 {
						cfg["node_meta"] = node.Meta
					}
					if len(regs) > 0 {
						cfg["services"] = regs
					}
					if acl {
						agentTok, err := ids.node(part, name)
						if err != nil {
							return err
						}
						tokens := map[string]any{"agent": agentTok}
						if transparentNode {
							// The node's DNS goes to this agent, and lookups
							// under default deny need a token.
							if tokens["dns"], err = ids.dns(part); err != nil {
								return err
							}
						}
						cfg["acl"] = map[string]any{"enabled": true, "default_policy": "deny", "enable_token_persistence": true,
							"tokens": tokens}
						cfg["encrypt"] = sec.gossip
					}
					if tls {
						labCA.clientTLS(cfg, rn.Files)
					}
					if sidecars > 0 {
						// Envoy gets its config from the local agent over gRPC.
						ports, _ := cfg["ports"].(map[string]any)
						if ports == nil {
							ports = map[string]any{}
						}
						if tls {
							ports["grpc_tls"] = GRPCTLSPort
						} else {
							ports["grpc"] = GRPCPort
						}
						cfg["ports"] = ports
					}
					deepMerge(cfg, defaults.Agents.Cfg(), dc.Agents.Cfg(), defaults.Client.Cfg(), dc.Client.Cfg(), partClient.Cfg(), node.Config)
					rn.Files["consul.json"] = mustJSON(cfg)
					deps := map[string]*spec.Dep{}
					serverCond := "ready"
					if isGW && meshFed && dcName != primary {
						// A secondary's servers are ready once federated,
						// which can take its own gateway.
						serverCond = "started"
					}
					for _, r := range serverRefs {
						deps[r] = &spec.Dep{Condition: serverCond}
					}
					maps.Copy(deps, setupDeps)
					rn.Processes["consul"] = &spec.Process{
						Run:       spec.Command{"consul", "agent", "-config-file=" + VarNodeDir + "/consul.json"},
						DependsOn: deps,
						ReadyWhen: &spec.Ready{
							Consul:  withMembers(check, ">="+strconv.Itoa(n+1)),
							Timeout: "60s",
						},
					}
					for pn, p := range rn.Processes {
						if pn == "consul" {
							continue
						}
						if p.DependsOn == nil {
							p.DependsOn = map[string]*spec.Dep{}
						}
						if _, ok := p.DependsOn[key+"/consul"]; !ok {
							p.DependsOn[key+"/consul"] = &spec.Dep{Condition: "started"}
						}
					}
				}
				role := "client"
				switch node.Agent {
				case "none":
					role = "none"
				case "dataplane":
					role = DataplaneRole
				}
				if isGW {
					role = "gateway"
				}
				rn.Labels = &spec.NodeLabels{DC: dcName, Name: name, Role: role, Group: nodeName, Services: slices.Sorted(maps.Keys(services)),
					Partition: placed.Partition, Namespaces: nodeNamespaces(c, dc, placed.Part, services)}
				if isGW && gw.namespace != "" {
					rn.Labels.Namespaces = []string{gw.namespace}
				}
				out.Nodes[key] = rn
			}
		}
		// External services: on this dc's LAN from the top of the subnet
		// down, registered by the same process as dataplane services.
		extEntries, err := exts.place(dcName, pfx, clientOff)
		if err != nil {
			return err
		}
		dpEntries = append(dpEntries, extEntries...)
		ten.Policies, ten.Tokens = ids.policies, ids.tokens
		if s1 := out.Nodes[dcName+"-server-1"]; s1.Processes[Tenancy] != nil {
			s1.Files[TenancyManifest] = mustJSON(ten)
		}
		if len(dpEntries) > 0 {
			deps := map[string]*spec.Dep{}
			for _, r := range serverRefs {
				deps[r] = &spec.Dep{Condition: "ready"}
			}
			maps.Copy(deps, setupDeps)
			s1 := out.Nodes[dcName+"-server-1"]
			s1.Files[DataplaneManifest] = mustJSON(dpEntries)
			s1.Processes[DataplaneHealth] = dataplaneHealthProcess(deps, apiEnv(token, tls), serverIPs)
		}
	}
	return exts.nodes(out)
}

// expandService returns the agent service registration and, when the
// service has a run: command, the process that runs it.
func expandService(name, dcName string, svc *spec.Service, agent spec.ConsulCheck, sidecarPort int) (map[string]any, *spec.Process) {
	reg := map[string]any{"name": name}
	if svc.Namespace != "" && svc.Namespace != spec.DefaultNamespace {
		reg["namespace"] = svc.Namespace
	}
	if svc.Port > 0 {
		reg["port"] = svc.Port
	}
	check := svc.Check
	if check == "" {
		check = "none"
		if len(svc.Run) > 0 && svc.Port > 0 {
			check = "http"
			if isFake(svc) && fakeProtocol(svc) != "http" {
				check = fakeProtocol(svc)
			}
		}
	}
	switch check {
	case "http":
		reg["check"] = map[string]any{"http": fmt.Sprintf("http://127.0.0.1:%d%s", svc.Port, healthPath(svc)), "interval": "2s"}
	case "tcp":
		reg["check"] = map[string]any{"tcp": fmt.Sprintf("127.0.0.1:%d", svc.Port), "interval": "2s"}
	case "grpc":
		reg["check"] = map[string]any{"grpc": fmt.Sprintf("127.0.0.1:%d", svc.Port), "interval": "2s"}
	}
	if inMesh(svc) {
		var ups []any
		for _, dest := range slices.Sorted(maps.Keys(svc.Upstreams)) {
			u := svc.Upstreams[dest]
			up := map[string]any{"destination_name": dest, "local_bind_port": u.Port}
			for k, v := range map[string]string{"datacenter": u.DC, "destination_peer": u.Peer,
				"destination_partition": u.Partition, "destination_namespace": u.Namespace} {
				if v != "" {
					up[k] = v
				}
			}
			ups = append(ups, up)
		}
		proxy := map[string]any{}
		if len(ups) > 0 {
			proxy["upstreams"] = ups
		}
		if svc.Connect == "tproxy" {
			proxy["mode"] = "transparent"
			proxy["transparent_proxy"] = map[string]any{"outbound_listener_port": TproxyOutboundPort}
		}
		reg["connect"] = map[string]any{"sidecar_service": map[string]any{"port": sidecarPort, "proxy": proxy}}
	}
	deepMerge(reg, svc.Register)

	if len(svc.Run) == 0 {
		return reg, nil
	}
	run := svc.Run
	env := svc.Env
	if isFake(svc) {
		run = spec.Command{VarParker, "fake-service", "--name", name, "--port", strconv.Itoa(svc.Port), "--dc", dcName,
			"--control", VarNodeDir + "/" + fake.SocketName(name)}
		env = maps.Clone(svc.Env)
		if env == nil {
			env = map[string]string{}
		}
		env[FakeEnv] = fakeConfig(svc)
	}
	p := &spec.Process{Run: run, Env: env}
	if svc.Port > 0 {
		// 60s: after a leader change the agent retries its catalog sync
		// only every ~15s, so a restart can take a few rounds to show.
		p.ReadyWhen = &spec.Ready{Port: svc.Port, Timeout: "60s"}
		if isFake(svc) && fakeProtocol(svc) != "tcp" {
			fakeReady(svc, p.ReadyWhen)
		}
		if check != "none" && !fakeStartsUnhealthy(svc) {
			agent.ServiceHealthy = name
			p.ReadyWhen.Consul = &agent
		}
	}
	return reg, p
}

func mergeService(layers ...*spec.Service) *spec.Service {
	out := &spec.Service{}
	for _, l := range layers {
		if l == nil {
			continue
		}
		if l.Port != 0 {
			out.Port = l.Port
		}
		if len(l.Run) > 0 {
			out.Run = l.Run
		}
		if l.Check != "" {
			out.Check = l.Check
		}
		if len(l.Env) > 0 {
			if out.Env == nil {
				out.Env = map[string]string{}
			}
			maps.Copy(out.Env, l.Env)
		}
		if len(l.Register) > 0 {
			if out.Register == nil {
				out.Register = map[string]any{}
			}
			deepMerge(out.Register, l.Register)
		}
		if l.Connect != "" {
			out.Connect = l.Connect
		}
		if l.Namespace != "" {
			out.Namespace = l.Namespace
		}
		if l.Fake != nil {
			out.Fake = mergeFake(out.Fake, l.Fake)
		}
		if len(l.Upstreams) > 0 {
			if out.Upstreams == nil {
				out.Upstreams = map[string]*spec.Upstream{}
			}
			maps.Copy(out.Upstreams, l.Upstreams)
		}
	}
	return out
}

func logLevel(d *spec.Inheritable, dc *spec.Datacenter, client bool) string {
	lvl := "INFO"
	for _, a := range []*spec.AgentSettings{d.Agents, dc.Agents, pick(d, client), pick(&dc.Inheritable, client)} {
		if a != nil && a.LogLevel != "" {
			lvl = a.LogLevel
		}
	}
	return lvl
}

func pick(i *spec.Inheritable, client bool) *spec.AgentSettings {
	if client {
		return i.Client
	}
	return i.Server
}

func others(all []string, self string) []string {
	var o []string
	for _, a := range all {
		if a != self {
			o = append(o, a)
		}
	}
	return o
}

// deepMerge merges each layer into dst; later layers win, maps merge, everything else replaces.
func deepMerge(dst map[string]any, layers ...map[string]any) {
	for _, l := range layers {
		for k, v := range l {
			if vm, ok := v.(map[string]any); ok {
				if dm, ok := dst[k].(map[string]any); ok {
					deepMerge(dm, vm)
					continue
				}
				cp := map[string]any{}
				deepMerge(cp, vm)
				dst[k] = cp
				continue
			}
			dst[k] = v
		}
	}
}

func mustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// checkOverlaps rejects lab networks whose subnets overlap each other: they
// would become two bridges routing the same addresses.
func checkOverlaps(nets map[string]*spec.Network) error {
	names := slices.Sorted(maps.Keys(nets))
	pfx := map[string]netip.Prefix{}
	for _, n := range names {
		p, err := netip.ParsePrefix(nets[n].Subnet)
		if err != nil {
			return fmt.Errorf("networks.%s.subnet: %w", n, err)
		}
		pfx[n] = p.Masked()
	}
	for i, a := range names {
		for _, b := range names[i+1:] {
			if pfx[a].Overlaps(pfx[b]) {
				return fmt.Errorf("networks %s (%s) and %s (%s) overlap; give one of them another subnet", a, pfx[a], b, pfx[b])
			}
		}
	}
	return nil
}

func withMembers(c spec.ConsulCheck, members string) *spec.ConsulCheck {
	c.Members = members
	return &c
}

// apiEnv points the consul CLI and API clients run on a server at its
// local agent.
func apiEnv(token string, tls bool) map[string]string {
	env := map[string]string{"CONSUL_HTTP_ADDR": "127.0.0.1:8500"}
	if token != "" {
		env["CONSUL_HTTP_TOKEN"] = token
	}
	if tls {
		env["CONSUL_HTTP_ADDR"] = fmt.Sprintf("https://127.0.0.1:%d", HTTPSPort)
		env["CONSUL_CACERT"] = VarNodeDir + "/" + CAFile
	}
	return env
}

// ConfigEntries is the process that applies a dc's config_entries, and
// ConfigEntriesReadyLine what it logs when all are written.
const (
	ConfigEntries          = "config-entries"
	ConfigEntriesReadyLine = "parker: config entries applied"
	ConfigEntriesManifest  = "config-entries.json"
)

// EntryRef is one line of the config-entries manifest.
type EntryRef struct {
	File  string `json:"file"`
	Label string `json:"label"`
	// Partition the entry is written into ("" for the default).
	Partition string `json:"partition,omitempty"`
}

func expandConfigEntries(srv *spec.RawNode, dcName string, dc *spec.Datacenter, gen []gatewayEntry, serverRefs []string, aclDeps map[string]*spec.Dep, env map[string]string) error {
	var refs []EntryRef
	add := func(e spec.ConfigEntry, src *spec.EntrySource, label, partition string) error {
		var name, body string
		if src != nil && src.HCL != "" {
			name, body = fmt.Sprintf("config-entry-%d.hcl", len(refs)), src.HCL
		} else {
			b, err := json.MarshalIndent(e, "", "  ")
			if err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
			name, body = fmt.Sprintf("config-entry-%d.json", len(refs)), string(b)
		}
		srv.Files[name] = body
		refs = append(refs, EntryRef{File: VarNodeDir + "/" + name, Label: label, Partition: partition})
		return nil
	}
	source := func(srcs []spec.EntrySource, i int) *spec.EntrySource {
		if i < len(srcs) {
			return &srcs[i]
		}
		return nil
	}
	// Generated exported-services entries fold into one the user wrote in
	// the same partition: Consul keeps one per partition.
	merged := map[int]bool{}
	mergeInto := func(e spec.ConfigEntry, src *spec.EntrySource, label, partition string) (spec.ConfigEntry, error) {
		if e["Kind"] != "exported-services" {
			return e, nil
		}
		for i, g := range gen {
			if !g.merge || g.partition != partition || merged[i] {
				continue
			}
			if src != nil && src.HCL != "" {
				return nil, fmt.Errorf("%s: consul.peering exports go into this partition's exported-services entry, which Parker can't merge into an HCL file; list them in the file's Services, or write the entry in YAML", label)
			}
			e = maps.Clone(e)
			e["Services"] = mergeExports(e["Services"], g.entry["Services"])
			merged[i] = true
		}
		return e, nil
	}
	for i, e := range dc.ConfigEntries {
		src, label := source(dc.EntrySources, i), dc.EntryLabel(dcName, i)
		e, err := mergeInto(e, src, label, "")
		if err != nil {
			return err
		}
		if err := add(e, src, label, ""); err != nil {
			return err
		}
	}
	// Each partition's entries are written into it.
	for _, pn := range slices.Sorted(maps.Keys(dc.Partitions)) {
		part := dc.Partitions[pn]
		if part == nil {
			continue
		}
		for i, e := range part.ConfigEntries {
			src, label := source(part.EntrySources, i), part.EntryLabel(dcName, pn, i)
			e, err := mergeInto(e, src, label, partitionName(pn))
			if err != nil {
				return err
			}
			if err := add(e, src, label, partitionName(pn)); err != nil {
				return err
			}
		}
	}
	// Gateways' own entries last: their listeners need the protocols that
	// service-defaults in config_entries set.
	for i, g := range gen {
		if merged[i] {
			continue
		}
		name := fmt.Sprintf("gateway-entry-%d.json", i)
		srv.Files[name] = mustJSON(g.entry)
		refs = append(refs, EntryRef{File: VarNodeDir + "/" + name, Label: g.label, Partition: g.partition})
	}
	srv.Files[ConfigEntriesManifest] = mustJSON(refs)
	deps := map[string]*spec.Dep{}
	for _, r := range serverRefs {
		deps[r] = &spec.Dep{Condition: "ready"}
	}
	maps.Copy(deps, aclDeps)
	srv.Processes[ConfigEntries] = &spec.Process{
		Run:       spec.Command{VarParker, ConfigEntries, VarNodeDir + "/" + ConfigEntriesManifest},
		Env:       env,
		DependsOn: deps,
		ReadyWhen: &spec.Ready{Log: ConfigEntriesReadyLine, Timeout: "60s"},
	}
	return nil
}

// mergeExports adds the generated exported services to the user's,
// appending consumers to a service (by name and namespace) listed in both.
func mergeExports(user, gen any) []any {
	key := func(v any) string {
		m, _ := v.(map[string]any)
		ns, _ := m["Namespace"].(string)
		if ns == spec.DefaultNamespace {
			ns = ""
		}
		name, _ := m["Name"].(string)
		return ns + "/" + name
	}
	us, _ := user.([]any)
	out := make([]any, 0, len(us))
	at := map[string]int{}
	for _, u := range us {
		if m, ok := u.(map[string]any); ok {
			u = maps.Clone(m)
			at[key(u)] = len(out)
		}
		out = append(out, u)
	}
	gs, _ := gen.([]any)
	for _, g := range gs {
		if i, ok := at[key(g)]; ok {
			m := out[i].(map[string]any)
			cs, _ := m["Consumers"].([]any)
			m["Consumers"] = append(slices.Clone(cs), g.(map[string]any)["Consumers"].([]any)...)
			continue
		}
		out = append(out, g)
	}
	return out
}

// Mesh: a service with connect: sidecar (or tproxy) gets a sidecar proxy
// registered with it and an Envoy process, `consul connect envoy`, on its
// node.
const (
	SidecarSuffix      = "-sidecar"
	SidecarAdminPort   = 19000 // Envoy admin of a node's first sidecar; then 19001, ...
	SidecarPort        = 21000 // public listener of a node's first sidecar; then 21001, ...
	TproxyOutboundPort = 15001 // Envoy's outbound listener in transparent mode
	SidecarCmd         = "sidecar"
	GRPCPort           = 8502
	GRPCTLSPort        = 8503
)

func inMesh(svc *spec.Service) bool { return svc.Connect == "sidecar" || svc.Connect == "tproxy" }

// meshServices reports whether any service is in the mesh, and checks
// their connect settings.
func meshServices(c *spec.Consul) (bool, error) {
	defaults := c.Defaults
	if defaults == nil {
		defaults = &spec.Inheritable{}
	}
	mesh := false
	var errs []error
	for _, dcName := range slices.Sorted(maps.Keys(c.Datacenters)) {
		dc := c.Datacenters[dcName]
		for _, placed := range dc.AllNodes() {
			nodeName, node := placed.Name, placed.Node
			if node == nil {
				continue
			}
			var partSD *spec.Service
			if placed.Part != nil {
				partSD = placed.Part.ServiceDefaults
			}
			services := node.Services
			if node.Service != nil {
				services = map[string]*spec.Service{nodeName: node.Service}
			}
			transparent := ""
			for _, svcName := range slices.Sorted(maps.Keys(services)) {
				svc := mergeService(defaults.ServiceDefaults, dc.ServiceDefaults, partSD, services[svcName])
				where := fmt.Sprintf("consul.datacenters.%s.nodes.%s: service %s", dcName, nodeName, svcName)
				errs = append(errs, checkFake(where, svc, services[svcName])...)
				switch svc.Connect {
				case "", "none":
					if len(svc.Upstreams) > 0 {
						errs = append(errs, fmt.Errorf("%s: upstreams need connect: sidecar or tproxy", where))
					}
					continue
				case "sidecar":
				case "tproxy":
					if transparent != "" {
						errs = append(errs, fmt.Errorf("%s: connect: tproxy: the node already has one (%s); a node takes at most one transparent proxy", where, transparent))
					}
					transparent = svcName
				default:
					errs = append(errs, fmt.Errorf("%s: connect: want none, sidecar or tproxy, got %q", where, svc.Connect))
					continue
				}
				if node.Agent == "none" {
					errs = append(errs, fmt.Errorf("%s: connect: %s needs a Consul agent on the node", where, svc.Connect))
				}
				if node.Agent == "dataplane" && svc.Connect == "tproxy" {
					errs = append(errs, fmt.Errorf("%s: connect: tproxy isn't supported on dataplane nodes yet; use sidecar", where))
				}
				mesh = true
				ports := map[int]string{}
				for _, dest := range slices.Sorted(maps.Keys(svc.Upstreams)) {
					u := svc.Upstreams[dest]
					switch {
					case u == nil || u.Port <= 0 || u.Port > 65535:
						errs = append(errs, fmt.Errorf("%s: upstream %s needs a port", where, dest))
					case u.Port == svc.Port:
						errs = append(errs, fmt.Errorf("%s: upstream %s port %d is the service's own port", where, dest, u.Port))
					case ports[u.Port] != "":
						errs = append(errs, fmt.Errorf("%s: upstreams %s and %s both use port %d", where, ports[u.Port], dest, u.Port))
					default:
						ports[u.Port] = dest
					}
				}
			}
		}
	}
	return mesh, errors.Join(errs...)
}

// sidecar is one Envoy on a node.
type sidecar struct {
	svc, key    string
	admin, port int
	tls         bool
	// marked runs Envoy through `parker sidecar`, which marks its upstream
	// sockets so a transparent proxy on the node lets them through;
	// transparent also applies that proxy's iptables rules.
	marked, transparent bool
}

// process runs Envoy for the service through consul connect envoy, ready
// once Envoy's admin /ready answers.
func (s sidecar) process(env map[string]string) *spec.Process {
	env["CONSUL_GRPC_ADDR"] = fmt.Sprintf("127.0.0.1:%d", GRPCPort)
	if s.tls {
		env["CONSUL_GRPC_ADDR"] = fmt.Sprintf("https://127.0.0.1:%d", GRPCTLSPort)
	}
	run := spec.Command{"consul", "connect", "envoy", "-sidecar-for", s.svc,
		"-admin-bind", fmt.Sprintf("127.0.0.1:%d", s.admin), "--", "--log-level", "info"}
	uses := []string{"envoy"}
	if s.marked {
		wrap := spec.Command{VarParker, SidecarCmd}
		if s.transparent {
			wrap = append(wrap, "--transparent", "--inbound-port", strconv.Itoa(s.port),
				"--outbound-port", strconv.Itoa(TproxyOutboundPort))
		}
		run = append(append(wrap, "--"), run...)
		uses = []string{"consul", "envoy"}
	}
	return &spec.Process{
		Run:       run,
		Uses:      uses,
		Env:       env,
		DependsOn: map[string]*spec.Dep{s.key + "/consul": {Condition: "ready"}},
		ReadyWhen: &spec.Ready{HTTP: &spec.HTTPCheck{URL: fmt.Sprintf("http://127.0.0.1:%d/ready", s.admin)}, Timeout: "60s"},
		Restart:   "on-failure",
	}
}
