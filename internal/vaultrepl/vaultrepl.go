// Package vaultrepl drives Vault Enterprise replication through Vault's
// API: turning a primary on, activating secondaries, and the promote,
// demote, disable and update-primary operations. Callers bring the HTTP
// round trip (a Do), so it runs both inside a lab node (the
// vault-replication job) and through a node's network namespace (the
// scenario steps).
package vaultrepl

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Modes.
const (
	Performance = "performance"
	DR          = "dr"
)

// Do calls Vault's API on one server: method and path (/v1/...), with
// token when set, a JSON body when not nil, decoding the JSON answer into
// out when not nil. An answer of 300 or more is an error.
type Do func(ctx context.Context, method, path, token string, body, out any) error

// ModeStatus is one mode's replication status.
type ModeStatus struct {
	// Mode is primary, secondary, disabled (or bootstrapping, ...).
	Mode string `json:"mode"`
	// State: running on a primary; stream-wals on a secondary in sync,
	// merkle-diff or merkle-sync while it catches up, idle when lost.
	State string `json:"state"`
	// PrimaryClusterAddr, on a secondary.
	PrimaryClusterAddr string `json:"primary_cluster_addr"`
	// ClusterID is the replication set's id.
	ClusterID string `json:"cluster_id"`
}

// Status is a cluster's replication status.
type Status struct {
	DR          ModeStatus `json:"dr"`
	Performance ModeStatus `json:"performance"`
}

// Of is mode's status.
func (s Status) Of(mode string) ModeStatus {
	if mode == DR {
		return s.DR
	}
	return s.Performance
}

// GetStatus reads sys/replication/status, which needs no token.
func GetStatus(ctx context.Context, do Do) (Status, error) {
	var r struct {
		Data Status `json:"data"`
	}
	err := do(ctx, "GET", "/v1/sys/replication/status", "", nil, &r)
	if err == nil && r.Data.DR.Mode == "" && r.Data.Performance.Mode == "" {
		err = errors.New("no replication status (not Vault Enterprise?)")
	}
	return r.Data, err
}

// Summary is a short text of the modes in use: "perf primary, dr
// secondary"; "" when replication is off.
func (s Status) Summary() string {
	var out []string
	for _, m := range []struct{ name, label string }{{Performance, "perf"}, {DR, "dr"}} {
		ms := s.Of(m.name)
		if ms.Mode == "" || ms.Mode == "disabled" {
			continue
		}
		t := m.label + " " + ms.Mode
		if ms.Mode != "primary" && ms.State != "" && ms.State != "stream-wals" {
			t += " (" + ms.State + ")"
		}
		out = append(out, t)
	}
	return strings.Join(out, ", ")
}

// EnablePrimary turns mode on with the cluster as its primary.
func EnablePrimary(ctx context.Context, do Do, token, mode string) error {
	return do(ctx, "POST", "/v1/sys/replication/"+mode+"/primary/enable", token, map[string]any{}, nil)
}

// SecondaryToken mints the activation token of secondary id on the
// primary, revoking an earlier secondary of that id first.
func SecondaryToken(ctx context.Context, do Do, token, mode, id string) (string, error) {
	_ = do(ctx, "POST", "/v1/sys/replication/"+mode+"/primary/revoke-secondary", token, map[string]any{"id": id}, nil)
	var r struct {
		WrapInfo struct {
			Token string `json:"token"`
		} `json:"wrap_info"`
	}
	if err := do(ctx, "POST", "/v1/sys/replication/"+mode+"/primary/secondary-token", token, map[string]any{"id": id}, &r); err != nil {
		return "", err
	}
	if r.WrapInfo.Token == "" {
		return "", errors.New("the primary minted no activation token")
	}
	return r.WrapInfo.Token, nil
}

// PathsFilter sets a performance secondary's paths filter on the primary.
func PathsFilter(ctx context.Context, do Do, token, id, mode string, paths []string) error {
	return do(ctx, "POST", "/v1/sys/replication/performance/primary/paths-filter/"+id, token, map[string]any{"mode": mode, "paths": paths}, nil)
}

// EnableSecondary activates the cluster as a secondary of mode with an
// activation token. caFile, on the secondary's machine, verifies the
// primary's API.
func EnableSecondary(ctx context.Context, do Do, token, mode, activation, caFile string) error {
	body := map[string]any{"token": activation}
	if caFile != "" {
		body["ca_file"] = caFile
	}
	return do(ctx, "POST", "/v1/sys/replication/"+mode+"/secondary/enable", token, body, nil)
}

// UpdatePrimary points a secondary at another primary with an activation
// token from it. A DR secondary takes a DR operation token instead of a
// Vault token.
func UpdatePrimary(ctx context.Context, do Do, token, drToken, mode, activation, caFile string) error {
	body := map[string]any{"token": activation}
	if caFile != "" {
		body["ca_file"] = caFile
	}
	if mode == DR {
		body["dr_operation_token"] = drToken
		token = ""
	}
	return do(ctx, "POST", "/v1/sys/replication/"+mode+"/secondary/update-primary", token, body, nil)
}

// Promote makes a secondary a primary.
func Promote(ctx context.Context, do Do, token, drToken, mode string) error {
	body := map[string]any{}
	if mode == DR {
		body["dr_operation_token"] = drToken
		token = ""
	}
	return do(ctx, "POST", "/v1/sys/replication/"+mode+"/secondary/promote", token, body, nil)
}

// Demote makes a primary a secondary.
func Demote(ctx context.Context, do Do, token, mode string) error {
	return do(ctx, "POST", "/v1/sys/replication/"+mode+"/primary/demote", token, map[string]any{}, nil)
}

// Disable turns mode off; role is the cluster's (primary or secondary).
func Disable(ctx context.Context, do Do, token, drToken, mode, role string) error {
	body := map[string]any{}
	if mode == DR && role == "secondary" {
		body["dr_operation_token"] = drToken
		token = ""
	}
	return do(ctx, "POST", "/v1/sys/replication/"+mode+"/"+role+"/disable", token, body, nil)
}

// GenerateRoot makes a root token on an unsealed cluster from its unseal
// (or recovery) key, the way `vault operator generate-root` does.
func GenerateRoot(ctx context.Context, do Do, key string) (string, error) {
	return generate(ctx, do, "/v1/sys/generate-root", "", key)
}

// DROperationToken makes a DR secondary's operation token from the
// unseal key it shares with its primary. Newer releases want a token the
// secondary accepts for it too: one made on the primary, such as the
// lab's root token of the secondary.
func DROperationToken(ctx context.Context, do Do, token, key string) (string, error) {
	return generate(ctx, do, "/v1/sys/replication/dr/secondary/generate-operation-token", token, key)
}

func generate(ctx context.Context, do Do, base, token, key string) (string, error) {
	// Cancel any attempt left over, then start ours.
	_ = do(ctx, "DELETE", base+"/attempt", token, nil, nil)
	var a struct {
		Nonce string `json:"nonce"`
		OTP   string `json:"otp"`
	}
	if err := do(ctx, "PUT", base+"/attempt", token, map[string]any{}, &a); err != nil {
		return "", err
	}
	if a.OTP == "" || a.Nonce == "" {
		return "", fmt.Errorf("%s/attempt gave no nonce or one-time password", base)
	}
	var u struct {
		Complete bool   `json:"complete"`
		Encoded  string `json:"encoded_token"`
	}
	if err := do(ctx, "PUT", base+"/update", token, map[string]any{"key": key, "nonce": a.Nonce}, &u); err != nil {
		return "", err
	}
	if !u.Complete || u.Encoded == "" {
		return "", fmt.Errorf("%s: the key didn't complete it", base)
	}
	return DecodeToken(u.Encoded, a.OTP)
}

// DecodeToken decodes an encoded token with its one-time password: the
// token is the base64 of the token XOR the password.
func DecodeToken(encoded, otp string) (string, error) {
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(encoded, "="))
	if err != nil {
		return "", fmt.Errorf("decoding the token: %w", err)
	}
	if len(b) != len(otp) {
		return "", fmt.Errorf("decoding the token: %d bytes for a %d-byte password", len(b), len(otp))
	}
	out := make([]byte, len(b))
	for i := range b {
		out[i] = b[i] ^ otp[i]
	}
	return string(out), nil
}
