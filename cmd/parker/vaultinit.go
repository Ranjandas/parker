package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/vaultrepl"
)

// vaultInitCmd runs on a Vault cluster's first server: it initialises the
// cluster (once), unseals every server, creates the lab's root token and
// then waits, so the process stays up for status.
func vaultInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:    expand.VaultInit + " MANIFEST",
		Short:  "Initialise and unseal a Vault cluster (run by parker up)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var s expand.VaultInitSpec
			if err := json.Unmarshal(b, &s); err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			ctx, stop := signalContext()
			defer stop()
			// Restarted in the same run (a fault on this node), it leaves the
			// servers be: a restarted server stays sealed, as a real one
			// does. A new run (up, resume) unseals them again.
			run := os.Getenv("PARKER_RUN")
			done := filepath.Join(filepath.Dir(s.KeysFile), expand.VaultInitDone)
			if b, err := os.ReadFile(done); err != nil || run == "" || string(b) != run {
				tr, err := vaultTransport(s.CAFile)
				if err != nil {
					return err
				}
				v := vaultHTTP{c: &http.Client{Timeout: 5 * time.Second, Transport: tr}}
				if err := vaultInit(ctx, v, s, 90*time.Second, os.Stderr); err != nil {
					return err
				}
				if run != "" {
					if err := os.WriteFile(done, []byte(run), 0o600); err != nil {
						return err
					}
				}
			} else {
				fmt.Fprintln(os.Stderr, "vault-init: already done in this run; leaving the servers as they are")
			}
			fmt.Println(expand.VaultReadyLine)
			<-ctx.Done()
			return nil
		},
	}
}

// vaultAPI is what vaultInit needs from Vault.
type vaultAPI interface {
	// Initialized reports whether the cluster at addr is initialised; an
	// error means the server isn't answering yet.
	Initialized(ctx context.Context, addr string) (bool, error)
	// Init initialises it with one key share (a recovery key when the
	// servers auto-unseal) and returns the key and the initial root token.
	Init(ctx context.Context, addr string, recovery bool) (key, root string, err error)
	// Sealed reports whether the server at addr is sealed.
	Sealed(ctx context.Context, addr string) (bool, error)
	// Unseal sends a key and reports whether the server is now unsealed.
	Unseal(ctx context.Context, addr, key string) (bool, error)
	// CreateToken creates a root token with this id.
	CreateToken(ctx context.Context, addr, token, id string) error
	// TokenExists reports whether a token works.
	TokenExists(ctx context.Context, addr, token string) bool
	// MountPKI mounts a PKI secrets engine at path with the CA in bundle,
	// unless one is mounted there already.
	MountPKI(ctx context.Context, addr, token, path, bundle string) error
	// MountTransit mounts the Transit secrets engine at path, unless one
	// is mounted there, and creates the keys that don't exist yet.
	MountTransit(ctx context.Context, addr, token, path string, keys []string) error
	// MountConsul mounts and configures the Consul secrets engine, unless
	// one is mounted at its path, and writes its role.
	MountConsul(ctx context.Context, addr, token string, e expand.VaultConsulEngine) error
	// DRSecondary reports whether the cluster at addr is a DR secondary.
	DRSecondary(ctx context.Context, addr string) bool
}

// vaultKeys is the keys file.
type vaultKeys struct {
	Key  string `json:"key"`
	Root string `json:"initial_root_token"`
}

func vaultInit(ctx context.Context, v vaultAPI, s expand.VaultInitSpec, timeout time.Duration, log io.Writer) error {
	if len(s.Addrs) == 0 {
		return errors.New("vault-init: no servers")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	first := s.Addrs[0]
	retry := func(what string, f func() (bool, error)) error {
		var last error
		for {
			done, err := f()
			if done {
				return nil
			}
			if err != nil {
				last = err
			}
			select {
			case <-ctx.Done():
				if last != nil {
					return fmt.Errorf("vault-init: %s: %w", what, last)
				}
				return fmt.Errorf("vault-init: %s: timed out", what)
			case <-time.After(500 * time.Millisecond):
			}
		}
	}

	var initialized bool
	if err := retry("waiting for "+first, func() (bool, error) {
		ok, err := v.Initialized(ctx, first)
		initialized = ok
		return err == nil, err
	}); err != nil {
		return err
	}
	var keys vaultKeys
	if initialized {
		b, err := os.ReadFile(s.KeysFile)
		if err != nil {
			return fmt.Errorf("vault-init: the cluster is initialised but its keys are gone: %w", err)
		}
		if err := json.Unmarshal(b, &keys); err != nil {
			return fmt.Errorf("vault-init: %s: %w", s.KeysFile, err)
		}
	} else {
		key, root, err := v.Init(ctx, first, s.Recovery)
		if err != nil {
			return fmt.Errorf("vault-init: init: %w", err)
		}
		keys = vaultKeys{Key: key, Root: root}
		b, _ := json.Marshal(keys)
		if err := os.WriteFile(s.KeysFile, b, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(log, "vault-init: initialised %s\n", first)
	}

	// The first server unsealed becomes the leader; the others join it
	// through retry_join once unsealed too. Servers that auto-unseal do it
	// themselves.
	for _, addr := range s.Addrs {
		if s.Recovery {
			if err := retry("waiting for "+addr+" to auto-unseal", func() (bool, error) {
				sealed, err := v.Sealed(ctx, addr)
				return err == nil && !sealed, err
			}); err != nil {
				return err
			}
			fmt.Fprintf(log, "vault-init: %s auto-unsealed\n", addr)
			continue
		}
		if err := retry("unsealing "+addr, func() (bool, error) {
			return v.Unseal(ctx, addr, keys.Key)
		}); err != nil {
			return err
		}
		fmt.Fprintf(log, "vault-init: unsealed %s\n", addr)
	}
	// A DR secondary takes no requests; its tokens are the primary's.
	if s.Replica && v.DRSecondary(ctx, first) {
		fmt.Fprintf(log, "vault-init: %s is a DR secondary; nothing more to set up\n", first)
		return nil
	}

	if s.RootToken != "" {
		if err := retry("creating the root token", func() (bool, error) {
			if v.TokenExists(ctx, first, s.RootToken) {
				return true, nil
			}
			return false, v.CreateToken(ctx, first, keys.Root, s.RootToken)
		}); err != nil {
			return err
		}
	}
	for _, r := range s.PKIRoots {
		token := s.RootToken
		if token == "" {
			token = keys.Root
		}
		// A standby forwards to the active server, which a fresh cluster
		// may still be electing.
		if err := retry("mounting "+r.Path, func() (bool, error) {
			err := v.MountPKI(ctx, first, token, r.Path, r.Bundle)
			return err == nil, err
		}); err != nil {
			return err
		}
		fmt.Fprintf(log, "vault-init: %s holds the lab CA\n", r.Path)
	}
	if len(s.TransitKeys) > 0 {
		token := s.RootToken
		if token == "" {
			token = keys.Root
		}
		if err := retry("mounting "+expand.VaultTransitMount, func() (bool, error) {
			err := v.MountTransit(ctx, first, token, expand.VaultTransitMount, s.TransitKeys)
			return err == nil, err
		}); err != nil {
			return err
		}
		fmt.Fprintf(log, "vault-init: %s holds the keys %s\n", expand.VaultTransitMount, strings.Join(s.TransitKeys, ", "))
	}
	if e := s.ConsulEngine; e != nil {
		token := s.RootToken
		if token == "" {
			token = keys.Root
		}
		if err := retry("mounting "+e.Path, func() (bool, error) {
			err := v.MountConsul(ctx, first, token, *e)
			return err == nil, err
		}); err != nil {
			return err
		}
		fmt.Fprintf(log, "vault-init: %s mints tokens of %s\n", e.Path, e.Address)
	}
	return nil
}

// vaultTransport trusts the CA in caFile for https:// servers; nil (the
// default transport) without one.
func vaultTransport(caFile string) (http.RoundTripper, error) {
	if caFile == "" {
		return nil, nil
	}
	b, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("vault-init: no certificates in %s", caFile)
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return t, nil
}

// vaultHTTP talks to Vault's HTTP API.
type vaultHTTP struct{ c *http.Client }

func (v vaultHTTP) do(ctx context.Context, method, url, token string, body, out any) (int, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return 0, err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := v.c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, bytes.TrimSpace(b))
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: %w", method, url, err)
		}
	}
	return resp.StatusCode, nil
}

func (v vaultHTTP) Initialized(ctx context.Context, addr string) (bool, error) {
	var r struct {
		Initialized bool `json:"initialized"`
	}
	_, err := v.do(ctx, http.MethodGet, addr+"/v1/sys/init", "", nil, &r)
	return r.Initialized, err
}

func (v vaultHTTP) Init(ctx context.Context, addr string, recovery bool) (string, string, error) {
	var r struct {
		Keys         []string `json:"keys"`
		RecoveryKeys []string `json:"recovery_keys"`
		Root         string   `json:"root_token"`
	}
	req := map[string]any{"secret_shares": 1, "secret_threshold": 1}
	if recovery {
		req = map[string]any{"recovery_shares": 1, "recovery_threshold": 1}
	}
	// Init writes the keyring to storage and can take a while; a timeout
	// here would lose the keys of a cluster that did initialise.
	long := vaultHTTP{c: &http.Client{Timeout: 60 * time.Second, Transport: v.c.Transport}}
	if _, err := long.do(ctx, http.MethodPut, addr+"/v1/sys/init", "", req, &r); err != nil {
		return "", "", err
	}
	if recovery {
		r.Keys = r.RecoveryKeys
	}
	if len(r.Keys) == 0 || r.Root == "" {
		return "", "", errors.New("init returned no key or root token")
	}
	return r.Keys[0], r.Root, nil
}

func (v vaultHTTP) Unseal(ctx context.Context, addr, key string) (bool, error) {
	var st struct {
		Sealed bool `json:"sealed"`
	}
	if _, err := v.do(ctx, http.MethodGet, addr+"/v1/sys/seal-status", "", nil, &st); err != nil {
		return false, err
	}
	if !st.Sealed {
		return true, nil
	}
	if _, err := v.do(ctx, http.MethodPut, addr+"/v1/sys/unseal", "", map[string]any{"key": key}, &st); err != nil {
		return false, err
	}
	return !st.Sealed, nil
}

func (v vaultHTTP) CreateToken(ctx context.Context, addr, token, id string) error {
	_, err := v.do(ctx, http.MethodPost, addr+"/v1/auth/token/create-orphan", token,
		map[string]any{"id": id, "policies": []string{"root"}, "display_name": "parker"}, nil)
	return err
}

func (v vaultHTTP) MountPKI(ctx context.Context, addr, token, path, bundle string) error {
	path = strings.Trim(path, "/")
	var mounts map[string]any
	if _, err := v.do(ctx, http.MethodGet, addr+"/v1/sys/mounts", token, nil, &mounts); err != nil {
		return err
	}
	if _, ok := mounts[path+"/"]; ok {
		var ca struct {
			Data struct {
				Certificate string `json:"certificate"`
			} `json:"data"`
		}
		if _, err := v.do(ctx, http.MethodGet, addr+"/v1/"+path+"/cert/ca", token, nil, &ca); err == nil && ca.Data.Certificate != "" {
			return nil // mounted with its CA, on an earlier run
		}
	} else if _, err := v.do(ctx, http.MethodPost, addr+"/v1/sys/mounts/"+path, token,
		map[string]any{"type": "pki", "config": map[string]any{"max_lease_ttl": "87600h"}}, nil); err != nil {
		return err
	}
	_, err := v.do(ctx, http.MethodPost, addr+"/v1/"+path+"/config/ca", token, map[string]any{"pem_bundle": bundle}, nil)
	return err
}

func (v vaultHTTP) Sealed(ctx context.Context, addr string) (bool, error) {
	var st struct {
		Sealed bool `json:"sealed"`
	}
	_, err := v.do(ctx, http.MethodGet, addr+"/v1/sys/seal-status", "", nil, &st)
	return st.Sealed, err
}

func (v vaultHTTP) MountTransit(ctx context.Context, addr, token, path string, keys []string) error {
	path = strings.Trim(path, "/")
	var mounts map[string]any
	if _, err := v.do(ctx, http.MethodGet, addr+"/v1/sys/mounts", token, nil, &mounts); err != nil {
		return err
	}
	if _, ok := mounts[path+"/"]; !ok {
		if _, err := v.do(ctx, http.MethodPost, addr+"/v1/sys/mounts/"+path, token, map[string]any{"type": "transit"}, nil); err != nil {
			return err
		}
	}
	for _, k := range keys {
		// Creating a key that exists changes nothing.
		if _, err := v.do(ctx, http.MethodPost, addr+"/v1/"+path+"/keys/"+k, token, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func (v vaultHTTP) MountConsul(ctx context.Context, addr, token string, e expand.VaultConsulEngine) error {
	path := strings.Trim(e.Path, "/")
	var mounts map[string]any
	if _, err := v.do(ctx, http.MethodGet, addr+"/v1/sys/mounts", token, nil, &mounts); err != nil {
		return err
	}
	if _, ok := mounts[path+"/"]; !ok {
		if _, err := v.do(ctx, http.MethodPost, addr+"/v1/sys/mounts/"+path, token, map[string]any{"type": "consul"}, nil); err != nil {
			return err
		}
	}
	access := map[string]any{"address": e.Address, "scheme": e.Scheme, "token": e.Token}
	if e.CACert != "" {
		access["ca_cert"] = e.CACert
	}
	if _, err := v.do(ctx, http.MethodPost, addr+"/v1/"+path+"/config/access", token, access, nil); err != nil {
		return err
	}
	_, err := v.do(ctx, http.MethodPost, addr+"/v1/"+path+"/roles/"+e.Role, token, map[string]any{e.PolicyField: []string{e.Policy}}, nil)
	return err
}

func (v vaultHTTP) TokenExists(ctx context.Context, addr, token string) bool {
	_, err := v.do(ctx, http.MethodGet, addr+"/v1/auth/token/lookup-self", token, nil, nil)
	return err == nil
}

func (v vaultHTTP) DRSecondary(ctx context.Context, addr string) bool {
	st, err := vaultrepl.GetStatus(ctx, func(ctx context.Context, method, path, token string, body, out any) error {
		_, err := v.do(ctx, method, addr+path, token, body, out)
		return err
	})
	return err == nil && st.DR.Mode == "secondary"
}
