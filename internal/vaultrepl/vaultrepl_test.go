package vaultrepl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestDecodeToken(t *testing.T) {
	tok, otp := "hvs.AAAAAQJ1x0ZpQyDxyz01", "abcdefghijklmnopqrstuvwx"
	x := make([]byte, len(tok))
	for i := range x {
		x[i] = tok[i] ^ otp[i]
	}
	for _, enc := range []string{base64.RawStdEncoding.EncodeToString(x), base64.StdEncoding.EncodeToString(x)} {
		if got, err := DecodeToken(enc, otp); err != nil || got != tok {
			t.Errorf("DecodeToken(%q) = %q, %v", enc, got, err)
		}
	}
	if _, err := DecodeToken(base64.RawStdEncoding.EncodeToString(x[:5]), otp); err == nil {
		t.Error("a short token decoded")
	}
}

func TestStatusAndSummary(t *testing.T) {
	body := `{"data":{"dr":{"mode":"secondary","state":"stream-wals"},"performance":{"mode":"primary","state":"running"}}}`
	do := func(_ context.Context, method, path, _ string, _, out any) error {
		if method != "GET" || path != "/v1/sys/replication/status" {
			t.Errorf("%s %s", method, path)
		}
		return json.Unmarshal([]byte(body), out)
	}
	s, err := GetStatus(context.Background(), do)
	if err != nil {
		t.Fatal(err)
	}
	if s.Of(DR).Mode != "secondary" || s.Of(Performance).State != "running" {
		t.Errorf("status %+v", s)
	}
	if got := s.Summary(); got != "perf primary, dr secondary" {
		t.Errorf("summary %q", got)
	}
	if got := (Status{Performance: ModeStatus{Mode: "secondary", State: "merkle-diff"}, DR: ModeStatus{Mode: "disabled"}}).Summary(); got != "perf secondary (merkle-diff)" {
		t.Errorf("summary %q", got)
	}
	body = `{"data":{}}`
	if _, err := GetStatus(context.Background(), do); err == nil {
		t.Error("CE status had no error")
	}
}

func TestGenerate(t *testing.T) {
	tok, otp := "hvs.root", "12345678"
	x := make([]byte, len(tok))
	for i := range x {
		x[i] = tok[i] ^ otp[i]
	}
	var calls []string
	do := func(_ context.Context, method, path, _ string, body, out any) error {
		calls = append(calls, method+" "+path)
		switch method + " " + path {
		case "PUT /v1/sys/generate-root/attempt":
			return json.Unmarshal([]byte(`{"nonce":"n1","otp":"`+otp+`"}`), out)
		case "PUT /v1/sys/generate-root/update":
			if b := body.(map[string]any); b["key"] != "k" || b["nonce"] != "n1" {
				t.Errorf("update body %v", b)
			}
			return json.Unmarshal([]byte(`{"complete":true,"encoded_token":"`+base64.RawStdEncoding.EncodeToString(x)+`"}`), out)
		}
		return nil
	}
	got, err := GenerateRoot(context.Background(), do, "k")
	if err != nil || got != tok {
		t.Fatalf("GenerateRoot = %q, %v (calls %v)", got, err, calls)
	}
	if calls[0] != "DELETE /v1/sys/generate-root/attempt" {
		t.Errorf("calls %v", calls)
	}
}

func TestEnsureMounts(t *testing.T) {
	var made []string
	do := func(_ context.Context, method, path, _ string, body, out any) error {
		if method == "GET" {
			return json.Unmarshal([]byte(`{"secret/":{},"data":{"secret/":{},"sys/":{}}}`), out)
		}
		made = append(made, method+" "+path)
		return nil
	}
	if err := EnsureMounts(context.Background(), do, "t", []string{"secret/", "private/", "team/x"}); err != nil {
		t.Fatal(err)
	}
	if len(made) != 2 || made[0] != "POST /v1/sys/mounts/private" || made[1] != "POST /v1/sys/mounts/team/x" {
		t.Errorf("made %v", made)
	}
}
