package fake

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func TestPadValue(t *testing.T) {
	for _, n := range []int64{100, 9 << 10} {
		if got := int64(len("Cookie: ") + len(PadValue("Cookie", n))); got != n {
			t.Errorf("%d: line is %d bytes", n, got)
		}
	}
	if PadValue("Cookie", 3) != "x" {
		t.Error("a pad smaller than the name still has a value")
	}
	if _, _, err := ParsePad("Cookie=9KB"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"Cookie", "=9KB", "Cookie=x", "Bad Name=1KB", "Cookie=8MB"} {
		if _, _, err := ParsePad(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestUpstreamRequestConfig(t *testing.T) {
	c, err := Load("web", "", 1, env(map[string]string{EnvConfig: `{"forward_headers":["Cookie"],"upstream_request":{"headers":{"X-T":"a"},"pad_header":{"bytes":"1KB"},"body_bytes":"2KB"}}`}))
	if err != nil {
		t.Fatal(err)
	}
	u := c.Upstream
	if strings.Join(u.Forward, ",") != "Cookie" || u.Headers["X-T"] != "a" || u.PadName != DefaultPadHeader || u.PadBytes != 1024 || u.BodyBytes != 2048 || u.method() != "POST" {
		t.Errorf("upstream request: %+v (%s)", u, u.method())
	}
	for _, bad := range []string{
		`{"forward_headers":["Bad Name"]}`,
		`{"upstream_request":{"method":"FETCH"}}`,
		`{"upstream_request":{"pad_header":{"name":"a:b","bytes":10}}}`,
		`{"upstream_request":{"pad_header":{"bytes":"8MB"}}}`,
	} {
		if _, err := Load("web", "", 1, env(map[string]string{EnvConfig: bad})); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
}

// seen records what an upstream received.
type seen struct {
	mu     sync.Mutex
	method string
	hdr    http.Header
	body   int
}

func recorder(t *testing.T) (*httptest.Server, *seen) {
	s := &seen{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.method, s.hdr, s.body = r.Method, r.Header.Clone(), len(b)
		s.mu.Unlock()
	}))
	t.Cleanup(ts.Close)
	return ts, s
}

func TestUpstreamCallsCarry(t *testing.T) {
	up, got := recorder(t)
	f := DefaultFaults()
	cfg := &Config{Name: "web", Upstreams: []string{up.URL}, Workers: 1, Faults: f, RequestFaults: true,
		Upstream: UpstreamRequest{Forward: []string{"Cookie"}, Headers: map[string]string{"X-T": "a"}, PadName: "X-Big", PadBytes: 500, BodyBytes: 300}}
	ts, _ := start(t, cfg)
	get(t, ts.URL, map[string]string{"Cookie": "s=1", "Authorization": "secret"})
	if got.method != "POST" || got.hdr.Get("Cookie") != "s=1" || got.hdr.Get("Authorization") != "" || got.hdr.Get("X-T") != "a" || got.body != 300 {
		t.Errorf("upstream saw %s %v body %d", got.method, got.hdr, got.body)
	}
	if n := len("X-Big: ") + len(got.hdr.Get("X-Big")); n != 500 {
		t.Errorf("pad header is %d bytes", n)
	}
	// A caller pads this hop's upstream calls for one request.
	get(t, ts.URL, map[string]string{HeaderFault: "web=pad-upstreams:2KB"})
	if n := len(DefaultPadHeader+": ") + len(got.hdr.Get(DefaultPadHeader)); n != 2048 {
		t.Errorf("pad-upstreams header is %d bytes", n)
	}
}

// The case behind #176: a front service passes a 9 KB cookie to an
// upstream that, like Tomcat, refuses headers over 8 KB with a 400.
func TestBigCookieThroughChain(t *testing.T) {
	f := DefaultFaults()
	limited := f
	limited.Limits.HeaderBytes, limited.Limits.HeaderCode = 8<<10, 400
	api, _ := start(t, &Config{Name: "api", Workers: 1, Faults: limited})
	cookie := PadValue("Cookie", 9<<10)
	for _, c := range []struct {
		name    string
		forward []string
		code    int
	}{{"forwarded", []string{"Cookie"}, 400}, {"not forwarded", nil, 200}} {
		web, _ := start(t, &Config{Name: "web", Upstreams: []string{api.URL}, Workers: 1, Faults: f, Upstream: UpstreamRequest{Forward: c.forward}})
		r, _ := get(t, web.URL, map[string]string{"Cookie": cookie})
		up := r.UpstreamCalls[api.URL]
		if up.Code != c.code {
			t.Errorf("%s: api answered %d (%s), want %d", c.name, up.Code, up.Error, c.code)
		}
		if c.code == 400 && (up.Limit != "max_header_bytes" || r.Code != 500) {
			t.Errorf("%s: web %d, api limit %q", c.name, r.Code, up.Limit)
		}
	}
}

func TestMergeUpstreamRequestValidates(t *testing.T) {
	b := spec.ByteSize(-1)
	if err := Validate(&spec.Fake{UpstreamRequest: &spec.FakeUpstreamRequest{BodyBytes: &b}}); err == nil {
		t.Error("negative body_bytes: want an error")
	}
}
