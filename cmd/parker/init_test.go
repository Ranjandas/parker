package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestPatternsLoad(t *testing.T) {
	dir := t.TempDir()
	for _, p := range patternNames() {
		text, err := renderPattern(p, "demo-"+p, spec.SchemaURL)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(text, "# yaml-language-server: $schema="+spec.SchemaURL+"\n") {
			t.Errorf("%s: no modeline", p)
		}
		path := filepath.Join(dir, p+".yaml")
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := spec.Load(path, nil)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if f.Name != "demo-"+p {
			t.Errorf("%s: name %q", p, f.Name)
		}
	}
}

func TestLabNameFrom(t *testing.T) {
	for in, want := range map[string]string{
		"My Lab":                            "my-lab",
		"consul_test":                       "consul-test",
		"--x--":                             "x",
		"___":                               "lab",
		"a-very-long-directory-name-indeed": "a-very-long-directory-na",
	} {
		if got := labNameFrom(in); got != want {
			t.Errorf("labNameFrom(%q) = %q, want %q", in, got, want)
		}
	}
}
