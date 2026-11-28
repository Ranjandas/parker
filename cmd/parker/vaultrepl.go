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
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/vaultrepl"
)

// vaultReplicationCmd runs on a replication primary's first server: it
// turns replication on, activates the secondaries, and waits until they
// stream, then stays up for status. Run again (resume), it leaves what is
// set up as it is.
func vaultReplicationCmd() *cobra.Command {
	return &cobra.Command{
		Use:    expand.VaultReplication + " MANIFEST",
		Short:  "Set up Vault replication (run by parker up)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var s expand.VaultReplicationSpec
			if err := json.Unmarshal(b, &s); err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			ctx, stop := signalContext()
			defer stop()
			tr, err := vaultTransport(s.CAFile)
			if err != nil {
				return err
			}
			r := &replicator{tr: tr, log: os.Stderr}
			if err := r.run(ctx, s, 240*time.Second); err != nil {
				return err
			}
			fmt.Println(expand.VaultReplicationReadyLine)
			<-ctx.Done()
			return nil
		},
	}
}

type replicator struct {
	tr  http.RoundTripper
	log io.Writer
}

// at is a vaultrepl.Do on the server at addr.
func (r *replicator) at(addr string, timeout time.Duration) vaultrepl.Do {
	c := &http.Client{Timeout: timeout, Transport: r.tr}
	return func(ctx context.Context, method, path, token string, body, out any) error {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, addr+path, rd)
		if err != nil {
			return err
		}
		if token != "" {
			req.Header.Set("X-Vault-Token", token)
		}
		resp, err := c.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			return fmt.Errorf("%s %s%s: %s: %s", method, addr, path, resp.Status, bytes.TrimSpace(b))
		}
		if out != nil && len(b) > 0 {
			return json.Unmarshal(b, out)
		}
		return nil
	}
}

// active is the address of the cluster's active server.
func (r *replicator) active(ctx context.Context, c expand.VaultReplica) (string, error) {
	var errs []error
	for _, a := range c.Addrs {
		var h struct {
			Initialized bool `json:"initialized"`
			Sealed      bool `json:"sealed"`
			Standby     bool `json:"standby"`
			PerfStandby bool `json:"performance_standby"`
		}
		err := r.at(a, 5*time.Second)(ctx, http.MethodGet, "/v1/sys/health?standbycode=200&perfstandbyok=true&sealedcode=200&uninitcode=200&drsecondarycode=200&performancestandbycode=200", "", nil, &h)
		if err == nil && h.Initialized && !h.Sealed && !h.Standby && !h.PerfStandby {
			return a, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return "", fmt.Errorf("%s has no active server: %w", c.Name, err)
	}
	return "", fmt.Errorf("%s has no active server", c.Name)
}

func (r *replicator) run(ctx context.Context, s expand.VaultReplicationSpec, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
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
					return fmt.Errorf("vault-replication: %s: %w", what, last)
				}
				return fmt.Errorf("vault-replication: %s: timed out", what)
			case <-time.After(time.Second):
			}
		}
	}
	keysJSON, err := os.ReadFile(s.Primary.KeysFile)
	if err != nil {
		return fmt.Errorf("vault-replication: %s's keys: %w", s.Primary.Name, err)
	}
	var keys vaultKeys
	if err := json.Unmarshal(keysJSON, &keys); err != nil {
		return fmt.Errorf("vault-replication: %s: %w", s.Primary.Name, err)
	}
	p := s.Primary
	onPrimary := func(f func(do vaultrepl.Do) error) (bool, error) {
		a, err := r.active(ctx, p)
		if err != nil {
			return false, err
		}
		err = f(r.at(a, 60*time.Second))
		return err == nil, err
	}

	for _, m := range s.Modes {
		if err := retry("turning on "+m+" replication on "+p.Name, func() (bool, error) {
			return onPrimary(func(do vaultrepl.Do) error {
				st, err := vaultrepl.GetStatus(ctx, do)
				if err != nil {
					return err
				}
				if st.Of(m).Mode == "primary" {
					return nil
				}
				if err := vaultrepl.EnablePrimary(ctx, do, p.RootToken, m); err != nil {
					return err
				}
				return errors.New("enabled; waiting for it to report primary")
			})
		}); err != nil {
			return err
		}
		fmt.Fprintf(r.log, "vault-replication: %s is the %s primary\n", p.Name, m)
	}

	for _, sec := range s.Secondaries {
		m := sec.Mode
		if err := retry("activating "+sec.Name+" as a "+m+" secondary", func() (bool, error) {
			a, err := r.active(ctx, sec.VaultReplica)
			if err != nil {
				// Activated: its servers seal until they have the primary's key.
				if st, serr := vaultrepl.GetStatus(ctx, r.at(sec.Addrs[0], 5*time.Second)); serr == nil && st.Of(m).Mode == "secondary" {
					return true, nil
				}
				return false, err
			}
			do := r.at(a, 60*time.Second)
			st, err := vaultrepl.GetStatus(ctx, do)
			if err != nil {
				return false, err
			}
			if st.Of(m).Mode == "secondary" {
				return true, nil
			}
			var act string
			if _, err := onPrimary(func(pd vaultrepl.Do) error {
				if m == vaultrepl.DR {
					// DR secondaries copy the primary's tokens: give the
					// secondary's lab root token to the primary, so it works on
					// the secondary once promoted.
					if pd(ctx, http.MethodGet, "/v1/auth/token/lookup-self", sec.RootToken, nil, nil) != nil {
						if err := pd(ctx, http.MethodPost, "/v1/auth/token/create-orphan", p.RootToken,
							map[string]any{"id": sec.RootToken, "policies": []string{"root"}, "display_name": "parker-" + sec.Name}, nil); err != nil {
							return err
						}
					}
				}
				var err error
				if act, err = vaultrepl.SecondaryToken(ctx, pd, p.RootToken, m, sec.Name); err != nil {
					return err
				}
				if f := sec.PathsFilter; f != nil {
					return vaultrepl.PathsFilter(ctx, pd, p.RootToken, sec.Name, f.Mode, f.Paths)
				}
				return nil
			}); err != nil {
				return false, err
			}
			if err := vaultrepl.EnableSecondary(ctx, do, sec.RootToken, m, act, s.CAFile); err != nil {
				return false, err
			}
			return true, nil
		}); err != nil {
			return err
		}
		// From now on the secondary unseals with the primary's key.
		if err := os.WriteFile(sec.KeysFile, keysJSON, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(r.log, "vault-replication: activated %s as a %s secondary of %s\n", sec.Name, m, p.Name)

		for _, a := range sec.Addrs {
			if err := retry("unsealing "+a+" with "+p.Name+"'s key", func() (bool, error) {
				return vaultHTTP{c: &http.Client{Timeout: 5 * time.Second, Transport: r.tr}}.Unseal(ctx, a, keys.Key)
			}); err != nil {
				return err
			}
		}
		if m == vaultrepl.Performance {
			// The secondary's own tokens went with its data: make its lab
			// root token again, with a root token from the primary's key.
			if err := retry("making "+sec.Name+"'s root token", func() (bool, error) {
				a, err := r.active(ctx, sec.VaultReplica)
				if err != nil {
					return false, err
				}
				do := r.at(a, 10*time.Second)
				if do(ctx, http.MethodGet, "/v1/auth/token/lookup-self", sec.RootToken, nil, nil) == nil {
					return true, nil
				}
				root, err := vaultrepl.GenerateRoot(ctx, do, keys.Key)
				if err != nil {
					return false, err
				}
				err = do(ctx, http.MethodPost, "/v1/auth/token/create-orphan", root,
					map[string]any{"id": sec.RootToken, "policies": []string{"root"}, "display_name": "parker"}, nil)
				_ = do(ctx, http.MethodPost, "/v1/auth/token/revoke-self", root, nil, nil)
				return err == nil, err
			}); err != nil {
				return err
			}
		}
		if err := retry("waiting for "+sec.Name+" to stream from "+p.Name, func() (bool, error) {
			var errs []error
			for _, a := range sec.Addrs {
				st, err := vaultrepl.GetStatus(ctx, r.at(a, 5*time.Second))
				if err == nil && st.Of(m).Mode == "secondary" && slices.Contains([]string{"stream-wals"}, st.Of(m).State) {
					return true, nil
				}
				if err == nil {
					err = fmt.Errorf("%s: %s %s, state %q", a, m, st.Of(m).Mode, st.Of(m).State)
				}
				errs = append(errs, err)
			}
			return false, errors.Join(errs...)
		}); err != nil {
			return err
		}
		fmt.Fprintf(r.log, "vault-replication: %s streams from %s\n", sec.Name, p.Name)
	}
	return nil
}
