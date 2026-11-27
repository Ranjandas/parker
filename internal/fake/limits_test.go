package fake

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	"github.com/Ranjandas/parker/internal/spec"
)

func size(n int64) *spec.ByteSize { b := spec.ByteSize(n); return &b }
func count(n int) *int            { return &n }

func limitsOf(t *testing.T, sl *spec.FakeLimits) Limits {
	t.Helper()
	f := DefaultFaults()
	if err := f.Apply(&spec.Fake{Limits: sl}); err != nil {
		t.Fatal(err)
	}
	return f.Limits
}

// Each limit passes a request at its value and refuses one byte (or one
// header, or one parameter) over it.
func TestLimitsAtAndOver(t *testing.T) {
	req := func(mod func(*http.Request)) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/a?x=1&y=2", nil)
		r.Header.Set("Cookie", strings.Repeat("c", 100))
		if mod != nil {
			mod(r)
		}
		return r
	}
	base := measureHead(req(nil), "/a?x=1&y=2")
	cookie := int64(len("Cookie: ") + 100)
	for _, c := range []struct {
		name     string
		limits   *spec.FakeLimits
		over     func(*http.Request)
		key      string
		wantCode int
	}{
		{"header bytes", &spec.FakeLimits{MaxHeaderBytes: size(base.total)},
			func(r *http.Request) { r.Header.Set("X-A", "1") }, "max_header_bytes", 431},
		{"header line", &spec.FakeLimits{MaxHeaderLineBytes: size(cookie)},
			func(r *http.Request) { r.Header.Set("Cookie", strings.Repeat("c", 101)) }, "max_header_line_bytes", 431},
		{"header count", &spec.FakeLimits{MaxHeaderCount: count(base.count)},
			func(r *http.Request) { r.Header.Set("X-A", "1") }, "max_header_count", 431},
		{"url", &spec.FakeLimits{MaxURLBytes: size(int64(len("/a?x=1&y=2")))},
			func(r *http.Request) { r.RequestURI = "/a?x=1&y=22" }, "max_url_bytes", 414},
		{"form params", &spec.FakeLimits{MaxFormParams: count(2)},
			func(r *http.Request) { r.RequestURI = "/a?x=1&y=2&z=3"; r.URL.RawQuery = "x=1&y=2&z=3" }, "max_form_params", 400},
		{"tomcat codes", &spec.FakeLimits{MaxHeaderBytes: size(base.total), Codes: &spec.FakeLimitCodes{Header: 400}},
			func(r *http.Request) { r.Header.Set("X-A", "1") }, "max_header_bytes", 400},
	} {
		l := limitsOf(t, c.limits)
		if rf := l.check(req(nil)); rf != nil {
			t.Errorf("%s: at the limit, refused: %s", c.name, rf.msg)
		}
		rf := l.check(req(c.over))
		if rf == nil {
			t.Errorf("%s: over the limit, passed", c.name)
			continue
		}
		if rf.limit != c.key || rf.code != c.wantCode || !strings.Contains(rf.msg, c.key) {
			t.Errorf("%s: got %d %s %q", c.name, rf.code, rf.limit, rf.msg)
		}
	}
}

func TestLimitsBody(t *testing.T) {
	l := limitsOf(t, &spec.FakeLimits{MaxBodyBytes: size(10), MaxFormParams: count(2)})
	post := func(body string, chunked bool, ct string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://api/", strings.NewReader(body))
		if chunked {
			r.ContentLength = -1
		}
		r.Header.Set("Content-Type", ct)
		return r
	}
	if rf := l.check(post("0123456789", false, "text/plain")); rf != nil {
		t.Errorf("10 bytes: %s", rf.msg)
	}
	for _, chunked := range []bool{false, true} {
		rf := l.check(post("0123456789x", chunked, "text/plain"))
		if rf == nil || rf.code != 413 || rf.limit != "max_body_bytes" {
			t.Errorf("11 bytes (chunked %v): %+v", chunked, rf)
		}
	}
	// Form parameters count the query and a urlencoded body together.
	r := post("a=1&b=2", false, "application/x-www-form-urlencoded")
	r.URL.RawQuery = "q=1"
	if rf := l.check(r); rf == nil || rf.limit != "max_form_params" {
		t.Errorf("3 params: %+v", rf)
	}
	// The body is still there for whoever reads it after the check.
	r = post("a=1", false, "application/x-www-form-urlencoded")
	if rf := l.check(r); rf != nil {
		t.Fatal(rf.msg)
	}
	if err := r.ParseForm(); err != nil || r.PostForm.Get("a") != "1" {
		t.Errorf("body after check: %v %v", err, r.PostForm)
	}
}

func TestLimitsApply(t *testing.T) {
	f := DefaultFaults()
	if f.Limits.On() {
		t.Error("no limits by default")
	}
	if err := f.Apply(&spec.Fake{Limits: &spec.FakeLimits{MaxHeaderBytes: size(8192), MaxBodyBytes: size(1 << 20)}}); err != nil {
		t.Fatal(err)
	}
	if got := f.Summary(); !strings.HasSuffix(got, " · limits headers 8KB (431), body 1MB (413)") {
		t.Errorf("summary: %s", got)
	}
	// A change sets only what it names; 0 turns a limit off.
	if err := f.Apply(&spec.Fake{Limits: &spec.FakeLimits{MaxHeaderBytes: size(0)}}); err != nil {
		t.Fatal(err)
	}
	if f.Limits.HeaderBytes != 0 || f.Limits.BodyBytes != 1<<20 {
		t.Errorf("after change: %+v", f.Limits)
	}
	for _, bad := range []*spec.FakeLimits{
		{MaxHeaderBytes: size(maxHeaderCap + 1)},
		{MaxHeaderCount: count(-1)},
		{Codes: &spec.FakeLimitCodes{Header: 99}},
	} {
		if err := Validate(&spec.Fake{Limits: bad}); err == nil {
			t.Errorf("%+v: want an error", bad)
		}
	}
}

// A refused request answers the limit's code, names the limit, and
// doesn't reach the upstreams.
func TestLimitRefusesBeforeUpstreams(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	t.Cleanup(up.Close)
	f := DefaultFaults()
	f.Limits.HeaderBytes, f.Limits.HeaderCode = 1024, 400
	ts, _ := start(t, &Config{Name: "api", Upstreams: []string{up.URL}, Workers: 1, Faults: f})
	r, res := get(t, ts.URL, map[string]string{"Cookie": strings.Repeat("c", 2000)})
	if res.StatusCode != 400 || r.Code != 400 || r.Limit != "max_header_bytes" || !strings.Contains(r.Error, "> max_header_bytes 1KB") {
		t.Errorf("got %d %+v", res.StatusCode, r)
	}
	if calls.Load() != 0 {
		t.Error("a refused request called its upstream")
	}
	if line := hopLine(r); !strings.Contains(line, "400") || !strings.Contains(line, "max_header_bytes") {
		t.Errorf("render: %q", line)
	}
	if r, _ := get(t, ts.URL, nil); r.Code != 200 || calls.Load() != 1 {
		t.Errorf("small request: %+v", r)
	}
}

// Over HTTP the server reads more than Go's 1 MB of headers, so the
// limit, not net/http, decides.
func TestLimitAboveGoDefault(t *testing.T) {
	f := DefaultFaults()
	f.Limits.HeaderBytes = 2 << 20
	s := New(&Config{Name: "api", Workers: 1, Faults: f})
	s.log.SetOutput(new(strings.Builder))
	ts := httptest.NewUnstartedServer(s.Handler())
	ts.Config.MaxHeaderBytes = maxHeaderCap
	ts.Start()
	t.Cleanup(ts.Close)
	big := strings.Repeat("c", 1<<20+100)
	if r, res := get(t, ts.URL, map[string]string{"Cookie": big}); res.StatusCode != 200 {
		t.Errorf("1 MB header under a 2 MB limit: %d %s", res.StatusCode, r.Error)
	}
}

// With protocol: grpc, max_body_bytes bounds the request message.
func TestLimitGRPCMessage(t *testing.T) {
	f := DefaultFaults()
	f.Limits.BodyBytes = 10
	ts, _ := startGRPC(t, &Config{Name: "db", Workers: 1, Faults: f})
	s := New(&Config{ClientTimeout: 5e9})
	cl := connect.NewClient[handleRequest, handleResponse](s.h2c, ts.URL+procHandle, connect.WithGRPC(), connect.WithCodec(wireCodec{}))
	if _, err := cl.CallUnary(context.Background(), connect.NewRequest(&handleRequest{Data: []byte("small")})); err != nil {
		t.Fatalf("small message: %v", err)
	}
	_, err := cl.CallUnary(context.Background(), connect.NewRequest(&handleRequest{Data: []byte(strings.Repeat("x", 11))}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeResourceExhausted || !strings.Contains(ce.Message(), "max_body_bytes") {
		t.Errorf("big message: %v", err)
	}
}
