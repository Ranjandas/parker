package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/Ranjandas/parker/internal/lab"
)

func TestSavedLabArg(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("mylab", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for arg, want := range map[string]bool{
		"minimal":          true,
		"mylab":            false, // a file by that name wins
		"minimal.yaml":     false,
		"lab.yml":          false,
		"examples/minimal": false,
		"./minimal":        false,
	} {
		if got := savedLabArg(arg); got != want {
			t.Errorf("savedLabArg(%q) = %v, want %v", arg, got, want)
		}
	}
}

// `parker up LAB` brings back exactly what the last up ran: the saved
// expanded topology loads and expands to the same lab, keeping a subnet
// up moved the network to (and its auto: mark, so it can move again).
func TestUpBySavedName(t *testing.T) {
	base := t.TempDir()
	t.Setenv("PARKER_RUN_DIR", base)
	_, low, err := load("../../examples/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var lan string
	for n, nw := range low.Networks {
		if nw.Auto {
			lan = n
		}
	}
	if lan == "" {
		t.Fatal("minimal has no auto network")
	}
	low.Networks[lan].Subnet = "10.9.0.0/24" // as if up had moved it
	b, err := yaml.MarshalWithOptions(low, yaml.UseLiteralStyleIfMultiline(true), yaml.IndentSequence(true))
	if err != nil {
		t.Fatal(err)
	}
	dir := lab.RunDir(low.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lab.TopologyFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir()) // no file called minimal here
	got, err := loadUp(context.Background(), low.Name, true)
	if err != nil {
		t.Fatal(err)
	}
	gb, err := yaml.MarshalWithOptions(got, yaml.UseLiteralStyleIfMultiline(true), yaml.IndentSequence(true))
	if err != nil {
		t.Fatal(err)
	}
	if string(gb) != string(b) {
		t.Errorf("re-expanded saved topology differs:\n--- saved\n%s\n--- loaded\n%s", b, gb)
	}
	if n := got.Networks[lan]; n.Subnet != "10.9.0.0/24" || !n.Auto {
		t.Errorf("%s: %+v, want the moved subnet, still auto", lan, n)
	}

	_, err = loadUp(context.Background(), "gone", true)
	if err == nil || !strings.Contains(err.Error(), "gone is not a file") || !strings.Contains(err.Error(), "no saved topology") {
		t.Errorf("missing lab: %v", err)
	}
}
