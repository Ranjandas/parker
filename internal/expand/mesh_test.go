package expand

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestMeshSidecars(t *testing.T) {
	low, err := Expand(load(t, "../../examples/mesh.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c := agentConfig(t, low.Nodes["dc1-server-2"])["connect"]; !reflect.DeepEqual(c, map[string]any{"enabled": true}) {
		t.Errorf("servers: connect = %v", c)
	}
	web := low.Nodes["dc1-web"]
	cfg := agentConfig(t, web)
	if p := cfg["ports"].(map[string]any); p["grpc"] != float64(GRPCPort) {
		t.Errorf("client ports = %v", p)
	}
	reg := cfg["services"].([]any)[0].(map[string]any)
	want := map[string]any{"sidecar_service": map[string]any{"port": float64(SidecarPort), "proxy": map[string]any{
		"upstreams": []any{map[string]any{"destination_name": "api", "local_bind_port": float64(9091)}},
	}}}
	if !reflect.DeepEqual(reg["connect"], want) {
		t.Errorf("web connect = %v", reg["connect"])
	}
	// api has a sidecar but no upstreams.
	apiReg := agentConfig(t, low.Nodes["dc1-api"])["services"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(apiReg["connect"], map[string]any{"sidecar_service": map[string]any{"port": float64(SidecarPort), "proxy": map[string]any{}}}) {
		t.Errorf("api connect = %v", apiReg["connect"])
	}

	sc := web.Processes["web"+SidecarSuffix]
	if sc == nil {
		t.Fatalf("no sidecar process: %v", slices.Sorted(maps.Keys(web.Processes)))
	}
	if got := strings.Join(sc.Run, " "); got != "consul connect envoy -sidecar-for web -admin-bind 127.0.0.1:19000 -- --log-level info" {
		t.Errorf("run = %s", got)
	}
	if !slices.Equal(sc.Uses, []string{"envoy"}) || sc.Env["CONSUL_GRPC_ADDR"] != "127.0.0.1:8502" ||
		sc.DependsOn["dc1-web/consul"].Condition != "ready" || sc.ReadyWhen.HTTP.URL != "http://127.0.0.1:19000/ready" {
		t.Errorf("sidecar = %+v", sc)
	}
	if low.Versions["envoy"] != "1.35.12" {
		t.Error("envoy pin not carried")
	}
}

func unmarshalStrict(s string, v any) error {
	return yaml.UnmarshalWithOptions([]byte(s), v, yaml.Strict())
}

func TestMeshSecure(t *testing.T) {
	low, err := Expand(load(t, "../../hack/e2e/mesh-secure.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	web := low.Nodes["dc1-web"]
	p := agentConfig(t, web)["ports"].(map[string]any)
	if p["grpc_tls"] != float64(GRPCTLSPort) || p["https"] != float64(HTTPSPort) || p["grpc"] != nil {
		t.Errorf("ports = %v", p)
	}
	env := web.Processes["web"+SidecarSuffix].Env
	if env["CONSUL_GRPC_ADDR"] != "https://127.0.0.1:8503" || env["CONSUL_CACERT"] == "" || env["CONSUL_HTTP_TOKEN"] == "" ||
		!strings.HasPrefix(env["CONSUL_HTTP_ADDR"], "https://") {
		t.Errorf("env = %v", env)
	}
}

func TestMeshTwoSidecarsOnANode(t *testing.T) {
	f := load(t, "../../examples/mesh.yaml")
	web := f.Consul.Datacenters["dc1"].Nodes["web"]
	web.Services = map[string]*spec.Service{"web": web.Service, "admin": {Port: 8081}}
	web.Service = nil
	low, err := Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	ps := low.Nodes["dc1-web"].Processes
	if ps["admin"+SidecarSuffix].ReadyWhen.HTTP.URL != "http://127.0.0.1:19000/ready" || ps["web"+SidecarSuffix].ReadyWhen.HTTP.URL != "http://127.0.0.1:19001/ready" {
		t.Errorf("admin ports: %s %s", ps["admin"+SidecarSuffix].ReadyWhen.HTTP.URL, ps["web"+SidecarSuffix].ReadyWhen.HTTP.URL)
	}
}

func TestMeshErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*spec.File)
		want string
	}{
		{"no envoy", func(f *spec.File) { delete(f.Versions, "envoy") }, "connect: sidecar needs versions.envoy"},
		{"two tproxies", func(f *spec.File) {
			n := f.Consul.Datacenters["dc1"].Nodes["api"]
			n.Services = map[string]*spec.Service{"a": {Port: 1, Connect: "tproxy"}, "b": {Port: 2, Connect: "tproxy"}}
			n.Service = nil
		}, "service b: connect: tproxy: the node already has one (a)"},
		{"bogus", func(f *spec.File) { f.Consul.Datacenters["dc1"].Nodes["api"].Service.Connect = "yes" }, `want none, sidecar or tproxy, got "yes"`},
		{"upstreams without connect", func(f *spec.File) {
			s := f.Consul.Datacenters["dc1"].Nodes["web"].Service
			s.Connect = "none"
		}, "service web: upstreams need connect: sidecar or tproxy"},
		{"same port", func(f *spec.File) {
			s := f.Consul.Datacenters["dc1"].Nodes["web"].Service
			s.Upstreams["db"] = &spec.Upstream{Port: 9091}
		}, "upstreams api and db both use port 9091"},
		{"own port", func(f *spec.File) {
			f.Consul.Datacenters["dc1"].Nodes["web"].Service.Upstreams["api"].Port = 8080
		}, "upstream api port 8080 is the service's own port"},
		{"no port", func(f *spec.File) {
			f.Consul.Datacenters["dc1"].Nodes["web"].Service.Upstreams["api"].Port = 0
		}, "upstream api needs a port"},
		{"no agent", func(f *spec.File) { f.Consul.Datacenters["dc1"].Nodes["api"].Agent = "none" }, "connect: sidecar needs a Consul agent"},
	} {
		f := load(t, "../../examples/mesh.yaml")
		c.edit(f)
		if _, err := Expand(f); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

func TestUpstreamYAML(t *testing.T) {
	f := load(t, "../../examples/mesh.yaml")
	if u := f.Consul.Datacenters["dc1"].Nodes["web"].Service.Upstreams["api"]; u == nil || *u != (spec.Upstream{Port: 9091}) {
		t.Errorf("bare port: %+v", u)
	}
	var s spec.Service
	if err := unmarshalStrict("upstreams: { db: { port: 9092, dc: dc2 }, billing: { port: 9093, peer: other } }", &s); err != nil {
		t.Fatal(err)
	}
	if *s.Upstreams["db"] != (spec.Upstream{Port: 9092, DC: "dc2"}) || s.Upstreams["billing"].Peer != "other" {
		t.Errorf("objects: %+v %+v", s.Upstreams["db"], s.Upstreams["billing"])
	}
	low, err := Expand(func() *spec.File {
		f := load(t, "../../examples/mesh.yaml")
		f.Consul.Datacenters["dc1"].Nodes["web"].Service.Upstreams = s.Upstreams
		return f
	}())
	if err != nil {
		t.Fatal(err)
	}
	ups := agentConfig(t, low.Nodes["dc1-web"])["services"].([]any)[0].(map[string]any)["connect"].(map[string]any)["sidecar_service"].(map[string]any)["proxy"].(map[string]any)["upstreams"].([]any)
	want := []any{
		map[string]any{"destination_name": "billing", "local_bind_port": float64(9093), "destination_peer": "other"},
		map[string]any{"destination_name": "db", "local_bind_port": float64(9092), "datacenter": "dc2"},
	}
	if !reflect.DeepEqual(ups, want) {
		t.Errorf("upstreams = %v", ups)
	}
}

func TestTransparentProxy(t *testing.T) {
	low, err := Expand(load(t, "../../examples/tproxy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	reg := agentConfig(t, low.Nodes["dc1-web"])["services"].([]any)[0].(map[string]any)
	want := map[string]any{"sidecar_service": map[string]any{"port": float64(SidecarPort), "proxy": map[string]any{
		"mode":              "transparent",
		"transparent_proxy": map[string]any{"outbound_listener_port": float64(TproxyOutboundPort)},
	}}}
	if !reflect.DeepEqual(reg["connect"], want) {
		t.Errorf("web connect = %v", reg["connect"])
	}
	sc := low.Nodes["dc1-web"].Processes["web"+SidecarSuffix]
	wantRun := VarParker + " sidecar --transparent --inbound-port 21000 --outbound-port 15001 -- " +
		"consul connect envoy -sidecar-for web -admin-bind 127.0.0.1:19000 -- --log-level info"
	if got := strings.Join(sc.Run, " "); got != wantRun {
		t.Errorf("tproxy sidecar run = %s", got)
	}
	if !slices.Equal(sc.Uses, []string{"consul", "envoy"}) {
		t.Errorf("uses = %v", sc.Uses)
	}
	// api is a plain sidecar on a node without a transparent proxy.
	if got := low.Nodes["dc1-api"].Processes["api"+SidecarSuffix].Run[0]; got != "consul" {
		t.Errorf("api sidecar runs %s", got)
	}
}

func TestTransparentProxyMarksOtherSidecars(t *testing.T) {
	f := load(t, "../../examples/tproxy.yaml")
	n := f.Consul.Datacenters["dc1"].Nodes["web"]
	n.Services = map[string]*spec.Service{"admin": {Port: 7000, Connect: "sidecar"}, "web": n.Service}
	n.Service = nil
	low, err := Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	ps := low.Nodes["dc1-web"].Processes
	admin := strings.Join(ps["admin"+SidecarSuffix].Run, " ")
	if !strings.HasPrefix(admin, VarParker+" sidecar -- consul connect envoy -sidecar-for admin -admin-bind 127.0.0.1:19000") {
		t.Errorf("admin sidecar is marked but not transparent: %s", admin)
	}
	if web := strings.Join(ps["web"+SidecarSuffix].Run, " "); !strings.Contains(web, "--inbound-port 21001") {
		t.Errorf("web takes the second sidecar port: %s", web)
	}
}

func TestTransparentProxyDNSToken(t *testing.T) {
	f := load(t, "../../hack/e2e/tproxy-secure.yaml")
	low, err := Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	web := agentConfig(t, low.Nodes["dc1-web"])["acl"].(map[string]any)["tokens"].(map[string]any)
	if web["dns"] == nil || web["dns"] != web["agent"] {
		t.Errorf("transparent node tokens = %v, want dns = agent", web)
	}
	api := agentConfig(t, low.Nodes["dc1-api"])["acl"].(map[string]any)["tokens"].(map[string]any)
	if _, ok := api["dns"]; ok {
		t.Errorf("plain sidecar node gets a dns token: %v", api)
	}
}
