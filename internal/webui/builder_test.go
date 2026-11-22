package webui_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/daemon"
	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/labfile"
	"github.com/Ranjandas/parker/internal/spec"
)

// The lab builder (static/buildmodel.js) writes YAML in the browser. Its
// fixtures under testdata/builder are what it writes for each starter and
// for labs built with its edits; gen.js makes them.

const fixtures = "testdata/builder"

// TestBuilderFixturesCurrent regenerates the fixtures with node and
// compares. PARKER_UPDATE_FIXTURES=1 rewrites them instead.
func TestBuilderFixturesCurrent(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	if os.Getenv("PARKER_UPDATE_FIXTURES") != "" {
		dir = fixtures
	}
	if out, err := exec.Command(node, filepath.Join(fixtures, "gen.js"), dir).CombinedOutput(); err != nil {
		t.Fatalf("gen.js: %v\n%s", err, out)
	}
	got, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	want, _ := filepath.Glob(filepath.Join(fixtures, "*.yaml"))
	if len(got) != len(want) {
		t.Fatalf("gen.js wrote %d files, testdata has %d; run with PARKER_UPDATE_FIXTURES=1", len(got), len(want))
	}
	for _, g := range got {
		a, _ := os.ReadFile(g)
		b, err := os.ReadFile(filepath.Join(fixtures, filepath.Base(g)))
		if err != nil || string(a) != string(b) {
			t.Errorf("%s is stale; run with PARKER_UPDATE_FIXTURES=1:\n%s", filepath.Base(g), a)
		}
	}
}

// TestBuilderFixturesValidate runs each fixture through validate.
func TestBuilderFixturesValidate(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(fixtures, "*.yaml"))
	if len(files) < 10 {
		t.Fatalf("only %d fixtures", len(files))
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		r := daemon.ValidateYAML(string(b))
		if !r.Ok {
			t.Errorf("%s: %v", filepath.Base(f), r.Errors)
		}
		if !strings.HasPrefix(string(b), "# Built in the Parker lab builder.") {
			t.Errorf("%s: no header", filepath.Base(f))
		}
	}
}

// TestBuilderRebuildsExamples: what the builder writes for meshfed and
// peering expands to the same lab as the examples.
func TestBuilderRebuildsExamples(t *testing.T) {
	for fixture, example := range map[string]string{
		"meshfed.yaml":       "mesh-federation.yaml",
		"steps-meshfed.yaml": "mesh-federation.yaml",
		"peering.yaml":       "peering.yaml",
		"steps-peering.yaml": "peering.yaml",
	} {
		got, want := expanded(t, filepath.Join(fixtures, fixture)), expanded(t, filepath.Join("..", "..", "examples", example))
		if got != want {
			t.Errorf("%s doesn't expand like examples/%s:\n%s\n---\n%s", fixture, example, got, want)
		}
	}
}

func expanded(t *testing.T, path string) string {
	t.Helper()
	f, err := spec.Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.Scenario, f.Scenarios = nil, nil
	low, err := expand.Expand(f)
	if err != nil {
		t.Fatal(err)
	}
	// The builder starts on the newest pins; the examples may lag.
	low.Versions = nil
	b, err := json.MarshalIndent(low, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	// Keys and certificates are new on every expand; labels name lines.
	return volatile.ReplaceAllString(string(b), "")
}

var volatile = regexp.MustCompile(`-----BEGIN [A-Z ]+-----[^-]*-----END [A-Z ]+-----|\(line \d+\)`)

// TestBuilderOpensExamples: every example opens on the canvas, an
// untouched canvas changes nothing, and edits made on the canvas patch
// the file so that it still checks and reads back as the canvas shows.
func TestBuilderOpensExamples(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	files, _ := filepath.Glob("../../examples/*.yaml")
	if len(files) < 15 {
		t.Fatalf("%d examples", len(files))
	}
	type item struct {
		Name   string          `json:"name"`
		Tree   json.RawMessage `json:"tree"`
		Source json.RawMessage `json:"source,omitempty"`
		Want   json.RawMessage `json:"want,omitempty"`
	}
	type result struct {
		Name  string          `json:"name"`
		Error string          `json:"error"`
		Noop  []labfile.Op    `json:"noop"`
		Raw   bool            `json:"raw"`
		Ops   []labfile.Op    `json:"ops"`
		Want  json.RawMessage `json:"want"`
		Same  bool            `json:"same"`
		Got   json.RawMessage `json:"got"`
	}
	run := func(items []item, verify bool) map[string]result {
		dir := t.TempDir()
		in, out := filepath.Join(dir, "in.json"), filepath.Join(dir, "out.json")
		b, _ := json.Marshal(items)
		if err := os.WriteFile(in, b, 0o600); err != nil {
			t.Fatal(err)
		}
		args := []string{filepath.Join(fixtures, "roundtrip.js"), in, out}
		if verify {
			args = append(args, "verify")
		}
		if o, err := exec.Command(node, args...).CombinedOutput(); err != nil {
			t.Fatalf("roundtrip.js: %v\n%s", err, o)
		}
		var res []result
		b, _ = os.ReadFile(out)
		if err := json.Unmarshal(b, &res); err != nil {
			t.Fatal(err)
		}
		m := map[string]result{}
		for _, r := range res {
			m[r.Name] = r
		}
		return m
	}
	texts := map[string]string{}
	var items []item
	for _, f := range files {
		b, _ := os.ReadFile(f)
		name := filepath.Base(f)
		texts[name] = string(b)
		c := labfile.Check(string(b), f)
		if !c.OK {
			t.Errorf("%s doesn't check: %v", name, c.Problems)
			continue
		}
		tree, _ := labfile.TreeJSON(c.Merged)
		src, _ := labfile.TreeJSON(string(b))
		items = append(items, item{Name: name, Tree: tree, Source: src})
	}
	// And a file with no scenario, so that the edit renames a node too.
	const inline = `# A lab with comments.
parker: v1alpha1
name: rt
versions: { consul: 1.22.7, envoy: 1.35.12 }
consul:
  defaults:
    service_template: { run: fake-service, connect: sidecar } # the mesh
  datacenters:
    dc1:
      servers: 1
      nodes:
        # the front end
        web:
          service:
            port: 8080
            upstreams: { api: 9091 } # calls api
        api: { service: { port: 9090 } }
`
	texts["inline.yaml"] = inline
	{
		c := labfile.Check(inline, "")
		tree, _ := labfile.TreeJSON(c.Merged)
		src, _ := labfile.TreeJSON(inline)
		items = append(items, item{Name: "inline.yaml", Tree: tree, Source: src})
	}
	edited := 0
	var back []item
	for _, it := range items {
		r := run([]item{it}, false)[it.Name]
		if r.Error != "" {
			t.Errorf("%s: %s", it.Name, r.Error)
			continue
		}
		if len(r.Noop) > 0 {
			t.Errorf("%s: an untouched canvas makes ops: %+v", it.Name, r.Noop)
		}
		if r.Ops == nil {
			continue
		}
		f := filepath.Join("..", "..", "examples", it.Name)
		got, err := labfile.Patch(texts[it.Name], r.Ops)
		if err != nil {
			t.Errorf("%s: patch: %v\nops: %+v", it.Name, err, r.Ops)
			continue
		}
		c := labfile.Check(got, f)
		if it.Name == "inline.yaml" {
			for _, l := range []string{"# A lab with comments.", "        # the front end", "        web-renamed:", "            upstreams: { api: 9091, added: 9092 } # calls api"} {
				if !strings.Contains(got, l+"\n") {
					t.Errorf("inline: no line %q in\n%s", l, got)
				}
			}
		}
		if !c.OK {
			t.Errorf("%s: the edited file doesn't check: %v\n%s", it.Name, c.Problems, got)
			continue
		}
		tree, _ := labfile.TreeJSON(c.Merged)
		back = append(back, item{Name: it.Name, Tree: tree, Want: r.Want})
		edited++
	}
	t.Logf("%d examples took canvas edits", edited)
	if edited < 12 {
		t.Errorf("only %d examples took canvas edits", edited)
	}
	want := map[string]json.RawMessage{}
	for _, it := range back {
		want[it.Name] = it.Want
	}
	for name, r := range run(back, true) {
		if !r.Same {
			t.Errorf("%s: the edited file reads back as\n%s\nnot\n%s", name, r.Got, want[name])
		}
	}
}

// TestBuildModelUnits runs testdata/builder/units.js, unit checks of
// buildmodel.js helpers.
func TestBuildModelUnits(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	if out, err := exec.Command(node, filepath.Join(fixtures, "units.js")).CombinedOutput(); err != nil {
		t.Fatalf("units.js: %v\n%s", err, out)
	}
}
