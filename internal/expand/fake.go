package expand

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

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
	switch f.UpstreamFailure {
	case "", "fail", "ignore":
	default:
		errs = append(errs, fmt.Errorf("%s: fake.upstream_failure: want fail or ignore, got %q", where, f.UpstreamFailure))
	}
	if r := f.Ready; r != nil && r.Delay != "" {
		if d, err := time.ParseDuration(r.Delay); err != nil || d < 0 {
			errs = append(errs, fmt.Errorf("%s: fake.ready.delay: want a duration like 5s, got %q", where, r.Delay))
		}
	}
	return errs
}
