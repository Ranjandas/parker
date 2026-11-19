package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Ranjandas/parker/internal/spec"
)

// patterns are the starter files `parker init` can write. NAME and SCHEMA
// are filled in.
var patterns = map[string]string{
	"minimal": `# yaml-language-server: $schema=SCHEMA
# One datacenter with three Consul servers.
parker: v1alpha1
name: NAME
versions: { consul: 1.22.7 }
consul:
  datacenters:
    dc1:
      servers: 3
`,
	"clients": `# yaml-language-server: $schema=SCHEMA
# Three servers, two nodes running an echo service, and one bare client.
parker: v1alpha1
name: NAME
versions: { consul: 1.22.7 }
consul:
  datacenters:
    dc1:
      servers: 3
      nodes:
        web:
          count: 2
          service: { port: 8080, run: fake-service, env: { MESSAGE: hello } }
        client: {}
`,
	"raw": `# yaml-language-server: $schema=SCHEMA
# Low level only: a bridge and a namespace running a process.
parker: v1alpha1
name: NAME
networks:
  lan: { subnet: 10.50.0.0/24 }
nodes:
  box:
    networks: { lan: { ip: 10.50.0.10 } }
    processes:
      sleeper: { run: sleep infinity, restart: always }
`,
}

func patternNames() []string {
	names := make([]string, 0, len(patterns))
	for k := range patterns {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

func renderPattern(pattern, name, schema string) (string, error) {
	t, ok := patterns[pattern]
	if !ok {
		return "", fmt.Errorf("unknown pattern %q (have %s)", pattern, strings.Join(patternNames(), ", "))
	}
	return strings.NewReplacer("SCHEMA", schema, "NAME", name).Replace(t), nil
}

func initCmd() *cobra.Command {
	var pattern, schema string
	var force bool
	cmd := &cobra.Command{
		Use:   "init [FILE]",
		Short: "Write a starter topology file (default parker.yaml)",
		Long: "Write a starter topology file. Its first line points editors running\n" +
			"yaml-language-server (VS Code YAML, Neovim, ...) at the schema, for\n" +
			"autocomplete and hover docs.\n\nPatterns: " + strings.Join(patternNames(), ", "),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "parker.yaml"
			if len(args) == 1 {
				path = args[0]
			}
			name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			if name == "parker" {
				if wd, err := os.Getwd(); err == nil {
					name = filepath.Base(wd)
				}
			}
			name = labNameFrom(name)
			text, err := renderPattern(pattern, name, schema)
			if err != nil {
				return err
			}
			flag := os.O_WRONLY | os.O_CREATE | os.O_EXCL
			if force {
				flag = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			}
			f, err := os.OpenFile(path, flag, 0o644)
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%s exists (use --force to overwrite)", path)
			}
			if err != nil {
				return err
			}
			if _, err := f.WriteString(text); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			say("wrote %s (pattern %s); next: parker up %s", path, pattern, path)
			return nil
		},
	}
	cmd.Flags().StringVarP(&pattern, "pattern", "p", "minimal", "starter: "+strings.Join(patternNames(), ", "))
	cmd.Flags().StringVar(&schema, "schema", spec.SchemaURL, "schema URL or path for the editor modeline (see `parker schema`)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite FILE")
	return cmd
}

// labNameFrom turns a file or directory name into a valid lab name.
func labNameFrom(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	n := strings.Trim(b.String(), "-")
	if len(n) > 24 {
		n = strings.Trim(n[:24], "-")
	}
	if n == "" {
		n = "lab"
	}
	return n
}

func schemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Print the JSON Schema for topology files",
		Long: "Print the JSON Schema for topology files. Save it next to your files and\n" +
			"point the editor at it, e.g. `parker init --schema ./parker.schema.json`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := os.Stdout.Write(spec.Schema)
			return err
		},
	}
}
