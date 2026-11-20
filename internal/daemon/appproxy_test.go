package daemon

import (
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/lab"
)

// appLab writes a lab whose node "web" sits on loopback, so a test server
// stands in for an app inside the lab. ca is the node's rendered ca.pem.
func appLab(t *testing.T, ca string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("PARKER_RUN_DIR", base)
	dir := filepath.Join(base, "p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	topo := "parker: v1alpha1\nname: p\nnetworks:\n  lo: { subnet: 127.0.0.0/8 }\nnodes:\n  web:\n    networks: { lo: { ip: 127.0.0.1 } }\n"
	if ca != "" {
		topo += "    files:\n      ca.pem: |\n"
		for _, l := range strings.Split(strings.TrimSpace(ca), "\n") {
			topo += "        " + l + "\n"
		}
	}
	if err := os.WriteFile(filepath.Join(dir, lab.TopologyFile), []byte(topo), 0o644); err != nil {
		t.Fatal(err)
	}
}

func portOf(t *testing.T, raw string) string {
	t.Helper()
	u, _ := url.Parse(raw)
	_, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAppProxyHTTP(t *testing.T) {
	appLab(t, "")
	var got *http.Request
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "http://"+r.Host+"/new?x=1", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "hello from web")
	}))
	defer up.Close()
	port := portOf(t, up.URL)
	h := AppProxy()

	r := httptest.NewRequest("GET", "/app/p/web/http/"+port+"/ui/a%2Fb?q=1", nil)
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("X-Forwarded-Host", "web-80--p.parker.localhost:4000")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "hello from web" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if got.URL.EscapedPath() != "/ui/a%2Fb" || got.URL.RawQuery != "q=1" || got.Header.Get("Authorization") != "" || got.Header.Get("X-Forwarded-Host") != "web-80--p.parker.localhost:4000" {
		t.Errorf("upstream saw path %q query %q auth %q fwd %q", got.URL.EscapedPath(), got.URL.RawQuery, got.Header.Get("Authorization"), got.Header.Get("X-Forwarded-Host"))
	}

	// A redirect to the app's own address stays on the browser's origin.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/app/p/web/http/"+port+"/old", nil))
	if loc := w.Header().Get("Location"); loc != "/new?x=1" {
		t.Errorf("location %q", loc)
	}
}

func TestAppProxyHTTPSWithLabCA(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "consul ui")
	}))
	defer up.Close()
	port := portOf(t, up.URL)

	// Without the lab CA the upstream isn't trusted.
	appLab(t, "")
	w := httptest.NewRecorder()
	AppProxy().ServeHTTP(w, httptest.NewRequest("GET", "/app/p/web/https/"+port+"/", nil))
	if w.Code != http.StatusBadGateway {
		t.Errorf("no CA: %d", w.Code)
	}

	appLab(t, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: up.Certificate().Raw})))
	w = httptest.NewRecorder()
	AppProxy().ServeHTTP(w, httptest.NewRequest("GET", "/app/p/web/https/"+port+"/", nil))
	if w.Code != 200 || w.Body.String() != "consul ui" {
		t.Errorf("with CA: %d %q", w.Code, w.Body)
	}
}

func TestAppProxyRefuses(t *testing.T) {
	appLab(t, "")
	for path, want := range map[string]int{
		"/app/p/web":               http.StatusBadRequest, // too short
		"/app/p/web/ftp/80/":       http.StatusBadRequest,
		"/app/p/web/http/99999/":   http.StatusBadRequest,
		"/app/p/nope/http/80/":     http.StatusForbidden, // no such node
		"/app/other/web/http/80/":  http.StatusForbidden, // no such lab
		"/app/p/8.8.8.8/http/80/":  http.StatusForbidden, // outside the lab's networks
		"/app/p/web/http/1/closed": http.StatusBadGateway,
	} {
		w := httptest.NewRecorder()
		AppProxy().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("%s: %d, want %d (%s)", path, w.Code, want, strings.TrimSpace(w.Body.String()))
		}
	}
}

func TestRequireToken(t *testing.T) {
	h := requireToken("tok", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for auth, want := range map[string]int{"": 401, "Bearer nope": 401, "Bearer tok": 204} {
		r := httptest.NewRequest("GET", "/app/x", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%q: %d", auth, w.Code)
		}
	}
}
