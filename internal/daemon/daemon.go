// Package daemon is `parker daemon`: it owns lab processes after `up`
// returns (restart policies, reaping) and serves the Parker API on a unix
// socket. The CLI is a client of it.
package daemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/api/parkerv1/parkerv1connect"
	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/lab"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/spec"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// Version is reported by the Version RPC; main sets it.
var Version = "dev"

// SocketPath is where the daemon listens: <run base>/parker.sock.
func SocketPath() string { return filepath.Join(lab.RunBase(), "parker.sock") }

type labState struct {
	h *supervisor.Handle
}

// Server implements parkerv1connect.ParkerServiceHandler.
type Server struct {
	life     context.Context
	shutdown func()

	mu   sync.Mutex
	labs map[string]*labState
	// busy serialises up/down per lab.
	busy map[string]*sync.Mutex
}

var _ parkerv1connect.ParkerServiceHandler = (*Server)(nil)

func (s *Server) labLock(name string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.busy[name]
	if !ok {
		m = &sync.Mutex{}
		s.busy[name] = m
	}
	return m
}

func (s *Server) stopSupervising(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ls, ok := s.labs[name]; ok {
		ls.h.Stop()
		delete(s.labs, name)
	}
}

func (s *Server) Version(context.Context, *connect.Request[parkerv1.VersionRequest]) (*connect.Response[parkerv1.VersionResponse], error) {
	return connect.NewResponse(&parkerv1.VersionResponse{Version: Version, Pid: int32(os.Getpid())}), nil
}

// lineWriter turns writes into LogLine messages, one per line.
type lineWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	send func(string) error
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			w.buf.WriteString(line) // incomplete; keep for later
			return len(p), nil
		}
		if err := w.send(strings.TrimSuffix(line, "\n")); err != nil {
			return 0, err
		}
	}
}

func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		_ = w.send(w.buf.String())
		w.buf.Reset()
	}
}

func streamWriter(stream *connect.ServerStream[parkerv1.LogLine]) *lineWriter {
	return &lineWriter{send: func(l string) error { return stream.Send(&parkerv1.LogLine{Line: l}) }}
}

func (s *Server) Up(ctx context.Context, req *connect.Request[parkerv1.UpRequest], stream *connect.ServerStream[parkerv1.LogLine]) error {
	low, err := parseTopology(req.Msg.TopologyYaml)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	l := s.labLock(low.Name)
	l.Lock()
	defer l.Unlock()

	s.stopSupervising(low.Name)
	w := streamWriter(stream)
	defer w.Flush()
	h, err := lab.Up(ctx, low, lab.Options{
		Log: w, KeepOnFailure: req.Msg.KeepOnFailure, NoFirewall: req.Msg.NoFirewall, Life: s.life,
	})
	if err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	s.mu.Lock()
	s.labs[low.Name] = &labState{h: h}
	s.mu.Unlock()
	return nil
}

func parseTopology(b []byte) (*spec.File, error) {
	// spec.Load reads a file (it needs a path for positions in errors).
	f, err := os.CreateTemp("", "parker-topology-*.yaml")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	in, err := spec.Load(f.Name(), nil)
	if err != nil {
		return nil, err
	}
	return expand.Expand(in)
}

func (s *Server) Down(ctx context.Context, req *connect.Request[parkerv1.DownRequest], stream *connect.ServerStream[parkerv1.LogLine]) error {
	name := req.Msg.Lab
	if name == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("lab is required"))
	}
	l := s.labLock(name)
	l.Lock()
	defer l.Unlock()
	s.stopSupervising(name)
	w := streamWriter(stream)
	defer w.Flush()
	if err := lab.Down(name, w); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return nil
}

func (s *Server) handle(name string) *supervisor.Handle {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ls, ok := s.labs[name]; ok {
		return ls.h
	}
	return nil
}

func (s *Server) ListLabs(context.Context, *connect.Request[parkerv1.ListLabsRequest]) (*connect.Response[parkerv1.ListLabsResponse], error) {
	names, err := lab.Labs()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &parkerv1.ListLabsResponse{}
	for _, n := range names {
		ns, _ := netsetup.Namespaces(n)
		resp.Labs = append(resp.Labs, &parkerv1.LabSummary{Name: n, Namespaces: int32(len(ns)), Supervised: s.handle(n) != nil})
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) Status(_ context.Context, req *connect.Request[parkerv1.StatusRequest]) (*connect.Response[parkerv1.StatusResponse], error) {
	st, err := lab.GetStatus(req.Msg.Lab, s.handle(req.Msg.Lab))
	if errors.Is(err, os.ErrNotExist) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no lab %q (no run dir under %s)", req.Msg.Lab, lab.RunBase()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(StatusToProto(st)), nil
}

func StatusToProto(st *lab.Status) *parkerv1.StatusResponse {
	out := &parkerv1.StatusResponse{Lab: st.Lab, RunDir: st.RunDir, Supervised: st.Supervised}
	for _, n := range st.Nodes {
		pn := &parkerv1.NodeStatus{Name: n.Name, Ips: n.IPs, Namespace: n.Namespace}
		for _, p := range n.Processes {
			pn.Processes = append(pn.Processes, &parkerv1.ProcessStatus{
				Name: p.Name, Pid: int32(p.Pid), Alive: p.Alive, Ready: p.Ready, Restarts: int32(p.Restarts), LogPath: p.LogPath,
			})
		}
		out.Nodes = append(out.Nodes, pn)
	}
	return out
}

func (s *Server) Logs(ctx context.Context, req *connect.Request[parkerv1.LogsRequest], stream *connect.ServerStream[parkerv1.LogChunk]) error {
	path, err := LogPath(req.Msg.Lab, req.Msg.Node, req.Msg.Process)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	n := int(req.Msg.TailLines)
	if n == 0 {
		n = 50
	}
	return FollowLog(ctx, path, n, req.Msg.Follow, func(b []byte) error {
		return stream.Send(&parkerv1.LogChunk{Data: b})
	})
}

// LogPath resolves a node's process log. process defaults to the node's
// only process, else "consul".
func LogPath(labName, node, process string) (string, error) {
	low, err := lab.LoadTopology(labName)
	if err != nil {
		return "", fmt.Errorf("lab %q: %w", labName, err)
	}
	node = strings.ReplaceAll(node, "/", "-")
	n, ok := low.Nodes[node]
	if !ok {
		return "", fmt.Errorf("lab %q has no node %q", labName, node)
	}
	if process == "" {
		if len(n.Processes) == 1 {
			for p := range n.Processes {
				process = p
			}
		} else {
			process = "consul"
		}
	}
	if _, ok := n.Processes[process]; !ok {
		return "", fmt.Errorf("node %s has no process %q", node, process)
	}
	return filepath.Join(supervisor.NodeDir(lab.RunDir(labName), node), process+".log"), nil
}

// FollowLog sends the last n lines of path (n < 0: all), then, if follow,
// whatever is appended until ctx is done.
func FollowLog(ctx context.Context, path string, n int, follow bool, send func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	start, err := tailOffset(f, n)
	if err != nil {
		return err
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, 32*1024)
	for {
		k, err := f.Read(buf)
		if k > 0 {
			if err := send(append([]byte(nil), buf[:k]...)); err != nil {
				return err
			}
			continue
		}
		if err != nil && err != io.EOF {
			return err
		}
		if !follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// tailOffset returns where the last n lines of f start.
func tailOffset(f *os.File, n int) (int64, error) {
	if n < 0 {
		return 0, nil
	}
	var offs []int64
	var pos int64
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			offs = append(offs, pos)
			pos += int64(len(line))
			if len(offs) > n {
				offs = offs[1:]
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if n == 0 {
		return pos, nil
	}
	if len(offs) == 0 {
		return pos, nil
	}
	return offs[0], nil
}

func (s *Server) Shutdown(context.Context, *connect.Request[parkerv1.ShutdownRequest]) (*connect.Response[parkerv1.ShutdownResponse], error) {
	go func() {
		time.Sleep(100 * time.Millisecond) // let the response go out
		s.shutdown()
	}()
	return connect.NewResponse(&parkerv1.ShutdownResponse{}), nil
}

// ServeOptions configures Serve.
type ServeOptions struct {
	Socket string   // unix socket (always served)
	Listen string   // optional TCP address for remote clients (TLS + token)
	SANs   []string // extra names/IPs for the self-signed certificate
}

// Serve runs the daemon until ctx is done or Shutdown is called. Labs keep
// running when it exits.
func Serve(ctx context.Context, o ServeOptions, log io.Writer) error {
	socket := o.Socket
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		return err
	}
	if c, err := net.DialTimeout("unix", socket, time.Second); err == nil {
		c.Close()
		return fmt.Errorf("a parker daemon is already listening on %s", socket)
	}
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return lab.PrivHint(err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		ln.Close()
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &Server{life: ctx, shutdown: cancel, labs: map[string]*labState{}, busy: map[string]*sync.Mutex{}}

	mux := http.NewServeMux()
	mux.Handle(parkerv1connect.NewParkerServiceHandler(s))
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: mux, Protocols: &protocols, ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 2)
	go func() { errc <- srv.Serve(ln) }()
	say := func(f string, a ...any) { fmt.Fprintf(log, f+"\n", a...) }
	say("parker daemon %s (pid %d) listening on %s", Version, os.Getpid(), socket)
	var tcp *http.Server
	if o.Listen != "" {
		var tcpErr <-chan error
		tcp, tcpErr, err = serveTCP(ctx, o.Listen, o.SANs, s, say)
		if err != nil {
			_ = srv.Close()
			_ = os.Remove(socket)
			return err
		}
		go func() { errc <- <-tcpErr }()
	}
	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	shutCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_ = srv.Shutdown(shutCtx)
	if tcp != nil {
		_ = tcp.Shutdown(shutCtx)
	}
	_ = os.Remove(socket)
	say("parker daemon stopped; labs keep running")
	return nil
}

// Client returns an API client for the daemon on socket.
func Client(socket string) parkerv1connect.ParkerServiceClient {
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	tr := &http.Transport{
		Protocols: &protocols,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return parkerv1connect.NewParkerServiceClient(&http.Client{Transport: tr}, "http://parker", connect.WithGRPC())
}
