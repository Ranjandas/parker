package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	parkerv1 "github.com/Ranjandas/parker/internal/api/parkerv1"
	"github.com/Ranjandas/parker/internal/api/parkerv1/parkerv1connect"
)

type callStub struct {
	parkerv1connect.UnimplementedParkerServiceHandler
	n    int
	reqs []*parkerv1.FakeRequest
}

func (s *callStub) Fake(_ context.Context, req *connect.Request[parkerv1.FakeRequest]) (*connect.Response[parkerv1.FakeResponse], error) {
	s.reqs = append(s.reqs, req.Msg)
	s.n++
	dc := "dc1"
	if s.n%4 == 0 {
		dc = "dc2"
	}
	body := fmt.Sprintf(`{"name":"web","dc":"dc1","code":200,"duration":"2ms","fact":"Fact %d.","upstream_calls":{"u":{"name":"api","dc":%q,"code":200,"duration":"1ms"}}}`, s.n, dc)
	return connect.NewResponse(&parkerv1.FakeResponse{CallStatus: 200, CallBody: body}), nil
}

func stubClient(t *testing.T) (*callStub, parkerv1connect.ParkerServiceClient) {
	st := &callStub{}
	_, h := parkerv1connect.NewParkerServiceHandler(st)
	mux := http.NewServeMux()
	mux.Handle("/", h)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return st, parkerv1connect.NewParkerServiceClient(ts.Client(), ts.URL)
}

func TestCallOnce(t *testing.T) {
	st, cl := stubClient(t)
	var out bytes.Buffer
	req := &parkerv1.FakeRequest{Lab: "x", Selector: "dc1/web", Action: "call", Path: "/a", Headers: map[string]string{"X-Parker-Fault": "api=delay:1s"}}
	if err := callOnce(context.Background(), cl, req, &callFlags{}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "web  dc1  200  2ms\n└─ api  dc1  200  1ms\n" {
		t.Errorf("tree:\n%s", got)
	}
	if r := st.reqs[0]; r.Path != "/a" || r.Headers["X-Parker-Fault"] != "api=delay:1s" {
		t.Errorf("request = %+v", r)
	}
	out.Reset()
	_ = callOnce(context.Background(), cl, req, &callFlags{json: true}, &out)
	if !strings.HasPrefix(out.String(), `{"name":"web"`) {
		t.Errorf("--json: %s", out.String())
	}
	out.Reset()
	_ = callOnce(context.Background(), cl, req, &callFlags{fact: true}, &out)
	if !strings.Contains(out.String(), "✦ Fact 3.") {
		t.Errorf("--fact: %s", out.String())
	}
}

func TestCallLoop(t *testing.T) {
	_, cl := stubClient(t)
	var out bytes.Buffer
	req := &parkerv1.FakeRequest{Lab: "x", Selector: "dc1/web", Action: "call"}
	if err := callLoop(context.Background(), cl, req, &callFlags{every: time.Millisecond, count: 8}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if n := strings.Count(got, "web@dc1 → api@dc"); n != 8 {
		t.Errorf("%d lines:\n%s", n, got)
	}
	if !strings.Contains(got, "--- 8 requests, 0 failed\n    api: dc1 75% · dc2 25%") {
		t.Errorf("summary:\n%s", got)
	}
}

func TestCallRequestFlags(t *testing.T) {
	f := &callFlags{headers: []string{"X-A: 1"}, pads: []string{"Cookie=9KB"}, body: "2MB"}
	req, err := f.request("lab", []string{"dc1/web", "/a"})
	if err != nil {
		t.Fatal(err)
	}
	if n := len("Cookie: ") + len(req.Headers["Cookie"]); n != 9<<10 {
		t.Errorf("pad header line is %d bytes, want 9KB", n)
	}
	if req.Headers["X-A"] != "1" || req.BodyBytes != 2<<20 || req.Path != "/a" || req.Selector != "dc1/web" {
		t.Errorf("request = %+v", req)
	}
	for _, bad := range []*callFlags{{pads: []string{"Cookie"}}, {pads: []string{"Cookie=lots"}}, {body: "big"}} {
		if _, err := bad.request("lab", []string{"dc1/web"}); err == nil {
			t.Errorf("%+v: want an error", bad)
		}
	}
}
