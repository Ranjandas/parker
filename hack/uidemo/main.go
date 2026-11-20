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
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/api/parkerv1/parkerv1connect"
	"github.com/Ranjandas/parker/internal/daemon"
	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/graph"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/webui"
)

type fake struct {
	parkerv1connect.UnimplementedParkerServiceHandler
	mu       sync.Mutex
	broken   bool
	examples string
}

// labExamples are the examples the demo labs are expanded from.
var labExamples = map[string]string{"meshfed": "mesh-federation.yaml", "minimal": "minimal.yaml", "partitions": "partitions.yaml", "gateways": "gateways.yaml"}

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
		{Name: "meshfed", Namespaces: 6, Supervised: true}, {Name: "gateways"}, {Name: "minimal", Namespaces: 3, Supervised: true}, {Name: "partitions"}}}), nil
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

func main() {
	addr := flag.String("listen", "127.0.0.1:8090", "address")
	token := flag.String("token", "demo", "sign-in token")
	examples := flag.String("examples", "examples", "the repository's examples directory")
	flag.Parse()
	_, api := parkerv1connect.NewParkerServiceHandler(&fake{broken: true, examples: *examples})
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("open http://%s/#token=%s\n", ln.Addr(), *token)
	srv := &http.Server{Handler: webui.New(webui.Options{API: api, Token: *token, Where: "demo (fake daemon)", Version: "demo"}), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}
