package expand

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/Ranjandas/parker/internal/fake"
	"github.com/Ranjandas/parker/internal/spec"
)

// FakeEnv is the env var a fake-service process gets its fake: block in.
const FakeEnv = "PARKER_FAKE"

// isFake reports whether the service runs Parker's built-in fake service.
func isFake(svc *spec.Service) bool {
	return len(svc.Run) == 1 && svc.Run[0] == "fake-service"
}

// fakeConfig is the fake: block the service's fake-service process gets
// (in PARKER_FAKE), with its upstreams filled in from upstreams: when the
// block names none: each through the sidecar's local port.
func fakeConfig(svc *spec.Service) string {
	f := spec.Fake{}
	if svc.Fake != nil {
		f = *svc.Fake
	}
	if len(f.Upstreams) == 0 && inMesh(svc) {
		for _, dest := range slices.Sorted(maps.Keys(svc.Upstreams)) {
			if u := svc.Upstreams[dest]; u != nil && u.Port > 0 {
				f.Upstreams = append(f.Upstreams, fmt.Sprintf("http://127.0.0.1:%d/", u.Port))
			}
		}
	}
	b, err := json.Marshal(f)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// fakeProtocol is the fake's protocol: http, grpc or tcp.
func fakeProtocol(svc *spec.Service) string {
	if svc.Fake != nil && svc.Fake.Protocol != "" {
		return svc.Fake.Protocol
	}
	return "http"
}

// healthPath is the path Parker's http check requests: /health on a fake
// service, so faults injected on / don't fail the check by accident.
func healthPath(svc *spec.Service) string {
	if isFake(svc) {
		return "/health"
	}
	return "/"
}

// fakeReady makes up wait on the fake's /ready, and gives a ready.delay
// its time on top of the usual timeout.
func fakeReady(svc *spec.Service, r *spec.Ready) {
	code := 200
	if f := svc.Fake; f != nil && f.Ready != nil {
		if f.Ready.Code != 0 {
			code = f.Ready.Code
		}
		if d, err := time.ParseDuration(f.Ready.Delay); err == nil && d > 0 {
			r.Timeout = (60*time.Second + d).String()
		}
	}
	r.HTTP = &spec.HTTPCheck{URL: fmt.Sprintf("http://127.0.0.1:%d/ready", svc.Port), Status: code}
}

// fakeStartsUnhealthy reports a fake set to fail its health check from
// the start: up can't wait for Consul to call it healthy.
func fakeStartsUnhealthy(svc *spec.Service) bool {
	return isFake(svc) && svc.Fake != nil && svc.Fake.Health != nil && svc.Fake.Health.Code >= 400
}

// mergeFake layers b's set fields over a's.
func mergeFake(a, b *spec.Fake) *spec.Fake {
	out := &spec.Fake{}
	if a != nil {
		*out = *a
	}
	if b.Protocol != "" {
		out.Protocol = b.Protocol
	}
	if b.Message != "" {
		out.Message = b.Message
	}
	if b.Version != "" {
		out.Version = b.Version
	}
	if len(b.Upstreams) > 0 {
		out.Upstreams = slices.Clone(b.Upstreams)
	}
	if b.Workers != 0 {
		out.Workers = b.Workers
	}
	if b.UpstreamFailure != "" {
		out.UpstreamFailure = b.UpstreamFailure
	}
	if b.Health != nil {
		h := *b.Health
		out.Health = &h
	}
	if b.Latency != nil {
		l := spec.FakeLatency{}
		if out.Latency != nil {
			l = *out.Latency
		}
		l.P50 = cmp.Or(b.Latency.P50, l.P50)
		l.P90 = cmp.Or(b.Latency.P90, l.P90)
		l.P99 = cmp.Or(b.Latency.P99, l.P99)
		l.Variance = cmp.Or(b.Latency.Variance, l.Variance)
		out.Latency = &l
	}
	if b.Errors != nil {
		e := spec.FakeErrors{}
		if out.Errors != nil {
			e = *out.Errors
		}
		if b.Errors.Rate != nil {
			e.Rate = b.Errors.Rate
		}
		e.Code = cmp.Or(b.Errors.Code, e.Code)
		e.Type = cmp.Or(b.Errors.Type, e.Type)
		e.Delay = cmp.Or(b.Errors.Delay, e.Delay)
		out.Errors = &e
	}
	if b.RateLimit != nil {
		r := spec.FakeRateLimit{}
		if out.RateLimit != nil {
			r = *out.RateLimit
		}
		if b.RateLimit.RPS != nil {
			r.RPS = b.RateLimit.RPS
		}
		r.Code = cmp.Or(b.RateLimit.Code, r.Code)
		out.RateLimit = &r
	}
	if b.Seed != 0 {
		out.Seed = b.Seed
	}
	if b.RequestFaults != nil {
		v := *b.RequestFaults
		out.RequestFaults = &v
	}
	if b.Ready != nil {
		r := spec.FakeReady{}
		if out.Ready != nil {
			r = *out.Ready
		}
		if b.Ready.Delay != "" {
			r.Delay = b.Ready.Delay
		}
		if b.Ready.Code != 0 {
			r.Code = b.Ready.Code
		}
		if b.Ready.FailureCode != 0 {
			r.FailureCode = b.Ready.FailureCode
		}
		out.Ready = &r
	}
	return out
}

// checkFake validates a service's fake: block. own is the service as the
// node declares it: a fake: block inherited from service_defaults is fine
// on a service that runs a command (it is ignored there).
func checkFake(where string, svc, own *spec.Service) []error {
	f := svc.Fake
	if f == nil {
		return nil
	}
	var errs []error
	if own != nil && own.Fake != nil && !isFake(svc) {
		errs = append(errs, fmt.Errorf("%s: fake: needs run: fake-service", where))
	}
	if f.Workers < 0 {
		errs = append(errs, fmt.Errorf("%s: fake.workers: want 1 or more, got %d", where, f.Workers))
	}
	if f.Protocol == "tcp" && (svc.Check == "http" || svc.Check == "grpc") {
		errs = append(errs, fmt.Errorf("%s: check: %s can't reach a fake with protocol: tcp (use tcp)", where, svc.Check))
	}
	if f.Protocol != "grpc" && svc.Check == "grpc" && isFake(svc) {
		errs = append(errs, fmt.Errorf("%s: check: grpc needs fake.protocol: grpc", where))
	}
	if err := fake.Validate(f); err != nil {
		errs = append(errs, fmt.Errorf("%s: fake.%w", where, err))
	}
	return errs
}

// fakeProtocolEntries are the service-defaults that give each grpc fake in
// the mesh protocol: grpc, so Envoy routes it as HTTP/2 (per-request
// retries, gRPC status in metrics). A service the lab's own
// config_entries name is left to them.
func fakeProtocolEntries(c *spec.Consul) map[string][]gatewayEntry {
	defaults := c.Defaults
	if defaults == nil {
		defaults = &spec.Inheritable{}
	}
	out := map[string][]gatewayEntry{}
	for _, dcName := range slices.Sorted(maps.Keys(c.Datacenters)) {
		dc := c.Datacenters[dcName]
		named := map[string]bool{} // partition/name the lab configures
		note := func(part string, es []spec.ConfigEntry) {
			for _, e := range es {
				if k, _ := e["Kind"].(string); k == "service-defaults" {
					n, _ := e["Name"].(string)
					named[part+"/"+n] = true
				}
			}
		}
		note("", dc.ConfigEntries)
		for pn, p := range dc.Partitions {
			if p != nil {
				note(partitionName(pn), p.ConfigEntries)
			}
		}
		nodes := dc.AllNodes()
		slices.SortFunc(nodes, func(a, b spec.PlacedNode) int { return cmp.Compare(a.Partition+"/"+a.Name, b.Partition+"/"+b.Name) })
		for _, placed := range nodes {
			node := placed.Node
			if node == nil {
				continue
			}
			var partSD *spec.Service
			if placed.Part != nil {
				partSD = placed.Part.ServiceDefaults
			}
			services := node.Services
			if node.Service != nil {
				services = map[string]*spec.Service{placed.Name: node.Service}
			}
			part := partitionName(placed.Partition)
			for _, svcName := range slices.Sorted(maps.Keys(services)) {
				svc := mergeService(defaults.ServiceDefaults, dc.ServiceDefaults, partSD, services[svcName])
				if !isFake(svc) || !inMesh(svc) || fakeProtocol(svc) != "grpc" || named[part+"/"+svcName] {
					continue
				}
				named[part+"/"+svcName] = true
				e := spec.ConfigEntry{"Kind": "service-defaults", "Name": svcName, "Protocol": "grpc"}
				if svc.Namespace != "" && svc.Namespace != spec.DefaultNamespace {
					e["Namespace"] = svc.Namespace
				}
				out[dcName] = append(out[dcName], gatewayEntry{
					label: fmt.Sprintf("consul.datacenters.%s.nodes.%s: service %s: fake.protocol grpc", dcName, placed.Name, svcName),
					entry: e, partition: part,
				})
			}
		}
	}
	return out
}
