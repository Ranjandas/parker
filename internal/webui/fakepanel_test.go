package webui_test

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/Ranjandas/parker/internal/daemon"
	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/graph"
	"github.com/Ranjandas/parker/internal/lab"
	"github.com/Ranjandas/parker/internal/spec"
)

type panelFake struct {
	Upstreams []string
	Callers   []string
	Chain     []struct{ Name, Via string }
}

type panelOut struct {
	Fakes           map[string]panelFake
	Header, Summary string
	Empty           string
}

// panelOf runs static/fakepanel.js over an example's graph and fakes, as
// the UI gets them from the Graph and Fake calls.
func panelOf(t *testing.T, node, example string) (panelOut, map[string]bool) {
	t.Helper()
	f, err := spec.Load(filepath.Join("..", "..", "examples", example), nil)
	if err != nil {
		t.Fatal(err)
	}
	low, err := expand.Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	g, err := protojson.Marshal(daemon.GraphToProto("lab", graph.Build(low)))
	if err != nil {
		t.Fatal(err)
	}
	type fake struct{ Node, Service, Protocol string }
	var fakes []fake
	isFake := map[string]bool{}
	for _, n := range slices.Sorted(maps.Keys(low.Nodes)) {
		for _, fi := range lab.FakeInfos(low, n) {
			fakes = append(fakes, fake{fi.Node, fi.Service, fi.Protocol})
			isFake[n+"/"+fi.Service] = true
		}
	}
	fj, err := json.Marshal(fakes)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gp, fp := filepath.Join(dir, "graph.json"), filepath.Join(dir, "fakes.json")
	if err := os.WriteFile(gp, g, 0o600); err != nil {
		t.Fatal(err)
	}
	// The UI reads the Fake call's JSON names (node, service, protocol).
	if err := os.WriteFile(fp, []byte(strings.NewReplacer(`"Node"`, `"node"`, `"Service"`, `"service"`, `"Protocol"`, `"protocol"`).Replace(string(fj))), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, filepath.Join("testdata", "fakepanel", "check.js"), gp, fp).Output()
	if err != nil {
		t.Fatalf("check.js: %v", err)
	}
	var r panelOut
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	return r, isFake
}

// TestFakePanel checks the node page's fake panel over the examples: the
// per-request fault picker lists only fakes, never the service itself,
// each name once; callers are fakes; and fake-chain.yaml shows what it
// should.
func TestFakePanel(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	examples, _ := filepath.Glob(filepath.Join("..", "..", "examples", "*.yaml"))
	for _, ex := range examples {
		ex = filepath.Base(ex)
		t.Run(ex, func(t *testing.T) {
			r, isFake := panelOf(t, node, ex)
			services := map[string]bool{}
			for k := range isFake {
				services[k[strings.Index(k, "/")+1:]] = true
			}
			for k, p := range r.Fakes {
				self := k[strings.Index(k, "/")+1:]
				seen := map[string]bool{}
				for _, c := range p.Chain {
					if c.Name == self || seen[c.Name] || !services[c.Name] {
						t.Errorf("%s: chain %v: %q is itself, listed twice or not a fake", k, p.Chain, c.Name)
					}
					seen[c.Name] = true
				}
				if len(p.Upstreams) == 0 && len(p.Chain) > 0 {
					t.Errorf("%s: no upstreams but a chain %v", k, p.Chain)
				}
			}
		})
	}

	r, _ := panelOf(t, node, "fake-chain.yaml")
	web := r.Fakes["dc1-web/web"]
	if !slices.Equal(web.Upstreams, []string{"api"}) {
		t.Errorf("web's upstreams = %v, want [api]", web.Upstreams)
	}
	if b, _ := json.Marshal(web.Chain); string(b) != `[{"Name":"api","Via":""},{"Name":"db","Via":"api"}]` {
		t.Errorf("web's chain = %s, want api, then db via api", b)
	}
	if db := r.Fakes["dc1-db/db"]; len(db.Upstreams) != 0 || !slices.Equal(db.Callers, []string{"dc1-api"}) {
		t.Errorf("dc1 db: upstreams %v, callers %v; want none, [dc1-api]", db.Upstreams, db.Callers)
	}
	// dc2's api is web's failover, so web calls it too.
	if c := r.Fakes["dc2-api/api"].Callers; !slices.Equal(c, []string{"dc1-web"}) {
		t.Errorf("dc2 api's callers = %v, want [dc1-web]", c)
	}
	if want := "api=error-rate:100;db=delay:1000ms"; r.Header != want {
		t.Errorf("header = %q, want %q", r.Header, want)
	}
	if want := "2 faults: api fails 100% · db +1s"; r.Summary != want {
		t.Errorf("summary = %q, want %q", r.Summary, want)
	}
	if r.Empty != "" {
		t.Errorf("no rows sends %q, want nothing", r.Empty)
	}
}
