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
	b, err := json.MarshalIndent(low, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	// Keys and certificates are new on every expand; labels name lines.
	return volatile.ReplaceAllString(string(b), "")
}

var volatile = regexp.MustCompile(`-----BEGIN [A-Z ]+-----[^-]*-----END [A-Z ]+-----|\(line \d+\)`)
