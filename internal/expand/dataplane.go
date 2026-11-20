package expand

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/Ranjandas/parker/internal/spec"
)

// Dataplane nodes (agent: dataplane) run no Consul agent. Their services
// are registered in the catalog by one process per datacenter on
// server-1, which also keeps their health checks current by probing them
// (the job consul-k8s's endpoints controller does on Kubernetes), and each
// sidecar is consul-dataplane, which talks to the servers over gRPC and
// runs Envoy.
const (
	DataplaneHealth          = "dataplane-health"
	DataplaneHealthReadyLine = "parker: dataplane services registered"
	DataplaneManifest        = "dataplane.json"

	DataplaneRole = "dataplane"

	// DataplaneGracefulPort is consul-dataplane's graceful shutdown port
	// for a node's first sidecar; then 20301, ...
	DataplaneGracefulPort = 20300
)

// DataplaneEntry is one catalog registration the health process keeps:
// a node's service or sidecar proxy, and where to probe it.
type DataplaneEntry struct {
	Node       string            `json:"node"`
	Address    string            `json:"address"`
	Datacenter string            `json:"datacenter"`
	Partition  string            `json:"partition,omitempty"`
	NodeMeta   map[string]string `json:"node_meta,omitempty"`
	Service    DataplaneService  `json:"service"`
	// Probe is host:port, TCP-probed for the service's health, or an
	// http:// URL that must answer below 400; "" is always passing.
	Probe string `json:"probe,omitempty"`
}

type DataplaneService struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Port      int             `json:"port,omitempty"`
	Address   string          `json:"address,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	Kind      string          `json:"kind,omitempty"`
	Proxy     *DataplaneProxy `json:"proxy,omitempty"`
}

type DataplaneProxy struct {
	DestinationServiceName string              `json:"destination_service_name"`
	DestinationServiceID   string              `json:"destination_service_id"`
	LocalServiceAddress    string              `json:"local_service_address"`
	LocalServicePort       int                 `json:"local_service_port,omitempty"`
	Upstreams              []DataplaneUpstream `json:"upstreams,omitempty"`
}

type DataplaneUpstream struct {
	DestinationName      string `json:"destination_name"`
	DestinationNamespace string `json:"destination_namespace,omitempty"`
	DestinationPartition string `json:"destination_partition,omitempty"`
	DestinationPeer      string `json:"destination_peer,omitempty"`
	Datacenter           string `json:"datacenter,omitempty"`
	LocalBindPort        int    `json:"local_bind_port"`
}

// dataplaneEntries are the registrations of one service on a dataplane
// node: the service, and its sidecar proxy when it is in the mesh.
func dataplaneEntries(node, ip, dc, part string, meta map[string]string, name string, svc *spec.Service, sidecarPort int) []DataplaneEntry {
	ns := ""
	if svc.Namespace != "" && svc.Namespace != spec.DefaultNamespace {
		ns = svc.Namespace
	}
	base := DataplaneEntry{Node: node, Address: ip, Datacenter: dc, Partition: part, NodeMeta: meta}
	s := base
	s.Service = DataplaneService{ID: name, Name: name, Port: svc.Port, Address: ip, Namespace: ns}
	if svc.Port > 0 && svc.Check != "none" {
		s.Probe = fmt.Sprintf("%s:%d", ip, svc.Port)
		if isFake(svc) && fakeProtocol(svc) != "tcp" && svc.Check != "tcp" && svc.Check != "none" {
			s.Probe = fmt.Sprintf("http://%s:%d%s", ip, svc.Port, healthPath(svc))
		}
	}
	out := []DataplaneEntry{s}
	if inMesh(svc) {
		p := base
		proxy := &DataplaneProxy{DestinationServiceName: name, DestinationServiceID: name, LocalServiceAddress: "127.0.0.1", LocalServicePort: svc.Port}
		for _, dest := range slices.Sorted(maps.Keys(svc.Upstreams)) {
			u := svc.Upstreams[dest]
			proxy.Upstreams = append(proxy.Upstreams, DataplaneUpstream{DestinationName: dest, DestinationNamespace: u.Namespace,
				DestinationPartition: u.Partition, DestinationPeer: u.Peer, Datacenter: u.DC, LocalBindPort: u.Port})
		}
		p.Service = DataplaneService{ID: name + "-sidecar-proxy", Name: name + "-sidecar-proxy", Port: sidecarPort, Address: ip,
			Namespace: ns, Kind: "connect-proxy", Proxy: proxy}
		p.Probe = fmt.Sprintf("%s:%d", ip, sidecarPort)
		out = append(out, p)
	}
	return out
}

// dataplane is one consul-dataplane sidecar.
type dataplane struct {
	svc, node, ns, part, dc string
	servers                 []string
	admin, graceful         int
	tls                     bool
	token                   string
	deps                    map[string]*spec.Dep
}

func (d dataplane) process() *spec.Process {
	run := spec.Command{"consul-dataplane",
		"-addresses", "exec=echo " + strings.Join(d.servers, " "),
		"-proxy-id", d.svc + "-sidecar-proxy",
		"-proxy-node-name", d.node,
		"-envoy-admin-bind-address", "127.0.0.1",
		"-envoy-admin-bind-port", strconv.Itoa(d.admin),
		"-graceful-port", strconv.Itoa(d.graceful),
		"-log-level", "info",
	}
	if d.tls {
		run = append(run, "-grpc-port", strconv.Itoa(GRPCTLSPort), "-ca-certs", VarNodeDir+"/"+CAFile, "-tls-server-name", "server."+d.dc+".consul")
	} else {
		run = append(run, "-grpc-port", strconv.Itoa(GRPCPort), "-tls-disabled")
	}
	if d.part != "" {
		run = append(run, "-proxy-partition", d.part)
	}
	if d.ns != "" {
		run = append(run, "-proxy-namespace", d.ns)
	}
	if d.token != "" {
		run = append(run, "-credential-type", "static", "-static-token", d.token)
	}
	return &spec.Process{
		Run:       run,
		Uses:      []string{"envoy"},
		DependsOn: d.deps,
		ReadyWhen: &spec.Ready{HTTP: &spec.HTTPCheck{URL: fmt.Sprintf("http://127.0.0.1:%d/ready", d.admin)}, Timeout: "60s"},
		Restart:   "on-failure",
	}
}

// dataplaneHealthProcess registers a dc's dataplane services and keeps
// their checks current.
func dataplaneHealthProcess(deps map[string]*spec.Dep, env map[string]string) *spec.Process {
	return &spec.Process{
		Run:       spec.Command{VarParker, DataplaneHealth, VarNodeDir + "/" + DataplaneManifest},
		Env:       env,
		DependsOn: deps,
		ReadyWhen: &spec.Ready{Log: DataplaneHealthReadyLine, Timeout: "60s"},
		Restart:   "on-failure",
	}
}
