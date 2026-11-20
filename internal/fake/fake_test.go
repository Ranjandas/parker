package fake

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func code(t *testing.T, url string) int {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestHealthAndReady(t *testing.T) {
	c, err := Load("api", "", 1, env(map[string]string{"HEALTH_CHECK_RESPONSE_CODE": "418",
		EnvConfig: `{"ready":{"delay":"10s","failure_code":500}}`}))
	if err != nil {
		t.Fatal(err)
	}
	ts, _ := start(t, c)
	if got := code(t, ts.URL+"/health"); got != 418 {
		t.Errorf("/health = %d, want 418", got)
	}
	if got := code(t, ts.URL+"/ready"); got != 500 {
		t.Errorf("/ready before the delay = %d, want 500", got)
	}
	ts2, s2 := start(t, c)
	s2.started = time.Now().Add(-11 * time.Second)
	ts = ts2
	if got := code(t, ts.URL+"/ready"); got != 200 {
		t.Errorf("/ready after the delay = %d, want 200", got)
	}
	// / still answers normally: health is separate.
	if got := code(t, ts.URL+"/"); got != 200 {
		t.Errorf("/ = %d", got)
	}
}

func TestHealthEnvErrors(t *testing.T) {
	for _, e := range []map[string]string{
		{"HEALTH_CHECK_RESPONSE_CODE": "ok"},
		{"READY_CHECK_RESPONSE_FAILURE_CODE": "99"},
		{"READY_CHECK_RESPONSE_DELAY": "later"},
		{EnvConfig: `{"ready":{"delay":"x"}}`},
	} {
		if _, err := Load("x", "", 1, env(e)); err == nil {
			t.Errorf("%v: want an error", e)
		}
	}
}

func TestCycleStops(t *testing.T) {
	// web and api call each other, as in examples/dataplane.yaml.
	webCfg := &Config{Name: "web", Workers: 1, ClientTimeout: 2 * time.Second}
	web, _ := start(t, webCfg)
	api, _ := start(t, &Config{Name: "api", Upstreams: []string{web.URL}, Workers: 1, ClientTimeout: 2 * time.Second})
	webCfg.Upstreams = []string{api.URL}

	r, res := get(t, web.URL, nil)
	if res.StatusCode != 200 {
		t.Fatalf("code %d: %+v", res.StatusCode, r)
	}
	a := r.UpstreamCalls[api.URL]
	w := a.UpstreamCalls[web.URL]
	if a.Name != "api" || w == nil || w.Name != "web" || !w.Cycle || len(w.UpstreamCalls) != 0 {
		t.Errorf("web -> api -> web should stop there: %+v / %+v", a, w)
	}
}

// recordSleeps replaces s.sleep, before any request, with one that adds
// up the delays instead of waiting.
func recordSleeps(s *Server) *atomic.Int64 {
	var total atomic.Int64
	s.sleep = func(_ context.Context, d time.Duration) { total.Add(int64(d)) }
	return &total
}

func faultCfg(t *testing.T, name, block string, upstreams ...string) *Config {
	t.Helper()
	c, err := Load(name, "", 1, env(map[string]string{EnvConfig: block}))
	if err != nil {
		t.Fatal(err)
	}
	c.Upstreams = upstreams
	return c
}

func TestInjectedErrors(t *testing.T) {
	ts, _ := start(t, faultCfg(t, "api", `{"errors":{"rate":1,"code":503}}`))
	r, res := get(t, ts.URL, nil)
	if res.StatusCode != 503 || r.Injected != "error" || r.Error == "" {
		t.Errorf("rate 1: %d %+v", res.StatusCode, r)
	}
	ts, s := start(t, faultCfg(t, "api", `{"errors":{"rate":1,"type":"delay","delay":"2s"}}`))
	slept := recordSleeps(s)
	r, res = get(t, ts.URL, nil)
	if res.StatusCode != 200 || r.Injected != "delay" || time.Duration(slept.Load()) != 2*time.Second {
		t.Errorf("type delay: %d %+v slept %s", res.StatusCode, r, time.Duration(slept.Load()))
	}
}

func TestSeedRepeats(t *testing.T) {
	codes := func() string {
		ts, _ := start(t, faultCfg(t, "api", `{"errors":{"rate":0.5},"seed":42}`))
		var b strings.Builder
		for range 20 {
			_, res := get(t, ts.URL, nil)
			b.WriteString(strconv.Itoa(res.StatusCode / 100))
		}
		return b.String()
	}
	a, b := codes(), codes()
	if a != b || !strings.Contains(a, "2") || !strings.Contains(a, "5") {
		t.Errorf("seeded runs differ or don't mix: %s vs %s", a, b)
	}
}

func TestLatency(t *testing.T) {
	in := newInjector(7)
	f := DefaultFaults()
	f.P50, f.P90, f.P99 = 10*time.Millisecond, 50*time.Millisecond, 200*time.Millisecond
	n := map[time.Duration]int{}
	for range 10000 {
		n[in.latency(f)]++
	}
	if n[f.P50] < 4500 || n[f.P50] > 5500 || n[f.P90] < 3500 || n[f.P90] > 4500 || n[f.P99] < 800 || n[f.P99] > 1200 {
		t.Errorf("draws: %v", n)
	}
	f.Variance = 10
	for range 1000 {
		if d := in.latency(f); d < 9*time.Millisecond || d > 220*time.Millisecond {
			t.Fatalf("variance 10%%: %s", d)
		}
	}
	// p90 and p99 default to the one below.
	g := DefaultFaults()
	g.P50 = 30 * time.Millisecond
	for range 100 {
		if d := in.latency(g); d != g.P50 {
			t.Fatalf("p50 only: %s", d)
		}
	}

	ts, s := start(t, faultCfg(t, "api", `{"latency":{"p50":"40ms"}}`))
	slept := recordSleeps(s)
	get(t, ts.URL, nil)
	if time.Duration(slept.Load()) != 40*time.Millisecond {
		t.Errorf("slept %s", time.Duration(slept.Load()))
	}
}

func TestRateLimit(t *testing.T) {
	ts, _ := start(t, faultCfg(t, "api", `{"rate_limit":{"rps":1,"code":429}}`))
	if _, res := get(t, ts.URL, nil); res.StatusCode != 200 {
		t.Errorf("first request: %d", res.StatusCode)
	}
	r, res := get(t, ts.URL, nil)
	if res.StatusCode != 429 || r.Injected != "rate_limit" {
		t.Errorf("second request at once: %d %+v", res.StatusCode, r)
	}
}

func TestRequestFaults(t *testing.T) {
	api, _ := start(t, faultCfg(t, "api", ``))
	web, ws := start(t, faultCfg(t, "web", ``, api.URL))
	slept := recordSleeps(ws)

	// Query params: this hop only.
	r, res := get(t, web.URL+"/?error-rate=100", nil)
	if res.StatusCode != 500 || r.Injected != "error" || r.UpstreamCalls != nil {
		t.Errorf("?error-rate=100: %d %+v", res.StatusCode, r)
	}
	get(t, web.URL+"/?delay=1.5", nil)
	if time.Duration(slept.Load()) != 1500*time.Millisecond {
		t.Errorf("?delay=1.5 slept %s", time.Duration(slept.Load()))
	}
	// The header targets a hop further down.
	r, res = get(t, web.URL, map[string]string{HeaderFault: "db=delay:1s; api=error-rate:100"})
	if a := r.UpstreamCalls[api.URL]; res.StatusCode != 500 || a.Code != 500 || a.Injected != "error" {
		t.Errorf("header on api: %d %+v", res.StatusCode, r.UpstreamCalls)
	}
	if _, res := get(t, web.URL+"/?error-rate=lots", nil); res.StatusCode != 400 {
		t.Errorf("bad fault: %d", res.StatusCode)
	}

	off, _ := start(t, faultCfg(t, "web", `{"request_faults":false}`))
	if _, res := get(t, off.URL+"/?error-rate=100", nil); res.StatusCode != 200 {
		t.Errorf("request_faults: false: %d", res.StatusCode)
	}
}

func TestFaultEnv(t *testing.T) {
	c, err := Load("api", "", 1, env(map[string]string{
		"TIMING_50_PERCENTILE": "10ms", "TIMING_99_PERCENTILE": "1s", "TIMING_VARIANCE": "5",
		"ERROR_RATE": "0.25", "ERROR_CODE": "502", "ERROR_TYPE": "delay", "ERROR_DELAY": "3s",
		"RATE_LIMIT": "20", "RATE_LIMIT_CODE": "429", "RAND_SEED": "9",
		EnvConfig: `{"errors":{"rate":0.5}}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	f := c.Faults
	if f.P50 != 10*time.Millisecond || f.P99 != time.Second || f.Variance != 5 || f.ErrorCode != 502 || f.ErrorType != "delay" ||
		f.ErrorDelay != 3*time.Second || f.RateLimit != 20 || f.RateLimitCode != 429 || c.Seed != 9 {
		t.Errorf("faults = %+v seed %d", f, c.Seed)
	}
	if f.ErrorRate != 0.5 {
		t.Errorf("fake: block should win: rate %v", f.ErrorRate)
	}
	for _, e := range []map[string]string{
		{"ERROR_RATE": "2"}, {"ERROR_TYPE": "boom"}, {"TIMING_50_PERCENTILE": "fast"}, {"ERROR_CODE": "9"},
		{EnvConfig: `{"errors":{"rate":1.5}}`}, {EnvConfig: `{"latency":{"p50":"-1s"}}`}, {EnvConfig: `{"rate_limit":{"code":42}}`},
	} {
		if _, err := Load("x", "", 1, env(e)); err == nil {
			t.Errorf("%v: want an error", e)
		}
	}
}
