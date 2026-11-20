package fake

import (
	_ "embed"
	"net/http"
)

// UIControlPath is where /ui's control panel reads and writes faults. The
// daemon's app proxy answers it from the fake's control socket; the fake
// itself refuses it, so nothing that reaches the service port (another
// service in the mesh, a browser on the port) can change faults.
const UIControlPath = "/.parker/fake/"

//go:embed ui.html
var uiPage []byte

func (s *Server) ui(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ui" && r.URL.Path != "/ui/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:")
	_, _ = w.Write(uiPage)
}

func refuseControl(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "faults change through Parker: open this page from the Parker UI, or use `parker fake`", http.StatusForbidden)
}
