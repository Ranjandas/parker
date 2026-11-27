package fake

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/spec"
)

func on() *bool { b := true; return &b }

func quirkFaults(t *testing.T, sl *spec.FakeLimits) Faults {
	t.Helper()
	f := DefaultFaults()
	if err := f.Apply(&spec.Fake{Limits: sl}); err != nil {
		t.Fatal(err)
	}
	return f
}

// The case behind #179: a header with an underscore vanishes behind
// Nginx, so the upstream never sees it, and the answer says where.
func TestUnderscoreHeaderDropped(t *testing.T) {
	up, got := recorder(t)
	api, _ := start(t, &Config{Name: "api", Upstreams: []string{up.URL}, Workers: 1, Faults: quirkFaults(t, &spec.FakeLimits{Profile: "nginx"}),
		Upstream: UpstreamRequest{Forward: []string{"X_Tenant_Id", "X-Tenant-Id"}}})
	r, _ := get(t, api.URL, map[string]string{"X_Tenant_Id": "acme", "X-Tenant-Id": "acme"})
	if r.Code != 200 || strings.Join(r.DroppedHeaders, ",") != "x_tenant_id" {
		t.Errorf("answer %d, dropped %v", r.Code, r.DroppedHeaders)
	}
	if got.hdr.Get("X_Tenant_Id") != "" || got.hdr.Get("X-Tenant-Id") != "acme" {
		t.Errorf("upstream saw %v", got.hdr)
	}
	if n := injectedNote(r); n != " dropped_headers=x_tenant_id" {
		t.Errorf("log note %q", n)
	}

	api, _ = start(t, &Config{Name: "api", Workers: 1, Faults: quirkFaults(t, &spec.FakeLimits{UnderscoreHeaders: "reject"})})
	r, _ = get(t, api.URL, map[string]string{"X_Tenant_Id": "acme"})
	if r.Code != 400 || r.Limit != "underscore_headers" || !strings.Contains(r.Error, "x_tenant_id") {
		t.Errorf("reject: %d %q %q", r.Code, r.Limit, r.Error)
	}
	// allow turns a profile's drop off.
	api, _ = start(t, &Config{Name: "api", Workers: 1, Faults: quirkFaults(t, &spec.FakeLimits{Profile: "nginx", UnderscoreHeaders: "allow"})})
	if r, _ = get(t, api.URL, map[string]string{"X_Tenant_Id": "acme"}); len(r.DroppedHeaders) != 0 {
		t.Errorf("allow: dropped %v", r.DroppedHeaders)
	}
}

func TestStrictCookies(t *testing.T) {
	up, got := recorder(t)
	api, _ := start(t, &Config{Name: "api", Upstreams: []string{up.URL}, Workers: 1, Faults: quirkFaults(t, &spec.FakeLimits{StrictCookies: on()}),
		Upstream: UpstreamRequest{Forward: []string{"Cookie"}}})
	r, _ := get(t, api.URL, map[string]string{"Cookie": `ok=1; spaced=a b; q="quoted"; list=a,b; bs=a\b; =nameless; tok=x-y_z`})
	if strings.Join(r.DroppedCookies, ",") != "spaced,list,bs," || r.Code != 200 {
		t.Errorf("dropped %q", r.DroppedCookies)
	}
	if c := got.hdr.Get("Cookie"); c != `ok=1; q="quoted"; tok=x-y_z` {
		t.Errorf("upstream saw Cookie %q", c)
	}
	for pair, want := range map[string]bool{"a=": true, `a=""`: true, `a="b"`: true, `a="b`: false, "a b=1": false, "a": false, "a=é": false} {
		if validCookie(pair) != want {
			t.Errorf("%q: want %v", pair, want)
		}
	}
}

// rawGet sends a request target as is, which a Go client would escape.
func rawGet(t *testing.T, addr, target string) *Response {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", target)
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var r Response
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		t.Fatalf("%s: %d %v", target, res.StatusCode, err)
	}
	return &r
}

func TestStrictPathChars(t *testing.T) {
	api, _ := start(t, &Config{Name: "api", Workers: 1, Faults: quirkFaults(t, &spec.FakeLimits{Profile: "tomcat"})})
	addr := strings.TrimPrefix(api.URL, "http://")
	for _, target := range []string{"/search?q=a|b", "/items/{id}", "/a?x=[1]", "/a^b", "/a?b=\"c\""} {
		r := rawGet(t, addr, target)
		if r.Code != 400 || r.Limit != "strict_path_chars" || !strings.Contains(r.Error, "tomcat relaxedPathChars") {
			t.Errorf("%s: %d %q %q", target, r.Code, r.Limit, r.Error)
		}
	}
	for _, target := range []string{"/search?q=a%7Cb", "/items/42?x=1&y=2"} {
		if r := rawGet(t, addr, target); r.Code != 200 {
			t.Errorf("%s: %d %s", target, r.Code, r.Error)
		}
	}
	// Off, the same target passes.
	plain, _ := start(t, &Config{Name: "api", Workers: 1, Faults: DefaultFaults()})
	if r := rawGet(t, strings.TrimPrefix(plain.URL, "http://"), "/search?q=a|b"); r.Code != 200 {
		t.Errorf("off: %d", r.Code)
	}
}

func TestQuirksApply(t *testing.T) {
	if err := Validate(&spec.Fake{Limits: &spec.FakeLimits{UnderscoreHeaders: "strip"}}); err == nil || !strings.Contains(err.Error(), "underscore_headers") {
		t.Errorf("bad underscore_headers: %v", err)
	}
	off := false
	l := limitsOf(t, &spec.FakeLimits{Profile: "tomcat", StrictCookies: &off})
	if l.StrictCookies || !l.StrictPath {
		t.Errorf("tomcat with strict_cookies false: %+v", l)
	}
	if s := limitsOf(t, &spec.FakeLimits{UnderscoreHeaders: "drop", StrictPathChars: on()}).summary(); s != "underscore headers dropped, strict path chars (400)" {
		t.Errorf("summary %q", s)
	}
}
