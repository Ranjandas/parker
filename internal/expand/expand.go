// Package expand turns the high-level `consul:` section into the low-level
// layer of networks, raw nodes and processes. The engine only runs the
// low-level form, so `parker expand` output is itself a valid input.
package expand

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"slices"
	"strconv"

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

func newSecrets() (secrets, error) {
	uuid := func() (string, error) {
		b := make([]byte, 16)
		if _, err := io.ReadFull(Rand, b); err != nil {
			return "", err
		}
		b[6] = b[6]&0x0f | 0x40
		b[8] = b[8]&0x3f | 0x80
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
	}
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
	return out, nil
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
	if c.Federated() {
		wp, err := netip.ParsePrefix(wanSubnet)
		if err != nil {
			return fmt.Errorf("networks.%s.subnet: %w", WANNetwork, err)
		}
		wanBase := wp.Masked().Addr()
		out.Networks[WANNetwork] = &spec.Network{Subnet: wanSubnet}
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

	if c.Federated() {
		for _, dcName := range dcNames {
			if aclOn(defaults, c.Datacenters[dcName]) != aclOn(defaults, c.Datacenters[primary]) {
				return fmt.Errorf("consul.datacenters.%s.acl: federated datacenters must all have acl on or all off (set it under consul.defaults)", dcName)
			}
		}
	}
	if c.Federated() {
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
		if c.Federated() {
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
				"log_level":            logLevel(defaults, dc, false),
				"disable_update_check": true,
				"ui_config":            map[string]any{"enabled": ui && s == 0},
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
				attach[WANNetwork] = &spec.Attachment{IP: wan}
				ready.Consul.WANMembers = ">=" + strconv.Itoa(len(allWAN))
			}
			if tls {
				ips := []string{ip}
				if c.Federated() {
					ips = append(ips, wanIPs[dcName][s])
				}
				if err := labCA.serverTLS(cfg, files, name, dcName, ips); err != nil {
					return err
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
				Processes: map[string]*spec.Process{
					"consul": {
						Run:       spec.Command{"consul", "agent", "-config-file=" + VarNodeDir + "/consul.json"},
						Version:   version,
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
				Run:       spec.Command{VarParker, ACLSetup, "--agent-token", sec.agent},
				Env:       apiEnv(token, tls),
				DependsOn: deps,
				ReadyWhen: &spec.Ready{Log: ACLReadyLine, Timeout: "60s"},
			}
		}

		if len(dc.ConfigEntries) > 0 {
			if err := expandConfigEntries(out.Nodes[dcName+"-server-1"], dcName, dc, serverRefs, aclDeps, apiEnv(token, tls)); err != nil {
				return err
			}
		}

		clientOff := 100
		for _, nodeName := range slices.Sorted(maps.Keys(dc.Nodes)) {
			node := dc.Nodes[nodeName]
			if node == nil {
				node = &spec.Node{}
			}
			count := max(node.Count, 1)
			for k := range count {
				name := nodeName
				if node.Count > 1 {
					name = nodeName + "-" + strconv.Itoa(k+1)
				}
				ip := ipAt(clientOff)
				clientOff++
				key := dcName + "-" + name
				rn := &spec.RawNode{
					Networks:  map[string]*spec.Attachment{lan: {IP: ip}},
					Files:     map[string]string{},
					Processes: map[string]*spec.Process{},
				}
				for pn, p := range node.Processes {
					rn.Processes[pn] = p
				}

				services := node.Services
				if node.Service != nil {
					services = map[string]*spec.Service{nodeName: node.Service}
				}
				var regs []any
				for _, svcName := range slices.Sorted(maps.Keys(services)) {
					svc := mergeService(defaults.ServiceDefaults, dc.ServiceDefaults, services[svcName])
					reg, proc := expandService(svcName, svc, check)
					if acl {
						reg["token"] = sec.agent // registers and syncs the service
					}
					regs = append(regs, reg)
					if proc != nil {
						rn.Processes[svcName] = proc
					}
				}

				if node.Agent != "none" {
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
						"log_level":            logLevel(defaults, dc, true),
						"disable_update_check": true,
					}
					if len(node.Meta) > 0 {
						cfg["node_meta"] = node.Meta
					}
					if len(regs) > 0 {
						cfg["services"] = regs
					}
					if acl {
						cfg["acl"] = map[string]any{"enabled": true, "default_policy": "deny", "enable_token_persistence": true,
							"tokens": map[string]any{"agent": sec.agent}}
						cfg["encrypt"] = sec.gossip
					}
					if tls {
						labCA.clientTLS(cfg, rn.Files)
					}
					deepMerge(cfg, defaults.Agents.Cfg(), dc.Agents.Cfg(), defaults.Client.Cfg(), dc.Client.Cfg(), node.Config)
					rn.Files["consul.json"] = mustJSON(cfg)
					deps := map[string]*spec.Dep{}
					for _, r := range serverRefs {
						deps[r] = &spec.Dep{Condition: "ready"}
					}
					maps.Copy(deps, aclDeps)
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
				out.Nodes[key] = rn
			}
		}
	}
	return nil
}

// expandService returns the agent service registration and, when the
// service has a run: command, the process that runs it.
func expandService(name string, svc *spec.Service, agent spec.ConsulCheck) (map[string]any, *spec.Process) {
	reg := map[string]any{"name": name}
	if svc.Port > 0 {
		reg["port"] = svc.Port
	}
	check := svc.Check
	if check == "" {
		check = "none"
		if len(svc.Run) > 0 && svc.Port > 0 {
			check = "http"
		}
	}
	switch check {
	case "http":
		reg["check"] = map[string]any{"http": fmt.Sprintf("http://127.0.0.1:%d/", svc.Port), "interval": "2s"}
	case "tcp":
		reg["check"] = map[string]any{"tcp": fmt.Sprintf("127.0.0.1:%d", svc.Port), "interval": "2s"}
	}
	deepMerge(reg, svc.Register)

	if len(svc.Run) == 0 {
		return reg, nil
	}
	run := svc.Run
	if len(run) == 1 && run[0] == "fake-service" {
		run = spec.Command{VarParker, "fake-service", "--name", name, "--port", strconv.Itoa(svc.Port)}
	}
	p := &spec.Process{Run: run, Env: svc.Env}
	if svc.Port > 0 {
		p.ReadyWhen = &spec.Ready{Port: svc.Port, Timeout: "30s"}
		if check != "none" {
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
}

func expandConfigEntries(srv *spec.RawNode, dcName string, dc *spec.Datacenter, serverRefs []string, aclDeps map[string]*spec.Dep, env map[string]string) error {
	var refs []EntryRef
	for i, e := range dc.ConfigEntries {
		var name, body string
		if i < len(dc.EntrySources) && dc.EntrySources[i].HCL != "" {
			name, body = fmt.Sprintf("config-entry-%d.hcl", i), dc.EntrySources[i].HCL
		} else {
			b, err := json.MarshalIndent(e, "", "  ")
			if err != nil {
				return fmt.Errorf("%s: %w", dc.EntryLabel(dcName, i), err)
			}
			name, body = fmt.Sprintf("config-entry-%d.json", i), string(b)
		}
		srv.Files[name] = body
		refs = append(refs, EntryRef{File: VarNodeDir + "/" + name, Label: dc.EntryLabel(dcName, i)})
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
