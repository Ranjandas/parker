package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Level is how much a fake service logs: debug adds each request's
// headers and upstream calls, info (the default) logs every request, warn
// only failed or faulted requests, error only failed ones.
type Level int32

const (
	LevelDebug Level = iota - 1
	// LevelInfo is the zero Level.
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	}
	return "info"
}

// ParseLevel reads a level; trace is debug, warning is warn.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace", "debug":
		return LevelDebug, nil
	case "info", "":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error", "err":
		return LevelError, nil
	}
	return LevelInfo, fmt.Errorf("log level %q: want trace, debug, info, warn or error", s)
}

// Level is the log level in force.
func (s *Server) Level() Level { return Level(s.level.Load()) }

// SetLevel changes the log level; the change itself is always logged.
func (s *Server) SetLevel(l Level) {
	s.level.Store(int32(l))
	s.log.Printf("[INFO] log level: %s", l)
}

// logf logs at level l when the level in force lets it through.
func (s *Server) logf(l Level, format string, a ...any) {
	if l < s.Level() {
		return
	}
	s.log.Printf("["+strings.ToUpper(l.String())+"] "+format, a...)
}

// logLevelHandlers serve GET /log-level and POST /log-level
// ({"level":"debug"} or {"reset":true}, back to the starting level).
func (s *Server) logLevelHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /log-level", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"level": s.Level().String()})
	})
	mux.HandleFunc("POST /log-level", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Level string `json:"level"`
			Reset bool   `json:"reset"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		l := s.cfg.LogLevel
		if !req.Reset {
			var err error
			if l, err = ParseLevel(req.Level); err != nil || req.Level == "" {
				if err == nil {
					err = errors.New("want level or reset")
				}
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		}
		s.SetLevel(l)
		writeJSON(w, http.StatusOK, map[string]string{"level": l.String()})
	})
}

// ControlLogLevel reads (level "" and no reset), sets or resets a running
// fake service's log level through its control socket.
func ControlLogLevel(ctx context.Context, sock, level string, reset bool) (string, error) {
	method := http.MethodGet
	var body io.Reader
	if level != "" || reset {
		b, _ := json.Marshal(map[string]any{"level": level, "reset": reset})
		method, body = http.MethodPost, bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://fake/log-level", body)
	if err != nil {
		return "", err
	}
	res, err := controlClient(sock).Do(req)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file") {
			return "", errors.New("no control socket: is the service running (and was the lab brought up by this Parker version)?")
		}
		return "", err
	}
	defer res.Body.Close()
	var out struct{ Level, Error string }
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode == http.StatusNotFound {
		return "", errors.New("this fake service has no log levels: restart it with this Parker version")
	}
	if res.StatusCode != http.StatusOK {
		return "", errors.New(out.Error)
	}
	return out.Level, nil
}
