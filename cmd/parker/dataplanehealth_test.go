package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/Ranjandas/parker/internal/expand"
)

type fakeCatalog struct {
	mu   sync.Mutex
	fail int // fail this many calls first
	regs []*api.CatalogRegistration
}

func (f *fakeCatalog) Register(r *api.CatalogRegistration, _ *api.WriteOptions) (*api.WriteMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail > 0 {
		f.fail--
		return nil, errors.New("no leader")
	}
	f.regs = append(f.regs, r)
	return nil, nil
}

func (f *fakeCatalog) statuses() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.regs {
		out = append(out, r.Service.ID+"="+r.Check.Status)
	}
	return out
}

func TestKeepHealth(t *testing.T) {
	entries := []expand.DataplaneEntry{
		{Node: "api", Address: "10.1.0.102", Datacenter: "dc1", Probe: "10.1.0.102:9090",
			Service: expand.DataplaneService{ID: "api", Name: "api", Port: 9090}},
		{Node: "api", Address: "10.1.0.102", Datacenter: "dc1", Probe: "10.1.0.102:21000",
			Service: expand.DataplaneService{ID: "api-sidecar-proxy", Name: "api-sidecar-proxy", Port: 21000, Kind: "connect-proxy",
				Proxy: &expand.DataplaneProxy{DestinationServiceName: "api", DestinationServiceID: "api", LocalServicePort: 9090,
					Upstreams: []expand.DataplaneUpstream{{DestinationName: "web", LocalBindPort: 9092}}}}},
	}
	var mu sync.Mutex
	up := map[string]bool{"10.1.0.102:9090": true, "10.1.0.102:21000": true}
	probe := func(a string) bool { mu.Lock(); defer mu.Unlock(); return up[a] }
	cat := &fakeCatalog{fail: 1}
	var out, log bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = keepHealth(ctx, cat, entries, probe, 5*time.Millisecond, &out, &log); close(done) }()

	wait := func(want string) {
		t.Helper()
		for range 400 {
			if strings.Join(cat.statuses(), " ") == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("registrations %v, want %s", cat.statuses(), want)
	}
	// api's first try fails and is retried on the next round; each is
	// registered once.
	wait("api-sidecar-proxy=passing api=passing")
	mu.Lock()
	up["10.1.0.102:9090"] = false
	mu.Unlock()
	wait("api-sidecar-proxy=passing api=passing api=critical")
	cancel()
	<-done

	if out.String() != expand.DataplaneHealthReadyLine+"\n" {
		t.Errorf("out = %q", out.String())
	}
	if l := log.String(); !strings.Contains(l, "register api on api: no leader (retrying)") || !strings.Contains(l, "api on api is critical") {
		t.Errorf("log = %q", l)
	}
	r := cat.regs[0]
	if r.Service.Kind != api.ServiceKindConnectProxy || r.Service.Proxy.DestinationServiceName != "api" || r.Service.Proxy.Upstreams[0].LocalBindPort != 9092 ||
		r.Check.CheckID != "parker:api-sidecar-proxy" || r.Check.ServiceID != "api-sidecar-proxy" || r.Node != "api" || r.Address != "10.1.0.102" {
		t.Errorf("proxy registration = %+v", r)
	}
}

func TestProbeHTTP(t *testing.T) {
	code := 200
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
	defer ts.Close()
	if !tcpProbe(ts.URL + "/health") {
		t.Error("200 should pass")
	}
	code = 503
	if tcpProbe(ts.URL + "/health") {
		t.Error("503 should fail")
	}
	if !tcpProbe(strings.TrimPrefix(ts.URL, "http://")) {
		t.Error("host:port is a TCP probe")
	}
}
