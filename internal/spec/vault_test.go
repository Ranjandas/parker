package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestVaultValidate(t *testing.T) {
	for text, want := range map[string]string{
		"vault:\n  clusters:\n    east: {}\n":                                                                                                                                      "versions.vault: required",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters: {}\n":                                                                                                                    "at least one cluster",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { dev: true, servers: 3 }\n":                                                                                     "dev: true runs one server",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { storage: file, servers: 2 }\n":                                                                                 "storage: file has no HA",
		"versions: { vault: 1.3.10 }\nvault:\n  clusters:\n    e: { servers: 3 }\n":                                                                                                "needs Vault 1.4 or later",
		"versions: { vault: 1.21.1, consul: 1.22.7 }\nconsul:\n  datacenters:\n    east: { servers: 1 }\nvault:\n  clusters:\n    east: {}\n":                                      "names must be unique",
		"versions: { vault: 1.3.10 }\nvault:\n  clusters:\n    e: { servers: 2, storage: { consul: dc9 } }\n":                                                                      "storage.consul: \"dc9\" is not a Consul datacenter",
		"versions: { vault: 1.21.1, consul: 1.22.7 }\nconsul:\n  datacenters:\n    dc1: { servers: 1, connect: { ca: { vault: west } } }\nvault:\n  clusters:\n    e: {}\n":        `connect.ca.vault: "west" is not a Vault cluster`,
		"versions: { consul: 1.22.7 }\nconsul:\n  datacenters:\n    dc1: { servers: 1, connect: { ca: { vault: e } } }\n":                                                          "the lab has no vault: section",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { seal: { transit: x } }\n":                                                                                      `seal.transit: "x" is not a Vault cluster`,
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { seal: { transit: e } }\n":                                                                                      "can't unseal itself",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    d: { dev: true }\n    e: { seal: { transit: d } }\n":                                                                "d runs in dev mode",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    a: { seal: { transit: b } }\n    b: { seal: { transit: a } }\n":                                                     "auto-unseals with transit itself",
		"versions: { vault: 1.0.3 }\nvault:\n  clusters:\n    a: { storage: file }\n    b: { storage: file, seal: { transit: a } }\n":                                              "needs Vault 1.1 or later",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { register: dc9 }\n":                                                                                             `register: "dc9" is not a Consul datacenter`,
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { dev: true, register: dc9 }\n":                                                                                  "register: not supported with dev: true",
		"versions: { vault: 1.3.10, consul: 1.22.7 }\nconsul:\n  datacenters:\n    dc1: { servers: 1 }\nvault:\n  clusters:\n    e: { storage: { consul: dc1 }, register: dc1 }\n": "registers the servers in dc1 already",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { storage: s3 }\n":                                                                                               `storage: "s3" is not raft, file or {consul: DC}`,
	} {
		p := filepath.Join(t.TempDir(), "lab.yaml")
		if err := os.WriteFile(p, []byte("parker: v1alpha1\n"+text), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(p, nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
}

func TestVaultAtLeast(t *testing.T) {
	for v, want := range map[string]bool{
		"1.4.0": true, "1.3.10": false, "1.21.1+ent": true, "0.11.6": false, "2.0.0": true,
		"path:/usr/bin/vault": true, "v1.5.0": true, "1.4.0-rc1": true,
	} {
		if got := VaultAtLeast(v, 1, 4); got != want {
			t.Errorf("%s: %v, want %v", v, got, want)
		}
	}
}

func TestVaultStorageShapes(t *testing.T) {
	for in, want := range map[string]VaultStorage{
		"raft":          {Kind: VaultStorageRaft},
		"file":          {Kind: VaultStorageFile},
		"{consul: dc1}": {Kind: VaultStorageConsul, Consul: "dc1"},
	} {
		var s VaultStorage
		if err := yaml.Unmarshal([]byte(in), &s); err != nil || s != want {
			t.Errorf("%s: %+v %v", in, s, err)
		}
		b, _ := yaml.Marshal(s)
		var back VaultStorage
		if err := yaml.Unmarshal(b, &back); err != nil || back != want {
			t.Errorf("%s round trip: %s", in, b)
		}
	}
}
