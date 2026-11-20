package expand

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDataplane(t *testing.T) {
	low, err := Expand(load(t, "../../examples/dataplane.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	api := low.Nodes["dc1-api"]
	if api.Processes["consul"] != nil || api.Labels.Role != DataplaneRole {
		t.Fatalf("api: processes %v, role %s", api.Processes, api.Labels.Role)
	}
	if p := api.Processes["api"]; p.ReadyWhen != nil && p.ReadyWhen.Consul != nil {
		t.Error("api's service waits on an agent it doesn't have")
	}
	dp := api.Processes["api"+SidecarSuffix]
	run := strings.Join(dp.Run, " ")
	for _, want := range []string{"consul-dataplane -addresses exec=echo 10.1.0.10 10.1.0.11 10.1.0.12", "-proxy-id api-sidecar-proxy",
		"-proxy-node-name api", "-envoy-admin-bind-port 19000", "-graceful-port 20300", "-grpc-port 8502 -tls-disabled"} {
		if !strings.Contains(run, want) {
			t.Errorf("run %q lacks %q", run, want)
		}
	}
	if strings.Contains(run, "-static-token") || strings.Contains(run, "-proxy-partition") {
		t.Errorf("run %q: no ACLs or partitions here", run)
	}
	if dp.DependsOn["dc1-server-1/"+DataplaneHealth].Condition != "ready" || dp.ReadyWhen.HTTP.URL != "http://127.0.0.1:19000/ready" {
		t.Errorf("sidecar = %+v", dp)
	}
	// Servers open gRPC for the dataplanes; the agent node web is unchanged.
	if p := agentConfig(t, low.Nodes["dc1-server-1"])["ports"].(map[string]any); p["grpc"] != float64(GRPCPort) {
		t.Errorf("server ports = %v", p)
	}
	if low.Nodes["dc1-web"].Processes["consul"] == nil {
		t.Error("web lost its agent")
	}

	s1 := low.Nodes["dc1-server-1"]
	hp := s1.Processes[DataplaneHealth]
	if hp == nil || strings.Join(hp.Run, " ") != VarParker+" "+DataplaneHealth+" "+VarNodeDir+"/"+DataplaneManifest || hp.ReadyWhen.Log != DataplaneHealthReadyLine {
		t.Fatalf("health process = %+v", hp)
	}
	for _, s := range []string{"dc1-server-1", "dc1-server-2", "dc1-server-3"} {
		if hp.DependsOn[s+"/consul"] == nil {
			t.Errorf("health process doesn't wait for %s", s)
		}
	}
	var entries []DataplaneEntry
	if err := json.Unmarshal([]byte(s1.Files[DataplaneManifest]), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	svc, proxy := entries[0], entries[1]
	if svc.Node != "api" || svc.Datacenter != "dc1" || svc.Service.ID != "api" || svc.Service.Port != 9090 || svc.Probe != "http://"+svc.Address+":9090/health" {
		t.Errorf("service entry = %+v", svc)
	}
	p := proxy.Service.Proxy
	if proxy.Service.Kind != "connect-proxy" || proxy.Service.ID != "api-sidecar-proxy" || proxy.Service.Port != SidecarPort ||
		p.DestinationServiceName != "api" || p.LocalServicePort != 9090 || len(p.Upstreams) != 1 || p.Upstreams[0].DestinationName != "web" || p.Upstreams[0].LocalBindPort != 9092 {
		t.Errorf("proxy entry = %+v (%+v)", proxy, p)
	}
}

func TestDataplaneSecure(t *testing.T) {
	low, err := Expand(load(t, "../../hack/e2e/dataplane-secure.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	api := low.Nodes["dc1-api"]
	run := strings.Join(api.Processes["api"+SidecarSuffix].Run, " ")
	for _, want := range []string{"-grpc-port 8503 -ca-certs " + VarNodeDir + "/" + CAFile + " -tls-server-name server.dc1.consul", "-credential-type static -static-token "} {
		if !strings.Contains(run, want) {
			t.Errorf("run %q lacks %q", run, want)
		}
	}
	if api.Files[CAFile] == "" {
		t.Error("api has no CA to verify the servers")
	}
	cfg := agentConfig(t, low.Nodes["dc1-server-1"])
	if g := cfg["tls"].(map[string]any)["grpc"]; g == nil || g.(map[string]any)["verify_incoming"] != false {
		t.Errorf("servers' gRPC asks dataplanes for a client cert: %v", g)
	}
	if p := cfg["ports"].(map[string]any); p["grpc_tls"] != float64(GRPCTLSPort) {
		t.Errorf("server ports = %v", p)
	}
	if hp := low.Nodes["dc1-server-1"].Processes[DataplaneHealth]; hp.Env["CONSUL_HTTP_TOKEN"] == "" || hp.Env["CONSUL_CACERT"] == "" {
		t.Errorf("health env = %v", hp.Env)
	}
}

func TestDataplaneRefusesTproxy(t *testing.T) {
	f := load(t, "../../examples/dataplane.yaml")
	f.Consul.Datacenters["dc1"].Nodes["api"].Service.Connect = "tproxy"
	if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), "dataplane") {
		t.Errorf("tproxy on a dataplane: %v", err)
	}
}
