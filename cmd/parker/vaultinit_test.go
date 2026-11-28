package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Ranjandas/parker/internal/expand"
)

// fakeVault is a cluster of servers that are sealed until unsealed with
// the key init returned; the first answers only after a few tries.
type fakeVault struct {
	inited   bool
	key      string
	sealed   map[string]bool
	tokens   []string
	created  int
	notUpYet int
	pki      map[string]string
	transit  []string
	recovery bool
	consul   []string
	dr       bool
}

func (f *fakeVault) DRSecondary(context.Context, string) bool { return f.dr }

func (f *fakeVault) Initialized(_ context.Context, addr string) (bool, error) {
	if f.notUpYet > 0 {
		f.notUpYet--
		return false, io.ErrUnexpectedEOF
	}
	return f.inited, nil
}
func (f *fakeVault) Init(_ context.Context, _ string, recovery bool) (string, string, error) {
	f.inited, f.key, f.recovery = true, "k1", recovery
	if recovery {
		// Auto-unseal: the servers unseal themselves once initialised.
		for a := range f.sealed {
			f.sealed[a] = false
		}
	}
	return "k1", "initial-root", nil
}
func (f *fakeVault) Sealed(_ context.Context, addr string) (bool, error) { return f.sealed[addr], nil }
func (f *fakeVault) MountTransit(_ context.Context, _, token, path string, keys []string) error {
	for _, k := range keys {
		if !slices.Contains(f.transit, token+" "+path+k) {
			f.transit = append(f.transit, token+" "+path+k)
		}
	}
	return nil
}
func (f *fakeVault) Unseal(_ context.Context, addr, key string) (bool, error) {
	if key == f.key {
		f.sealed[addr] = false
	}
	return !f.sealed[addr], nil
}
func (f *fakeVault) CreateToken(_ context.Context, _, token, id string) error {
	if token == "initial-root" {
		f.tokens = append(f.tokens, id)
		f.created++
	}
	return nil
}
func (f *fakeVault) MountPKI(_ context.Context, _, token, path, bundle string) error {
	if f.pki == nil {
		f.pki = map[string]string{}
	}
	if _, ok := f.pki[path]; !ok {
		f.pki[path] = token + " " + bundle
	}
	return nil
}
func (f *fakeVault) MountConsul(_ context.Context, _, token string, e expand.VaultConsulEngine) error {
	f.consul = append(f.consul, token+" "+e.Path+" "+e.Address+" "+e.Role)
	return nil
}
func (f *fakeVault) TokenExists(_ context.Context, _, token string) bool {
	return slices.Contains(f.tokens, token)
}

func TestVaultInit(t *testing.T) {
	addrs := []string{"http://a:8200", "http://b:8200", "http://c:8200"}
	keys := filepath.Join(t.TempDir(), "keys.json")
	s := expand.VaultInitSpec{Addrs: addrs, RootToken: "lab-root", KeysFile: keys,
		PKIRoots: []expand.VaultPKIRoot{{Path: "connect-root/", Bundle: "CA"}}}
	f := &fakeVault{sealed: map[string]bool{addrs[0]: true, addrs[1]: true, addrs[2]: true}, notUpYet: 2}
	if err := vaultInit(context.Background(), f, s, 10*time.Second, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.pki["connect-root/"] != "lab-root CA" {
		t.Errorf("pki %v", f.pki)
	}
	for _, a := range addrs {
		if f.sealed[a] {
			t.Errorf("%s still sealed", a)
		}
	}
	if !slices.Contains(f.tokens, "lab-root") {
		t.Errorf("tokens %v", f.tokens)
	}
	b, err := os.ReadFile(keys)
	if err != nil {
		t.Fatal(err)
	}
	var k vaultKeys
	if json.Unmarshal(b, &k); k.Key != "k1" || k.Root != "initial-root" {
		t.Errorf("keys file %s", b)
	}
	if st, _ := os.Stat(keys); st.Mode().Perm() != 0o600 {
		t.Errorf("keys file mode %v", st.Mode())
	}

	// A resumed lab: initialised, sealed again; the kept key unseals it
	// and the root token isn't made twice.
	for _, a := range addrs {
		f.sealed[a] = true
	}
	if err := vaultInit(context.Background(), f, s, 10*time.Second, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.sealed[addrs[2]] || f.created != 1 {
		t.Errorf("resume: sealed %v, tokens created %d", f.sealed, f.created)
	}

	// A DR secondary on a resumed lab: unsealed, and nothing asked of it,
	// which it would refuse.
	for _, a := range addrs {
		f.sealed[a] = true
	}
	f.dr, f.tokens, f.pki = true, nil, map[string]string{}
	s.Replica = true
	if err := vaultInit(context.Background(), f, s, 10*time.Second, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.sealed[addrs[2]] || len(f.tokens) != 0 || len(f.pki) != 0 {
		t.Errorf("DR secondary: sealed %v, tokens %v, pki %v", f.sealed, f.tokens, f.pki)
	}
}

func TestVaultInitLostKeys(t *testing.T) {
	f := &fakeVault{inited: true, sealed: map[string]bool{}}
	s := expand.VaultInitSpec{Addrs: []string{"http://a:8200"}, KeysFile: filepath.Join(t.TempDir(), "gone.json")}
	if err := vaultInit(context.Background(), f, s, time.Second, io.Discard); err == nil {
		t.Error("no error with the keys gone")
	}
}

// A cluster that auto-unseals gets a recovery key and unseals itself; one
// that others auto-unseal with makes their Transit keys.
func TestVaultInitAutoUnseal(t *testing.T) {
	addrs := []string{"http://a:8200", "http://b:8200"}
	f := &fakeVault{sealed: map[string]bool{addrs[0]: true, addrs[1]: true}}
	s := expand.VaultInitSpec{Addrs: addrs, RootToken: "lab-root", KeysFile: filepath.Join(t.TempDir(), "keys.json"),
		Recovery: true, TransitKeys: []string{"autounseal-west"},
		ConsulEngine: &expand.VaultConsulEngine{Path: "consul/", Address: "10.1.0.10:8500", Role: "management"}}
	if err := vaultInit(context.Background(), f, s, 5*time.Second, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !f.recovery || f.sealed[addrs[1]] {
		t.Errorf("recovery %v, sealed %v", f.recovery, f.sealed)
	}
	want := []string{"lab-root transit/autounseal-west"}
	if !slices.Equal(f.transit, want) {
		t.Errorf("transit %q, want %q", f.transit, want)
	}
	if want := []string{"lab-root consul/ 10.1.0.10:8500 management"}; !slices.Equal(f.consul, want) {
		t.Errorf("consul %q, want %q", f.consul, want)
	}
}
