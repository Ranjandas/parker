package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Ranjandas/parker/internal/spec"
)

// SocketName is the control socket of a fake service, in its node's
// directory.
func SocketName(service string) string { return service + ".fake.sock" }

// ControlHandler serves the control API: GET /faults, POST /faults (a
// fake: block's fault fields, applied over the current ones) and POST
// /reset (back to the faults the service started with).
func (s *Server) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /faults", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Faults())
	})
	mux.HandleFunc("POST /faults", func(w http.ResponseWriter, r *http.Request) {
		var change spec.Fake
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&change); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		f, err := s.Change(&change)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, f)
	})
	mux.HandleFunc("POST /reset", func(w http.ResponseWriter, r *http.Request) {
		s.SetFaults(s.cfg.Faults)
		s.log.Printf("faults reset: %s", s.cfg.Faults.Summary())
		writeJSON(w, http.StatusOK, s.cfg.Faults)
	})
	return mux
}

// Change applies a change to the faults in force and returns them.
func (s *Server) Change(change *spec.Fake) (Faults, error) {
	s.mu.Lock()
	f := s.faults
	err := f.Apply(change)
	if err == nil {
		s.faults = f
	}
	s.mu.Unlock()
	if err != nil {
		return Faults{}, err
	}
	s.log.Printf("faults changed: %s", f.Summary())
	return f, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ListenControl serves the control API on a unix socket at path,
// replacing a stale one.
func (s *Server) ListenControl(path string) error {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	go func() { _ = http.Serve(l, s.ControlHandler()) }()
	return nil
}

func controlClient(sock string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
}

// Control talks to a running fake service's control socket: change nil
// gets its faults, reset puts the starting ones back, and anything else
// applies change.
func Control(ctx context.Context, sock string, change *spec.Fake, reset bool) (Faults, error) {
	method, path := http.MethodGet, "/faults"
	var body io.Reader
	switch {
	case reset:
		method, path = http.MethodPost, "/reset"
	case change != nil:
		b, err := json.Marshal(change)
		if err != nil {
			return Faults{}, err
		}
		method, body = http.MethodPost, bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://fake"+path, body)
	if err != nil {
		return Faults{}, err
	}
	res, err := controlClient(sock).Do(req)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file") {
			return Faults{}, errors.New("no control socket: is the service running (and was the lab brought up by this Parker version)?")
		}
		return Faults{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var e struct{ Error string }
		_ = json.NewDecoder(res.Body).Decode(&e)
		return Faults{}, errors.New(e.Error)
	}
	var f Faults
	return f, json.NewDecoder(res.Body).Decode(&f)
}

// Summary is the faults on one line: "health 503 · errors 30% (503) ·
// latency p50 20ms p99 1s · rate limit 50/s (429)".
func (f Faults) Summary() string {
	var parts []string
	parts = append(parts, fmt.Sprintf("health %d", f.HealthCode))
	if f.ErrorRate > 0 {
		e := fmt.Sprintf("errors %g%% (%d)", f.ErrorRate*100, f.ErrorCode)
		if f.ErrorType == "delay" {
			e = fmt.Sprintf("errors %g%% (delay %s)", f.ErrorRate*100, f.ErrorDelay)
		}
		parts = append(parts, e)
	} else {
		parts = append(parts, "errors off")
	}
	if f.P50 > 0 || f.P90 > 0 || f.P99 > 0 {
		l := "latency"
		for _, p := range []struct {
			n string
			d time.Duration
		}{{"p50", f.P50}, {"p90", f.P90}, {"p99", f.P99}} {
			if p.d > 0 {
				l += fmt.Sprintf(" %s %s", p.n, p.d)
			}
		}
		if f.Variance > 0 {
			l += fmt.Sprintf(" ±%d%%", f.Variance)
		}
		parts = append(parts, l)
	} else {
		parts = append(parts, "latency off")
	}
	if f.RateLimit > 0 {
		parts = append(parts, fmt.Sprintf("rate limit %g/s (%d)", f.RateLimit, f.RateLimitCode))
	} else {
		parts = append(parts, "rate limit off")
	}
	return strings.Join(parts, " · ")
}
