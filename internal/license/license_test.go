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
		Versions: map[string]string{"consul": "1.22.7+ent", "vault": "1.21.1+ent"},
		Nodes: map[string]*spec.RawNode{"n": {Processes: map[string]*spec.Process{
			"c": {Version: "1.21.5+ent"},
			"v": {Run: spec.Command{"vault", "server"}, Version: "1.20.4+ent"},
		}}},
		License: &spec.License{File: "/x"},
	}
	got := Needed(f, [2]string{"consul", "1.22.8+ent"}, [2]string{"consul", "1.22.8"})
	want := map[string]string{
		"consul": "versions.consul is 1.22.7+ent; nodes.n.processes.c.version is 1.21.5+ent; the scenario runs consul 1.22.8+ent; license.file is set",
		"vault":  "versions.vault is 1.21.1+ent; nodes.n.processes.v.version is 1.20.4+ent",
	}
	if len(got) != len(want) {
		t.Errorf("Needed: %q", got)
	}
	for p, w := range want {
		if g := strings.Join(got[p], "; "); g != w {
			t.Errorf("Needed[%s]:\n got %s\nwant %s", p, g, w)
		}
	}
	if got := Needed(&spec.File{License: &spec.License{Vault: &spec.LicenseFile{File: "/v"}}}); len(got) != 1 || got["vault"][0] != "license.vault is set" {
		t.Errorf("license.vault: %q", got)
	}
}

func TestResolveOrder(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.hclic"), filepath.Join(dir, "b.hclic")
	os.WriteFile(a, []byte("  from-yaml\n"), 0o600)
	os.WriteFile(b, []byte("from-path\n"), 0o600)
	all := env(map[string]string{"CONSUL_LICENSE_PATH": b, "CONSUL_LICENSE": "from-env", "VAULT_LICENSE": "vault-env"})
	for _, c := range []struct {
		f       *spec.File
		product string
		env     func(string) string
		want    string
	}{
		{&spec.File{License: &spec.License{File: a}}, "consul", all, "from-yaml"}, // the file's own license wins
		{&spec.File{License: &spec.License{Consul: &spec.LicenseFile{File: a}}}, "consul", all, "from-yaml"},
		{&spec.File{License: &spec.License{File: a}}, "vault", all, "vault-env"}, // file: is Consul's
		{&spec.File{License: &spec.License{Vault: &spec.LicenseFile{File: a}}}, "vault", all, "from-yaml"},
		{&spec.File{}, "consul", all, "from-path"},
		{&spec.File{}, "consul", env(map[string]string{"CONSUL_LICENSE": "from-env"}), "from-env"},
		{&spec.File{}, "vault", env(map[string]string{"VAULT_LICENSE_PATH": b}), "from-path"},
	} {
		lic, _, err := Resolve(c.f, c.product, c.env)
		if err != nil || string(lic) != c.want {
			t.Errorf("Resolve(%s) = %q, %v; want %q", c.product, lic, err, c.want)
		}
	}
	if _, _, err := Resolve(&spec.File{}, "consul", env(map[string]string{"VAULT_LICENSE": "x"})); err != ErrMissing {
		t.Errorf("no license: %v", err)
	}
	if _, _, err := Resolve(&spec.File{}, "consul", env(map[string]string{"CONSUL_LICENSE_PATH": filepath.Join(dir, "nope")})); err == nil || !strings.Contains(err.Error(), "CONSUL_LICENSE_PATH") {
		t.Errorf("missing file: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, []byte("\n"), 0o600)
	if _, _, err := Resolve(&spec.File{}, "consul", env(map[string]string{"CONSUL_LICENSE_PATH": empty})); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty file: %v", err)
	}
}

func TestFor(t *testing.T) {
	oss := &spec.File{Versions: map[string]string{"consul": "1.22.7", "vault": "1.21.1"}}
	if lic, err := For(oss, env(map[string]string{"CONSUL_LICENSE": "x"})); lic != nil || err != nil {
		t.Errorf("OSS lab takes no license: %q %v", lic, err)
	}
	ent := &spec.File{Versions: map[string]string{"consul": "1.22.7+ent"}}
	_, err := For(ent, env(nil))
	if err == nil || !strings.Contains(err.Error(), "versions.consul is 1.22.7+ent needs a Consul Enterprise license") || !strings.Contains(err.Error(), "CONSUL_LICENSE_PATH") {
		t.Errorf("missing license error: %v", err)
	}
	if lic, err := For(ent, env(map[string]string{"CONSUL_LICENSE": "abc", "VAULT_LICENSE": "v"})); len(lic) != 1 || string(lic["consul"]) != "abc" || err != nil {
		t.Errorf("For = %q %v", lic, err)
	}
	// Vault Enterprise beside Consul CE takes Vault's license only.
	vent := &spec.File{Versions: map[string]string{"consul": "1.22.7", "vault": "1.21.1+ent"}}
	if _, err := For(vent, env(map[string]string{"CONSUL_LICENSE": "abc"})); err == nil || !strings.Contains(err.Error(), "needs a Vault Enterprise license: set VAULT_LICENSE_PATH") {
		t.Errorf("missing Vault license: %v", err)
	}
	if lic, err := For(vent, env(map[string]string{"CONSUL_LICENSE": "abc", "VAULT_LICENSE": "v"})); len(lic) != 1 || string(lic["vault"]) != "v" || err != nil {
		t.Errorf("For Vault = %q %v", lic, err)
	}
	if err := Missing(vent, Licenses{"consul": []byte("abc")}); err == nil || !strings.Contains(err.Error(), "Vault Enterprise license") {
		t.Errorf("Missing: %v", err)
	}
	if err := Missing(vent, Licenses{"vault": []byte("v")}); err != nil {
		t.Errorf("Missing with the license: %v", err)
	}
}

func TestWriteRemoveRedact(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(Path(dir, "consul"), []byte("old"), 0o644)
	paths, err := Write(dir, Licenses{"consul": []byte("secret-license-text"), "vault": []byte("vault-license-text")})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"consul", "vault"} {
		if paths[p] != filepath.Join(dir, p+".hclic") {
			t.Errorf("path %s: %q", p, paths[p])
		}
		st, err := os.Stat(paths[p])
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v %v", st.Mode(), err)
		}
	}
	if err := Remove(dir); err != nil || Remove(dir) != nil {
		t.Errorf("Remove: %v", err)
	}
	for _, p := range []string{"consul", "vault"} {
		if _, err := os.Stat(Path(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s still there", p)
		}
	}
	lic := []byte("02MV4UU43BK5HGYYTOJZWFQMTMNNEWU33J")
	vlic := []byte("02MV4UU43BK5HGYYTOJZWFQMTMNNEWVAULT")
	if got := Redact("agent: bad license "+string(lic)+" here, "+string(vlic), Licenses{"consul": lic, "vault": vlic}); got != "agent: bad license [license] here, [license]" {
		t.Errorf("Redact: %q", got)
	}
	if got := Redact("short", Licenses{"consul": []byte("ab")}); got != "short" {
		t.Errorf("Redact short: %q", got)
	}
}
