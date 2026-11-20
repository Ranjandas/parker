package fake

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
)

func TestWireCodecRoundTrip(t *testing.T) {
	c := wireCodec{}
	for _, m := range []any{&handleRequest{Data: []byte("hi")}, &handleResponse{Message: `{"name":"api"}`},
		&healthRequest{Service: "api"}, &healthResponse{Status: healthNotServing}, &handleResponse{}} {
		b, err := c.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var got any
		switch m.(type) {
		case *handleRequest:
			got = &handleRequest{}
		case *handleResponse:
			got = &handleResponse{}
		case *healthRequest:
			got = &healthRequest{}
		case *healthResponse:
			got = &healthResponse{}
		}
		if err := c.Unmarshal(b, got); err != nil {
			t.Fatal(err)
		}
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(m)
		if string(gb) != string(wb) {
			t.Errorf("round trip: got %s, want %s", gb, wb)
		}
	}
	// Unknown fields are skipped, as protobuf requires.
	var r handleResponse
	if err := c.Unmarshal([]byte{0x10, 0x05, 0x0a, 0x01, 'x'}, &r); err != nil || r.Message != "x" {
		t.Errorf("unknown field: %v %+v", err, r)
	}
	if err := c.Unmarshal([]byte{0x0a, 0x05}, &r); err == nil {
		t.Error("a truncated message should fail")
	}
}

// startGRPC serves a grpc-mode fake over h2c.
func startGRPC(t *testing.T, cfg *Config) (*httptest.Server, *Server) {
	t.Helper()
	cfg.Protocol = "grpc"
	s := New(cfg)
	s.log = log.New(io.Discard, "", 0)
	ts := httptest.NewUnstartedServer(s.Handler())
	ts.Config.Protocols = new(http.Protocols)
	ts.Config.Protocols.SetHTTP1(true)
	ts.Config.Protocols.SetUnencryptedHTTP2(true)
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, s
}

func grpcURI(ts *httptest.Server) string { return "grpc://" + strings.TrimPrefix(ts.URL, "http://") }

func TestGRPCChain(t *testing.T) {
	f := DefaultFaults()
	db, dbs := startGRPC(t, &Config{Name: "db", Workers: 1, Faults: f})
	// An HTTP fake calls a gRPC one, which answers with its JSON nested.
	web, _ := start(t, &Config{Name: "web", Upstreams: []string{grpcURI(db)}, Workers: 1, Faults: f})
	r, res := get(t, web.URL, map[string]string{HeaderRequestID: "id1"})
	up := r.UpstreamCalls[grpcURI(db)]
	if res.StatusCode != 200 || up == nil || up.Name != "db" || up.Type != "gRPC" || up.Code != 200 {
		t.Fatalf("web -> db over gRPC: %d %+v", res.StatusCode, r.UpstreamCalls)
	}
	// An injected error comes back as a gRPC status, mapped to its code.
	f.ErrorRate, f.ErrorCode = 1, 503
	dbs.SetFaults(f)
	r, res = get(t, web.URL, nil)
	up = r.UpstreamCalls[grpcURI(db)]
	if res.StatusCode != 500 || up == nil || up.Code != 503 || up.Injected != "error" || up.Name != "db" {
		t.Errorf("injected error over gRPC: %d %+v", res.StatusCode, up)
	}
	// The plain HTTP API still answers on the same port.
	if r, _ := get(t, db.URL+"/health", nil); r == nil {
		t.Error("no /health in grpc mode")
	}
}

func TestGRPCUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	web, _ := start(t, &Config{Name: "web", Upstreams: []string{"grpc://" + addr}, Workers: 1, Faults: DefaultFaults()})
	r, res := get(t, web.URL, nil)
	if up := r.UpstreamCalls["grpc://"+addr]; res.StatusCode != 500 || up == nil || up.Code != -1 || up.Error == "" {
		t.Errorf("unreachable gRPC upstream: %d %+v", res.StatusCode, r.UpstreamCalls)
	}
}

func TestGRPCHealth(t *testing.T) {
	ts, s := startGRPC(t, &Config{Name: "db", Workers: 1, Faults: DefaultFaults()})
	cl := connect.NewClient[healthRequest, healthResponse](h2cClient(5*time.Second), ts.URL+procHealthCheck,
		connect.WithGRPC(), connect.WithCodec(wireCodec{}))
	check := func() int32 {
		res, err := cl.CallUnary(context.Background(), connect.NewRequest(&healthRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg.Status
	}
	if st := check(); st != healthServing {
		t.Errorf("status = %d, want serving", st)
	}
	f := s.Faults()
	f.HealthCode = 503
	s.SetFaults(f)
	if st := check(); st != healthNotServing {
		t.Errorf("status = %d, want not serving", st)
	}
}

func TestGRPCCodes(t *testing.T) {
	for _, c := range []int{400, 404, 429, 500, 503, 504} {
		if got := httpCode(grpcCode(c)); got != c {
			t.Errorf("%d -> %v -> %d", c, grpcCode(c), got)
		}
	}
	var ce *connect.Error
	if !errors.As(connect.NewError(grpcCode(418), errors.New("x")), &ce) || ce.Code() != connect.CodeInternal {
		t.Error("unmapped codes should be internal")
	}
}
