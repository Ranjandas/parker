package fake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// HeaderRequestID ties the hops of one request together.
const HeaderRequestID = "X-Request-Id"

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
}

// Server is a fake service.
type Server struct {
	cfg    *Config
	client *http.Client
	ips    []string
	log    *log.Logger
	// now is time.Now outside tests.
	now func() time.Time
}

func New(cfg *Config) *Server {
	return &Server{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.ClientTimeout},
		ips:    localIPs(),
		log:    log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds),
		now:    time.Now,
	}
}

// Handler serves the fake service's HTTP API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serve)
	return mux
}

// ListenAndServe serves on cfg.Port until the process ends.
func (s *Server) ListenAndServe() error {
	fmt.Printf("fake-service %s listening on :%d (upstreams: %s)\n", s.cfg.Name, s.cfg.Port, strings.Join(s.cfg.Upstreams, ", "))
	return http.ListenAndServe(fmt.Sprintf(":%d", s.cfg.Port), s.Handler())
}

// Serve is ListenAndServe for a config built from the environment.
func Serve(name, dc string, port int) error {
	cfg, err := Load(name, dc, port, nil)
	if err != nil {
		return err
	}
	return New(cfg).ListenAndServe()
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	start := s.now()
	id := r.Header.Get(HeaderRequestID)
	if id == "" {
		id = newID()
	}
	resp := &Response{
		Name: s.cfg.Name, Node: s.cfg.Node, DC: s.cfg.DC, Version: s.cfg.Version,
		URI: r.URL.RequestURI(), Type: "HTTP", IPAddresses: s.ips, From: r.RemoteAddr,
		Body: body(s.cfg.Message), Code: http.StatusOK,
	}
	if len(s.cfg.Upstreams) > 0 {
		resp.UpstreamCalls = s.callUpstreams(r.Context(), id)
		for _, u := range s.cfg.Upstreams {
			if up := resp.UpstreamCalls[u]; up.Error != "" || up.Code >= 400 {
				if !s.cfg.IgnoreUpstreamErrors {
					resp.Code = http.StatusInternalServerError
					resp.Error = "upstream " + u + " failed"
				}
				break
			}
		}
	}
	s.finish(resp, start)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(HeaderRequestID, id)
	w.WriteHeader(resp.Code)
	_ = json.NewEncoder(w).Encode(resp)
	s.log.Printf("request id=%s path=%s from=%s code=%d duration=%s", id, resp.URI, r.RemoteAddr, resp.Code, resp.Duration)
}

func (s *Server) finish(resp *Response, start time.Time) {
	end := s.now()
	resp.StartTime = start.UTC().Format(time.RFC3339Nano)
	resp.EndTime = end.UTC().Format(time.RFC3339Nano)
	resp.Duration = end.Sub(start).String()
}

// callUpstreams calls every upstream, Workers at a time, keyed by URI.
func (s *Server) callUpstreams(ctx context.Context, id string) map[string]*Response {
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
			r := s.call(ctx, u, id)
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
func (s *Server) call(ctx context.Context, uri, id string) *Response {
	start := s.now()
	fail := func(code int, err string) *Response {
		r := &Response{URI: uri, Type: "HTTP", Code: code, Error: err}
		s.finish(r, start)
		return r
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return fail(-1, err.Error())
	}
	req.Header.Set(HeaderRequestID, id)
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
