package daemon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
)

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestRemoteTLSAndToken(t *testing.T) {
	fakeLab(t)
	t.Setenv("PARKER_STATE_DIR", t.TempDir())
	sock := filepath.Join(t.TempDir(), "p.sock")
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ServeOptions{Socket: sock, Listen: addr}, os.Stderr) }()

	tok := ""
	for range 100 {
		if b, err := os.ReadFile(TokenPath()); err == nil {
			if _, err := os.Stat(CertPath()); err == nil {
				tok = string(b[:len(b)-1])
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if tok == "" {
		t.Fatal("token/cert not created")
	}
	good := Target{Host: "https://" + addr, Token: tok, CAFile: CertPath()}
	c, err := good.Dial()
	if err != nil {
		t.Fatal(err)
	}
	var verr error
	for range 50 {
		if _, verr = c.Version(ctx, connect.NewRequest(&parkerv1.VersionRequest{})); verr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if verr != nil {
		t.Fatalf("with token: %v", verr)
	}
	if _, err := c.Status(ctx, connect.NewRequest(&parkerv1.StatusRequest{Lab: "t"})); err != nil {
		t.Fatalf("status over TLS: %v", err)
	}

	bad := good
	bad.Token = "nope"
	cb, _ := bad.Dial()
	if _, err := cb.Version(ctx, connect.NewRequest(&parkerv1.VersionRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("wrong token: want unauthenticated, got %v", err)
	}
	untrusted := good
	untrusted.CAFile = ""
	cu, _ := untrusted.Dial()
	if _, err := cu.Version(ctx, connect.NewRequest(&parkerv1.VersionRequest{})); err == nil {
		t.Fatal("untrusted cert accepted")
	}
	if _, err := (Target{Host: "https://" + addr}).Dial(); err == nil {
		t.Fatal("dial without token should fail")
	}

	// The same listener serves the web UI; a browser session reaches the
	// API with the token it signed in with.
	webUI(t, addr, tok)

	// Restarting keeps the same certificate.
	c1, _ := os.ReadFile(CertPath())
	cancel()
	<-done
	if _, err := ensureCert(nil); err != nil {
		t.Fatal(err)
	}
	c2, _ := os.ReadFile(CertPath())
	if string(c1) != string(c2) {
		t.Fatal("certificate regenerated")
	}
}

func TestTargetParse(t *testing.T) {
	if _, err := (Target{Host: "tcp://x"}).Dial(); err == nil {
		t.Fatal("want error for tcp://")
	}
	if (Target{Host: "unix:///tmp/x"}).IsRemote() || !(Target{Host: "https://h:1"}).IsRemote() {
		t.Fatal("IsRemote")
	}
}

func webUI(t *testing.T, addr, tok string) {
	t.Helper()
	pem, err := os.ReadFile(CertPath())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	base := "https://" + addr
	get := func(path string) (int, string) {
		r, err := hc.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	post := func(path, body string) (int, string) {
		r, err := hc.Post(base+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	if code, body := get("/"); code != 200 || !strings.Contains(body, "app.js") {
		t.Fatalf("GET /: %d %.80s", code, body)
	}
	if code, _ := post("/ui/api/parker.v1.ParkerService/Version", "{}"); code != http.StatusUnauthorized {
		t.Fatalf("UI API before sign-in: %d", code)
	}
	if code, _ := post("/ui/session", `{"token":"wrong"}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong token sign-in: %d", code)
	}
	if code, body := post("/ui/session", `{"token":"`+tok+`"}`); code != http.StatusNoContent {
		t.Fatalf("sign-in: %d %s", code, body)
	}
	if code, body := post("/ui/api/parker.v1.ParkerService/Version", "{}"); code != 200 || !strings.Contains(body, `"version"`) {
		t.Fatalf("UI API after sign-in: %d %s", code, body)
	}
	if code, body := post("/ui/api/parker.v1.ParkerService/Status", `{"lab":"t"}`); code != 200 || !strings.Contains(body, `"lab":"t"`) {
		t.Fatalf("UI status: %d %s", code, body)
	}
	// The app proxy is behind the token on the listener.
	if code, _ := get("/app/t/a/http/80/"); code != http.StatusUnauthorized {
		t.Errorf("app proxy without the token: %d", code)
	}
	// An app's origin reaches only the UI's app gateway, never the API,
	// even holding the token.
	for _, path := range []string{"/parker.v1.ParkerService/Version", "/app/t/a/http/80/"} {
		r, _ := http.NewRequest("POST", base+path, strings.NewReader("{}"))
		r.Host = "a-80--t.parker.localhost"
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(b), "Open it from the Parker UI") {
			t.Errorf("app host %s: %d %s", path, resp.StatusCode, b)
		}
	}
}
