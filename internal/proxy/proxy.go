// Package proxy lets a client machine reach a lab's addresses through the
// daemon: a SOCKS5 + HTTP proxy (`parker proxy`) and single-port forwards
// (`parker port-forward`). Every connection is a Dial stream on the API, so
// it works the same against a local or remote daemon.
package proxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/api/parkerv1/parkerv1connect"
)

// DialFunc opens a connection to address (host:port) inside a lab.
type DialFunc func(ctx context.Context, address string) (net.Conn, error)

// APIDialer dials through the daemon's Dial RPC. lab scopes bare node names.
func APIDialer(c parkerv1connect.ParkerServiceClient, lab string) DialFunc {
	return func(ctx context.Context, address string) (net.Conn, error) {
		ctx, cancel := context.WithCancel(ctx)
		stream := c.Dial(ctx)
		if err := stream.Send(&parkerv1.DialRequest{Msg: &parkerv1.DialRequest_Open{Open: &parkerv1.DialOpen{Address: address, Lab: lab}}}); err != nil {
			cancel()
			return nil, err
		}
		first, err := stream.Receive()
		if err != nil {
			cancel()
			return nil, err
		}
		if first.GetOpened() == nil {
			cancel()
			return nil, errors.New("dial: unexpected first message")
		}
		local, remote := net.Pipe()
		go func() { // remote end -> stream
			defer stream.CloseRequest()
			buf := make([]byte, 32*1024)
			for {
				n, err := remote.Read(buf)
				if n > 0 {
					if stream.Send(&parkerv1.DialRequest{Msg: &parkerv1.DialRequest_Data{Data: append([]byte(nil), buf[:n]...)}}) != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
		go func() { // stream -> remote end
			defer cancel()
			defer remote.Close()
			for {
				msg, err := stream.Receive()
				if err != nil {
					return
				}
				if _, err := remote.Write(msg.GetData()); err != nil {
					return
				}
			}
		}()
		return local, nil
	}
}

func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if c, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}

// Serve accepts on ln and handles SOCKS5 and HTTP proxy clients (sniffed
// from the first byte) until ctx is done.
func Serve(ctx context.Context, ln net.Listener, dial DialFunc, logf func(string, ...any)) error {
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			if err := handle(ctx, c, dial); err != nil && logf != nil {
				logf("proxy: %v", err)
			}
		}()
	}
}

func handle(ctx context.Context, c net.Conn, dial DialFunc) error {
	br := bufio.NewReader(c)
	b, err := br.Peek(1)
	if err != nil {
		c.Close()
		return nil
	}
	bc := &bufConn{Conn: c, r: br}
	if b[0] == 0x05 {
		return socks5(ctx, bc, dial)
	}
	return httpProxy(ctx, bc, br, dial)
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b *bufConn) CloseWrite() error {
	if c, ok := b.Conn.(interface{ CloseWrite() error }); ok {
		return c.CloseWrite()
	}
	return b.Conn.Close()
}

// socks5 implements the CONNECT command with no authentication (RFC 1928).
func socks5(ctx context.Context, c *bufConn, dial DialFunc) error {
	defer c.Close()
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return err
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return err
	}
	if _, err := c.Write([]byte{5, 0}); err != nil { // no auth
		return err
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return err
	}
	reply := func(code byte) error {
		_, err := c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
		return err
	}
	if req[1] != 1 { // CONNECT only
		_ = reply(7)
		return fmt.Errorf("socks5: unsupported command %d", req[1])
	}
	var host string
	switch req[3] {
	case 1:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(c, ip); err != nil {
			return err
		}
		host = net.IP(ip).String()
	case 4:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(c, ip); err != nil {
			return err
		}
		host = net.IP(ip).String()
	case 3:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return err
		}
		name := make([]byte, l[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return err
		}
		host = string(name)
	default:
		_ = reply(8)
		return fmt.Errorf("socks5: bad address type %d", req[3])
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return err
	}
	addr := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	up, err := dial(ctx, addr)
	if err != nil {
		_ = reply(2) // not allowed by ruleset
		return fmt.Errorf("%s: %v", addr, err)
	}
	if err := reply(0); err != nil {
		up.Close()
		return err
	}
	pipe(c, up)
	return nil
}

// httpProxy handles CONNECT tunnels and plain absolute-URI requests (one
// request per connection).
func httpProxy(ctx context.Context, c *bufConn, br *bufio.Reader, dial DialFunc) error {
	defer c.Close()
	req, err := http.ReadRequest(br)
	if err != nil {
		return nil
	}
	fail := func(code int, err error) error {
		fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\nparker proxy: %v\n", code, http.StatusText(code), err)
		return err
	}
	if req.Method == http.MethodConnect {
		up, err := dial(ctx, req.Host)
		if err != nil {
			return fail(http.StatusForbidden, err)
		}
		if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			up.Close()
			return err
		}
		pipe(c, up)
		return nil
	}
	if req.URL.Host == "" {
		return fail(http.StatusBadRequest, errors.New("not a proxy request (use an absolute URL)"))
	}
	host := req.URL.Host
	if !strings.Contains(host, ":") || strings.HasSuffix(host, "]") {
		host = net.JoinHostPort(strings.Trim(host, "[]"), "80")
	}
	up, err := dial(ctx, host)
	if err != nil {
		return fail(http.StatusForbidden, err)
	}
	defer up.Close()
	req.Header.Del("Proxy-Connection")
	req.Header.Set("Connection", "close")
	req.Close = true
	if err := req.Write(up); err != nil {
		return err
	}
	_, _ = io.Copy(c, up)
	return nil
}

// Forward listens on ln and connects each client to target via dial.
func Forward(ctx context.Context, ln net.Listener, target string, dial DialFunc, logf func(string, ...any)) error {
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			up, err := dial(ctx, target)
			if err != nil {
				if logf != nil {
					logf("port-forward: %v", err)
				}
				c.Close()
				return
			}
			pipe(c, up)
		}()
	}
}
