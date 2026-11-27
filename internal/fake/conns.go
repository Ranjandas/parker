package fake

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
)

// gate holds requests to max_concurrent at once, with up to queue more
// waiting, the way a server's worker pool and accept queue do. The
// limits are read on each request, so a fake: step changes them live.
type gate struct {
	mu       sync.Mutex
	free     *sync.Cond
	inFlight int
	waiting  int
	refused  atomic.Int64
}

func newGate() *gate {
	g := &gate{}
	g.free = sync.NewCond(&g.mu)
	return g
}

// enter waits for a worker; it reports false when the request is
// refused (every worker and queue slot busy) or ends while it waits.
// A true enter must be followed by leave.
func (g *gate) enter(ctx context.Context, l Limits) bool {
	if l.Concurrent <= 0 {
		g.mu.Lock()
		g.inFlight++
		g.mu.Unlock()
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inFlight < l.Concurrent {
		g.inFlight++
		return true
	}
	if g.waiting >= l.Queue {
		g.refused.Add(1)
		return false
	}
	g.waiting++
	defer func() { g.waiting-- }()
	// Wake on a free worker or when the request ends.
	stop := context.AfterFunc(ctx, func() {
		g.mu.Lock()
		g.free.Broadcast()
		g.mu.Unlock()
	})
	defer stop()
	for g.inFlight >= l.Concurrent {
		if ctx.Err() != nil {
			return false
		}
		g.free.Wait()
	}
	g.inFlight++
	return true
}

func (g *gate) leave() {
	g.mu.Lock()
	g.inFlight--
	g.free.Broadcast()
	g.mu.Unlock()
}

// busy is how many requests are handled and waiting now.
func (g *gate) busy() (inFlight, waiting int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inFlight, g.waiting
}

type connKey struct{}

// connContext gives each connection a request counter, for
// max_requests_per_conn.
func connContext(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, new(atomic.Int64))
}

// pooled wraps the app's handler with the worker pool and the
// per-connection request limit.
func (s *Server) pooled(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := s.Faults().Limits
		if n, ok := r.Context().Value(connKey{}).(*atomic.Int64); ok && l.PerConn > 0 && r.ProtoMajor == 1 {
			if n.Add(1) >= int64(l.PerConn) {
				// As Tomcat's maxKeepAliveRequests: this answer is the
				// connection's last.
				w.Header().Set("Connection", "close")
				s.logf(LevelInfo, "connection from %s closed after %d requests (max_requests_per_conn %d)", r.RemoteAddr, n.Load(), l.PerConn)
			}
		}
		if !s.gate.enter(r.Context(), l) {
			in, q := s.gate.busy()
			msg := fmt.Sprintf("server busy: %d requests at once and %d queued (%s %d, queue %d)", in, q, l.name("max_concurrent"), l.Concurrent, l.Queue)
			s.logf(LevelWarn, "request from %s refused: %s", r.RemoteAddr, msg)
			if l.WhenFull != "answer" && r.ProtoMajor == 1 {
				if hj, ok := w.(http.Hijacker); ok {
					if c, _, err := hj.Hijack(); err == nil {
						if tc, ok := c.(*net.TCPConn); ok {
							_ = tc.SetLinger(0) // a reset, as a refused connection gets
						}
						c.Close()
						return
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(l.FullCode)
			_ = writeBody(w, &Response{Name: s.cfg.Name, Node: s.cfg.Node, DC: s.cfg.DC, Version: s.cfg.Version, URI: r.URL.RequestURI(),
				Type: "HTTP", Code: l.FullCode, Error: msg, Limit: "max_concurrent"})
			return
		}
		defer s.gate.leave()
		next(w, r)
	}
}

// configure sets the server-wide limits on srv, from the limits the fake
// starts with.
func (s *Server) configure(srv *http.Server) {
	l := s.cfg.Faults.Limits
	srv.MaxHeaderBytes = maxHeaderCap
	srv.ConnContext = connContext
	srv.IdleTimeout = l.IdleTimeout
	srv.ReadHeaderTimeout = l.HeaderTimeout
	if l.H2Streams > 0 {
		srv.HTTP2 = &http.HTTP2Config{MaxConcurrentStreams: l.H2Streams}
	}
}
