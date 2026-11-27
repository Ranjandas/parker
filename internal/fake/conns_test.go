package fake

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/Ranjandas/parker/internal/spec"
)

// held starts a fake whose one upstream blocks until release is called,
// so its requests stay in flight.
func held(t *testing.T, l Limits) (url string, release func()) {
	t.Helper()
	block := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	f := DefaultFaults()
	f.Limits = l
	ts, _ := start(t, &Config{Name: "api", Upstreams: []string{up.URL}, Workers: 1, Faults: f, ClientTimeout: 10 * time.Second})
	once := false
	release = func() {
		if !once {
			once = true
			close(block)
		}
	}
	t.Cleanup(func() { release(); up.Close() })
	return ts.URL, release
}

// send makes a request in the background.
func send(url string) chan *http.Response {
	ch := make(chan *http.Response, 1)
	go func() {
		res, err := http.Get(url)
		if err != nil {
			ch <- nil
			return
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		ch <- res
	}()
	return ch
}

func TestPoolRefuses(t *testing.T) {
	for _, when := range []string{"refuse", "answer"} {
		l := DefaultLimits()
		l.Concurrent, l.WhenFull = 1, when
		url, release := held(t, l)
		first := send(url)
		time.Sleep(100 * time.Millisecond) // the first is in flight
		res, err := http.Get(url)
		switch when {
		case "refuse":
			if err == nil {
				t.Errorf("refuse: got %d, want the connection closed", res.StatusCode)
			}
		case "answer":
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 503 || !strings.Contains(string(b), `"limit":"max_concurrent"`) || !strings.Contains(string(b), "server busy: 1 requests at once") {
				t.Errorf("answer: %d %s", res.StatusCode, b)
			}
		}
		release()
		if r := <-first; r == nil || r.StatusCode != 200 {
			t.Errorf("%s: first request: %+v", when, r)
		}
	}
}

func TestPoolQueues(t *testing.T) {
	l := DefaultLimits()
	l.Concurrent, l.Queue = 1, 1
	url, release := held(t, l)
	first := send(url)
	time.Sleep(100 * time.Millisecond)
	second := send(url) // waits for the first
	time.Sleep(100 * time.Millisecond)
	if _, err := http.Get(url); err == nil {
		t.Error("third request: want it refused, the queue is full")
	}
	release()
	for i, ch := range []chan *http.Response{first, second} {
		if r := <-ch; r == nil {
			t.Errorf("request %d failed", i+1)
		}
	}
}

func TestMaxRequestsPerConn(t *testing.T) {
	f := DefaultFaults()
	f.Limits.PerConn = 2
	s := New(&Config{Name: "api", Workers: 1, Faults: f})
	s.log.SetOutput(io.Discard)
	ts := httptest.NewUnstartedServer(s.Handler())
	ts.Config.ConnContext = connContext
	ts.Start()
	t.Cleanup(ts.Close)
	cl := &http.Client{}
	var closes []bool
	for range 3 {
		res, err := cl.Get(ts.URL)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		closes = append(closes, res.Close)
	}
	if closes[0] || !closes[1] || closes[2] {
		t.Errorf("Connection: close on answers %v, want only the 2nd", closes)
	}
}

func TestConfigureServer(t *testing.T) {
	f := DefaultFaults()
	if err := f.Apply(&spec.Fake{Limits: &spec.FakeLimits{IdleTimeout: "20s", HeaderTimeout: "5s", H2MaxStreams: count(10)}}); err != nil {
		t.Fatal(err)
	}
	s := New(&Config{Faults: f})
	srv := &http.Server{}
	s.configure(srv)
	if srv.IdleTimeout != 20*time.Second || srv.ReadHeaderTimeout != 5*time.Second || srv.HTTP2.MaxConcurrentStreams != 10 || srv.ConnContext == nil || srv.MaxHeaderBytes != maxHeaderCap {
		t.Errorf("server: %+v", srv)
	}
	if got := f.Limits.summary(); got != "idle 20s, header timeout 5s, 10 h2 streams" {
		t.Errorf("summary: %s", got)
	}
	for _, bad := range []*spec.FakeLimits{{WhenFull: "drop"}, {IdleTimeout: "soon"}, {Queue: count(-1)}} {
		if err := Validate(&spec.Fake{Limits: bad}); err == nil {
			t.Errorf("%+v: want an error", bad)
		}
	}
}

func TestPoolGRPC(t *testing.T) {
	f := DefaultFaults()
	f.Limits.Concurrent = 1
	ts, s := startGRPC(t, &Config{Name: "db", Workers: 1, Faults: f})
	// Take the one worker.
	if !s.gate.enter(context.Background(), f.Limits) {
		t.Fatal("enter")
	}
	defer s.gate.leave()
	cl := connect.NewClient[handleRequest, handleResponse](New(&Config{ClientTimeout: 5e9}).h2c, ts.URL+procHandle, connect.WithGRPC(), connect.WithCodec(wireCodec{}))
	_, err := cl.CallUnary(context.Background(), connect.NewRequest(&handleRequest{}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeUnavailable || !strings.Contains(ce.Message(), "server busy") {
		t.Errorf("busy gRPC: %v", err)
	}
}
