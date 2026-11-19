// Command genschema writes the JSON Schema for Parker topology files,
// generated from the Go structs in internal/spec with their doc comments as
// descriptions. Run via `go generate ./internal/spec`.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/invopop/jsonschema"

	"github.com/Ranjandas/parker/internal/spec"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: genschema OUT (run in internal/spec)")
		os.Exit(2)
	}
	b, err := generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[1], b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// generate builds the schema; the working directory must be internal/spec.
func generate() ([]byte, error) {
	r := &jsonschema.Reflector{
		FieldNameTag:   "yaml",
		ExpandedStruct: true,
		Mapper:         mapper,
	}
	if err := r.AddGoComments("github.com/Ranjandas/parker/internal/spec", "."); err != nil {
		return nil, err
	}
	s := r.Reflect(&spec.File{})
	over := r.Reflect(&spec.ServerOverride{})
	over.Version, over.ID = "", ""
	s.Definitions["ServerOverride"] = over
	s.ID = spec.SchemaURL
	s.Title = "Parker topology file (" + spec.SchemaVersion + ")"
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// mapper describes the types that accept more than one YAML shape.
func mapper(t reflect.Type) *jsonschema.Schema {
	switch t {
	case reflect.TypeOf(spec.Command{}):
		return &jsonschema.Schema{
			Description: "A command: a string split on whitespace, or an argv list.",
			OneOf: []*jsonschema.Schema{
				{Type: "string"},
				{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
			},
		}
	case reflect.TypeOf(spec.Script{}):
		return &jsonschema.Schema{
			Description: "A command: a string run with sh -c, or an argv list.",
			OneOf: []*jsonschema.Schema{
				{Type: "string"},
				{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
			},
		}
	case reflect.TypeOf(spec.NodeGroup{}):
		return &jsonschema.Schema{
			Description: "A selector, or a list of selectors.",
			OneOf: []*jsonschema.Schema{
				{Type: "string"},
				{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
			},
		}
	case reflect.TypeOf(spec.Servers{}):
		over := &jsonschema.Schema{
			Type:                 "object",
			AdditionalProperties: jsonschema.FalseSchema,
			Properties:           jsonschema.NewProperties(),
		}
		over.Properties.Set("count", &jsonschema.Schema{Type: "integer", Minimum: "1", Description: "Number of servers."})
		over.Properties.Set("overrides", &jsonschema.Schema{
			Type:                 "object",
			Description:          "Per-server settings, keyed by server name (server-1, server-2, ...).",
			PropertyNames:        &jsonschema.Schema{Pattern: "^server-[0-9]+$"},
			AdditionalProperties: &jsonschema.Schema{Ref: "#/$defs/ServerOverride"},
		})
		return &jsonschema.Schema{
			Description: "Number of Consul servers, or {count, overrides}.",
			OneOf: []*jsonschema.Schema{
				{Type: "integer", Minimum: "1"},
				over,
			},
		}
	}
	return nil
}
