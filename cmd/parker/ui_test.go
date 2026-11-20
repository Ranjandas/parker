package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Ranjandas/parker/internal/daemon"
)

func TestUIURL(t *testing.T) {
	u, err := url.Parse(uiURL("127.0.0.1:4321", "tok", "meshfed"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "127.0.0.1:4321" || u.RawQuery != "" {
		t.Errorf("url %v: token must ride in the fragment only", u)
	}
	f, _ := url.ParseQuery(u.Fragment)
	if f.Get("token") != "tok" || f.Get("lab") != "meshfed" {
		t.Errorf("fragment %q", u.Fragment)
	}
	if strings.Contains(uiURL("h:1", "t", ""), "lab=") {
		t.Error("lab in link without a lab")
	}
}

// The UI's API calls reach a remote daemon with its token and without the
// browser's Origin.
func TestAPIProxyToRemote(t *testing.T) {
	var gotAuth, gotOrigin, gotPath string
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotOrigin, gotPath = r.Header.Get("Authorization"), r.Header.Get("Origin"), r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()
	p, err := apiProxy(daemon.Target{Host: up.URL, Token: "remote-token", Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:9/parker.v1.ParkerService/Status", strings.NewReader("{}"))
	r.Header.Set("Origin", "http://127.0.0.1:9")
	r.Header.Set("Authorization", "Bearer local-ui-token")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Body.String() != "ok" || gotAuth != "Bearer remote-token" || gotOrigin != "" || gotPath != "/parker.v1.ParkerService/Status" {
		t.Errorf("body %q auth %q origin %q path %q", w.Body, gotAuth, gotOrigin, gotPath)
	}
	if _, err := apiProxy(daemon.Target{Host: "ftp://x"}); err == nil {
		t.Error("bad host accepted")
	}
}
