package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeAPI records what reached the API.
type fakeAPI struct {
	path, raw, query, auth, cookie string
	calls                          int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	f.path, f.auth, f.cookie = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Cookie")
	f.raw, f.query = r.URL.EscapedPath(), r.URL.RawQuery
	_, _ = io.WriteString(w, `{"labs":[]}`)
}

func newTest(t *testing.T) (*Handler, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{}
	return New(Options{API: api, Token: "s3cret", Where: "/run/parker/parker.sock", Version: "v1"}), api
}

func do(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://parker.test"+path, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func login(t *testing.T, h http.Handler) string {
	t.Helper()
	w := do(h, "POST", "/ui/session", `{"token":"s3cret"}`, map[string]string{"Origin": "http://parker.test"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	c := w.Result().Cookies()
	if len(c) != 1 || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie: %+v", c)
	}
	return c[0].Name + "=" + c[0].Value
}

func TestServesTheApp(t *testing.T) {
	h, _ := newTest(t)
	for _, p := range []string{"/", "/app.js", "/app.css", "/theme.js", "/logo.svg"} {
		w := do(h, "GET", p, "", nil)
		if w.Code != 200 {
			t.Errorf("GET %s: %d", p, w.Code)
		}
	}
	w := do(h, "GET", "/", "", nil)
	if !strings.Contains(w.Body.String(), "app.js") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("index: %q csp %q", w.Body.String()[:60], w.Header().Get("Content-Security-Policy"))
	}
	if w := do(h, "POST", "/", "", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /: %d", w.Code)
	}
}

func TestLogin(t *testing.T) {
	h, _ := newTest(t)
	if w := do(h, "POST", "/ui/session", `{"token":"nope"}`, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", w.Code)
	}
	if w := do(h, "POST", "/ui/session", `not json`, nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad body: %d", w.Code)
	}
	if w := do(h, "POST", "/ui/session", `{"token":"s3cret"}`, map[string]string{"Origin": "https://evil.example"}); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin login: %d", w.Code)
	}
	cookie := login(t, h)
	w := do(h, "GET", "/ui/info", "", map[string]string{"Cookie": cookie})
	if !strings.Contains(w.Body.String(), `"signedIn":true`) || !strings.Contains(w.Body.String(), "parker.sock") {
		t.Errorf("info signed in: %s", w.Body)
	}
	w = do(h, "GET", "/ui/info", "", nil)
	if strings.TrimSpace(w.Body.String()) != `{"signedIn":false}` {
		t.Errorf("info signed out leaks: %s", w.Body)
	}
	// Logout ends the session.
	do(h, "DELETE", "/ui/session", "", map[string]string{"Cookie": cookie})
	if w := do(h, "POST", Prefix+"/parker.v1.ParkerService/ListLabs", `{}`, map[string]string{"Cookie": cookie}); w.Code != http.StatusUnauthorized {
		t.Errorf("after logout: %d", w.Code)
	}
}

func TestAPIGateway(t *testing.T) {
	h, api := newTest(t)
	cookie := login(t, h)
	list := Prefix + "/parker.v1.ParkerService/ListLabs"

	if w := do(h, "POST", list, `{}`, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: %d", w.Code)
	}
	if w := do(h, "POST", list, `{}`, map[string]string{"Cookie": "parker_session=forged"}); w.Code != http.StatusUnauthorized {
		t.Errorf("forged cookie: %d", w.Code)
	}
	if w := do(h, "POST", list, `{}`, map[string]string{"Cookie": cookie, "Origin": "https://evil.example"}); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin: %d", w.Code)
	}
	if w := do(h, "POST", list, `{}`, map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "cross-site"}); w.Code != http.StatusForbidden {
		t.Errorf("sec-fetch-site cross-site: %d", w.Code)
	}
	for _, p := range []string{"Exec", "Dial", "Up", "Shutdown", "Nope"} {
		if w := do(h, "POST", Prefix+"/parker.v1.ParkerService/"+p, `{}`, map[string]string{"Cookie": cookie}); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}
	if api.calls != 0 {
		t.Fatalf("refused requests reached the API %d times", api.calls)
	}
	w := do(h, "POST", list, `{}`, map[string]string{"Cookie": cookie, "Origin": "http://parker.test"})
	if w.Code != 200 || api.path != "/parker.v1.ParkerService/ListLabs" || api.auth != "Bearer s3cret" || api.cookie != "" {
		t.Errorf("forwarded: code %d path %q auth %q cookie %q", w.Code, api.path, api.auth, api.cookie)
	}
}

func TestSessionExpires(t *testing.T) {
	h, _ := newTest(t)
	now := time.Now()
	h.now = func() time.Time { return now }
	cookie := login(t, h)
	list := Prefix + "/parker.v1.ParkerService/ListLabs"
	if w := do(h, "POST", list, `{}`, map[string]string{"Cookie": cookie}); w.Code != 200 {
		t.Fatalf("fresh session: %d", w.Code)
	}
	now = now.Add(SessionTTL + time.Minute)
	if w := do(h, "POST", list, `{}`, map[string]string{"Cookie": cookie}); w.Code != http.StatusUnauthorized {
		t.Errorf("expired session: %d", w.Code)
	}
	h.mu.Lock()
	n := len(h.sessions)
	h.mu.Unlock()
	if n != 0 {
		t.Errorf("expired session kept: %d", n)
	}
}

func TestNoTokenMeansNoLogin(t *testing.T) {
	h := New(Options{API: &fakeAPI{}})
	if w := do(h, "POST", "/ui/session", `{"token":""}`, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("empty token accepted: %d", w.Code)
	}
}

func TestNewTokenIsRandom(t *testing.T) {
	a, b := NewToken(), NewToken()
	if a == b || len(a) != 64 {
		t.Errorf("tokens %q %q", a, b)
	}
}

// Every call the UI makes goes through, Graph and Validate included.
func TestAPIGatewayAllowsTheUIsCalls(t *testing.T) {
	h, api := newTest(t)
	cookie := login(t, h)
	for _, p := range []string{"Version", "ListLabs", "Status", "Describe", "Graph", "Validate", "Logs", "Run", "Down"} {
		w := do(h, "POST", Prefix+"/parker.v1.ParkerService/"+p, `{}`, map[string]string{"Cookie": cookie})
		if w.Code != 200 || api.path != "/parker.v1.ParkerService/"+p {
			t.Errorf("%s: %d, reached %q", p, w.Code, api.path)
		}
	}
}

// The builder reads the topology schema for its inspector.
func TestSchemaServed(t *testing.T) {
	h, _ := newTest(t)
	w := do(h, "GET", "/ui/schema.json", "", nil)
	var s struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil || s.Defs["Node"] == nil || s.Defs["Peering"] == nil {
		t.Errorf("%d %.200s", w.Code, w.Body)
	}
}

// The builder's version picker reads the release pins and Envoy support.
func TestVersionsServed(t *testing.T) {
	h, _ := newTest(t)
	w := do(h, "GET", "/ui/versions", "", nil)
	var v struct {
		Releases []struct{ Consul, Envoy, Dataplane string }
		Envoy    map[string][]string
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || len(v.Releases) == 0 || v.Releases[0].Envoy == "" || len(v.Envoy[compatMinor(v.Releases[0].Consul)]) == 0 {
		t.Errorf("%d %.300s", w.Code, w.Body)
	}
}

func compatMinor(v string) string {
	parts := strings.SplitN(v, ".", 3)
	return parts[0] + "." + parts[1]
}

// The builder's scripts are served next to the UI.
func TestBuilderScriptsServed(t *testing.T) {
	h, _ := newTest(t)
	idx := do(h, "GET", "/", "", nil).Body.String()
	for _, f := range []string{"buildmodel.js", "builder.js"} {
		if !strings.Contains(idx, `src="`+f+`"`) {
			t.Errorf("index.html doesn't load %s", f)
		}
		if w := do(h, "GET", "/"+f, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "use strict") {
			t.Errorf("%s: %d", f, w.Code)
		}
	}
}

// openApp asks for an app link and returns its URL.
func openApp(t *testing.T, h http.Handler, cookie, body string) (int, string) {
	t.Helper()
	w := do(h, "POST", "/ui/open", body, map[string]string{"Cookie": cookie, "Origin": "http://parker.test"})
	var out struct{ URL string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out.URL
}

func TestOpenApp(t *testing.T) {
	h, api := newTest(t)
	cookie := login(t, h)
	app := `{"lab":"meshfed","node":"dc1-server-1","port":8501,"scheme":"https","path":"/ui/"}`
	if w := do(h, "POST", "/ui/open", app, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("open without a session: %d", w.Code)
	}
	if w := do(h, "POST", "/ui/open", app, map[string]string{"Cookie": cookie, "Origin": "https://evil.example"}); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin open: %d", w.Code)
	}
	for _, bad := range []string{`{"lab":"Bad Lab","node":"a","port":80,"scheme":"http"}`, `{"lab":"l","node":"a--b","port":80,"scheme":"http"}`,
		`{"lab":"l","node":"a","port":0,"scheme":"http"}`, `{"lab":"l","node":"a","port":80,"scheme":"ftp"}`, `{"lab":"l","node":"a","port":80,"scheme":"http","path":"//evil"}`} {
		if code, _ := openApp(t, h, cookie, bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	code, link := openApp(t, h, cookie, app)
	u, err := url.Parse(link)
	if code != 200 || err != nil || u.Host != "dc1-server-1-8501s--meshfed.parker.localhost" || u.Path != "/.parker/open" {
		t.Fatalf("link %d %q", code, link)
	}
	if !IsAppHost(u.Host) || IsAppHost("parker.test") || IsAppHost("a.b.parker.localhost") {
		t.Error("IsAppHost")
	}

	// The ticket only works on its own app's origin, and only once.
	other := "http://web-8080--meshfed.parker.localhost" + u.RequestURI()
	if w := doURL(h, "GET", other, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("ticket on another app: %d", w.Code)
	}
	code, link = openApp(t, h, cookie, app)
	u, _ = url.Parse(link)
	w := doURL(h, "GET", link, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/ui/" {
		t.Fatalf("ticket: %d %q", w.Code, w.Header().Get("Location"))
	}
	ac := w.Result().Cookies()
	if len(ac) != 1 || ac[0].Name != "parker_app" || !ac[0].HttpOnly || ac[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("app cookie %+v", ac)
	}
	if w := doURL(h, "GET", link, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("ticket reused: %d", w.Code)
	}

	appCookie := ac[0].Name + "=" + ac[0].Value
	base := "http://" + u.Host
	// The app's requests reach the API's app proxy with the token, and
	// without Parker's cookie (the app keeps its own).
	w = doURL(h, "GET", base+"/v1/kv/a%2Fb?recurse=1", map[string]string{"Cookie": appCookie + "; app_pref=dark"})
	if w.Code != 200 || api.path != "/app/meshfed/dc1-server-1/https/8501/v1/kv/a/b" || api.auth != "Bearer s3cret" || api.cookie != "app_pref=dark" {
		t.Errorf("forwarded: %d path %q auth %q cookie %q", w.Code, api.path, api.auth, api.cookie)
	}
	if api.raw != "/app/meshfed/dc1-server-1/https/8501/v1/kv/a%2Fb" || api.query != "recurse=1" {
		t.Errorf("escaped path %q query %q", api.raw, api.query)
	}
	// An app origin never serves the Parker UI or its API, even signed in.
	calls := api.calls
	for _, p := range []string{"/ui/info", Prefix + "/parker.v1.ParkerService/ListLabs"} {
		do := doURL(h, "POST", base+p, map[string]string{"Cookie": cookie})
		if do.Code != http.StatusUnauthorized || api.calls != calls {
			t.Errorf("%s on an app origin with the UI cookie: %d", p, do.Code)
		}
	}
	// Its cookie is no good on another app.
	if w := doURL(h, "GET", "http://web-8080--meshfed.parker.localhost/", map[string]string{"Cookie": appCookie}); w.Code != http.StatusUnauthorized {
		t.Errorf("app cookie on another app: %d", w.Code)
	}
}

func TestAppLabelFallsBackToAHash(t *testing.T) {
	a := AppTarget{Lab: strings.Repeat("l", 40), Node: strings.Repeat("n", 30), Port: 8500, Scheme: "http"}
	b := a
	b.Port = 8501
	if la, lb := a.label(), b.label(); len(la) > 63 || !strings.HasPrefix(la, "app-") || la == lb || !IsAppHost(la+appDomain) {
		t.Errorf("labels %q %q", la, lb)
	}
}

func doURL(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
