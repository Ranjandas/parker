package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

const minimal = "# a lab\nparker: v1alpha1\nname: tiny\nversions: { consul: 1.22.7 }\nconsul:\n  datacenters:\n    dc1: { servers: 1 }\n"

func buildCall(t *testing.T, h *Handler, cookie, what string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	w := do(h, "POST", buildPrefix+what, string(b), map[string]string{"Cookie": cookie, "Origin": "http://parker.test"})
	return w.Code, w.Body.String()
}

func TestBuildNeedsASession(t *testing.T) {
	h, _ := newTest(t)
	cookie := login(t, h)
	body := `{"yaml":"x: 1"}`
	if w := do(h, "POST", buildPrefix+"check", body, nil); w.Code != 401 {
		t.Errorf("no cookie: %d", w.Code)
	}
	if w := do(h, "POST", buildPrefix+"check", body, map[string]string{"Cookie": cookie, "Origin": "https://evil.example"}); w.Code != 403 {
		t.Errorf("cross origin: %d", w.Code)
	}
	if w := do(h, "GET", buildPrefix+"check", "", map[string]string{"Cookie": cookie}); w.Code != 405 {
		t.Errorf("GET: %d", w.Code)
	}
	if code, _ := buildCall(t, h, cookie, "nope", map[string]string{}); code != 404 {
		t.Errorf("unknown: %d", code)
	}
}

func TestBuildCheckAndPatch(t *testing.T) {
	h, _ := newTest(t)
	cookie := login(t, h)
	code, body := buildCall(t, h, cookie, "check", map[string]string{"yaml": minimal})
	var res struct {
		OK       bool            `json:"ok"`
		Name     string          `json:"name"`
		Expanded string          `json:"expanded"`
		Tree     json.RawMessage `json:"tree"`
		Source   json.RawMessage `json:"source"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &res) != nil || !res.OK || res.Name != "tiny" || !strings.Contains(res.Expanded, "dc1-server-1") {
		t.Fatalf("check: %d %s", code, body)
	}
	// Key order kept: parker, name, versions, consul.
	if !strings.HasPrefix(string(res.Source), `{"parker":"v1alpha1","name":"tiny","versions":`) {
		t.Errorf("source tree: %s", res.Source)
	}
	code, body = buildCall(t, h, cookie, "check", map[string]string{"yaml": "parker: v1alpha1\nname: x\nconsul: { datacenters: { dc1: { servers: zero } } }\n"})
	if code != 200 || !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "problems") {
		t.Errorf("bad file: %d %s", code, body)
	}

	code, body = buildCall(t, h, cookie, "patch", map[string]any{"yaml": minimal, "ops": []map[string]any{
		{"op": "set", "path": []any{"consul", "datacenters", "dc1", "servers"}, "value": 3}}})
	var p struct{ YAML string }
	if code != 200 || json.Unmarshal([]byte(body), &p) != nil || p.YAML != strings.Replace(minimal, "servers: 1", "servers: 3", 1) {
		t.Errorf("patch: %d %s", code, body)
	}
	if code, _ = buildCall(t, h, cookie, "patch", map[string]any{"yaml": minimal, "ops": []map[string]any{
		{"op": "delete", "path": []any{"consul", "nope", "x"}}}}); code != 422 {
		t.Errorf("bad op: %d", code)
	}

	// parse: an entry typed in the builder, keys in order.
	code, body = buildCall(t, h, cookie, "parse", map[string]string{"yaml": "Kind: service-defaults\nName: api\nProtocol: http\n"})
	if code != 200 || body != `{"value":{"Kind":"service-defaults","Name":"api","Protocol":"http"}}`+"\n" {
		t.Errorf("parse: %d %q", code, body)
	}
	if code, _ = buildCall(t, h, cookie, "parse", map[string]string{"yaml": "Kind: [oops\n"}); code != 422 {
		t.Errorf("parse of bad YAML: %d", code)
	}
}

func TestBuildOpenSave(t *testing.T) {
	h, _ := newTest(t)
	cookie := login(t, h)
	dir := t.TempDir()
	path := filepath.Join(dir, "lab.yaml")
	if err := os.WriteFile(path, []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}
	code, body := buildCall(t, h, cookie, "open", map[string]string{"path": path})
	if code != 200 || !strings.Contains(body, `"yaml":"# a lab`) {
		t.Fatalf("open: %d %s", code, body)
	}
	for _, bad := range []string{"lab.yaml", filepath.Join(dir, "x.txt"), dir + "/"} {
		if code, _ := buildCall(t, h, cookie, "open", map[string]string{"path": bad}); code != 400 {
			t.Errorf("open %q: %d", bad, code)
		}
	}
	if code, _ := buildCall(t, h, cookie, "open", map[string]string{"path": filepath.Join(dir, "gone.yaml")}); code != 404 {
		t.Errorf("open missing: %d", code)
	}
	edited := minimal + "# more\n"
	if code, body := buildCall(t, h, cookie, "save", map[string]string{"path": path, "yaml": edited}); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	if b, _ := os.ReadFile(path); string(b) != edited {
		t.Errorf("saved %q", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("temp file left: %v", ents)
	}
	if code, _ := buildCall(t, h, cookie, "save", map[string]string{"path": filepath.Join(dir, "a.sh"), "yaml": "x"}); code != 400 {
		t.Errorf("save .sh: %d", code)
	}
}

func TestBuildUp(t *testing.T) {
	api := &fakeAPI{}
	h := New(Options{API: api, Token: "s3cret"})
	cookie := login(t, h)
	if code, _ := buildCall(t, h, cookie, "up", map[string]string{"yaml": minimal}); code != 501 {
		t.Errorf("no Up: %d", code)
	}
	var got *spec.File
	fail := false
	h = New(Options{API: api, Token: "s3cret", Up: func(_ context.Context, low *spec.File, w io.Writer) error {
		got = low
		fmt.Fprintln(w, "starting")
		if fail {
			return errors.New("boom")
		}
		return nil
	}})
	cookie = login(t, h)
	code, body := buildCall(t, h, cookie, "up", map[string]string{"yaml": minimal})
	if code != 200 || got == nil || got.Name != "tiny" || !strings.HasSuffix(body, "starting\n\nparker: up tiny\n") {
		t.Errorf("up: %d %q", code, body)
	}
	fail = true
	if _, body := buildCall(t, h, cookie, "up", map[string]string{"yaml": minimal}); !strings.HasSuffix(body, "parker: error: boom\n") {
		t.Errorf("failed up: %q", body)
	}
	got = nil
	if code, body := buildCall(t, h, cookie, "up", map[string]string{"yaml": "parker: v1alpha1\nnodes: 3\n"}); code != 422 || got != nil {
		t.Errorf("bad file went up: %d %s", code, body)
	}
}
