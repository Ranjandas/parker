package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const entriesLab = `parker: v1alpha1
versions: {consul: 1.22.7}
consul:
  datacenters:
    dc1:
      servers: 1
      config_entries:
        - Kind: proxy-defaults
          Name: global
        - file: entries/a.json
        - file: entries/b.hcl
`

func entriesDir(t *testing.T, lab string) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "entries"), 0o755)
	os.WriteFile(filepath.Join(dir, "entries", "a.json"), []byte(`{"Kind": "service-defaults", "Name": "web", "Protocol": "http"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "entries", "b.hcl"), []byte("Kind = \"service-intentions\"\nName = \"web\"\n"), 0o644)
	p := filepath.Join(dir, "lab.yaml")
	os.WriteFile(p, []byte(lab), 0o644)
	return p
}

func TestConfigEntries(t *testing.T) {
	f, err := Load(entriesDir(t, entriesLab), nil)
	if err != nil {
		t.Fatal(err)
	}
	dc := f.Consul.Datacenters["dc1"]
	if len(dc.ConfigEntries) != 3 || len(dc.EntrySources) != 3 {
		t.Fatalf("entries %v sources %v", dc.ConfigEntries, dc.EntrySources)
	}
	if dc.ConfigEntries[1]["Kind"] != "service-defaults" || dc.ConfigEntries[1]["Protocol"] != "http" {
		t.Errorf("json file not inlined: %v", dc.ConfigEntries[1])
	}
	if !strings.Contains(dc.EntrySources[2].HCL, "service-intentions") {
		t.Errorf("hcl text: %q", dc.EntrySources[2].HCL)
	}
	for i, line := range []int{8, 10, 11} {
		if dc.EntrySources[i].Line != line {
			t.Errorf("entry %d line %d, want %d", i, dc.EntrySources[i].Line, line)
		}
	}
	if l := dc.EntryLabel("dc1", 0); l != "consul.datacenters.dc1.config_entries[0] (line 8)" {
		t.Errorf("label %q", l)
	}
	if l := dc.EntryLabel("dc1", 2); !strings.HasSuffix(l, "(line 11, "+dc.EntrySources[2].File+")") {
		t.Errorf("label %q", l)
	}
}

func TestConfigEntryErrors(t *testing.T) {
	for _, c := range []struct{ name, entry, want string }{
		{"no kind", "- Name: web", "config_entries[0] (line 8): needs Kind"},
		{"missing file", "- file: entries/nope.json", "nope.json"},
		{"bad extension", "- file: lab.yaml", "want a .hcl or .json file"},
		{"file plus keys", "- {file: entries/a.json, Name: x}", "file: entries take no other keys"},
	} {
		t.Run(c.name, func(t *testing.T) {
			lab := strings.Replace(entriesLab, `        - Kind: proxy-defaults
          Name: global
        - file: entries/a.json
        - file: entries/b.hcl
`, "        "+c.entry+"\n", 1)
			_, err := Load(entriesDir(t, lab), nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err %v, want %q", err, c.want)
			}
		})
	}
}
