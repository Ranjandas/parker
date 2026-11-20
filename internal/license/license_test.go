package license

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestIsEnterprise(t *testing.T) {
	for v, want := range map[string]bool{"1.22.7+ent": true, "1.22.7+ent.fips1402": true, "1.22.7": false, "path:/x": false, "1.22.7+entx": false} {
		if IsEnterprise(v) != want {
			t.Errorf("IsEnterprise(%q) = %v", v, !want)
		}
	}
}

func TestNeeded(t *testing.T) {
	if why := Needed(&spec.File{Versions: map[string]string{"consul": "1.22.7", "envoy": "1.35.12"}}); len(why) != 0 {
		t.Errorf("OSS lab: %q", why)
	}
	f := &spec.File{
		Versions: map[string]string{"consul": "1.22.7+ent"},
		Nodes:    map[string]*spec.RawNode{"n": {Processes: map[string]*spec.Process{"c": {Version: "1.21.5+ent"}}}},
		License:  &spec.License{File: "/x"},
	}
	got := strings.Join(Needed(f, "1.22.8+ent", "1.22.8"), "; ")
	want := "versions.consul is 1.22.7+ent; nodes.n.processes.c.version is 1.21.5+ent; the scenario runs 1.22.8+ent; license: is set"
	if got != want {
		t.Errorf("Needed:\n got %s\nwant %s", got, want)
	}
}

func TestResolveOrder(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.hclic"), filepath.Join(dir, "b.hclic")
	os.WriteFile(a, []byte("  from-yaml\n"), 0o600)
	os.WriteFile(b, []byte("from-path\n"), 0o600)
	all := env(map[string]string{"CONSUL_LICENSE_PATH": b, "CONSUL_LICENSE": "from-env"})
	for _, c := range []struct {
		f    *spec.File
		env  func(string) string
		want string
	}{
		{&spec.File{License: &spec.License{File: a}}, all, "from-yaml"}, // the file's own license wins
		{&spec.File{}, all, "from-path"},
		{&spec.File{}, env(map[string]string{"CONSUL_LICENSE": "from-env"}), "from-env"},
	} {
		lic, _, err := Resolve(c.f, c.env)
		if err != nil || string(lic) != c.want {
			t.Errorf("Resolve = %q, %v; want %q", lic, err, c.want)
		}
	}
	if _, _, err := Resolve(&spec.File{}, env(nil)); err != ErrMissing {
		t.Errorf("no license: %v", err)
	}
	if _, _, err := Resolve(&spec.File{}, env(map[string]string{"CONSUL_LICENSE_PATH": filepath.Join(dir, "nope")})); err == nil || !strings.Contains(err.Error(), "CONSUL_LICENSE_PATH") {
		t.Errorf("missing file: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, []byte("\n"), 0o600)
	if _, _, err := Resolve(&spec.File{}, env(map[string]string{"CONSUL_LICENSE_PATH": empty})); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty file: %v", err)
	}
}

func TestFor(t *testing.T) {
	oss := &spec.File{Versions: map[string]string{"consul": "1.22.7"}}
	if lic, err := For(oss, env(map[string]string{"CONSUL_LICENSE": "x"})); lic != nil || err != nil {
		t.Errorf("OSS lab takes no license: %q %v", lic, err)
	}
	ent := &spec.File{Versions: map[string]string{"consul": "1.22.7+ent"}}
	_, err := For(ent, env(nil))
	if err == nil || !strings.Contains(err.Error(), "versions.consul is 1.22.7+ent needs a Consul Enterprise license") || !strings.Contains(err.Error(), "CONSUL_LICENSE_PATH") {
		t.Errorf("missing license error: %v", err)
	}
	if lic, err := For(ent, env(map[string]string{"CONSUL_LICENSE": "abc"})); string(lic) != "abc" || err != nil {
		t.Errorf("For = %q %v", lic, err)
	}
}

func TestWriteRemoveRedact(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(Path(dir), []byte("old"), 0o644)
	if err := Write(dir, []byte("secret-license-text")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(Path(dir))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", st.Mode(), err)
	}
	if err := Remove(dir); err != nil || Remove(dir) != nil {
		t.Errorf("Remove: %v", err)
	}
	if _, err := os.Stat(Path(dir)); !os.IsNotExist(err) {
		t.Error("still there")
	}
	lic := []byte("02MV4UU43BK5HGYYTOJZWFQMTMNNEWU33J")
	if got := Redact("agent: bad license "+string(lic)+" here", lic); got != "agent: bad license [license] here" {
		t.Errorf("Redact: %q", got)
	}
	if got := Redact("short", []byte("ab")); got != "short" {
		t.Errorf("Redact short: %q", got)
	}
}
