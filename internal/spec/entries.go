package spec

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/parser"
)

// resolveConfigEntries records each config entry's YAML line and reads
// file: entries (relative to dir). JSON files become inline entries; HCL
// is kept as text for `consul config write`.
func resolveConfigEntries(f *File, text []byte, dir string) error {
	if f.Consul == nil {
		return nil
	}
	ast, _ := parser.ParseBytes(text, 0)
	for _, name := range slices.Sorted(maps.Keys(f.Consul.Datacenters)) {
		dc := f.Consul.Datacenters[name]
		if dc == nil || len(dc.ConfigEntries) == 0 {
			continue
		}
		dc.EntrySources = make([]EntrySource, len(dc.ConfigEntries))
		for i, e := range dc.ConfigEntries {
			at := fmt.Sprintf("consul.datacenters.%s.config_entries[%d]", name, i)
			src := &dc.EntrySources[i]
			if ast != nil {
				if p, err := yaml.PathString(fmt.Sprintf("$.consul.datacenters.%s.config_entries[%d]", name, i)); err == nil {
					if n, err := p.FilterFile(ast); err == nil && n != nil {
						src.Line = n.GetToken().Position.Line
					}
				}
			}
			file, ok := e["file"].(string)
			if !ok {
				continue
			}
			if len(e) != 1 {
				return fmt.Errorf("%s: file: entries take no other keys", at)
			}
			if !filepath.IsAbs(file) {
				file = filepath.Join(dir, file)
			}
			b, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("%s: %w", at, err)
			}
			src.File = file
			switch strings.ToLower(filepath.Ext(file)) {
			case ".json":
				var m ConfigEntry
				if err := json.Unmarshal(b, &m); err != nil {
					return fmt.Errorf("%s: %s: %w", at, file, err)
				}
				dc.ConfigEntries[i] = m
			case ".hcl":
				src.HCL = string(b)
				dc.ConfigEntries[i] = ConfigEntry{"file": e["file"]}
			default:
				return fmt.Errorf("%s: %s: want a .hcl or .json file", at, file)
			}
		}
	}
	return nil
}

// Label names a config entry in messages, e.g.
// "consul.datacenters.dc1.config_entries[2] (line 14)".
func (dc *Datacenter) EntryLabel(dcName string, i int) string {
	l := fmt.Sprintf("consul.datacenters.%s.config_entries[%d]", dcName, i)
	if i < len(dc.EntrySources) {
		s := dc.EntrySources[i]
		switch {
		case s.File != "" && s.Line > 0:
			l += fmt.Sprintf(" (line %d, %s)", s.Line, s.File)
		case s.Line > 0:
			l += fmt.Sprintf(" (line %d)", s.Line)
		}
	}
	return l
}

// entryKind is the entry's Kind, in either casing.
func entryKind(e ConfigEntry) string {
	for _, k := range []string{"Kind", "kind"} {
		if s, ok := e[k].(string); ok {
			return s
		}
	}
	return ""
}
