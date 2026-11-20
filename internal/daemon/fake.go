package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"connectrpc.com/connect"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/fake"
	"github.com/Ranjandas/parker/internal/lab"
	"github.com/Ranjandas/parker/internal/spec"
)

// Fake lists, changes or resets a lab's fake services, or calls one.
func (s *Server) Fake(ctx context.Context, req *connect.Request[parkerv1.FakeRequest]) (*connect.Response[parkerv1.FakeResponse], error) {
	m := req.Msg
	low, err := lab.LoadTopology(m.Lab)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("lab %q is not running: %w", m.Lab, err))
	}
	sel := m.Selector
	nodes := slices.Sorted(maps.Keys(low.Nodes))
	if sel == "" {
		sel = "the lab"
	} else if nodes, err = lab.ResolveNodes(low, sel); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	out := &parkerv1.FakeResponse{}
	if m.Action == "call" {
		if len(nodes) != 1 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("call: %s matches %d nodes; name one", sel, len(nodes)))
		}
		status, body, err := lab.FakeCall(ctx, low, nodes[0], m.Service, m.Path, m.Headers)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		out.CallStatus, out.CallBody = int32(status), string(body)
		return connect.NewResponse(out), nil
	}
	var change *spec.Fake
	reset := false
	switch m.Action {
	case "", "list":
	case "set":
		change = &spec.Fake{}
		if err := json.Unmarshal([]byte(m.ChangeJson), change); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("change: %w", err))
		}
		if err := fake.Validate(change); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	case "reset":
		reset = true
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("action %q: want list, set, reset or call", m.Action))
	}
	var errs []error
	for _, n := range nodes {
		for _, fi := range lab.FakeInfos(low, n) {
			if m.Service != "" && fi.Service != m.Service {
				continue
			}
			f, err := lab.FakeControl(ctx, low, fi, change, reset)
			if err != nil {
				// A fake that's down still lists, without faults.
				if change != nil || reset {
					errs = append(errs, fmt.Errorf("%s/%s: %w", n, fi.Service, err))
				}
				out.Services = append(out.Services, &parkerv1.FakeService{Node: n, Service: fi.Service, Port: int32(fi.Port), Protocol: fi.Protocol, Summary: "not answering: " + err.Error()})
				continue
			}
			fj, _ := json.Marshal(f)
			out.Services = append(out.Services, &parkerv1.FakeService{Node: n, Service: fi.Service, Port: int32(fi.Port), Protocol: fi.Protocol,
				FaultsJson: string(fj), Summary: f.Summary(), Faulted: f.Injected()})
		}
	}
	if len(errs) > 0 {
		return nil, connect.NewError(connect.CodeUnavailable, errors.Join(errs...))
	}
	if len(out.Services) == 0 && (change != nil || reset || m.Selector != "") {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no fake services (run: fake-service) on %s", sel))
	}
	return connect.NewResponse(out), nil
}

// fakeUIControl answers a fake's /ui control panel, opened through the app
// proxy: GET or POST <UIControlPath>faults, POST <UIControlPath>reset. It
// goes to the fake's control socket, never to the service port, so only
// the Parker UI's signed-in browser can change faults: a service in the
// mesh can't, and neither can the page when opened on the port directly.
func fakeUIControl(w http.ResponseWriter, r *http.Request, t appTarget) {
	low, err := lab.LoadTopology(t.lab)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var fi *lab.FakeInfo
	for _, f := range lab.FakeInfos(low, t.node) {
		if f.Port == t.port {
			fi = &f
			break
		}
	}
	if fi == nil {
		http.Error(w, fmt.Sprintf("no fake service on %s:%d", t.node, t.port), http.StatusNotFound)
		return
	}
	var change *spec.Fake
	reset := false
	switch strings.TrimPrefix(t.rest, fake.UIControlPath) {
	case "faults":
		switch r.Method {
		case http.MethodGet:
		case http.MethodPost:
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "want application/json", http.StatusUnsupportedMediaType)
				return
			}
			change = &spec.Fake{}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(change); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := fake.Validate(change); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	case "reset":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		reset = true
	default:
		http.NotFound(w, r)
		return
	}
	f, err := lab.FakeControl(r.Context(), low, *fi, change, reset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"faults": f, "summary": f.Summary(), "injected": f.Injected()})
}
