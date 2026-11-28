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
		"vault:\n  clusters:\n    east: {}\n":                                                                                                                                                      "versions.vault: required",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters: {}\n":                                                                                                                                    "at least one cluster",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { dev: true, servers: 3 }\n":                                                                                                     "dev: true runs one server",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { storage: file, servers: 2 }\n":                                                                                                 "storage: file has no HA",
		"versions: { vault: 1.3.10 }\nvault:\n  clusters:\n    e: { servers: 3 }\n":                                                                                                                "needs Vault 1.4 or later",
		"versions: { vault: 1.21.1, consul: 1.22.7 }\nconsul:\n  datacenters:\n    east: { servers: 1 }\nvault:\n  clusters:\n    east: {}\n":                                                      "names must be unique",
		"versions: { vault: 1.3.10 }\nvault:\n  clusters:\n    e: { servers: 2, storage: { consul: dc9 } }\n":                                                                                      "storage.consul: \"dc9\" is not a Consul datacenter",
		"versions: { vault: 1.21.1, consul: 1.22.7 }\nconsul:\n  datacenters:\n    dc1: { servers: 1, connect: { ca: { vault: west } } }\nvault:\n  clusters:\n    e: {}\n":                        `connect.ca.vault: "west" is not a Vault cluster`,
		"versions: { consul: 1.22.7 }\nconsul:\n  datacenters:\n    dc1: { servers: 1, connect: { ca: { vault: e } } }\n":                                                                          "the lab has no vault: section",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { seal: { transit: x } }\n":                                                                                                      `seal.transit: "x" is not a Vault cluster`,
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { seal: { transit: e } }\n":                                                                                                      "can't unseal itself",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    d: { dev: true }\n    e: { seal: { transit: d } }\n":                                                                                "d runs in dev mode",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    a: { seal: { transit: b } }\n    b: { seal: { transit: a } }\n":                                                                     "auto-unseals with transit itself",
		"versions: { vault: 1.0.3 }\nvault:\n  clusters:\n    a: { storage: file }\n    b: { storage: file, seal: { transit: a } }\n":                                                              "needs Vault 1.1 or later",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { secrets: { consul: dc9 } }\n":                                                                                                  `secrets.consul: "dc9" is not a Consul datacenter`,
		"versions: { vault: 1.3.10, consul: 1.22.7 }\nconsul:\n  datacenters:\n    dc1: { servers: 1, connect: { ca: { vault: e } } }\nvault:\n  clusters:\n    e: { storage: { consul: dc1 } }\n": "keeps its data in dc1, so it can't be dc1's Connect CA",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { register: dc9 }\n":                                                                                                             `register: "dc9" is not a Consul datacenter`,
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { dev: true, register: dc9 }\n":                                                                                                  "register: not supported with dev: true",
		"versions: { vault: 1.3.10, consul: 1.22.7 }\nconsul:\n  datacenters:\n    dc1: { servers: 1 }\nvault:\n  clusters:\n    e: { storage: { consul: dc1 }, register: dc1 }\n":                 "registers the servers in dc1 already",
		"versions: { vault: 1.7.10+ent }\nvault:\n  clusters:\n    e: { storage: file }\n":                                                                                                         "needs Vault 1.8 or later (got 1.7.10+ent)",
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    e: { storage: s3 }\n":                                                                                                               `storage: "s3" is not raft, file or {consul: DC}`,
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

func TestVaultReplicationValidate(t *testing.T) {
	base := "versions: { vault: 1.21.1+ent }\nvault:\n  clusters:\n    a: {}\n    b: {}\n    c: {}\n    d: { dev: true }\n  replication:\n"
	for text, want := range map[string]string{
		"    performance: { primary: a, secondaries: [b] }\n":                                                    "",
		"    performance: { primary: a, secondaries: [b] }\n    dr: { primary: a, secondaries: [c] }\n":          "",
		"    performance: { primary: x, secondaries: [b] }\n":                                                    `primary: "x" is not a Vault cluster`,
		"    performance: { primary: a, secondaries: [] }\n":                                                     "at least one is required",
		"    performance: { primary: a, secondaries: [a] }\n":                                                    "a is the primary",
		"    performance: { primary: a, secondaries: [d] }\n":                                                    "d runs in dev mode",
		"    performance: { primary: a, secondaries: [b] }\n    dr: { primary: a, secondaries: [b] }\n":          "b is a performance secondary already",
		"    performance: { primary: a, secondaries: [b] }\n    dr: { primary: b, secondaries: [c] }\n":          "doesn't chain replication",
		"    performance: { primary: a, secondaries: [b], paths_filter: { c: { mode: deny, paths: [x/] } } }\n":  `"c" is not one of its secondaries`,
		"    performance: { primary: a, secondaries: [b], paths_filter: { b: { mode: maybe, paths: [x/] } } }\n": "want allow or deny",
		"    dr: { primary: a, secondaries: [b], paths_filter: { b: { mode: deny, paths: [x/] } } }\n":           "only performance replication filters paths",
	} {
		p := filepath.Join(t.TempDir(), "lab.yaml")
		if err := os.WriteFile(p, []byte("parker: v1alpha1\n"+base+text), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(p, nil)
		if want == "" {
			if err != nil {
				t.Errorf("%q: %v", text, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
	// Community builds, and secondaries whose data others rely on.
	for text, want := range map[string]string{
		"versions: { vault: 1.21.1 }\nvault:\n  clusters:\n    a: {}\n    b: {}\n  replication:\n    dr: { primary: a, secondaries: [b] }\n":                                                                                                      "needs Vault Enterprise",
		"versions: { vault: 1.21.1+ent }\nvault:\n  clusters:\n    a: {}\n    b: {}\n    c: { seal: { transit: b } }\n  replication:\n    dr: { primary: a, secondaries: [b] }\n":                                                                 "b unseals c with transit",
		"versions: { vault: 1.21.1+ent }\nvault:\n  clusters:\n    a: {}\n    b: { seal: { transit: a } }\n  replication:\n    dr: { primary: a, secondaries: [b] }\n":                                                                            "auto-unseals with transit",
		"versions: { vault: 1.21.1+ent }\nvault:\n  clusters:\n    a: {}\n    b: { secrets: { consul: dc1 } }\n  replication:\n    dr: { primary: a, secondaries: [b] }\n":                                                                        "has secrets.consul",
		"versions: { vault: 1.21.1+ent, consul: 1.22.7 }\nconsul:\n  datacenters:\n    dc1: { servers: 1, connect: { ca: { vault: b } } }\nvault:\n  clusters:\n    a: {}\n    b: {}\n  replication:\n    dr: { primary: a, secondaries: [b] }\n": "is dc1's Connect CA",
	} {
		p := filepath.Join(t.TempDir(), "lab.yaml")
		if err := os.WriteFile(p, []byte("parker: v1alpha1\n"+text), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
}
