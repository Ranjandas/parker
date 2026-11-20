package labfile

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/goccy/go-yaml"
)

// kv is a map entry; a map is []kv, so key order survives into JSON.
type kv struct {
	K string
	V any
}

// ordered is a map that marshals to a JSON object in its own order.
type ordered []kv

func (o ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, it := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(it.K)
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(toJSON(it.V))
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func toJSON(v any) any {
	switch x := v.(type) {
	case []kv:
		return ordered(x)
	case []any:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = toJSON(it)
		}
		return out
	}
	return v
}

// Tree decodes YAML keeping key order: maps are []kv.
func Tree(text string) (any, error) {
	var v any
	if err := yaml.UnmarshalWithOptions([]byte(text), &v, yaml.UseOrderedMap()); err != nil {
		return nil, err
	}
	return fromYAML(v), nil
}

func fromYAML(v any) any {
	switch x := v.(type) {
	case yaml.MapSlice:
		out := make([]kv, 0, len(x))
		for _, it := range x {
			out = append(out, kv{fmt.Sprint(it.Key), fromYAML(it.Value)})
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = fromYAML(it)
		}
		return out
	}
	return v
}

// TreeJSON is Tree as JSON, objects in the file's key order.
func TreeJSON(text string) (json.RawMessage, error) {
	t, err := Tree(text)
	if err != nil {
		return nil, err
	}
	return json.Marshal(toJSON(t))
}
