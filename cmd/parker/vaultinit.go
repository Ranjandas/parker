package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ranjandas/parker/internal/expand"
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
			v := vaultHTTP{c: &http.Client{Timeout: 5 * time.Second}}
			if err := vaultInit(ctx, v, s, 90*time.Second, os.Stderr); err != nil {
				return err
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
	// Init initialises it with one key share and returns the key and the
	// initial root token.
	Init(ctx context.Context, addr string) (key, root string, err error)
	// Unseal sends a key and reports whether the server is now unsealed.
	Unseal(ctx context.Context, addr, key string) (bool, error)
	// CreateToken creates a root token with this id.
	CreateToken(ctx context.Context, addr, token, id string) error
	// TokenExists reports whether a token works.
	TokenExists(ctx context.Context, addr, token string) bool
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
		key, root, err := v.Init(ctx, first)
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
	// through retry_join once unsealed too.
	for _, addr := range s.Addrs {
		if err := retry("unsealing "+addr, func() (bool, error) {
			return v.Unseal(ctx, addr, keys.Key)
		}); err != nil {
			return err
		}
		fmt.Fprintf(log, "vault-init: unsealed %s\n", addr)
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
	return nil
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

func (v vaultHTTP) Init(ctx context.Context, addr string) (string, string, error) {
	var r struct {
		Keys []string `json:"keys"`
		Root string   `json:"root_token"`
	}
	// Init writes the keyring to storage and can take a while; a timeout
	// here would lose the keys of a cluster that did initialise.
	long := vaultHTTP{c: &http.Client{Timeout: 60 * time.Second}}
	if _, err := long.do(ctx, http.MethodPut, addr+"/v1/sys/init", "", map[string]any{"secret_shares": 1, "secret_threshold": 1}, &r); err != nil {
		return "", "", err
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

func (v vaultHTTP) TokenExists(ctx context.Context, addr, token string) bool {
	_, err := v.do(ctx, http.MethodGet, addr+"/v1/auth/token/lookup-self", token, nil, nil)
	return err == nil
}
