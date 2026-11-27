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
}

func (f *fakeVault) Initialized(_ context.Context, addr string) (bool, error) {
	if f.notUpYet > 0 {
		f.notUpYet--
		return false, io.ErrUnexpectedEOF
	}
	return f.inited, nil
}
func (f *fakeVault) Init(context.Context, string) (string, string, error) {
	f.inited, f.key = true, "k1"
	return "k1", "initial-root", nil
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
}

func TestVaultInitLostKeys(t *testing.T) {
	f := &fakeVault{inited: true, sealed: map[string]bool{}}
	s := expand.VaultInitSpec{Addrs: []string{"http://a:8200"}, KeysFile: filepath.Join(t.TempDir(), "gone.json")}
	if err := vaultInit(context.Background(), f, s, time.Second, io.Discard); err == nil {
		t.Error("no error with the keys gone")
	}
}
