package fake

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// TCP mode: a line echo server. Each connection is greeted with one line
// naming the service, "<name> <node> <dc>" ("-" for an empty field), then every line sent is echoed back. Faults apply per connection: the
// rate limit and injected errors close it before the greeting, latency
// delays the greeting. A failing health (code 400 or more) stops
// listening, so a TCP health check goes critical, and listening resumes
// when health recovers.

// tcpHello is the greeting line.
func tcpHello(name, node, dc string) string {
	or := func(v string) string {
		if v == "" {
			return "-"
		}
		return v
	}
	return or(name) + " " + or(node) + " " + or(dc) + "\n"
}

// tcpPoll is how often a down listener checks whether health recovered.
var tcpPoll = 200 * time.Millisecond

// serveTCP serves the echo protocol on addr until the process ends.
func (s *Server) serveTCP(addr string) error {
	return s.serveTCPContext(context.Background(), addr, nil)
}

// serveTCPContext is serveTCP until ctx ends. bound, when set, gets each
// address the listener binds (tests use port 0).
func (s *Server) serveTCPContext(ctx context.Context, addr string, bound func(net.Addr)) error {
	for {
		for s.Faults().HealthCode >= 400 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(tcpPoll):
			}
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		if bound != nil {
			bound(ln.Addr())
		}
		// Close the listener when health fails or ctx ends.
		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(tcpPoll)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
				case <-stop:
					return
				case <-t.C:
					if s.Faults().HealthCode < 400 {
						continue
					}
				}
				ln.Close()
				return
			}
		}()
		for {
			c, err := ln.Accept()
			if err != nil {
				break
			}
			go s.echo(ctx, c)
		}
		close(stop)
		if ctx.Err() != nil {
			return nil
		}
		if s.Faults().HealthCode < 400 {
			return errors.New("fake tcp: listener closed")
		}
		s.log.Printf("health %d: stopped listening on %s", s.Faults().HealthCode, addr)
	}
}

func (s *Server) echo(ctx context.Context, c net.Conn) {
	defer c.Close()
	from := c.RemoteAddr().String()
	f := s.Faults()
	if !s.inj.allow(f.RateLimit, s.now()) {
		s.log.Printf("tcp from=%s rate limited", from)
		return
	}
	if f.ErrorRate > 0 && s.inj.float() < f.ErrorRate {
		if f.ErrorType != "delay" {
			s.log.Printf("tcp from=%s error injected", from)
			return
		}
		s.sleep(ctx, f.ErrorDelay)
	}
	s.sleep(ctx, s.inj.latency(f))
	if _, err := c.Write([]byte(tcpHello(s.cfg.Name, s.cfg.Node, s.cfg.DC))); err != nil {
		return
	}
	s.log.Printf("tcp from=%s connected", from)
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		if _, err := c.Write(append(sc.Bytes(), '\n')); err != nil {
			return
		}
	}
}

// callTCP connects to a tcp:// upstream, reads its greeting, and checks
// that a line comes back.
func (s *Server) callTCP(ctx context.Context, uri string) *Response {
	start := s.now()
	r := &Response{URI: uri, Type: "TCP", Code: -1}
	defer s.finish(r, start)
	if s.cfg.ClientTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.ClientTimeout)
		defer cancel()
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", strings.TrimSuffix(strings.TrimPrefix(uri, "tcp://"), "/"))
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	rd := bufio.NewReader(c)
	line, err := rd.ReadString('\n')
	if err != nil {
		r.Error = "no greeting: " + err.Error()
		return r
	}
	f := strings.Fields(line)
	if len(f) != 3 {
		// Not a fake: a plain TCP service that spoke first.
		r.Body = strings.TrimSpace(line)
		r.Code = 200
		return r
	}
	dash := func(v string) string {
		if v == "-" {
			return ""
		}
		return v
	}
	r.Name, r.Node, r.DC = f[0], dash(f[1]), dash(f[2])
	const ping = "parker ping"
	if _, err := c.Write([]byte(ping + "\n")); err != nil {
		r.Error = err.Error()
		return r
	}
	back, err := rd.ReadString('\n')
	if err != nil || strings.TrimSpace(back) != ping {
		r.Error = "echo failed"
		if err != nil {
			r.Error += ": " + err.Error()
		}
		return r
	}
	r.Code = 200
	return r
}
