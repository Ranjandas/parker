package spec

import _ "embed"

// Schema is the JSON Schema for topology files, generated from these
// structs by `go generate ./internal/spec`.
//
//go:embed parker.schema.json
var Schema []byte

// SchemaURL is where releases publish Schema; `parker init` points editors
// at it.
const SchemaURL = "https://github.com/Ranjandas/parker/releases/latest/download/parker.schema.json"
