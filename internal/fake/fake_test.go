package fake

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func start(t *testing.T, cfg *Config) (*httptest.Server, *Server) {
	t.Helper()
	s := New(cfg)
	s.log = log.New(io.Discard, "", 0)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, s
}

func get(t *testing.T, url string, hdr map[string]string) (*Response, *http.Response) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var r Response
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	return &r, res
}

func TestLoadEnvAndFakeBlock(t *testing.T) {
	c, err := Load("api", "dc1", 9090, env(map[string]string{
		"PARKER_NODE": "dc1-api-1", "MESSAGE": "from env", "VERSION": "v1",
		"UPSTREAM_URIS": "http://a:1, http://b:2", "UPSTREAM_WORKERS": "2", "HTTP_CLIENT_REQUEST_TIMEOUT": "5s",
		EnvConfig: `{"version":"v2","upstream_failure":"ignore"}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "api" || c.DC != "dc1" || c.Node != "dc1-api-1" || c.Message != "from env" {
		t.Errorf("identity: %+v", c)
	}
	if c.Version != "v2" {
		t.Errorf("fake: block should win over VERSION, got %q", c.Version)
	}
	if strings.Join(c.Upstreams, " ") != "http://a:1 http://b:2" || c.Workers != 2 || c.ClientTimeout != 5*time.Second || !c.IgnoreUpstreamErrors {
		t.Errorf("upstream settings: %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	for _, e := range []map[string]string{
		{"UPSTREAM_WORKERS": "0"},
		{"HTTP_CLIENT_REQUEST_TIMEOUT": "soon"},
		{EnvConfig: "{"},
		{EnvConfig: `{"upstream_failure":"maybe"}`},
	} {
		if _, err := Load("x", "", 1, env(e)); err == nil {
			t.Errorf("%v: want an error", e)
		}
	}
}

func TestAnswersWhoItIs(t *testing.T) {
	ts, _ := start(t, &Config{Name: "api", DC: "dc1", Node: "dc1-api-1", Version: "v1", Message: `{"k":"v"}`, Workers: 1})
	r, res := get(t, ts.URL+"/x?y=1", nil)
	if res.StatusCode != 200 || r.Code != 200 || r.Name != "api" || r.DC != "dc1" || r.Node != "dc1-api-1" || r.Version != "v1" {
		t.Errorf("got %d %+v", res.StatusCode, r)
	}
	if r.URI != "/x?y=1" || r.Type != "HTTP" || r.Duration == "" || r.StartTime == "" {
		t.Errorf("request fields: %+v", r)
	}
	if m, ok := r.Body.(map[string]any); !ok || m["k"] != "v" {
		t.Errorf("a JSON message should nest as JSON, got %#v", r.Body)
	}
	if res.Header.Get(HeaderRequestID) == "" {
		t.Error("no request id")
	}
}

func TestCallsAChainAndPassesTheRequestID(t *testing.T) {
	var seen atomic.Value
	db, _ := start(t, &Config{Name: "db", Workers: 1})
	spy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get(HeaderRequestID))
		http.Redirect(w, r, db.URL, http.StatusTemporaryRedirect)
	}))
	defer spy.Close()
	api, _ := start(t, &Config{Name: "api", Upstreams: []string{spy.URL}, Workers: 1})
	web, _ := start(t, &Config{Name: "web", Upstreams: []string{api.URL + "/"}, Workers: 1})

	r, _ := get(t, web.URL, map[string]string{HeaderRequestID: "abc"})
	a := r.UpstreamCalls[api.URL+"/"]
	if a == nil || a.Name != "api" || a.Code != 200 {
		t.Fatalf("web -> api: %+v", r.UpstreamCalls)
	}
	d := a.UpstreamCalls[spy.URL]
	if d == nil || d.Name != "db" || d.URI != spy.URL {
		t.Fatalf("api -> db: %+v", a.UpstreamCalls)
	}
	if seen.Load() != "abc" {
		t.Errorf("request id at db = %v, want abc", seen.Load())
	}
}

func TestUpstreamFailure(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	ts, _ := start(t, &Config{Name: "web", Upstreams: []string{bad.URL, "http://127.0.0.1:1/"}, Workers: 1, ClientTimeout: time.Second})
	r, res := get(t, ts.URL, nil)
	if res.StatusCode != 500 || r.Error == "" {
		t.Errorf("want 500 with an error, got %d %+v", res.StatusCode, r)
	}
	u := r.UpstreamCalls[bad.URL]
	if u.Code != 503 || u.Body != "nope" || u.Error == "" {
		t.Errorf("non-fake upstream: %+v", u)
	}
	if d := r.UpstreamCalls["http://127.0.0.1:1/"]; d.Code != -1 || d.Error == "" {
		t.Errorf("unreachable upstream: %+v", d)
	}

	ts2, _ := start(t, &Config{Name: "web", Upstreams: []string{bad.URL}, Workers: 1, IgnoreUpstreamErrors: true})
	if r, res := get(t, ts2.URL, nil); res.StatusCode != 200 || r.Error != "" {
		t.Errorf("upstream_failure: ignore: got %d %+v", res.StatusCode, r)
	}
}

func TestWorkersCallInParallel(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
	}))
	defer slow.Close()
	ups := []string{slow.URL + "/1", slow.URL + "/2", slow.URL + "/3"}
	for _, tc := range []struct {
		workers  int
		min, max time.Duration
	}{{1, 450 * time.Millisecond, 5 * time.Second}, {3, 0, 400 * time.Millisecond}} {
		ts, _ := start(t, &Config{Name: "web", Upstreams: ups, Workers: tc.workers, ClientTimeout: 5 * time.Second})
		t0 := time.Now()
		r, _ := get(t, ts.URL, nil)
		took := time.Since(t0)
		if len(r.UpstreamCalls) != 3 || took < tc.min || took > tc.max {
			t.Errorf("workers %d: %d calls in %s", tc.workers, len(r.UpstreamCalls), took)
		}
	}
}
