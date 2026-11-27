package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeDial maps "<anything>:<port>" to the test server, refusing "bad" hosts.
func fakeDial(target string) DialFunc {
	return func(ctx context.Context, address string) (net.Conn, error) {
		if strings.HasPrefix(address, "bad") {
			return nil, fmt.Errorf("%s is not in any running lab's network", address)
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", target)
	}
}

func startProxy(t *testing.T, dial DialFunc) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// A connection's goroutine can log after the test ends (the client
	// has its reply before the proxy logs the refusal), and t.Logf then
	// panics; drop those lines.
	var mu sync.Mutex
	done := false
	t.Cleanup(func() {
		cancel()
		mu.Lock()
		done = true
		mu.Unlock()
	})
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if !done {
			t.Logf(format, args...)
		}
	}
	go Serve(ctx, ln, dial, logf)
	return ln.Addr().String()
}

func backend(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s %s", r.Host, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPProxyPlainAndConnect(t *testing.T) {
	srv := backend(t)
	paddr := startProxy(t, fakeDial(srv.Listener.Addr().String()))
	pu, _ := url.Parse("http://" + paddr)
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := c.Get("http://server-1.dc1.minimal.parker:8500/v1/status/leader")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "hello server-1.dc1.minimal.parker:8500 /v1/status/leader" {
		t.Fatalf("plain: %q", b)
	}

	// CONNECT tunnel, then speak HTTP through it.
	conn, err := net.Dial("tcp", paddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT a.parker:80 HTTP/1.1\r\nHost: a.parker:80\r\n\r\n")
	buf := make([]byte, 1024)
	n, _ := conn.Read(buf)
	if !strings.HasPrefix(string(buf[:n]), "HTTP/1.1 200") {
		t.Fatalf("connect reply %q", buf[:n])
	}
	fmt.Fprintf(conn, "GET /x HTTP/1.1\r\nHost: a\r\nConnection: close\r\n\r\n")
	all, _ := io.ReadAll(conn)
	if !strings.Contains(string(all), "hello a /x") {
		t.Fatalf("tunnel: %q", all)
	}

	resp, err = c.Get("http://bad:80/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "not in any running lab") {
		t.Fatalf("refused target: %d %q", resp.StatusCode, b)
	}
}

func socksConnect(t *testing.T, paddr, host string, port int) (net.Conn, byte) {
	t.Helper()
	c, err := net.Dial("tcp", paddr)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte{5, 1, 0})
	r := make([]byte, 2)
	io.ReadFull(c, r)
	if r[1] != 0 {
		t.Fatalf("method %v", r)
	}
	req := []byte{5, 1, 0, 3, byte(len(host))}
	req = append(req, host...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	c.Write(req)
	rep := make([]byte, 10)
	io.ReadFull(c, rep)
	return c, rep[1]
}

func TestSOCKS5(t *testing.T) {
	srv := backend(t)
	paddr := startProxy(t, fakeDial(srv.Listener.Addr().String()))
	c, code := socksConnect(t, paddr, "dc1-server-1.minimal.parker", 8500)
	if code != 0 {
		t.Fatalf("reply %d", code)
	}
	fmt.Fprintf(c, "GET /ui HTTP/1.1\r\nHost: n\r\nConnection: close\r\n\r\n")
	all, _ := io.ReadAll(c)
	c.Close()
	if !strings.Contains(string(all), "hello n /ui") {
		t.Fatalf("%q", all)
	}
	c, code = socksConnect(t, paddr, "bad", 80)
	c.Close()
	if code != 2 {
		t.Fatalf("refused target: reply %d, want 2", code)
	}
}

func TestForward(t *testing.T) {
	srv := backend(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Forward(ctx, ln, "node.lab.parker:8500", fakeDial(srv.Listener.Addr().String()), t.Logf)
	resp, err := http.Get("http://" + ln.Addr().String() + "/v1/agent/self")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "/v1/agent/self") {
		t.Fatalf("%q", b)
	}
}
