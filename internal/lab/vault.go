package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// VaultState is what a Vault server says about itself (sys/health).
type VaultState struct {
	Initialized bool `json:"initialized"`
	Sealed      bool `json:"sealed"`
	Standby     bool `json:"standby"`
	PerfStandby bool `json:"performance_standby"`
	// Active: unsealed and not a standby.
	Active bool `json:"-"`
}

// IsVaultCluster reports whether name is a Vault cluster of the lab.
func IsVaultCluster(low *spec.File, name string) bool {
	return len(VaultNodes(low, name)) > 0
}

// VaultNodes are a Vault cluster's servers, sorted.
func VaultNodes(low *spec.File, cluster string) []string {
	var out []string
	for _, n := range slices.Sorted(maps.Keys(low.Nodes)) {
		if l := low.Nodes[n].Labels; l != nil && l.Product == expand.ProductVault && l.DC == cluster {
			out = append(out, n)
		}
	}
	return out
}

// vaultDo calls the Vault API of the server on node, from inside its
// netns, with the cluster's root token.
func vaultDo(ctx context.Context, low *spec.File, node, method, path string, body, out any) error {
	n := low.Nodes[node]
	if n == nil || n.Labels == nil || n.Labels.Product != expand.ProductVault {
		return fmt.Errorf("%s is not a Vault server", node)
	}
	cl := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: supervisor.DialerIn(netsetup.NetnsPath(low.Name, node)), DisableKeepAlives: true}}
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(expand.VaultPort)+path, r)
	if err != nil {
		return err
	}
	if tok := VaultToken(low, n.Labels.DC); tok != "" {
		req.Header.Set("X-Vault-Token", tok)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", node, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Errors []string `json:"errors"`
		}
		msg := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &e) == nil && len(e.Errors) > 0 {
			msg = strings.Join(e.Errors, "; ")
		}
		return fmt.Errorf("%s: %s %s: %s", node, method, path, msg)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// VaultStatus asks node's Vault server for its state.
func VaultStatus(ctx context.Context, low *spec.File, node string) (VaultState, error) {
	var s VaultState
	// Every state answers 200, so the body says which.
	err := vaultDo(ctx, low, node, http.MethodGet, "/v1/sys/health?standbycode=200&perfstandbyok=true&sealedcode=200&uninitcode=200&drsecondarycode=200&performancestandbycode=200&haunhealthycode=200&removedcode=200", nil, &s)
	s.Active = err == nil && s.Initialized && !s.Sealed && !s.Standby && !s.PerfStandby
	return s, err
}

// VaultActive is a cluster's active server.
func VaultActive(low *spec.File, cluster string) (string, error) {
	nodes := VaultNodes(low, cluster)
	states := make([]VaultState, len(nodes))
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Go(func() { states[i], errs[i] = VaultStatus(context.Background(), low, n) })
	}
	wg.Wait()
	for i, n := range nodes {
		if states[i].Active {
			return n, nil
		}
	}
	if err := errors.Join(errs...); err != nil {
		return "", fmt.Errorf("%s has no active Vault server (%v)", cluster, err)
	}
	return "", fmt.Errorf("%s has no active Vault server", cluster)
}

// VaultSeal seals node's server. Vault only seals an active server
// through the API; a standby is sealed the way Vault itself suggests, by
// restarting it, and stays sealed until unsealed.
func VaultSeal(ctx context.Context, low *spec.File, node string, restart func() error) error {
	s, err := VaultStatus(ctx, low, node)
	if err != nil {
		return err
	}
	if s.Sealed {
		return nil
	}
	if s.Active {
		return vaultDo(ctx, low, node, http.MethodPut, "/v1/sys/seal", nil, nil)
	}
	return restart()
}

// VaultUnseal unseals node's server with the cluster's kept key.
func VaultUnseal(ctx context.Context, low *spec.File, node string) error {
	key := VaultUnsealKey(low, low.Nodes[node].Labels.DC)
	if key == "" {
		return fmt.Errorf("%s: no unseal key (a dev server, or the cluster isn't initialised)", node)
	}
	var st struct {
		Sealed bool `json:"sealed"`
	}
	if err := vaultDo(ctx, low, node, http.MethodPut, "/v1/sys/unseal", map[string]string{"key": key}, &st); err != nil {
		return err
	}
	if st.Sealed {
		return fmt.Errorf("%s: still sealed after the key", node)
	}
	return nil
}

// VaultStepDown makes node's server give up being active. A standby has
// nothing to give up, so it's left alone.
func VaultStepDown(ctx context.Context, low *spec.File, node string) error {
	s, err := VaultStatus(ctx, low, node)
	if err != nil {
		return err
	}
	if !s.Active {
		return nil
	}
	return vaultDo(ctx, low, node, http.MethodPut, "/v1/sys/step-down", nil, nil)
}

// VaultRaftPeers counts a cluster's voting raft peers, asked of node.
func VaultRaftPeers(ctx context.Context, low *spec.File, node string) (int, error) {
	var r struct {
		Data struct {
			Config struct {
				Servers []struct {
					Voter bool `json:"voter"`
				} `json:"servers"`
			} `json:"config"`
		} `json:"data"`
	}
	if err := vaultDo(ctx, low, node, http.MethodGet, "/v1/sys/storage/raft/configuration", nil, &r); err != nil {
		return 0, err
	}
	n := 0
	for _, s := range r.Data.Config.Servers {
		if s.Voter {
			n++
		}
	}
	return n, nil
}
