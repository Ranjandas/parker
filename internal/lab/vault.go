package lab

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
	"github.com/Ranjandas/parker/internal/vaultrepl"
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
	tok := ""
	if n := low.Nodes[node]; n != nil && n.Labels != nil {
		tok = VaultToken(low, n.Labels.DC)
	}
	return vaultCall(ctx, low, node, method, path, tok, body, out)
}

// vaultDoer is a vaultrepl.Do on node's server, from inside its netns.
func vaultDoer(low *spec.File, node string, timeout time.Duration) vaultrepl.Do {
	return func(ctx context.Context, method, path, token string, body, out any) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return vaultCall(ctx, low, node, method, path, token, body, out)
	}
}

// vaultCall calls the Vault API of the server on node, from inside its
// netns, with token when set.
func vaultCall(ctx context.Context, low *spec.File, node, method, path, token string, body, out any) error {
	n := low.Nodes[node]
	if n == nil || n.Labels == nil || n.Labels.Product != expand.ProductVault {
		return fmt.Errorf("%s is not a Vault server", node)
	}
	base, caFile := VaultAPI(low, node)
	tr := &http.Transport{DialContext: supervisor.DialerIn(netsetup.NetnsPath(low.Name, node)), DisableKeepAlives: true}
	if caFile != "" {
		b, err := os.ReadFile(caFile)
		if err != nil {
			return fmt.Errorf("%s: %w", node, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			return fmt.Errorf("%s: no certificates in %s", node, caFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	timeout := 5 * time.Second
	if d, ok := ctx.Deadline(); ok && time.Until(d) > timeout {
		timeout = time.Until(d)
	}
	cl := &http.Client{Timeout: timeout, Transport: tr}
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
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
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// VaultAPI is the address of node's Vault API from inside the node
// (http or https) and, for https, the CA file that verifies it, both read
// from the server's readiness check.
func VaultAPI(low *spec.File, node string) (addr, caFile string) {
	addr = "http://127.0.0.1:" + strconv.Itoa(expand.VaultPort)
	n := low.Nodes[node]
	if n == nil {
		return addr, ""
	}
	p := n.Processes["vault"]
	if p == nil || p.ReadyWhen == nil || p.ReadyWhen.HTTP == nil || !strings.HasPrefix(p.ReadyWhen.HTTP.URL, "https://") {
		return addr, ""
	}
	addr = "https://127.0.0.1:" + strconv.Itoa(expand.VaultPort)
	if caFile = p.ReadyWhen.HTTP.CAFile; caFile != "" && !filepath.IsAbs(caFile) {
		caFile = filepath.Join(supervisor.NodeDir(RunDir(low.Name), node), caFile)
	}
	return addr, caFile
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
	if vaultAutoUnseal(ctx, low, node) {
		return fmt.Errorf("%s is a standby that auto-unseals, so a restart doesn't seal it; seal the active server, or seal the transit cluster and then restart it", node)
	}
	return restart()
}

// vaultAutoUnseal reports whether node's server unseals itself (a transit
// seal) rather than with a key.
func vaultAutoUnseal(ctx context.Context, low *spec.File, node string) bool {
	var st struct {
		Recovery bool `json:"recovery_seal"`
	}
	return vaultDo(ctx, low, node, http.MethodGet, "/v1/sys/seal-status", nil, &st) == nil && st.Recovery
}

// VaultUnseal unseals node's server with the cluster's kept key. A
// server that auto-unseals only does so as it starts, so it's restarted.
func VaultUnseal(ctx context.Context, low *spec.File, node string, restart func() error) error {
	if s, err := VaultStatus(ctx, low, node); err == nil && !s.Sealed {
		return nil
	}
	if vaultAutoUnseal(ctx, low, node) {
		return restart()
	}
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

// Vault states in the status summary.
const (
	VaultStateActive        = "active"
	VaultStateStandby       = "standby"
	VaultStatePerfStandby   = "perf-standby"
	VaultStateSealed        = "sealed"
	VaultStateUninitialized = "uninitialized"
)

// State is the one-word state of the status summary.
func (s VaultState) State() string {
	switch {
	case !s.Initialized:
		return VaultStateUninitialized
	case s.Sealed:
		return VaultStateSealed
	case s.PerfStandby:
		return VaultStatePerfStandby
	case s.Standby:
		return VaultStateStandby
	}
	return VaultStateActive
}

// vaultSummary is node's state and detail for status: ("", "") when the
// server doesn't answer.
func vaultSummary(low *spec.File, node string) [2]string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := VaultStatus(ctx, low, node)
	if err != nil {
		return [2]string{}
	}
	out := [2]string{s.State(), ""}
	if !s.Initialized || s.Sealed || VaultToken(low, low.Nodes[node].Labels.DC) == "" {
		return out
	}
	var detail []string
	if n, err := VaultRaftPeers(ctx, low, node); err == nil && n > 0 {
		detail = append(detail, fmt.Sprintf("raft %d voters", n))
	}
	if s.Active || s.PerfStandby {
		if st, err := vaultrepl.GetStatus(ctx, vaultDoer(low, node, 3*time.Second)); err == nil {
			if r := st.Summary(); r != "" {
				detail = append(detail, r)
			}
		}
	}
	out[1] = strings.Join(detail, ", ")
	return out
}

// VaultReplicationStatus is the replication of node's cluster, asked of
// its active server.
func VaultReplicationStatus(ctx context.Context, low *spec.File, node string) (vaultrepl.Status, error) {
	n := low.Nodes[node]
	if n == nil || n.Labels == nil || n.Labels.Product != expand.ProductVault {
		return vaultrepl.Status{}, fmt.Errorf("%s is not a Vault server", node)
	}
	a, err := VaultActive(low, n.Labels.DC)
	if err != nil {
		return vaultrepl.Status{}, err
	}
	return vaultrepl.GetStatus(ctx, vaultDoer(low, a, 5*time.Second))
}

// vaultKeysPath is the keys file of a cluster: vault-init's, in its first
// server's directory.
func vaultKeysPath(low *spec.File, cluster string) string {
	for _, n := range VaultNodes(low, cluster) {
		if _, ok := low.Nodes[n].Files[expand.VaultInitManifest]; ok {
			return filepath.Join(supervisor.NodeDir(RunDir(low.Name), n), expand.VaultInitKeys)
		}
	}
	return ""
}

// replMode is the replication mode an action on a cluster acts on: want
// when set, else the one mode the cluster is in with a role in roles.
func replMode(cluster, action, want string, st vaultrepl.Status, roles ...string) (string, error) {
	if want != "" {
		return want, nil
	}
	var in []string
	for _, m := range []string{vaultrepl.Performance, vaultrepl.DR} {
		if slices.Contains(roles, st.Of(m).Mode) {
			in = append(in, m)
		}
	}
	switch len(in) {
	case 1:
		return in[0], nil
	case 0:
		return "", fmt.Errorf("%s: %s needs a %s (performance %s, dr %s)", cluster, strings.ReplaceAll(action, "_", "-"), strings.Join(roles, " or "), st.Performance.Mode, st.DR.Mode)
	}
	return "", fmt.Errorf("%s is in both performance and dr replication; set replication: on the step", cluster)
}

// VaultReplicationAction runs promote, demote, disable or update_primary
// on a cluster, and says which mode it acted on. newPrimary is
// update_primary's primary cluster.
func VaultReplicationAction(ctx context.Context, low *spec.File, action, cluster, newPrimary, want string) (string, error) {
	a, err := VaultActive(low, cluster)
	if err != nil {
		return "", err
	}
	do := vaultDoer(low, a, 60*time.Second)
	st, err := vaultrepl.GetStatus(ctx, do)
	if err != nil {
		return "", fmt.Errorf("%s: %w", cluster, err)
	}
	var mode string
	switch action {
	case "promote", "update_primary":
		mode, err = replMode(cluster, action, want, st, "secondary")
	case "demote":
		mode, err = replMode(cluster, action, want, st, "primary")
	case "disable":
		mode, err = replMode(cluster, action, want, st, "primary", "secondary")
	default:
		return "", fmt.Errorf("unknown vault action %q", action)
	}
	if err != nil {
		return "", err
	}
	token := VaultToken(low, cluster)
	role := st.Of(mode).Mode
	drToken := ""
	if mode == vaultrepl.DR && role != "primary" {
		key := VaultUnsealKey(low, cluster)
		if key == "" {
			return "", fmt.Errorf("%s: no unseal key for a DR operation token", cluster)
		}
		if drToken, err = vaultrepl.DROperationToken(ctx, do, VaultToken(low, cluster), key); err != nil {
			return "", fmt.Errorf("%s: DR operation token: %w", cluster, err)
		}
	}
	switch action {
	case "promote":
		err = vaultrepl.Promote(ctx, do, token, drToken, mode)
	case "demote":
		err = vaultrepl.Demote(ctx, do, token, mode)
	case "disable":
		err = vaultrepl.Disable(ctx, do, token, drToken, mode, role)
	case "update_primary":
		err = vaultUpdatePrimary(ctx, low, cluster, newPrimary, mode, do, token, drToken)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", cluster, err)
	}
	return mode, nil
}

// vaultUpdatePrimary points cluster at newPrimary with an activation
// token from it; the cluster then unseals with newPrimary's key.
func vaultUpdatePrimary(ctx context.Context, low *spec.File, cluster, newPrimary, mode string, do vaultrepl.Do, token, drToken string) error {
	pa, err := VaultActive(low, newPrimary)
	if err != nil {
		return err
	}
	pdo := vaultDoer(low, pa, 60*time.Second)
	ptok := VaultToken(low, newPrimary)
	if mode == vaultrepl.DR && pdo(ctx, http.MethodGet, "/v1/auth/token/lookup-self", token, nil, nil) != nil {
		// As at activation: the secondary's lab token, on the primary.
		if err := pdo(ctx, http.MethodPost, "/v1/auth/token/create-orphan", ptok,
			map[string]any{"id": token, "policies": []string{"root"}, "display_name": "parker-" + cluster}, nil); err != nil {
			return err
		}
	}
	act, err := vaultrepl.SecondaryToken(ctx, pdo, ptok, mode, cluster)
	if err != nil {
		return fmt.Errorf("%s: %w", newPrimary, err)
	}
	_, caFile := VaultAPI(low, pa)
	if err := vaultrepl.UpdatePrimary(ctx, do, token, drToken, mode, act, caFile); err != nil {
		return err
	}
	if src, dst := vaultKeysPath(low, newPrimary), vaultKeysPath(low, cluster); src != "" && dst != "" {
		if b, err := os.ReadFile(src); err == nil {
			return os.WriteFile(dst, b, 0o600)
		}
	}
	return nil
}
