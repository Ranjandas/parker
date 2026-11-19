// Package expand turns the high-level `consul:` section into the low-level
// layer of networks, raw nodes and processes. The engine only runs the
// low-level form, so `parker expand` output is itself a valid input.
package expand

import (
	"encoding/json"
	"fmt"
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
		if err := expandConsul(in.Consul, out); err != nil {
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
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("expanded topology: %w", err)
	}
	return out, nil
}

func expandConsul(c *spec.Consul, out *spec.File) error {
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

	for i, dcName := range dcNames {
		dc := c.Datacenters[dcName]
		subnet := dc.Subnet
		if subnet == "" {
			subnet = fmt.Sprintf("10.%d.0.0/24", i+1)
		}
		pfx, err := netip.ParsePrefix(subnet)
		if err != nil {
			return fmt.Errorf("consul.datacenters.%s.subnet: %w", dcName, err)
		}
		base := pfx.Masked().Addr()
		ipAt := func(off int) string {
			a := base
			for range off {
				a = a.Next()
			}
			return a.String()
		}

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
				"primary_datacenter":   primary,
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
			deepMerge(cfg, defaults.Agents.Cfg(), dc.Agents.Cfg(), defaults.Server.Cfg(), dc.Server.Cfg())
			ov := dc.Servers.Overrides[name]
			version := ""
			if ov != nil {
				deepMerge(cfg, ov.Config)
				version = ov.Version
			}
			key := dcName + "-" + name
			out.Nodes[key] = &spec.RawNode{
				Networks: map[string]*spec.Attachment{lan: {IP: ip}},
				Files:    map[string]string{"consul.json": mustJSON(cfg)},
				Processes: map[string]*spec.Process{
					"consul": {
						Run:     spec.Command{"consul", "agent", "-config-file=" + VarNodeDir + "/consul.json"},
						Version: version,
						ReadyWhen: &spec.Ready{
							Consul:  &spec.ConsulCheck{Leader: true},
							Timeout: "90s",
						},
					},
				},
			}
			serverRefs = append(serverRefs, key+"/consul")
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
					reg, proc := expandService(svcName, svc)
					regs = append(regs, reg)
					if proc != nil {
						rn.Processes[svcName] = proc
					}
				}

				if node.Agent != "none" {
					cfg := map[string]any{
						"node_name":            name,
						"datacenter":           dcName,
						"primary_datacenter":   primary,
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
					deepMerge(cfg, defaults.Agents.Cfg(), dc.Agents.Cfg(), defaults.Client.Cfg(), dc.Client.Cfg(), node.Config)
					rn.Files["consul.json"] = mustJSON(cfg)
					deps := map[string]*spec.Dep{}
					for _, r := range serverRefs {
						deps[r] = &spec.Dep{Condition: "ready"}
					}
					rn.Processes["consul"] = &spec.Process{
						Run:       spec.Command{"consul", "agent", "-config-file=" + VarNodeDir + "/consul.json"},
						DependsOn: deps,
						ReadyWhen: &spec.Ready{
							Consul:  &spec.ConsulCheck{Members: ">=" + strconv.Itoa(n+1)},
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
func expandService(name string, svc *spec.Service) (map[string]any, *spec.Process) {
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
			p.ReadyWhen.Consul = &spec.ConsulCheck{ServiceHealthy: name}
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
