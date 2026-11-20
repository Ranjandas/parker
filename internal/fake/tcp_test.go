package fake

import (
	"bufio"
	"context"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"
)

// startTCP serves a tcp-mode fake on a free port and returns its address.
func startTCP(t *testing.T, cfg *Config) (string, *Server) {
	t.Helper()
	cfg.Protocol = "tcp"
	s := New(cfg)
	s.log = log.New(io.Discard, "", 0)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.serveTCPContext(ctx, addr, nil) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	waitDial(t, addr, true)
	return addr, s
}

// waitDial waits until addr accepts connections (up) or refuses them.
func waitDial(t *testing.T, addr string, up bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if c != nil {
			c.Close()
		}
		if (err == nil) == up {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: never became up=%v", addr, up)
}

func TestTCPEcho(t *testing.T) {
	addr, _ := startTCP(t, &Config{Name: "db", Node: "dc1-db", DC: "dc1", Faults: DefaultFaults()})
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	rd := bufio.NewReader(c)
	hello, _ := rd.ReadString('\n')
	if hello != "db dc1-db dc1\n" {
		t.Errorf("greeting = %q", hello)
	}
	c.Write([]byte("hello there\n"))
	if back, _ := rd.ReadString('\n'); back != "hello there\n" {
		t.Errorf("echo = %q", back)
	}
}

func TestTCPUpstream(t *testing.T) {
	addr, s := startTCP(t, &Config{Name: "db", Faults: DefaultFaults()})
	web, _ := start(t, &Config{Name: "web", Upstreams: []string{"tcp://" + addr}, Workers: 1, Faults: DefaultFaults()})
	r, res := get(t, web.URL, nil)
	up := r.UpstreamCalls["tcp://"+addr]
	if res.StatusCode != 200 || up == nil || up.Name != "db" || up.Type != "TCP" || up.Code != 200 || up.Duration == "" {
		t.Fatalf("web -> db over TCP: %d %+v", res.StatusCode, up)
	}
	// An injected error drops the connection: the caller sees a failure.
	f := s.Faults()
	f.ErrorRate = 1
	s.SetFaults(f)
	r, res = get(t, web.URL, nil)
	if up := r.UpstreamCalls["tcp://"+addr]; res.StatusCode != 500 || up.Code != -1 || !strings.Contains(up.Error, "greeting") {
		t.Errorf("injected TCP error: %d %+v", res.StatusCode, up)
	}
}

func TestTCPHealthStopsListening(t *testing.T) {
	old := tcpPoll
	tcpPoll = 10 * time.Millisecond
	t.Cleanup(func() { tcpPoll = old })
	addr, s := startTCP(t, &Config{Name: "db", Faults: DefaultFaults()})
	f := s.Faults()
	f.HealthCode = 503
	s.SetFaults(f)
	waitDial(t, addr, false)
	f.HealthCode = 200
	s.SetFaults(f)
	waitDial(t, addr, true)
}

func TestTCPPlainUpstream(t *testing.T) {
	// A plain TCP server that speaks first counts as up, with its banner.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("SSH-2.0-test\n"))
			c.Close()
		}
	}()
	s := New(&Config{Name: "web", Faults: DefaultFaults()})
	r := s.callTCP(context.Background(), "tcp://"+ln.Addr().String())
	if r.Code != 200 || r.Body != "SSH-2.0-test" {
		t.Errorf("plain upstream: %+v", r)
	}
}
