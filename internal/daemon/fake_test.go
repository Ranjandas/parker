package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/fake"
	"github.com/Ranjandas/parker/internal/lab"
	"github.com/Ranjandas/parker/internal/supervisor"
)

const fakeTopo = `parker: v1alpha1
name: f
networks:
  lan: { subnet: 10.78.0.0/24 }
nodes:
  dc1-api:
    networks: { lan: { ip: 10.78.0.10 } }
    processes:
      api: { run: [parker, fake-service, --name, api, --port, "9090"], env: { PARKER_FAKE: '{"protocol":"grpc"}' } }
  dc1-db:
    networks: { lan: { ip: 10.78.0.11 } }
    processes:
      db: { run: [parker, fake-service, --name, db, --port, "7000"], env: { PARKER_FAKE: '{}' } }
      consul: { run: sleep 1 }
`

func TestFakeRPC(t *testing.T) {
	base := t.TempDir()
	t.Setenv("PARKER_RUN_DIR", base)
	if err := os.MkdirAll(filepath.Join(base, "f"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "f", lab.TopologyFile), []byte(fakeTopo), 0o644); err != nil {
		t.Fatal(err)
	}
	// Only api answers on its control socket; db is down.
	dir := supervisor.NodeDir(lab.RunDir("f"), "dc1-api")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, _ := fake.Load("api", "dc1", 9090, func(string) string { return "" })
	fs := fake.New(cfg)
	fs.SetLogOutput(io.Discard)
	if err := fs.ListenControl(filepath.Join(dir, fake.SocketName("api"))); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	ctx := context.Background()
	do := func(r *parkerv1.FakeRequest) (*parkerv1.FakeResponse, error) {
		r.Lab = "f"
		res, err := s.Fake(ctx, connect.NewRequest(r))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	got, err := do(&parkerv1.FakeRequest{})
	if err != nil || len(got.Services) != 2 {
		t.Fatalf("list: %v %v", got, err)
	}
	api, db := got.Services[0], got.Services[1]
	if api.Node != "dc1-api" || api.Port != 9090 || api.Protocol != "grpc" || api.Faulted || !strings.Contains(api.FaultsJson, `"health_code":200`) {
		t.Errorf("api = %+v", api)
	}
	if db.Protocol != "http" || !strings.HasPrefix(db.Summary, "not answering") {
		t.Errorf("db = %+v", db)
	}
	got, err = do(&parkerv1.FakeRequest{Selector: "dc1-api", Action: "set", ChangeJson: `{"errors":{"rate":0.3,"code":503}}`})
	if err != nil || len(got.Services) != 1 || !got.Services[0].Faulted || !strings.Contains(got.Services[0].Summary, "errors 30% (503)") {
		t.Fatalf("set: %v %v", got, err)
	}
	if f := fs.Faults(); f.ErrorRate != 0.3 {
		t.Errorf("not applied: %+v", f)
	}
	if got, err = do(&parkerv1.FakeRequest{Selector: "dc1-api", Action: "reset"}); err != nil || got.Services[0].Faulted {
		t.Errorf("reset: %v %v", got, err)
	}
	// The fake's /ui control panel, through the app proxy.
	h := AppProxy()
	ui := func(method, path, body string) (int, string) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, "/app/f/dc1-api/http/9090"+fake.UIControlPath+path, rd)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if code, b := ui("POST", "faults", `{"health":{"code":503}}`); code != 200 || !strings.Contains(b, `"injected":true`) || fs.Faults().HealthCode != 503 {
		t.Errorf("ui set: %d %s", code, b)
	}
	if code, b := ui("GET", "faults", ""); code != 200 || !strings.Contains(b, "health 503") {
		t.Errorf("ui get: %d %s", code, b)
	}
	if code, _ := ui("POST", "reset", "{}"); code != 200 || fs.Faults().HealthCode != 200 {
		t.Errorf("ui reset: %d", code)
	}
	for _, c := range []struct {
		method, path, body string
		code               int
	}{
		{"POST", "faults", `{"errors":{"rate":9}}`, 400},
		{"GET", "reset", "", 405},
		{"GET", "other", "", 404},
	} {
		if code, b := ui(c.method, c.path, c.body); code != c.code {
			t.Errorf("%s %s: %d %s, want %d", c.method, c.path, code, b, c.code)
		}
	}
	// A port with no fake on it.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/app/f/dc1-api/http/8500"+fake.UIControlPath+"faults", nil))
	if w.Code != 404 {
		t.Errorf("no fake on the port: %d", w.Code)
	}
	// A form post (no JSON content type) is refused.
	req := httptest.NewRequest("POST", "/app/f/dc1-api/http/9090"+fake.UIControlPath+"faults", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d", w.Code)
	}

	for _, c := range []struct {
		r    *parkerv1.FakeRequest
		code connect.Code
		want string
	}{
		{&parkerv1.FakeRequest{Selector: "dc1-api", Action: "set", ChangeJson: `{"errors":{"rate":4}}`}, connect.CodeInvalidArgument, "errors.rate"},
		{&parkerv1.FakeRequest{Selector: "dc1-db", Action: "reset"}, connect.CodeUnavailable, "dc1-db/db"},
		{&parkerv1.FakeRequest{Action: "call"}, connect.CodeInvalidArgument, "name one"},
		{&parkerv1.FakeRequest{Action: "shout"}, connect.CodeInvalidArgument, "want list"},
		{&parkerv1.FakeRequest{Selector: "dc1-api", Service: "web", Action: "reset"}, connect.CodeNotFound, "no fake services"},
	} {
		if _, err := do(c.r); connect.CodeOf(err) != c.code || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %v %q", c.r, err, c.code, c.want)
		}
	}
}
