package fake

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFactSets(t *testing.T) {
	for v, want := range map[string]int{"": 0, "v1": 0, "v2": 1, "v3": 0, "2.0.4": 1, "canary": factSet("canary")} {
		if got := factSet(v); got != want {
			t.Errorf("factSet(%q) = %d, want %d", v, got, want)
		}
	}
	for i, set := range facts {
		if len(set) < 25 {
			t.Errorf("set %d has %d facts", i, len(set))
		}
		seen := map[string]bool{}
		for _, f := range set {
			if seen[f] || len(f) > 160 || !strings.HasSuffix(f, ".") {
				t.Errorf("set %d: bad or repeated fact %q", i, f)
			}
			seen[f] = true
		}
	}
	// In turn, wrapping around.
	ft := newFactTeller("v2", 0)
	first := ft.fact()
	for range len(facts[1]) - 1 {
		if ft.fact() == first {
			t.Fatal("a fact came round early")
		}
	}
	if ft.fact() != first || first != facts[1][0] {
		t.Error("facts don't go round in turn")
	}
}

func TestFactsInAnswers(t *testing.T) {
	ts, _ := start(t, &Config{Name: "api", Version: "v2", Facts: true, Workers: 1})
	a, _ := get(t, ts.URL, nil)
	b, _ := get(t, ts.URL, nil)
	if a.Fact == "" || a.Fact == b.Fact || !strings.Contains(strings.Join(facts[1], "\n"), a.Fact) {
		t.Errorf("facts %q, %q", a.Fact, b.Fact)
	}
	c, err := Load("api", "dc1", 1, env(map[string]string{EnvConfig: `{"facts":false}`}))
	if err != nil || c.Facts {
		t.Fatalf("facts: false: %+v %v", c, err)
	}
	ts, _ = start(t, c)
	if r, _ := get(t, ts.URL, nil); r.Fact != "" {
		t.Errorf("facts off, got %q", r.Fact)
	}
}

func TestUIPage(t *testing.T) {
	s := New(&Config{Name: "api", Workers: 1, Faults: DefaultFaults()})
	s.log = log.New(io.Discard, "", 0)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + "/ui")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(b), UIControlPath) {
		t.Errorf("/ui: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	// The control path never changes faults on the service port.
	res, err = http.Post(ts.URL+UIControlPath+"faults", "application/json", strings.NewReader(`{"health":{"code":503}}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden || s.Faults().HealthCode != 200 {
		t.Errorf("control on the port: %d, health %d", res.StatusCode, s.Faults().HealthCode)
	}
}
