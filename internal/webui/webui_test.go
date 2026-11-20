package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAPI records what reached the API.
type fakeAPI struct {
	path, auth, cookie string
	calls              int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	f.path, f.auth, f.cookie = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Cookie")
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

// Every call the UI makes goes through, Graph included.
func TestAPIGatewayAllowsTheUIsCalls(t *testing.T) {
	h, api := newTest(t)
	cookie := login(t, h)
	for _, p := range []string{"Version", "ListLabs", "Status", "Describe", "Graph", "Logs", "Run", "Down"} {
		w := do(h, "POST", Prefix+"/parker.v1.ParkerService/"+p, `{}`, map[string]string{"Cookie": cookie})
		if w.Code != 200 || api.path != "/parker.v1.ParkerService/"+p {
			t.Errorf("%s: %d, reached %q", p, w.Code, api.path)
		}
	}
}
