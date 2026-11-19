package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
