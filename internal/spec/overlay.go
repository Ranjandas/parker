package spec

import (
	"fmt"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
)

// AppendDirective, as the first item of a list in an overlay, appends the
// rest of the list to the base's list instead of replacing it.
const AppendDirective = "$patch: append"

// Merge deep-merges overlay into base: maps merge by key, a null removes
// a key the base has, lists and scalars replace (lists append when they start with
// {$patch: append}).
func Merge(base, overlay any) any {
	switch o := overlay.(type) {
	case yaml.MapSlice:
		b, ok := base.(yaml.MapSlice)
		if !ok {
			return stripDirectives(o)
		}
		out := slices.Clone(b)
		for _, it := range o {
			i := slices.IndexFunc(out, func(m yaml.MapItem) bool { return mapKey(m.Key) == mapKey(it.Key) })
			switch {
			case it.Value == nil && i >= 0:
				out = slices.Delete(out, i, i+1)
			case i >= 0:
				out[i].Value = Merge(out[i].Value, it.Value)
			default:
				out = append(out, yaml.MapItem{Key: it.Key, Value: stripDirectives(it.Value)})
			}
		}
		return out
	case []any:
		if len(o) > 0 && isAppend(o[0]) {
			b, _ := base.([]any)
			return append(slices.Clone(b), stripDirectives(o[1:]).([]any)...)
		}
		return stripDirectives(o)
	}
	return overlay
}

// dedupe keeps the last of repeated keys, as YAML merge keys (<<: *x)
// leave them after decoding into a MapSlice.
func dedupe(v any) any {
	switch t := v.(type) {
	case yaml.MapSlice:
		out := make(yaml.MapSlice, 0, len(t))
		for _, it := range t {
			it.Value = dedupe(it.Value)
			if i := slices.IndexFunc(out, func(m yaml.MapItem) bool { return mapKey(m.Key) == mapKey(it.Key) }); i >= 0 {
				out[i].Value = it.Value
				continue
			}
			out = append(out, it)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = dedupe(x)
		}
		return out
	}
	return v
}

func isAppend(v any) bool {
	m, ok := v.(yaml.MapSlice)
	return ok && len(m) == 1 && mapKey(m[0].Key) == "$patch" && fmt.Sprint(m[0].Value) == "append"
}

// stripDirectives drops a leading {$patch: append} from lists that have
// nothing to append to.
func stripDirectives(v any) any {
	switch t := v.(type) {
	case yaml.MapSlice:
		out := make(yaml.MapSlice, 0, len(t))
		for _, it := range t {
			out = append(out, yaml.MapItem{Key: it.Key, Value: stripDirectives(it.Value)})
		}
		return out
	case []any:
		if len(t) > 0 && isAppend(t[0]) {
			t = t[1:]
		}
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = stripDirectives(x)
		}
		return out
	}
	return v
}

func mapKey(k any) string { return fmt.Sprint(k) }

// ApplySet applies one --set: [SELECTOR:]PATH=VALUE. PATH is dot-separated
// (consul.datacenters.dc1.servers); a selector prefix roots it at a
// datacenter (dc1:), a node or gateway (dc1/web:, dc1/team-a/billing:).
// VALUE is YAML, so 5, true, [a, b] and {port: 9000} keep their types.
func ApplySet(doc any, set string) (any, error) {
	lhs, val, ok := strings.Cut(set, "=")
	if !ok || lhs == "" {
		return nil, fmt.Errorf("--set %q: want PATH=VALUE or SELECTOR:PATH=VALUE", set)
	}
	var root []string
	path := lhs
	if sel, rest, ok := strings.Cut(lhs, ":"); ok {
		r, err := setRoot(doc, sel)
		if err != nil {
			return nil, fmt.Errorf("--set %q: %w", set, err)
		}
		root, path = r, rest
	}
	keys := append(root, strings.Split(path, ".")...)
	if slices.Contains(keys, "") {
		return nil, fmt.Errorf("--set %q: empty key in %q", set, lhs)
	}
	var v any
	if err := yaml.UnmarshalWithOptions([]byte(val), &v, yaml.UseOrderedMap()); err != nil {
		return nil, fmt.Errorf("--set %q: value: %w", set, err)
	}
	if strings.TrimSpace(val) == "" {
		v = ""
	}
	return setPath(doc, keys, v, set)
}

func setPath(node any, keys []string, v any, set string) (any, error) {
	if len(keys) == 0 {
		if v == nil {
			return nil, nil
		}
		return Merge(nil, v), nil
	}
	if node == true { // a gateway's mesh: true shorthand
		node = nil
	}
	m, ok := node.(yaml.MapSlice)
	if node != nil && !ok {
		return nil, fmt.Errorf("--set %q: %s is not a map", set, keys[0])
	}
	m = slices.Clone(m)
	i := slices.IndexFunc(m, func(it yaml.MapItem) bool { return mapKey(it.Key) == keys[0] })
	var child any
	if i >= 0 {
		child = m[i].Value
	}
	nv, err := setPath(child, keys[1:], v, set)
	if err != nil {
		return nil, err
	}
	switch {
	case nv == nil && len(keys) == 1 && i >= 0:
		return slices.Delete(m, i, i+1), nil
	case nv == nil && len(keys) == 1:
	case i >= 0:
		m[i].Value = nv
	default:
		m = append(m, yaml.MapItem{Key: keys[0], Value: nv})
	}
	return m, nil
}

// setRoot maps a selector prefix to the path of what it names in doc.
func setRoot(doc any, sel string) ([]string, error) {
	parts := strings.Split(sel, "/")
	dcPath := []string{"consul", "datacenters", parts[0]}
	dc, _ := lookup(doc, dcPath).(yaml.MapSlice)
	if parts[0] == "" || dc == nil {
		return nil, fmt.Errorf("no datacenter %q", parts[0])
	}
	switch len(parts) {
	case 1:
		return dcPath, nil
	case 2:
		var found [][]string
		for _, kind := range []string{"nodes", "gateways"} {
			if hasKey(lookup(dc, []string{kind}), parts[1]) {
				found = append(found, append(slices.Clone(dcPath), kind, parts[1]))
			}
			parts2, _ := lookup(dc, []string{"partitions"}).(yaml.MapSlice)
			for _, p := range parts2 {
				if hasKey(lookup(p.Value, []string{kind}), parts[1]) {
					found = append(found, append(slices.Clone(dcPath), "partitions", mapKey(p.Key), kind, parts[1]))
				}
			}
		}
		switch len(found) {
		case 0:
			return nil, fmt.Errorf("no node or gateway %q in %s", parts[1], parts[0])
		case 1:
			return found[0], nil
		}
		var where []string
		for _, f := range found {
			where = append(where, strings.Join(f, "."))
		}
		return nil, fmt.Errorf("%s matches %s; name the partition (dc/partition/name)", sel, strings.Join(where, " and "))
	case 3:
		for _, kind := range []string{"nodes", "gateways"} {
			if hasKey(lookup(dc, []string{"partitions", parts[1], kind}), parts[2]) {
				return append(slices.Clone(dcPath), "partitions", parts[1], kind, parts[2]), nil
			}
		}
		return nil, fmt.Errorf("no node or gateway %q in partition %s of %s", parts[2], parts[1], parts[0])
	}
	return nil, fmt.Errorf("selector %q: want dc, dc/name or dc/partition/name", sel)
}

func lookup(node any, keys []string) any {
	for _, k := range keys {
		m, ok := node.(yaml.MapSlice)
		if !ok {
			return nil
		}
		node = nil
		for _, it := range m {
			if mapKey(it.Key) == k {
				node = it.Value
				break
			}
		}
	}
	return node
}

// hasKey reports whether map m has key k, even with a null value
// (web: with nothing under it).
func hasKey(m any, k string) bool {
	ms, ok := m.(yaml.MapSlice)
	return ok && slices.ContainsFunc(ms, func(it yaml.MapItem) bool { return mapKey(it.Key) == k })
}
