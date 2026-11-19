package main

import (
	"bytes"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

// The committed schema must match the structs.
func TestSchemaUpToDate(t *testing.T) {
	t.Chdir("../../internal/spec")
	b, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, spec.Schema) {
		t.Fatal("internal/spec/parker.schema.json is stale: run `go generate ./internal/spec`")
	}
}
