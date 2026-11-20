package fake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// HeaderRequestID ties the hops of one request together.
	HeaderRequestID = "X-Request-Id"
	// HeaderTrail lists the fake services a request has passed through,
	// so a cycle (web -> api -> web) stops instead of recursing.
	HeaderTrail = "X-Parker-Trail"
	// maxHops bounds a chain however it is wired.
	maxHops = 16
)

// Response is what a fake service answers: close to fake-service's JSON,
// plus Parker's node, dc and version.
type Response struct {
	Name        string   `json:"name"`
	Node        string   `json:"node,omitempty"`
	DC          string   `json:"dc,omitempty"`
	Version     string   `json:"version,omitempty"`
	URI         string   `json:"uri"`
	Type        string   `json:"type"`
	IPAddresses []string `json:"ip_addresses,omitempty"`
	StartTime   string   `json:"start_time"`
	EndTime     string   `json:"end_time"`
	Duration    string   `json:"duration"`
	// From is the caller's address.
	From string `json:"from,omitempty"`
	// Body is the message: JSON when it parses as JSON, else a string.
	Body          any                  `json:"body,omitempty"`
	UpstreamCalls map[string]*Response `json:"upstream_calls,omitempty"`
	Code          int                  `json:"code"`
	Error         string               `json:"error,omitempty"`
	// Injected names the fault injected into this answer, if any:
	// error, delay or rate_limit.
	Injected string `json:"injected,omitempty"`
	// Cycle is set when this service was already on the request's path:
	// its upstreams aren't called again.
	Cycle bool `json:"cycle,omitempty"`
	// Fact is a short space fact, a new one each answer (fake: {facts:
	// false} drops it).
	Fact string `json:"fact,omitempty"`
}

// Server is a fake service.
type Server struct {
	cfg    *Config
	client *http.Client
	h2c    *http.Client
	ips    []string
	log    *log.Logger
	// now is time.Now outside tests.
	now     func() time.Time
	started time.Time
	// sleep waits for d or the request to end; time.Sleep-like outside
	// tests.
	sleep func(ctx context.Context, d time.Duration)

	// level is the Level in force.
	level atomic.Int32

	mu     sync.RWMutex
	faults Faults
	inj    *injector
	facts  *factTeller
}

func New(cfg *Config) *Server {
	s := &Server{
		cfg:     cfg,
		client:  &http.Client{Timeout: cfg.ClientTimeout, Transport: freshConns()},
		h2c:     h2cClient(cfg.ClientTimeout),
		ips:     localIPs(),
		log:     log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds),
		now:     time.Now,
		started: time.Now(),
		sleep:   sleepCtx,
		faults:  cfg.Faults,
		inj:     newInjector(cfg.Seed),
		facts:   newFactTeller(cfg.Version, uint64(cfg.Seed)),
	}
	s.level.Store(int32(cfg.LogLevel))
	return s
}

// freshConns opens a connection per upstream call. Through a sidecar's TCP
// proxy a kept-alive connection stays pinned to the instance it first
// reached, so a failover or a traffic split wouldn't show.
func freshConns() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	return tr
}

func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// SetLogOutput sends the request log to w (stdout by default).
func (s *Server) SetLogOutput(w io.Writer) { s.log.SetOutput(w) }

// Faults returns the faults in force now.
func (s *Server) Faults() Faults {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.faults
}

// SetFaults replaces the faults in force.
func (s *Server) SetFaults(f Faults) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = f
}

// Handler serves the fake service's HTTP API.
// In grpc mode the same port also serves FakeService.Handle and the gRPC
// health check, over HTTP/2 without TLS.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serve)
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/ready", s.ready)
	mux.HandleFunc("/ui", s.ui)
	mux.HandleFunc("/ui/", s.ui)
	mux.HandleFunc(UIControlPath, refuseControl)
	if s.cfg.Protocol == "grpc" {
		s.grpcHandlers(mux)
	}
	return mux
}

// ListenAndServe serves on cfg.Port until the process ends.
func (s *Server) ListenAndServe() error {
	fmt.Printf("fake-service %s (%s) listening on :%d (upstreams: %s)\n", s.cfg.Name, s.cfg.Protocol, s.cfg.Port, strings.Join(s.cfg.Upstreams, ", "))
	addr := fmt.Sprintf(":%d", s.cfg.Port)
	if s.cfg.Protocol == "tcp" {
		return s.serveTCP(addr)
	}
	srv := &http.Server{Addr: addr, Handler: s.Handler(), Protocols: new(http.Protocols)}
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetUnencryptedHTTP2(true)
	return srv.ListenAndServe()
}

// Serve is ListenAndServe for a config built from the environment, with
// the control API on a unix socket at control ("" for none).
func Serve(name, dc string, port int, control string) error {
	cfg, err := Load(name, dc, port, nil)
	if err != nil {
		return err
	}
	s := New(cfg)
	if control != "" {
		if err := s.ListenControl(control); err != nil {
			return err
		}
	}
	return s.ListenAndServe()
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	resp, id := s.handle(r, "HTTP")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(HeaderRequestID, id)
	w.WriteHeader(resp.Code)
	_ = json.NewEncoder(w).Encode(resp)
}

// handle answers one request (HTTP or gRPC): faults, then upstreams.
func (s *Server) handle(r *http.Request, typ string) (*Response, string) {
	start := s.now()
	id := r.Header.Get(HeaderRequestID)
	if id == "" {
		id = newID()
	}
	resp := &Response{
		Name: s.cfg.Name, Node: s.cfg.Node, DC: s.cfg.DC, Version: s.cfg.Version,
		URI: r.URL.RequestURI(), Type: typ, IPAddresses: s.ips, From: r.RemoteAddr,
		Body: body(s.cfg.Message), Code: http.StatusOK,
	}
	if s.cfg.Facts {
		resp.Fact = s.facts.fact()
	}
	var trail []string
	if t := r.Header.Get(HeaderTrail); t != "" {
		trail = strings.Split(t, ",")
	}
	if s.inject(r, resp) {
		switch {
		case len(s.cfg.Upstreams) == 0:
		case slices.Contains(trail, s.cfg.Name) || len(trail) >= maxHops:
			// Answer, but don't go round again.
			resp.Cycle = true
		default:
			resp.UpstreamCalls = s.callUpstreams(r.Context(), id, strings.Join(append(trail, s.cfg.Name), ","), r.Header.Get(HeaderFault))
			for _, u := range s.cfg.Upstreams {
				if up := resp.UpstreamCalls[u]; up.Code < 0 || up.Code >= 400 {
					if !s.cfg.IgnoreUpstreamErrors {
						resp.Code = http.StatusInternalServerError
						resp.Error = "upstream " + u + " failed"
					}
					break
				}
			}
		}
	}
	s.finish(resp, start)
	lvl := LevelInfo
	switch {
	case resp.Code >= 500 || resp.Code < 0:
		lvl = LevelError
	case resp.Code >= 400 || resp.Injected != "":
		lvl = LevelWarn
	}
	s.logf(lvl, "request id=%s type=%s path=%s from=%s code=%d duration=%s%s", id, typ, resp.URI, r.RemoteAddr, resp.Code, resp.Duration, injectedNote(resp))
	if s.Level() == LevelDebug {
		for _, k := range slices.Sorted(maps.Keys(r.Header)) {
			s.logf(LevelDebug, "request id=%s header %s: %s", id, k, strings.Join(r.Header[k], ", "))
		}
		for _, u := range slices.Sorted(maps.Keys(resp.UpstreamCalls)) {
			up := resp.UpstreamCalls[u]
			s.logf(LevelDebug, "request id=%s upstream %s code=%d duration=%s%s", id, u, up.Code, up.Duration, errNote(up.Error))
		}
	}
	return resp, id
}

// inject applies the request's faults: the rate limit, then an injected
// error, then latency. It reports whether the request goes on to its
// upstreams; when it doesn't, resp holds the error.
func (s *Server) inject(r *http.Request, resp *Response) bool {
	f := s.Faults()
	var rf requestFaults
	if s.cfg.RequestFaults {
		var err error
		if rf, err = parseRequestFaults(s.cfg.Name, r.URL.Query(), r.Header); err != nil {
			resp.Code, resp.Error = http.StatusBadRequest, err.Error()
			return false
		}
	}
	if !s.inj.allow(f.RateLimit, s.now()) {
		resp.Code, resp.Error, resp.Injected = f.RateLimitCode, "rate limit exceeded", "rate_limit"
		return false
	}
	rate := f.ErrorRate
	if rf.errorRate > 0 {
		rate = rf.errorRate
	}
	if rate > 0 && s.inj.float() < rate {
		if f.ErrorType == "delay" {
			s.sleep(r.Context(), f.ErrorDelay)
			resp.Injected = "delay"
		} else {
			resp.Code, resp.Error, resp.Injected = f.ErrorCode, "error injected", "error"
			return false
		}
	}
	s.sleep(r.Context(), s.inj.latency(f)+rf.delay)
	return true
}

// health answers the health check: HealthCode.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	status(w, s.Faults().HealthCode)
}

// ready fails until ReadyDelay has passed since the server started.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.now().Sub(s.started) < s.cfg.ReadyDelay {
		status(w, s.cfg.ReadyFailureCode)
		return
	}
	status(w, s.cfg.ReadyCode)
}

func status(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	state := "ok"
	if code >= 400 {
		state = "failing"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": state, "code": code})
}

func (s *Server) finish(resp *Response, start time.Time) {
	end := s.now()
	resp.StartTime = start.UTC().Format(time.RFC3339Nano)
	resp.EndTime = end.UTC().Format(time.RFC3339Nano)
	resp.Duration = end.Sub(start).String()
}

// callUpstreams calls every upstream, Workers at a time, keyed by URI.
func (s *Server) callUpstreams(ctx context.Context, id, trail, fault string) map[string]*Response {
	out := make(map[string]*Response, len(s.cfg.Upstreams))
	var mu sync.Mutex
	sem := make(chan struct{}, max(1, s.cfg.Workers))
	var wg sync.WaitGroup
	for _, u := range s.cfg.Upstreams {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r := s.call(ctx, u, id, trail, fault)
			mu.Lock()
			out[u] = r
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// call makes one upstream request. A fake upstream's JSON is nested as
// is; anything else becomes a Response with the code and body.
func (s *Server) call(ctx context.Context, uri, id, trail, fault string) *Response {
	start := s.now()
	fail := func(code int, err string) *Response {
		r := &Response{URI: uri, Type: "HTTP", Code: code, Error: err}
		s.finish(r, start)
		return r
	}
	hdr := http.Header{}
	hdr.Set(HeaderRequestID, id)
	hdr.Set(HeaderTrail, trail)
	if fault != "" {
		hdr.Set(HeaderFault, fault)
	}
	switch {
	case strings.HasPrefix(uri, "grpc://"):
		return s.callGRPC(ctx, uri, hdr)
	case strings.HasPrefix(uri, "tcp://"):
		return s.callTCP(ctx, uri)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return fail(-1, err.Error())
	}
	req.Header = hdr
	res, err := s.client.Do(req)
	if err != nil {
		return fail(-1, err.Error())
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return fail(res.StatusCode, err.Error())
	}
	var up Response
	if json.Unmarshal(raw, &up) != nil || up.Name == "" {
		r := fail(res.StatusCode, "")
		r.Body = strings.TrimSpace(string(raw))
		if res.StatusCode >= 400 {
			r.Error = res.Status
		}
		return r
	}
	// The upstream's own times are its view; keep them, but the URI and
	// code are what this caller saw (through Envoy, which may differ).
	up.URI = uri
	up.Code = res.StatusCode
	return &up
}

func body(msg string) any {
	if msg == "" {
		return nil
	}
	var v any
	if t := strings.TrimSpace(msg); (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Unmarshal([]byte(t), &v) == nil {
		return v
	}
	return msg
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func localIPs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
			out = append(out, n.IP.String())
		}
	}
	return out
}

func injectedNote(r *Response) string {
	if r.Injected == "" {
		return ""
	}
	return " injected=" + r.Injected
}

func errNote(e string) string {
	if e == "" {
		return ""
	}
	return fmt.Sprintf(" error=%q", e)
}
