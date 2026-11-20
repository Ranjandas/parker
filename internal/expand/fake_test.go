package expand

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func loadText(t *testing.T, text string) *spec.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lab.yaml")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return load(t, p)
}

func fakeEnv(t *testing.T, p *spec.Process) spec.Fake {
	t.Helper()
	var f spec.Fake
	if err := json.Unmarshal([]byte(p.Env[FakeEnv]), &f); err != nil {
		t.Fatalf("%s=%q: %v", FakeEnv, p.Env[FakeEnv], err)
	}
	return f
}

const fakeLab = `parker: v1alpha1
name: chain
versions: { consul: 1.22.7, envoy: 1.35.12 }
consul:
  datacenters:
    dc1:
      servers: 1
      service_defaults: { run: fake-service, connect: sidecar, fake: { version: v1, workers: 2 } }
      nodes:
        web:
          service: { port: 8080, upstreams: { api: 9091, cache: 9092 }, env: { MESSAGE: hi } }
        api:
          service: { port: 9090, fake: { version: v2, upstreams: ["http://db.service.consul:7000/"] } }
        db:
          service: { port: 7000, connect: none }
`

func TestFakeServiceProcess(t *testing.T) {
	low, err := Expand(loadText(t, fakeLab))
	if err != nil {
		t.Fatal(err)
	}
	web := low.Nodes["dc1-web"].Processes["web"]
	if got := strings.Join(web.Run, " "); got != VarParker+" fake-service --name web --port 8080 --dc dc1" {
		t.Errorf("run = %s", got)
	}
	if web.Env["MESSAGE"] != "hi" {
		t.Errorf("env lost: %v", web.Env)
	}
	f := fakeEnv(t, web)
	if strings.Join(f.Upstreams, " ") != "http://127.0.0.1:9091/ http://127.0.0.1:9092/" || f.Version != "v1" || f.Workers != 2 {
		t.Errorf("web fake = %+v", f)
	}
	// The node's own fake: block wins over service_defaults, field by field.
	f = fakeEnv(t, low.Nodes["dc1-api"].Processes["api"])
	if strings.Join(f.Upstreams, " ") != "http://db.service.consul:7000/" || f.Version != "v2" || f.Workers != 2 {
		t.Errorf("api fake = %+v", f)
	}
	// Outside the mesh with no upstreams, the block is empty.
	if f := fakeEnv(t, low.Nodes["dc1-db"].Processes["db"]); len(f.Upstreams) != 0 {
		t.Errorf("db fake = %+v", f)
	}
}

func TestFakeServiceOnlyForFakes(t *testing.T) {
	low, err := Expand(loadText(t, strings.Replace(fakeLab, "        db:\n          service: { port: 7000, connect: none }\n",
		"        db:\n          service: { port: 7000, connect: none, run: [sleep, infinity] }\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := low.Nodes["dc1-db"].Processes["db"].Env[FakeEnv]; ok {
		t.Error("a command service got the fake: block")
	}
}

func TestFakeValidation(t *testing.T) {
	for bad, want := range map[string]string{
		"run: [sleep, infinity], fake: { version: v1 }": "fake: needs run: fake-service",
		"fake: { upstream_failure: maybe }":             "fake.upstream_failure",
		"fake: { workers: -1 }":                         "fake.workers",
		"fake: { ready: { delay: soon } }":              "fake.ready.delay",
		"fake: { errors: { rate: 20 } }":                "errors.rate",
		"fake: { latency: { p50: fast } }":              "latency.p50",
	} {
		lab := strings.Replace(fakeLab, "port: 7000, connect: none", "port: 7000, connect: none, "+bad, 1)
		p := filepath.Join(t.TempDir(), "lab.yaml")
		if err := os.WriteFile(p, []byte(lab), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := spec.Load(p, nil)
		if err == nil {
			_, err = Expand(f)
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", bad, err, want)
		}
	}
}

func TestFakeHealthAndReadiness(t *testing.T) {
	lab := strings.Replace(fakeLab, "port: 7000, connect: none", "port: 7000, connect: none, fake: { ready: { delay: 30s } }", 1)
	lab = strings.Replace(lab, "env: { MESSAGE: hi }", "env: { MESSAGE: hi }, fake: { health: { code: 503 } }", 1)
	low, err := Expand(loadText(t, lab))
	if err != nil {
		t.Fatal(err)
	}
	db := low.Nodes["dc1-db"]
	reg := agentConfig(t, db)["services"].([]any)[0].(map[string]any)
	if c := reg["check"].(map[string]any)["http"]; c != "http://127.0.0.1:7000/health" {
		t.Errorf("check = %v", c)
	}
	r := db.Processes["db"].ReadyWhen
	if r.HTTP == nil || r.HTTP.URL != "http://127.0.0.1:7000/ready" || r.HTTP.Status != 200 || r.Timeout != "1m30s" || r.Consul == nil {
		t.Errorf("db ready_when = %+v", r)
	}
	// Set to fail its check from the start: up waits on /ready only.
	if r := low.Nodes["dc1-web"].Processes["web"].ReadyWhen; r.Consul != nil || r.HTTP == nil {
		t.Errorf("web ready_when = %+v", r)
	}
	// A command service keeps the plain check on /.
	cmd := strings.Replace(fakeLab, "port: 7000, connect: none", "port: 7000, connect: none, run: [sleep, infinity]", 1)
	low, err = Expand(loadText(t, cmd))
	if err != nil {
		t.Fatal(err)
	}
	reg = agentConfig(t, low.Nodes["dc1-db"])["services"].([]any)[0].(map[string]any)
	if c := reg["check"].(map[string]any)["http"]; c != "http://127.0.0.1:7000/" {
		t.Errorf("command service check = %v", c)
	}
	if low.Nodes["dc1-db"].Processes["db"].ReadyWhen.HTTP != nil {
		t.Error("command service waits on /ready")
	}
}
