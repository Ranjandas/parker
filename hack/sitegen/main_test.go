package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	guideDir   = "../../docs/guide"
	schemaFile = "../../internal/spec/parker.schema.json"
)

// The guide builds: every page is in nav.yaml, has front matter, and
// every link between pages lands on a page and heading that exist.
func TestGuideBuilds(t *testing.T) {
	pages, err := Load(guideDir, schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) < 20 {
		t.Fatalf("only %d pages", len(pages))
	}
	if err := build("../../site", guideDir, schemaFile, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// keysOnlyInReference are schema keys users never write by hand; the
// generated reference describes them and that is enough.
var keysOnlyInReference = map[string]bool{
	"labels": true, "group": true, "role": true, "namespaces": true,
}

// Every lab file key is explained somewhere in the guide's own pages, not
// only in the generated reference, so a new key can't ship undocumented.
func TestGuideExplainsEverySchemaKey(t *testing.T) {
	keys, err := SchemaKeys(schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(guideDir, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, f := range files {
		if filepath.Base(f) == "lab-file-reference.md" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
	}
	text := all.String()
	for _, k := range keys {
		if keysOnlyInReference[k] {
			continue
		}
		// `key`, key: in YAML, or `parent.key` in a table.
		if !strings.Contains(text, "`"+k+"`") && !strings.Contains(text, k+":") && !strings.Contains(text, "."+k+"`") {
			t.Errorf("lab file key %q isn't explained anywhere in docs/guide", k)
		}
	}
}
