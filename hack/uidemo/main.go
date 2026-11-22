// Command uidemo serves the web UI against a fake daemon that reports a
// canned meshfed lab (one sidecar failing its health check), for working
// on the UI and taking screenshots without root or a real lab:
//
//	go run ./hack/uidemo            # then open the printed link
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/api/parkerv1/parkerv1connect"
	"github.com/Ranjandas/parker/internal/daemon"
	"github.com/Ranjandas/parker/internal/expand"
	parkerfake "github.com/Ranjandas/parker/internal/fake"
	"github.com/Ranjandas/parker/internal/graph"
	"github.com/Ranjandas/parker/internal/lab"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/webui"
)

type fake struct {
	parkerv1connect.UnimplementedParkerServiceHandler
	mu       sync.Mutex
	broken   bool
	examples string
	faults   map[string]parkerfake.Faults // node/service -> faults set with the Fake call
}

// labExamples are the examples the demo labs are expanded from.
var labExamples = map[string]string{"tgw-failover": "terminating-failover.yaml", "tgw-failover-down": "terminating-failover.yaml", "meshfed": "mesh-federation.yaml", "minimal": "minimal.yaml", "partitions": "partitions.yaml", "gateways": "gateways.yaml", "tgw-failover-ent": "terminating-failover-partitions.yaml", "peering-partitions": "peering-partitions.yaml", "fake-chain": "fake-chain.yaml"}

func ptr[T any](v T) *T { return &v }

func proc(name string, pid int32, ready bool, restarts int32) *parkerv1.ProcessStatus {
	return &parkerv1.ProcessStatus{Name: name, Pid: pid, Alive: true, Ready: ptr(ready), Restarts: restarts,
		LogPath: "/var/lib/parker/run/meshfed/nodes/x/" + name + ".log"}
}

func (f *fake) status() *parkerv1.StatusResponse {
	f.mu.Lock()
	broken := f.broken
	f.mu.Unlock()
	apiHealth, sidecarRestarts := "passing", int32(0)
	if broken {
		apiHealth, sidecarRestarts = "critical", 2
	}
	return &parkerv1.StatusResponse{Lab: "meshfed", RunDir: "/var/lib/parker/run/meshfed", Supervised: true, Nodes: []*parkerv1.NodeStatus{
		{Name: "dc1-server-1", Ips: []string{"10.1.0.10", "10.255.0.10"}, Namespace: true, Role: "leader", Dc: "dc1", MembersAlive: 3, MembersTotal: 3,
			Processes: []*parkerv1.ProcessStatus{proc("consul", 4753, true, 0), proc("config-entries", 4767, true, 0)}},
		{Name: "dc1-mesh", Ips: []string{"10.1.0.100", "10.255.0.100"}, Namespace: true, Role: "client", Dc: "dc1", MembersAlive: 3, MembersTotal: 3,
			Health:    map[string]string{"mesh-gateway": "passing"},
			Processes: []*parkerv1.ProcessStatus{proc("consul", 4769, true, 0), proc("mesh-gateway", 4809, true, 0)}},
		{Name: "dc1-web", Ips: []string{"10.1.0.101"}, Namespace: true, Role: "client", Dc: "dc1", MembersAlive: 3, MembersTotal: 3,
			Health:    map[string]string{"web": "passing", "web-sidecar-proxy": "passing"},
			Processes: []*parkerv1.ProcessStatus{proc("consul", 4768, true, 0), proc("web", 4770, true, 0), proc("web-sidecar", 4808, true, 0)}},
		{Name: "dc2-server-1", Ips: []string{"10.2.0.10", "10.255.0.20"}, Namespace: true, Role: "leader", Dc: "dc2", MembersAlive: 3, MembersTotal: 3,
			Processes: []*parkerv1.ProcessStatus{proc("consul", 4862, true, 0)}},
		{Name: "dc2-api", Ips: []string{"10.2.0.100"}, Namespace: true, Role: "client", Dc: "dc2", MembersAlive: 3, MembersTotal: 3,
			Health:    map[string]string{"api": "passing", "api-sidecar-proxy": apiHealth},
			Processes: []*parkerv1.ProcessStatus{proc("consul", 4889, true, 0), proc("api", 4890, true, 0), proc("api-sidecar", 5121, true, sidecarRestarts)}},
		{Name: "dc2-mesh", Ips: []string{"10.2.0.101", "10.255.0.101"}, Namespace: true, Role: "client", Dc: "dc2", MembersAlive: 3, MembersTotal: 3,
			Health:    map[string]string{"mesh-gateway": "passing"},
			Processes: []*parkerv1.ProcessStatus{proc("consul", 4863, true, 0), proc("mesh-gateway", 4937, true, 0)}},
	}}
}

func (f *fake) Version(context.Context, *connect.Request[parkerv1.VersionRequest]) (*connect.Response[parkerv1.VersionResponse], error) {
	return connect.NewResponse(&parkerv1.VersionResponse{Version: "demo"}), nil
}

func (f *fake) ListLabs(context.Context, *connect.Request[parkerv1.ListLabsRequest]) (*connect.Response[parkerv1.ListLabsResponse], error) {
	return connect.NewResponse(&parkerv1.ListLabsResponse{Labs: []*parkerv1.LabSummary{
		{Name: "meshfed", Namespaces: 6, Supervised: true}, {Name: "tgw-failover", Namespaces: 12, Supervised: true}, {Name: "tgw-failover-down", Namespaces: 12, Supervised: true}, {Name: "gateways"}, {Name: "minimal", Namespaces: 3, Supervised: true}, {Name: "partitions"}, {Name: "tgw-failover-ent", Namespaces: 14, Supervised: true}, {Name: "peering-partitions", Namespaces: 8, Supervised: true}, {Name: "fake-chain", Namespaces: 9, Supervised: true}}}), nil
}

func (f *fake) Status(_ context.Context, r *connect.Request[parkerv1.StatusRequest]) (*connect.Response[parkerv1.StatusResponse], error) {
	switch r.Msg.Lab {
	case "meshfed":
		return connect.NewResponse(f.status()), nil
	case "minimal":
		st := &parkerv1.StatusResponse{Lab: "minimal", Supervised: true}
		for i, role := range []string{"leader", "follower", "follower"} {
			st.Nodes = append(st.Nodes, &parkerv1.NodeStatus{Name: fmt.Sprintf("dc1-server-%d", i+1), Ips: []string{fmt.Sprintf("10.1.0.%d", 10+i)},
				Namespace: true, Role: role, Dc: "dc1", MembersAlive: 3, MembersTotal: 3, Processes: []*parkerv1.ProcessStatus{proc("consul", int32(3100+i), true, 0)}})
		}
		return connect.NewResponse(st), nil
	case "tgw-failover", "tgw-failover-ent", "peering-partitions", "fake-chain":
		return f.allUp(r.Msg.Lab, "")
	case "tgw-failover-down":
		// dc1's terminating gateway is stopped: web's calls fail over.
		return f.allUp(r.Msg.Lab, "dc1-terminating")
	case "partitions", "gateways":
		return connect.NewResponse(&parkerv1.StatusResponse{Lab: r.Msg.Lab}), nil
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no lab %q", r.Msg.Lab))
}

const sidecarLog = `[2026-11-20 07:16:41.902][41][info][main] initializing epoch 0 (base id=0, hot restart version=11.104)
[2026-11-20 07:16:41.958][41][info][main] admin address: 127.0.0.1:19000
[2026-11-20 07:16:41.964][41][info][upstream] cds: add 1 cluster(s), remove 0 cluster(s)
[2026-11-20 07:16:41.970][41][warning][config] StreamAggregatedResources gRPC config stream to local_agent closed: 14, upstream connect error or disconnect/reset before headers. reset reason: connection failure, transport failure reason: TLS_error: CERTIFICATE_VERIFY_FAILED
[2026-11-20 07:16:44.215][41][warning][config] gRPC config stream to local_agent closed since 2245ms ago: 14, TLS_error: CERTIFICATE_VERIFY_FAILED
[2026-11-20 07:16:44.215][41][error][main] listener public_listener:10.2.0.100:21000 not created: no xDS config after 2s
[2026-11-20 07:16:44.216][41][error][main] exiting: initial fetch timeout for type.googleapis.com/envoy.config.listener.v3.Listener
parker: api-sidecar exited (code 1) after 2.3s, restart 2 in 1s
parker: starting api-sidecar: consul connect envoy -sidecar-for api -admin-bind 127.0.0.1:19000
[2026-11-20 07:16:45.312][52][info][main] initializing epoch 0 (base id=0, hot restart version=11.104)
[2026-11-20 07:16:45.379][52][warning][config] StreamAggregatedResources gRPC config stream to local_agent closed: 14, TLS_error: CERTIFICATE_VERIFY_FAILED
`

func (f *fake) Logs(ctx context.Context, r *connect.Request[parkerv1.LogsRequest], s *connect.ServerStream[parkerv1.LogChunk]) error {
	text := fmt.Sprintf("2026-11-20T05:05:12.880Z [INFO]  %s: started on %s\n", r.Msg.Process, r.Msg.Node)
	if strings.HasSuffix(r.Msg.Process, "sidecar") {
		text += sidecarLog
	}
	if err := s.Send(&parkerv1.LogChunk{Data: []byte(text)}); err != nil {
		return err
	}
	if !r.Msg.Follow {
		return nil
	}
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			line := fmt.Sprintf("2026-11-20T05:06:%02d.000Z [DEBUG] agent: Check status updated: check=%s-%d\n", i%60, r.Msg.Process, i)
			if err := s.Send(&parkerv1.LogChunk{Data: []byte(line)}); err != nil {
				return err
			}
		}
	}
}

func (f *fake) Describe(_ context.Context, r *connect.Request[parkerv1.DescribeRequest]) (*connect.Response[parkerv1.DescribeResponse], error) {
	return connect.NewResponse(&parkerv1.DescribeResponse{Node: r.Msg.Node, Yaml: "name: " + r.Msg.Node + "\nnetworks: [dc1]\nprocesses:\n  consul:\n    command: [consul, agent, -config-dir, /etc/consul]\n"}), nil
}

// allUp reports every node of a lab's example running and healthy, except
// down, whose processes are stopped.
func (f *fake) allUp(lab, down string) (*connect.Response[parkerv1.StatusResponse], error) {
	in, err := spec.Load(filepath.Join(f.examples, labExamples[lab]), nil)
	if err != nil {
		return nil, err
	}
	low, err := expand.Expand(in)
	if err != nil {
		return nil, err
	}
	st := &parkerv1.StatusResponse{Lab: lab, Supervised: true}
	pid := int32(5000)
	for _, name := range slices.Sorted(maps.Keys(low.Nodes)) {
		rn := low.Nodes[name]
		ns := &parkerv1.NodeStatus{Name: name, Namespace: true, Health: map[string]string{}}
		for _, net := range slices.Sorted(maps.Keys(rn.Networks)) {
			ns.Ips = append(ns.Ips, rn.Networks[net].IP)
		}
		if rn.Labels != nil {
			ns.Dc, ns.Role = rn.Labels.DC, rn.Labels.Role
			if ns.Role == "server" {
				ns.Role = "follower"
				if strings.HasSuffix(name, "-server-1") {
					ns.Role = "leader"
				}
			}
		}
		for _, pn := range slices.Sorted(maps.Keys(rn.Processes)) {
			pid++
			p := proc(pn, pid, true, 0)
			if name == down {
				p = &parkerv1.ProcessStatus{Name: pn, Stopped: true}
			}
			ns.Processes = append(ns.Processes, p)
		}
		st.Nodes = append(st.Nodes, ns)
	}
	return connect.NewResponse(st), nil
}

// Graph expands the lab's example for real, so the topology view shows what
// the daemon would.
func (f *fake) Graph(_ context.Context, r *connect.Request[parkerv1.GraphRequest]) (*connect.Response[parkerv1.GraphResponse], error) {
	ex, ok := labExamples[r.Msg.Lab]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no lab %q", r.Msg.Lab))
	}
	in, err := spec.Load(filepath.Join(f.examples, ex), nil)
	if err != nil {
		return nil, err
	}
	low, err := expand.Expand(in)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(daemon.GraphToProto(r.Msg.Lab, graph.Build(low))), nil
}

func (f *fake) Validate(_ context.Context, r *connect.Request[parkerv1.ValidateRequest]) (*connect.Response[parkerv1.ValidateResponse], error) {
	return connect.NewResponse(daemon.ValidateYAML(r.Msg.Yaml)), nil
}

func (f *fake) Run(_ context.Context, r *connect.Request[parkerv1.RunRequest], s *connect.ServerStream[parkerv1.LogLine]) error {
	var sc struct {
		Steps []map[string]any `json:"steps"`
	}
	if r.Msg.Inline != "" {
		_ = json.Unmarshal([]byte(r.Msg.Inline), &sc)
		for _, st := range sc.Steps {
			if st["restart"] == "dc2-api" {
				f.mu.Lock()
				f.broken = false
				f.mu.Unlock()
			}
			if ll, ok := st["log_level"].(map[string]any); ok {
				lvl, _ := ll["level"].(string)
				if lvl == "" {
					lvl = "info"
				}
				_ = s.Send(&parkerv1.LogLine{Line: fmt.Sprintf("[  0.1s]   %v/%v: %s", ll["on"], ll["process"], lvl)})
				continue
			}
			b, _ := json.Marshal(st)
			_ = s.Send(&parkerv1.LogLine{Line: "fault " + string(b) + ": ok"})
		}
		return nil
	}
	for _, l := range []string{"step 1/3 wait: dc1 ready ... ok (5.6s)", "step 2/3 wait: dc2 ready ... ok (0.2s)", "step 3/3 web in dc1 reaches api in dc2 ... ok (2.1s)", "scenario passed in 7.9s"} {
		time.Sleep(300 * time.Millisecond)
		if err := s.Send(&parkerv1.LogLine{Line: l}); err != nil {
			return err
		}
	}
	return nil
}

func (f *fake) Down(context.Context, *connect.Request[parkerv1.DownRequest], *connect.ServerStream[parkerv1.LogLine]) error {
	return nil
}

// fakeApp stands in for the daemon's app proxy: services answer like
// Parker's fake-service, anything under /ui/ like a Consul UI page.
func fakeApp(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, daemon.AppPrefix), "/", 5)
	if len(parts) < 4 {
		http.Error(w, "bad app path", http.StatusBadRequest)
		return
	}
	labName, node, port, rest := parts[0], parts[1], parts[3], "/"
	if len(parts) == 5 {
		rest += parts[4]
	}
	if strings.HasPrefix(rest, "/ui/") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>Consul by HashiCorp</title><body style="margin:0;font:14px system-ui;background:#f5f6f7">
<div style="background:#000;color:#fff;padding:12px 20px;font-weight:600">Consul · %s</div>
<div style="padding:24px"><h2 style="margin:0 0 8px">Services</h2><p style="color:#666">%s:%s in lab %s, through the Parker daemon (demo page: a real daemon proxies the agent's own UI here).</p>
<table style="border-collapse:collapse;background:#fff;min-width:420px"><tr><th align=left style="padding:8px 12px">Service</th><th align=left style="padding:8px 12px">Health</th></tr>
<tr><td style="padding:8px 12px">consul</td><td style="color:#16803c">passing</td></tr><tr><td style="padding:8px 12px">web</td><td style="color:#16803c">passing</td></tr>
<tr><td style="padding:8px 12px">api</td><td style="color:#d92d20">1 critical</td></tr></table></div>`, node, node, port, labName)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": strings.TrimPrefix(node, "dc1-"), "node": node, "message": "hello from " + node, "path": rest, "from": "10.1.0.100:51432", "time": time.Now().UTC()})
}

// fakeUp stands in for bringing a lab up: progress, and no lab.
func fakeUp(ctx context.Context, low *spec.File, w io.Writer) error {
	fmt.Fprintf(w, "lab %s: %d nodes, %d networks (demo: nothing starts)\n", low.Name, len(low.Nodes), len(low.Networks))
	for _, n := range slices.Sorted(maps.Keys(low.Nodes)) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(120 * time.Millisecond):
		}
		fmt.Fprintf(w, "  %s: ready\n", n)
	}
	return nil
}

func main() {
	addr := flag.String("listen", "127.0.0.1:8090", "address")
	token := flag.String("token", "demo", "sign-in token")
	examples := flag.String("examples", "examples", "the repository's examples directory")
	flag.Parse()
	api := http.NewServeMux()
	api.Handle(parkerv1connect.NewParkerServiceHandler(&fake{broken: true, examples: *examples, faults: map[string]parkerfake.Faults{}}))
	api.HandleFunc(daemon.AppPrefix, fakeApp)
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("open http://%s/#token=%s\n", ln.Addr(), *token)
	srv := &http.Server{Handler: webui.New(webui.Options{API: api, Token: *token, Where: "demo (fake daemon)", Version: "demo", Up: fakeUp}), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}

// Fake lists the lab's fake services with the faults set on them, changes
// them, and answers a call by walking the graph's upstream edges like the
// real fakes would: stored faults fail every request (an error rate of
// 50% fails it too, so screenshots are steady) and X-Parker-Fault breaks
// named hops.
func (f *fake) Fake(_ context.Context, r *connect.Request[parkerv1.FakeRequest]) (*connect.Response[parkerv1.FakeResponse], error) {
	m := r.Msg
	ex, ok := labExamples[m.Lab]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no lab %q", m.Lab))
	}
	in, err := spec.Load(filepath.Join(f.examples, ex), nil)
	if err != nil {
		return nil, err
	}
	low, err := expand.Expand(in)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	nodes := slices.Sorted(maps.Keys(low.Nodes))
	if m.Selector != "" {
		nodes = []string{m.Selector}
	}
	out := &parkerv1.FakeResponse{}
	if m.Action == "call" {
		g := graph.Build(low)
		req := map[string]string{}
		for _, hop := range strings.Split(m.Headers[parkerfake.HeaderFault], ";") {
			if name, list, ok := strings.Cut(hop, "="); ok {
				req[name] = list
			}
		}
		var hop func(node, svc string, start time.Time, depth int) map[string]any
		hop = func(node, svc string, start time.Time, depth int) map[string]any {
			d := 2 * time.Millisecond
			code, injected := 200, ""
			fl, ok := f.faults[node+"/"+svc]
			if !ok {
				fl = parkerfake.DefaultFaults()
			}
			d += fl.P50
			if fl.ErrorRate >= 0.5 {
				code, injected = fl.ErrorCode, "error"
			}
			for _, kv := range strings.Split(req[svc], ",") {
				k, v, _ := strings.Cut(kv, ":")
				switch k {
				case "error-rate":
					if v != "0" {
						code, injected = 503, "error"
					}
				case "delay":
					dd, _ := time.ParseDuration(v)
					d += dd
				}
			}
			resp := map[string]any{"name": svc, "node": node, "dc": strings.SplitN(node, "-", 2)[0], "uri": "/", "type": "HTTP", "start_time": start.Format(time.RFC3339Nano)}
			if injected != "" {
				resp["injected"], resp["error"] = injected, "error injected"
			} else if depth < 8 {
				calls := map[string]any{}
				var end time.Time
				for _, e := range g.Edges {
					if e.Kind != graph.Upstream || e.From != node {
						continue
					}
					up := strings.Fields(e.Label)[0]
					c := hop(e.To, up, start.Add(time.Millisecond), depth+1)
					calls[up] = c
					if c["code"].(int) >= 400 {
						code, resp["error"] = 500, "upstream "+up+" failed"
					}
					if t := c["end"].(time.Time); t.After(end) {
						end = t
					}
				}
				if len(calls) > 0 {
					resp["upstream_calls"] = calls
					if dd := end.Sub(start) + time.Millisecond; dd > d {
						d = dd
					}
				}
			}
			resp["code"], resp["duration"], resp["end"] = code, d.String(), start.Add(d)
			return resp
		}
		svc := m.Service
		if svc == "" {
			if fis := lab.FakeInfos(low, nodes[0]); len(fis) > 0 {
				svc = fis[0].Service
			}
		}
		res := hop(nodes[0], svc, time.Now(), 0)
		res["fact"] = "At its closest approach, Parker Solar Probe moves at about 430,000 mph (692,000 km/h)."
		var clean func(map[string]any)
		clean = func(x map[string]any) {
			delete(x, "end")
			if c, ok := x["upstream_calls"].(map[string]any); ok {
				for _, v := range c {
					clean(v.(map[string]any))
				}
			}
		}
		clean(res)
		b, _ := json.Marshal(res)
		out.CallStatus, out.CallBody = int32(res["code"].(int)), string(b)
		return connect.NewResponse(out), nil
	}
	for _, n := range nodes {
		for _, fi := range lab.FakeInfos(low, n) {
			if m.Service != "" && fi.Service != m.Service {
				continue
			}
			key := n + "/" + fi.Service
			fl, ok := f.faults[key]
			if !ok || m.Action == "reset" {
				fl = parkerfake.DefaultFaults()
			}
			if m.Action == "set" {
				var ch spec.Fake
				if err := json.Unmarshal([]byte(m.ChangeJson), &ch); err != nil {
					return nil, connect.NewError(connect.CodeInvalidArgument, err)
				}
				if err := fl.Apply(&ch); err != nil {
					return nil, connect.NewError(connect.CodeInvalidArgument, err)
				}
			}
			f.faults[key] = fl
			fj, _ := json.Marshal(fl)
			out.Services = append(out.Services, &parkerv1.FakeService{Node: n, Service: fi.Service, Port: int32(fi.Port), Protocol: fi.Protocol,
				FaultsJson: string(fj), Summary: fl.Summary(), Faulted: fl.Injected()})
		}
	}
	return connect.NewResponse(out), nil
}
