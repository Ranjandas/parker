package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/fake"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// FakeInfo is one fake service (run: fake-service) on a node.
type FakeInfo struct {
	Node, Service string
	Port          int
	// Protocol is http, grpc or tcp.
	Protocol string
}

// FakeServices are the fake services on a node.
func FakeServices(low *spec.File, node string) []string {
	var out []string
	for _, f := range FakeInfos(low, node) {
		out = append(out, f.Service)
	}
	return out
}

// FakeInfos are the fake services on a node, by name.
func FakeInfos(low *spec.File, node string) []FakeInfo {
	n := low.Nodes[node]
	if n == nil {
		return nil
	}
	var out []FakeInfo
	for _, name := range slices.Sorted(maps.Keys(n.Processes)) {
		p := n.Processes[name]
		raw, ok := p.Env[expand.FakeEnv]
		if !ok {
			continue
		}
		fi := FakeInfo{Node: node, Service: name, Protocol: "http"}
		for i, a := range p.Run {
			if a == "--port" && i+1 < len(p.Run) {
				fi.Port, _ = strconv.Atoi(p.Run[i+1])
			}
		}
		var f spec.Fake
		if json.Unmarshal([]byte(raw), &f) == nil && f.Protocol != "" {
			fi.Protocol = f.Protocol
		}
		out = append(out, fi)
	}
	return out
}

// pickFakes is the fake services on node, or just svc.
func pickFakes(low *spec.File, node, svc string) ([]FakeInfo, error) {
	all := FakeInfos(low, node)
	if len(all) == 0 {
		var procs []string
		if n := low.Nodes[node]; n != nil {
			procs = slices.Sorted(maps.Keys(n.Processes))
		}
		return nil, fmt.Errorf("%s: no fake services (run: fake-service) on this node; it runs %s", node, list(procs))
	}
	if svc == "" {
		return all, nil
	}
	for _, f := range all {
		if f.Service == svc {
			return []FakeInfo{f}, nil
		}
	}
	return nil, fmt.Errorf("%s: no fake service %q here (fake services: %s)", node, svc, list(FakeServices(low, node)))
}

// FakeControl reads (change nil), changes or resets one fake's faults
// through its control socket.
func FakeControl(ctx context.Context, low *spec.File, f FakeInfo, change *spec.Fake, reset bool) (fake.Faults, error) {
	sock := filepath.Join(supervisor.NodeDir(RunDir(low.Name), f.Node), fake.SocketName(f.Service))
	return fake.Control(ctx, sock, change, reset)
}

// FakeCall sends one request to a fake service from inside its node, as
// a client there would, and returns the status and body.
func FakeCall(ctx context.Context, low *spec.File, node, svc, path string, hdr map[string]string) (int, []byte, error) {
	fs, err := pickFakes(low, node, svc)
	if err != nil {
		return 0, nil, err
	}
	f := fs[0]
	if f.Protocol == "tcp" {
		return 0, nil, fmt.Errorf("%s/%s speaks tcp: there's no HTTP answer to show (try exec with nc)", node, f.Service)
	}
	if f.Port == 0 {
		return 0, nil, fmt.Errorf("%s/%s: no port", node, f.Service)
	}
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cl := &http.Client{Transport: &http.Transport{DialContext: supervisor.DialerIn(netsetup.NetnsPath(low.Name, node)), DisableKeepAlives: true}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", f.Port, path), nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := cl.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s/%s: %w", node, f.Service, PrivHint(err))
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return res.StatusCode, b, err
}

// Fake changes or reads the faults of the fake services on node through
// their control sockets.
func (l *ScenarioLab) Fake(ctx context.Context, node string, st *spec.FakeStep) ([]string, error) {
	fs, err := pickFakes(l.Low, node, st.Service)
	if err != nil {
		return nil, err
	}
	var change *spec.Fake
	if !st.Empty() && !st.Reset {
		change = st.Change()
	}
	var lines []string
	for _, fi := range fs {
		f, err := FakeControl(ctx, l.Low, fi, change, st.Reset)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", fi.Service, err)
		}
		lines = append(lines, fmt.Sprintf("%s/%s: %s", node, fi.Service, f.Summary()))
	}
	return lines, nil
}

// Call sends one request to a fake service on node.
func (l *ScenarioLab) Call(ctx context.Context, node, svc, path string, hdr map[string]string) (int, []byte, error) {
	return FakeCall(ctx, l.Low, node, svc, path, hdr)
}

func list(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}
