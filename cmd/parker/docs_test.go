package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/spec"
)

// The user guide (docs/guide) must keep up with the CLI: every command a
// user can see has a section in the command reference, which mentions
// each of its flags. Adding a command or flag without documenting it
// fails here.
func TestCLIReferenceCoversEveryCommandAndFlag(t *testing.T) {
	b, err := os.ReadFile("../../docs/guide/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	sections := map[string]string{}
	parts := regexp.MustCompile(`(?m)^#{2,3} `).Split(string(b), -1)
	for _, p := range parts {
		title, body, _ := strings.Cut(p, "\n")
		sections[strings.TrimSpace(title)] = body
	}
	globals := sections["Global flags"]
	newRoot().PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if !strings.Contains(globals, "`--"+f.Name) {
			t.Errorf("cli.md: global flag --%s isn't under Global flags", f.Name)
		}
	})
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" {
				continue
			}
			path := sub.CommandPath()
			body, ok := sections[path]
			if !ok {
				t.Errorf("cli.md: no \"### %s\" section", path)
				continue
			}
			sub.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
				if f.Name == "help" || f.Hidden {
					return
				}
				if !strings.Contains(body, "`--"+f.Name+"`") {
					t.Errorf("cli.md: %s doesn't mention --%s", path, f.Name)
				}
			})
			if sub.Name() != "completion" { // cobra's own subcommands
				walk(sub)
			}
		}
	}
	walk(newRoot())
}

// Every complete lab file in the guide (a yaml block starting with
// `parker: v1alpha1`) must load and expand, so examples don't rot.
func TestGuideLabFilesValidate(t *testing.T) {
	files, err := filepath.Glob("../../docs/guide/*.md")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile("(?s)```yaml\n(.*?)```")
	n := 0
	for _, doc := range files {
		b, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		for i, m := range block.FindAllStringSubmatch(string(b), -1) {
			text := m[1]
			if !strings.HasPrefix(text, "parker: v1alpha1") {
				continue
			}
			if strings.Contains(text, "kind: Module") || strings.Contains(text, "module:") || strings.Contains(text, "file:") {
				continue // needs files next to it
			}
			n++
			path := filepath.Join(t.TempDir(), "lab.yaml")
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := spec.Load(path, nil)
			if err == nil {
				_, err = expand.Expand(f)
			}
			if err != nil {
				t.Errorf("%s, yaml block %d: %v", filepath.Base(doc), i+1, err)
			}
		}
	}
	if n < 10 {
		t.Errorf("only %d lab files found in the guide", n)
	}
}
