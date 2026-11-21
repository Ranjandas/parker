package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// schemaMarker in a guide page is replaced by the lab file reference.
const schemaMarker = "<!-- schema-reference -->"

type jsonSchema struct {
	Ref                  string                 `json:"$ref"`
	Type                 any                    `json:"type"`
	Description          string                 `json:"description"`
	Properties           map[string]*jsonSchema `json:"properties"`
	AdditionalProperties json.RawMessage        `json:"additionalProperties"`
	Items                *jsonSchema            `json:"items"`
	OneOf                []*jsonSchema          `json:"oneOf"`
	AnyOf                []*jsonSchema          `json:"anyOf"`
	Enum                 []any                  `json:"enum"`
	Const                any                    `json:"const"`
	Required             []string               `json:"required"`
	Defs                 map[string]*jsonSchema `json:"$defs"`
	order                []string
}

// UnmarshalJSON keeps the properties' order as written.
func (s *jsonSchema) UnmarshalJSON(b []byte) error {
	type plain jsonSchema
	if err := json.Unmarshal(b, (*plain)(s)); err != nil {
		return err
	}
	var raw struct {
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || raw.Properties == nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw.Properties)))
	if _, err := dec.Token(); err != nil {
		return err
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		s.order = append(s.order, t.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	return nil
}

func (s *jsonSchema) additional() *jsonSchema {
	if len(s.AdditionalProperties) == 0 || string(s.AdditionalProperties) == "false" || string(s.AdditionalProperties) == "true" {
		return nil
	}
	var a jsonSchema
	if json.Unmarshal(s.AdditionalProperties, &a) != nil {
		return nil
	}
	return &a
}

type refGen struct {
	defs    map[string]*jsonSchema
	anchors map[string]string // def name -> anchor of its section
	out     strings.Builder
	queue   []section
}

type section struct {
	path   string // display path, e.g. consul.datacenters.<dc>
	anchor string
	s      *jsonSchema
	level  int
}

// SchemaKeys lists every property name in the schema, for the docs check.
func SchemaKeys(path string) ([]string, error) {
	root, err := readSchema(path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var walk func(s *jsonSchema)
	walk = func(s *jsonSchema) {
		if s == nil {
			return
		}
		for name, p := range s.Properties {
			seen[name] = true
			walk(p)
		}
		walk(s.additional())
		walk(s.Items)
		for _, v := range append(s.OneOf, s.AnyOf...) {
			walk(v)
		}
	}
	walk(root)
	for _, d := range root.Defs {
		walk(d)
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

func readSchema(path string) (*jsonSchema, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root jsonSchema
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &root, nil
}

// SchemaReference renders the lab file reference: a section for every
// object in the schema, with a table of its keys.
func SchemaReference(path string) (string, error) {
	root, err := readSchema(path)
	if err != nil {
		return "", err
	}
	g := &refGen{defs: root.Defs, anchors: map[string]string{}}
	g.queue = append(g.queue, section{path: "Top level", anchor: "top-level", s: root, level: 2})
	for len(g.queue) > 0 {
		sec := g.queue[0]
		g.queue = g.queue[1:]
		g.section(sec)
	}
	return g.out.String(), nil
}

func (g *refGen) resolve(s *jsonSchema) (*jsonSchema, string) {
	if s != nil && s.Ref != "" {
		name := strings.TrimPrefix(s.Ref, "#/$defs/")
		return g.defs[name], name
	}
	return s, ""
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

func anchorFor(path string) string {
	return strings.Trim(nonWord.ReplaceAllString(strings.ToLower(path), "-"), "-")
}

// object returns the object schema a property leads to (through refs,
// oneOf variants, maps and lists), with the path suffix to get there.
func (g *refGen) object(s *jsonSchema) (obj *jsonSchema, def, suffix string) {
	s, def = g.resolve(s)
	if s == nil {
		return nil, "", ""
	}
	if len(s.Properties) > 0 {
		return s, def, ""
	}
	for _, v := range append(s.OneOf, s.AnyOf...) {
		if o, d, suf := g.object(v); o != nil {
			return o, d, suf
		}
	}
	if a := s.additional(); a != nil {
		if o, d, suf := g.object(a); o != nil {
			return o, d, ".<name>" + suf
		}
	}
	if s.Items != nil {
		if o, d, suf := g.object(s.Items); o != nil {
			return o, d, "[]" + suf
		}
	}
	return nil, "", ""
}

func (g *refGen) typeOf(s *jsonSchema) string {
	s, _ = g.resolve(s)
	if s == nil {
		return ""
	}
	if s.Const != nil {
		return fmt.Sprintf("`%v`", s.Const)
	}
	if len(s.Enum) > 0 {
		var vs []string
		for _, e := range s.Enum {
			vs = append(vs, fmt.Sprintf("`%v`", e))
		}
		return strings.Join(vs, ", ")
	}
	if vs := append(s.OneOf, s.AnyOf...); len(vs) > 0 {
		var ts []string
		for _, v := range vs {
			ts = append(ts, g.typeOf(v))
		}
		return strings.Join(ts, " or ")
	}
	t, _ := s.Type.(string)
	switch {
	case t == "array" && s.Items != nil:
		return "list of " + g.typeOf(s.Items)
	case t == "object" && len(s.Properties) == 0 && s.additional() != nil:
		return "map of " + g.typeOf(s.additional())
	case t == "object" || len(s.Properties) > 0:
		return "object"
	case t == "integer":
		return "number"
	case t != "":
		return t
	}
	return "any"
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(s, "|", `\|`)
}

func (g *refGen) section(sec section) {
	s := sec.s
	title := "`" + sec.path + "`"
	if sec.path == "Top level" {
		title = sec.path
	}
	fmt.Fprintf(&g.out, "\n%s %s {#%s}\n\n", strings.Repeat("#", sec.level), title, sec.anchor)
	if s.Description != "" && sec.path != "Top level" {
		g.out.WriteString(oneLine(s.Description) + "\n\n")
	}
	g.out.WriteString("| Key | Type | Description |\n|---|---|---|\n")
	keys := s.order
	if len(keys) == 0 {
		for k := range s.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	for _, k := range keys {
		p := s.Properties[k]
		name := "`" + k + "`"
		if obj, def, suffix := g.object(p); obj != nil {
			childPath := k + suffix
			if sec.path != "Top level" {
				childPath = sec.path + "." + childPath
			}
			anchor, done := g.anchors[def]
			if def == "" || !done {
				anchor = anchorFor(childPath)
				if def != "" {
					g.anchors[def] = anchor
				}
				g.queue = append(g.queue, section{path: childPath, anchor: anchor, s: obj, level: 3})
			}
			name = "[`" + k + "`](#" + anchor + ")"
		}
		desc := p.Description
		if desc == "" {
			if r, _ := g.resolve(p); r != nil {
				desc = r.Description
			}
		}
		fmt.Fprintf(&g.out, "| %s | %s | %s |\n", name, oneLine(g.typeOf(p)), oneLine(desc))
	}
}
