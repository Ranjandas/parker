// Package webui serves Parker's web UI: a single-page app (embedded) that
// talks to the Parker API from the browser.
//
// The browser never holds the API token. It exchanges the token once at
// POST /ui/session for an HttpOnly session cookie, then calls the API
// under /ui/api/, which checks the cookie (and that the request comes from
// this origin), allows only the calls the UI needs, and forwards them to
// the API handler with the real token.
package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Ranjandas/parker/internal/spec"
)

//go:embed static
var static embed.FS

// Prefix is where the UI's API calls go; the rest of the path is the
// Connect procedure, e.g. /ui/api/parker.v1.ParkerService/Status.
const Prefix = "/ui/api"

const cookieName = "parker_session"

// SessionTTL is how long a browser session lasts without a new login.
var SessionTTL = 12 * time.Hour

// allowed are the API calls a browser session may make. Exec, Dial, Up
// and Shutdown stay CLI-only.
var allowed = map[string]bool{
	"/parker.v1.ParkerService/Version":  true,
	"/parker.v1.ParkerService/ListLabs": true,
	"/parker.v1.ParkerService/Status":   true,
	"/parker.v1.ParkerService/Describe": true,
	"/parker.v1.ParkerService/Graph":    true,
	"/parker.v1.ParkerService/Validate": true,
	"/parker.v1.ParkerService/Logs":     true,
	"/parker.v1.ParkerService/Run":      true,
	"/parker.v1.ParkerService/Down":     true,
}

// Options configure the UI handler.
type Options struct {
	// API serves the Parker API (Connect). Requests reach it with
	// `Authorization: Bearer <Token>` set.
	API http.Handler
	// Token is what a browser must present to start a session. With an
	// API that checks a token, it is that token.
	Token string
	// Where names the daemon in the UI (socket path or host).
	Where string
	// Version of the daemon or client serving the UI.
	Version string
}

// Handler serves the UI and its API gateway.
type Handler struct {
	o        Options
	files    http.Handler
	mu       sync.Mutex
	sessions map[string]time.Time
	tickets  map[string]ticket     // one-time app sign-in links
	apps     map[string]appSession // app origins' sessions
	now      func() time.Time
}

// New returns the UI handler.
func New(o Options) *Handler {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return &Handler{o: o, files: http.FileServer(http.FS(sub)), sessions: map[string]time.Time{}, tickets: map[string]ticket{}, apps: map[string]appSession{}, now: time.Now}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if IsAppHost(r.Host) {
		h.app(w, r)
		return
	}
	switch {
	case r.URL.Path == "/ui/open":
		h.open(w, r)
	case r.URL.Path == "/ui/session":
		h.session(w, r)
	case r.URL.Path == "/ui/info":
		h.info(w, r)
	case r.URL.Path == "/ui/schema.json":
		// The topology schema (`parker schema`): the builder's inspector
		// reads field docs, types and enums from it.
		w.Header().Set("Content-Type", "application/schema+json")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(spec.Schema)
	case strings.HasPrefix(r.URL.Path, Prefix+"/"):
		h.api(w, r)
	default:
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.files.ServeHTTP(w, r)
	}
}

// session: POST {"token": "..."} starts a session; DELETE ends it.
func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "want JSON {\"token\": \"...\"}", http.StatusBadRequest)
			return
		}
		if h.o.Token == "" || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(body.Token)), []byte(h.o.Token)) != 1 {
			http.Error(w, "wrong token (on the daemon host: parker daemon credentials)", http.StatusUnauthorized)
			return
		}
		id := newID()
		h.mu.Lock()
		h.gc()
		h.sessions[id] = h.now().Add(SessionTTL)
		h.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", HttpOnly: true, Secure: r.TLS != nil,
			SameSite: http.SameSiteStrictMode, MaxAge: int(SessionTTL.Seconds())})
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if c, err := r.Cookie(cookieName); err == nil {
			h.mu.Lock()
			delete(h.sessions, c.Value)
			h.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// info tells the page whether it is signed in, and where it points.
func (h *Handler) info(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	ok := h.valid(r)
	out := map[string]any{"signedIn": ok}
	if ok {
		out["where"] = h.o.Where
		out["version"] = h.o.Version
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (h *Handler) api(w http.ResponseWriter, r *http.Request) {
	proc := strings.TrimPrefix(r.URL.Path, Prefix)
	if !allowed[proc] {
		http.Error(w, "not available to the web UI", http.StatusNotFound)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	if !h.valid(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthenticated","message":"sign in again"}`))
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = proc
	r2.URL.RawPath = ""
	r2.RequestURI = ""
	r2.Header.Set("Authorization", "Bearer "+h.o.Token)
	r2.Header.Del("Cookie")
	h.o.API.ServeHTTP(w, r2)
}

func (h *Handler) valid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	exp, ok := h.sessions[c.Value]
	if !ok {
		return false
	}
	if h.now().After(exp) {
		delete(h.sessions, c.Value)
		return false
	}
	return true
}

// gc drops expired sessions and tickets; h.mu is held.
func (h *Handler) gc() {
	now := h.now()
	for id, exp := range h.sessions {
		if now.After(exp) {
			delete(h.sessions, id)
		}
	}
	for id, t := range h.tickets {
		if now.After(t.exp) {
			delete(h.tickets, id)
		}
	}
	for id, a := range h.apps {
		if now.After(a.exp) {
			delete(h.apps, id)
		}
	}
}

// sameOrigin refuses requests a browser sends from another site. Browsers
// set Origin on POST and DELETE; Sec-Fetch-Site where they support it.
func sameOrigin(r *http.Request) bool {
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" && s != "none" {
		return false
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true // not a browser cross-site request (curl, same-origin GET)
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

func newID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// NewToken returns a random token for a UI that has no API token of its
// own (`parker ui` against the local socket).
func NewToken() string { return newID() }
