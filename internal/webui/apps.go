package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// App UIs (the Consul UI, a service's page) open through the daemon's app
// proxy, each on an origin of its own: <node>-<port>--<lab>.parker.localhost
// on the UI's port. Browsers resolve *.localhost to loopback, so this works
// wherever the UI itself is on loopback (`parker ui`, or a browser on the
// daemon host). A separate origin keeps apps with absolute paths working
// and keeps their scripts away from the Parker UI and its session.
//
// The UI asks POST /ui/open for a link; the link carries a one-time ticket
// that the app origin trades for a cookie of its own, valid for that app
// only. Requests on the app origin go to the API handler at
// AppPath<lab>/<node>/<scheme>/<port>/<path> with the token.

// AppPath is where the API handler proxies to apps (daemon.AppPrefix).
const AppPath = "/app/"

const (
	appDomain    = ".parker.localhost"
	appCookie    = "parker_app"
	ticketTTL    = time.Minute
	appTicketURL = "/.parker/open"
)

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	appHostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// AppTarget is an app UI inside a lab.
type AppTarget struct {
	Lab    string `json:"lab"`
	Node   string `json:"node"`
	Port   int    `json:"port"`
	Scheme string `json:"scheme"`
}

func (t AppTarget) check() error {
	if !nameRe.MatchString(t.Lab) || !nameRe.MatchString(t.Node) {
		return fmt.Errorf("lab %q, node %q: want lowercase letters, digits and single hyphens", t.Lab, t.Node)
	}
	if t.Port < 1 || t.Port > 65535 {
		return fmt.Errorf("bad port %d", t.Port)
	}
	if t.Scheme != "http" && t.Scheme != "https" {
		return fmt.Errorf("scheme %q: want http or https", t.Scheme)
	}
	return nil
}

// label is the app's host label: readable when it fits in a DNS label, a
// hash of the target when not. Names never hold "--", so the readable form
// can't collide.
func (t AppTarget) label() string {
	l := fmt.Sprintf("%s-%d--%s", t.Node, t.Port, t.Lab)
	if t.Scheme == "https" {
		l = fmt.Sprintf("%s-%ds--%s", t.Node, t.Port, t.Lab)
	}
	if len(l) <= 63 {
		return l
	}
	sum := sha256.Sum256([]byte(t.Scheme + "://" + t.Lab + "/" + t.Node + ":" + strconv.Itoa(t.Port)))
	return "app-" + hex.EncodeToString(sum[:12])
}

func (t AppTarget) path() string {
	return fmt.Sprintf("%s%s/%s/%s/%d", AppPath, t.Lab, t.Node, t.Scheme, t.Port)
}

// hostname is r.Host without its port, lowercased.
func hostname(hostport string) string {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		h = hostport
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// IsAppHost tells whether a request's Host is an app origin.
func IsAppHost(hostport string) bool {
	l, ok := strings.CutSuffix(hostname(hostport), appDomain)
	return ok && appHostRe.MatchString(l)
}

type ticket struct {
	host   string // the app hostname it is valid on
	target AppTarget
	path   string
	exp    time.Time
}

type appSession struct {
	host   string
	target AppTarget
	exp    time.Time
}

// open: POST {lab, node, port, scheme, path} from a signed-in UI returns
// {"url": ...}, a link that signs the browser in to that app only.
func (h *Handler) open(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	if !h.valid(r) {
		http.Error(w, "sign in again", http.StatusUnauthorized)
		return
	}
	var body struct {
		AppTarget
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "want JSON {lab, node, port, scheme, path}", http.StatusBadRequest)
		return
	}
	if err := body.check(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Path == "" {
		body.Path = "/"
	}
	if !strings.HasPrefix(body.Path, "/") || strings.HasPrefix(body.Path, "//") {
		http.Error(w, "path must start with a single /", http.StatusBadRequest)
		return
	}
	host := body.label() + appDomain
	id := newID()
	h.mu.Lock()
	h.gc()
	h.tickets[id] = ticket{host: host, target: body.AppTarget, path: body.Path, exp: h.now().Add(ticketTTL)}
	h.mu.Unlock()
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	hp := host
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		hp = net.JoinHostPort(host, port)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": scheme + "://" + hp + appTicketURL + "?t=" + id})
}

// app serves an app origin: the ticket exchange, then the proxy.
func (h *Handler) app(w http.ResponseWriter, r *http.Request) {
	host := hostname(r.Host)
	if r.URL.Path == appTicketURL {
		id := r.URL.Query().Get("t")
		h.mu.Lock()
		t, ok := h.tickets[id]
		delete(h.tickets, id) // one use
		live := ok && t.host == host && !h.now().After(t.exp)
		var sid string
		if live {
			sid = newID()
			h.gc()
			h.apps[sid] = appSession{host: host, target: t.target, exp: h.now().Add(SessionTTL)}
		}
		h.mu.Unlock()
		if !live {
			http.Error(w, "This link has expired or was used already. Open the app again from the Parker UI.", http.StatusUnauthorized)
			return
		}
		// Lax, not Strict: the browser arrives here from the Parker UI's
		// origin, and the redirect must carry the cookie.
		http.SetCookie(w, &http.Cookie{Name: appCookie, Value: sid, Path: "/", HttpOnly: true, Secure: r.TLS != nil,
			SameSite: http.SameSiteLaxMode, MaxAge: int(SessionTTL.Seconds())})
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, t.path, http.StatusSeeOther)
		return
	}
	var s appSession
	ok := false
	if c, err := r.Cookie(appCookie); err == nil {
		h.mu.Lock()
		s, ok = h.apps[c.Value]
		if ok && h.now().After(s.exp) {
			delete(h.apps, c.Value)
			ok = false
		}
		h.mu.Unlock()
	}
	if !ok || s.host != host {
		http.Error(w, "Not signed in to this app. Open it from the Parker UI.", http.StatusUnauthorized)
		return
	}
	r2 := r.Clone(r.Context())
	prefix := s.target.path()
	r2.URL.Path = prefix + r.URL.Path
	r2.URL.RawPath = ""
	if esc := r.URL.EscapedPath(); esc != r.URL.Path {
		r2.URL.RawPath = prefix + esc
	}
	r2.RequestURI = ""
	r2.Header.Set("Authorization", "Bearer "+h.o.Token)
	r2.Header.Set("X-Forwarded-Host", r.Host)
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	r2.Header.Set("X-Forwarded-Proto", proto)
	// The app keeps its own cookies; Parker's stays here.
	r2.Header.Del("Cookie")
	for _, c := range r.Cookies() {
		if c.Name != appCookie {
			r2.AddCookie(c)
		}
	}
	h.o.API.ServeHTTP(w, r2)
}
